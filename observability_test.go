package reviews_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	r "github.com/StuHelper/stuhelper-reviews"
	"github.com/StuHelper/stuhelper-reviews/httpapi"
	"github.com/StuHelper/stuhelper-reviews/postgres"
)

func TestDiagnosticsClientCancellationAndOutboxMetrics(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	repo := f.service.Repo.(*postgres.Repository)

	var mu sync.Mutex
	var events []r.DiagnosticEvent
	record := func(ev r.DiagnosticEvent) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	}
	f.service.OnDiagnostic = record
	repo.OnDiagnostic = record

	// 1. OutboxMetrics starts empty, tracks oldest unconfirmed timestamp, and resets after confirmation.
	initialStats, err := f.service.OutboxMetrics(ctx)
	if err != nil || initialStats.PendingCount != 0 || initialStats.OldestPendingAt != nil {
		t.Fatalf("expected empty outbox metrics, got %+v %v", initialStats, err)
	}

	t1 := f.now.Round(time.Microsecond)
	f.now = t1
	in := input("course", "autumn", 4)
	in.Title = "SensitivePrivateTitle991"
	in.Body = "SensitivePrivateBody992"
	create(t, f, "obs-1", in)

	t2 := t1.Add(10 * time.Minute)
	f.now = t2
	create(t, f, "obs-2", in)

	stats1, err := repo.OutboxMetrics(ctx)
	if err != nil || stats1.PendingCount < 2 || stats1.OldestPendingAt == nil || !stats1.OldestPendingAt.Equal(t1) {
		t.Fatalf("expected oldest pending at %v, got %+v %v", t1, stats1, err)
	}

	pending, err := repo.PendingOutbox(ctx, 100, nil)
	if err != nil || int64(len(pending)) != stats1.PendingCount {
		t.Fatalf("pending outbox count mismatch: %d vs %d (%v)", len(pending), stats1.PendingCount, err)
	}
	for _, item := range pending {
		if item.Time.Equal(t1) {
			if err := repo.ConfirmOutbox(ctx, item.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	stats2, err := f.service.OutboxMetrics(ctx)
	if err != nil || stats2.PendingCount == 0 || stats2.OldestPendingAt == nil || !stats2.OldestPendingAt.Equal(t2) {
		t.Fatalf("expected oldest pending advanced to %v, got %+v %v", t2, stats2, err)
	}
	for _, item := range pending {
		if item.Time.Equal(t2) {
			if err := repo.ConfirmOutbox(ctx, item.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	statsEmpty, err := f.service.OutboxMetrics(ctx)
	if err != nil || statsEmpty.PendingCount != 0 || statsEmpty.OldestPendingAt != nil {
		t.Fatalf("expected confirmed outbox to report 0 pending, got %+v %v", statsEmpty, err)
	}

	// 2. Desensitized diagnostics on external & PostgreSQL SQLSTATE failures.
	f.moderation.failed = true
	_, _ = f.service.Create(ctx, r.Principal{Subject: "secret-student-subject"}, "mod-fail", in)
	f.moderation.failed = false

	// Trigger PostgreSQL 23505 unique constraint diagnostic via duplicate history revision in transaction.
	_ = repo.Transaction(ctx, func(tx r.Transaction) error {
		return tx.Event(r.Event{ID: "dup-audit-id", Type: "reviews.test.v1", Subject: "entry/secret", SchoolID: "school", Actor: "secret-actor", Revision: 1, At: t2})
	})
	_ = repo.Transaction(ctx, func(tx r.Transaction) error {
		return tx.Event(r.Event{ID: "dup-audit-id", Type: "reviews.test.v1", Subject: "entry/secret", SchoolID: "school", Actor: "secret-actor", Revision: 1, At: t2})
	})

	// 3. HTTP client cancellation returns 499 instead of 503, while server dependency outage still returns 503.
	h := httpapi.Handler{
		Service: f.service,
		Authenticate: func(req *http.Request) (r.Principal, error) {
			if err := req.Context().Err(); err != nil {
				return r.Principal{}, err
			}
			return student, nil
		},
		OnDiagnostic: record,
	}.Routes()

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelReq := httptest.NewRequest("GET", "/api/v1/reviews?limit=20", nil).WithContext(canceledCtx)
	cancelRec := httptest.NewRecorder()
	h.ServeHTTP(cancelRec, cancelReq)
	if cancelRec.Code != httpapi.StatusClientClosedRequest {
		t.Fatalf("expected 499 on client cancellation, got %d %s", cancelRec.Code, cancelRec.Body.String())
	}

	f.access.failed = true
	outageRec := httptest.NewRecorder()
	h.ServeHTTP(outageRec, httptest.NewRequest("GET", "/api/v1/reviews?limit=20", nil))
	f.access.failed = false
	if outageRec.Code != 503 {
		t.Fatalf("expected 503 on upstream access outage, got %d %s", outageRec.Code, outageRec.Body.String())
	}

	// Verify captured diagnostics contain moderation, postgres SQLSTATE 23505, canceled, and unavailable, with zero PII/body leaks.
	mu.Lock()
	captured := append([]r.DiagnosticEvent{}, events...)
	mu.Unlock()

	var sawModeration, sawSQLState, sawCanceled, sawHTTPUnavailable bool
	rawDump, _ := json.Marshal(captured)
	for _, secret := range []string{"secret-student-subject", "secret-actor", "SensitivePrivateTitle991", "SensitivePrivateBody992", "SELECT", "INSERT"} {
		if strings.Contains(string(rawDump), secret) {
			t.Fatalf("diagnostic event leaked sensitive string %q: %s", secret, string(rawDump))
		}
	}
	for _, ev := range captured {
		if ev.Duration < 0 {
			t.Fatalf("negative duration in diagnostic event: %+v", ev)
		}
		if ev.Dependency == "moderation" && ev.Category == "unavailable" {
			sawModeration = true
		}
		if ev.Dependency == "postgres" && ev.SQLState == "23505" && ev.Category == "conflict" {
			sawSQLState = true
		}
		if ev.Dependency == "http" && ev.Category == "canceled" {
			sawCanceled = true
		}
		if ev.Dependency == "http" && ev.Category == "unavailable" {
			sawHTTPUnavailable = true
		}
	}
	if !sawModeration || !sawSQLState || !sawCanceled || !sawHTTPUnavailable {
		t.Fatalf("missing expected diagnostic events: mod=%v sql=%v cancel=%v http503=%v (%s)",
			sawModeration, sawSQLState, sawCanceled, sawHTTPUnavailable, fmt.Sprintf("%+v", captured))
	}
}
