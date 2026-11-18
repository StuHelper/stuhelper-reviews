package reviews_test

import (
	"context"
	"errors"
	"testing"
	"time"

	r "github.com/StuHelper/stuhelper-reviews"
	"github.com/StuHelper/stuhelper-reviews/postgres"
)

func TestExternalDependencyTimeoutsAndLockFreeTransactions(t *testing.T) {
	f := fixture(t)
	repo := f.service.Repo.(*postgres.Repository)
	repo.DB.SetMaxOpenConns(1)
	ctx := context.Background()

	root := create(t, f, "tx-root", input("course", "autumn", 4))

	// 1. While Reply is waiting on external Moderation.Check, neither the single sql.DB connection
	// nor the RootID advisory lock may be held: a concurrent vote on the same thread must succeed immediately.
	gate := closureModerationGate{make(chan struct{}), make(chan struct{})}
	f.service.Moderation = gate
	replyDone := make(chan error, 1)
	go func() {
		_, err := f.service.Reply(ctx, r.Principal{Subject: "replier"}, root.ID, "slow-reply", r.Input{
			Kind:     "reply",
			TargetID: "course",
			Body:     "Reply waiting on external moderation",
		})
		replyDone <- err
	}()
	<-gate.entered

	voteCtx, cancelVote := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancelVote()
	if _, err := f.service.Action(voteCtx, r.Principal{Subject: "voter-1"}, root.ID, "vote-during-reply", r.Action{Action: "vote", Vote: 1}); err != nil {
		close(gate.release)
		<-replyDone
		t.Fatalf("concurrent vote blocked by Reply holding DB tx/RootID lock during external RPC: %v", err)
	}
	close(gate.release)
	if err := <-replyDone; err != nil {
		t.Fatalf("Reply failed after unblocking Moderation.Check: %v", err)
	}

	// 2. While republish is waiting on external Moderation.Check, concurrent reads/votes on the single DB connection must also succeed.
	f.service.Moderation = f.moderation
	toWithdraw := create(t, f, "tx-repub", input("other", "autumn", 3))
	withdrawn, err := f.service.Action(ctx, student, toWithdraw.ID, "wd-repub", r.Action{Action: "withdraw", Revision: toWithdraw.Revision})
	if err != nil {
		t.Fatal(err)
	}
	gate2 := closureModerationGate{make(chan struct{}), make(chan struct{})}
	f.service.Moderation = gate2
	repubDone := make(chan error, 1)
	repubIn := input("other", "autumn", 5)
	go func() {
		_, err := f.service.Action(ctx, student, toWithdraw.ID, "repub-slow", r.Action{
			Action:   "republish",
			Revision: withdrawn.Revision,
			Input:    &repubIn,
		})
		repubDone <- err
	}()
	<-gate2.entered

	readCtx, cancelRead := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancelRead()
	if _, err := f.service.Get(readCtx, student, root.ID); err != nil {
		close(gate2.release)
		<-repubDone
		t.Fatalf("DB pool exhausted while republish waited on external RPC: %v", err)
	}
	close(gate2.release)
	if err := <-repubDone; err != nil {
		t.Fatalf("republish failed after unblocking Moderation.Check: %v", err)
	}

	// 3. Verify hung external Moderation.Check without caller deadline still times out quickly via Config.ExternalTimeout -> ErrUnavailable.
	f.service.Config.ExternalTimeout = 60 * time.Millisecond
	hungGate := closureModerationGate{make(chan struct{}), make(chan struct{})}
	f.service.Moderation = hungGate
	start := time.Now()
	_, err = f.service.Create(ctx, student, "hung-create", input("c-hung", "autumn", 4))
	if !errors.Is(err, r.ErrUnavailable) || time.Since(start) > time.Second {
		t.Fatalf("expected bounded ErrUnavailable timeout on hung external Check, got err=%v elapsed=%v", err, time.Since(start))
	}
}
