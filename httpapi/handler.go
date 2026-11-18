// Package httpapi exposes the review contract without trusting client identity fields.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"strconv"
	"strings"
	"time"

	reviews "github.com/StuHelper/stuhelper-reviews"
)

const StatusClientClosedRequest = 499

type Authenticate func(*http.Request) (reviews.Principal, error)
type Handler struct {
	Service      *reviews.Service
	Authenticate Authenticate
	// PublicOrigin is the trusted browser origin when a reverse proxy terminates TLS.
	PublicOrigin string
	OnDiagnostic reviews.DiagnosticHook
}

type responseTracker struct {
	http.ResponseWriter
	req       *http.Request
	clientCtx context.Context
	start     time.Time
	hook      reviews.DiagnosticHook
}

func (t *responseTracker) Unwrap() http.ResponseWriter {
	return t.ResponseWriter
}

func (t *responseTracker) operationName() string {
	if t.req == nil {
		return "http_request"
	}
	if t.req.Pattern != "" {
		return t.req.Pattern
	}
	return t.req.Method
}

func (h Handler) Routes() http.Handler {
	m := http.NewServeMux()
	h.managementRoutes(m)
	m.HandleFunc("GET /api/v1/reviews/summaries", h.summaries)
	m.HandleFunc("GET /api/v1/reviews/me/overview", h.overview)
	m.HandleFunc("GET /api/v1/reviews", h.list)
	m.HandleFunc("POST /api/v1/reviews", h.create)
	m.HandleFunc("GET /api/v1/reviews/{id}", h.get)
	m.HandleFunc("POST /api/v1/reviews/{id}/actions", h.action)
	m.HandleFunc("POST /api/v1/reviews/{id}/replies", h.reply)
	m.HandleFunc("GET /api/v1/reviews/statistics", h.statistics)
	m.HandleFunc("GET /api/v1/reviews/compose", h.compose)
	m.HandleFunc("GET /api/v1/reviews/admin/entries", h.adminList)
	m.HandleFunc("GET /api/v1/reviews/admin/entries/{id}", h.adminGet)
	m.HandleFunc("GET /api/v1/reviews/me/references", h.references)
	m.HandleFunc("GET /api/v1/reviews/me/entries", h.mine)
	m.HandleFunc("GET /api/v1/reviews/me/entries/{id}", h.ownEntry)
	m.HandleFunc("GET /api/v1/reviews/me/drafts", h.drafts)
	m.HandleFunc("GET /api/v1/reviews/me/drafts/{id}", h.getDraft)
	m.HandleFunc("POST /api/v1/reviews/me/drafts/{id}/publish", h.publishDraft)
	m.HandleFunc("POST /api/v1/reviews/me/drafts", h.saveDraft)
	m.HandleFunc("DELETE /api/v1/reviews/me/drafts/{id}", h.deleteDraft)
	m.HandleFunc("GET /api/v1/reviews/me/votes", h.votes)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tracker := &responseTracker{
			ResponseWriter: w,
			req:            r,
			clientCtx:      r.Context(),
			start:          time.Now(),
			hook:           h.OnDiagnostic,
		}
		tracker.Header().Set("Cache-Control", "no-store")
		tracker.Header().Set("X-Content-Type-Options", "nosniff")
		if h.Service == nil {
			respond(tracker, nil, reviews.ErrUnavailable)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !h.sameOriginWrite(r) {
			respond(tracker, nil, reviews.NewErrorDetail(reviews.ErrForbidden, "origin_mismatch", "origin", nil))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		tracker.req = r.WithContext(ctx)
		m.ServeHTTP(tracker, tracker.req)
	})
}
func (h Handler) sameOriginWrite(r *http.Request) bool {
	for _, site := range r.Header.Values("Sec-Fetch-Site") {
		if site != "same-origin" && site != "none" {
			return false
		}
	}
	origins := r.Header.Values("Origin")
	if len(origins) == 0 || len(origins) == 1 && origins[0] == "" {
		return true
	}
	if len(origins) != 1 {
		return false
	}
	expected := h.PublicOrigin
	if expected == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		expected = scheme + "://" + r.Host
	}
	return strings.EqualFold(origins[0], expected)
}

type errorResponse struct {
	Code            string `json:"code"`
	Subcode         string `json:"subcode,omitempty"`
	Field           string `json:"field,omitempty"`
	CurrentRevision *int64 `json:"current_revision,omitempty"`
}

func respond(w http.ResponseWriter, v any, e error) {
	w.Header().Set("Content-Type", "application/json")
	if e != nil {
		tracker, tracked := w.(*responseTracker)
		if tracked && errors.Is(e, context.Canceled) && errors.Is(tracker.clientCtx.Err(), context.Canceled) {
			if tracker.hook != nil {
				tracker.hook(reviews.DiagnosticEvent{
					Operation:  tracker.operationName(),
					Dependency: "http",
					Category:   "canceled",
					Duration:   time.Since(tracker.start),
				})
			}
			w.WriteHeader(StatusClientClosedRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "canceled"})
			return
		}
		status := 503
		code := "unavailable"
		for _, x := range []struct {
			e error
			s int
		}{{reviews.ErrUnauthenticated, 401}, {reviews.ErrInput, 400}, {reviews.ErrForbidden, 403}, {reviews.ErrEnrollment, 403}, {reviews.ErrNotFound, 404}, {reviews.ErrConflict, 409}, {reviews.ErrWindow, 409}} {
			if errors.Is(e, x.e) {
				status = x.s
				code = x.e.Error()
			}
		}
		if tracked && tracker.hook != nil {
			tracker.hook(reviews.DiagnosticEvent{
				Operation:  tracker.operationName(),
				Dependency: "http",
				Category:   reviews.ClassifyDiagnosticCategory(e),
				Duration:   time.Since(tracker.start),
			})
		}
		payload := errorResponse{Code: code}
		var detail reviews.ErrorDetail
		if errors.As(e, &detail) {
			payload.Subcode = detail.Subcode
			payload.Field = detail.Field
			payload.CurrentRevision = detail.CurrentRevision
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(payload)
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" || len(r.Header.Values("Content-Type")) != 1 {
		return reviews.ErrInput
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		return reviews.NewErrorDetail(reviews.ErrInput, "malformed_json", "", nil)
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return reviews.NewErrorDetail(reviews.ErrInput, "malformed_json", "", nil)
	}
	return nil
}
func (h Handler) principal(w http.ResponseWriter, r *http.Request, required bool) (reviews.Principal, bool) {
	if h.Authenticate == nil {
		if !required && r.Header.Get("Authorization") == "" && r.Header.Get("Cookie") == "" {
			return reviews.Principal{}, true
		}
		respond(w, nil, reviews.ErrUnavailable)
		return reviews.Principal{}, false
	}
	p, e := h.Authenticate(r)
	if e != nil {
		respond(w, nil, e)
		return p, false
	}
	if required && p.Subject == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(401)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "unauthenticated"})
		return p, false
	}
	return p, true
}
func limit(r *http.Request) (int, error) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return 20, nil
	}
	n, e := strconv.Atoi(v)
	if e != nil || n < 1 || n > 100 {
		return 0, reviews.NewErrorDetail(reviews.ErrInput, "invalid_pagination", "limit", nil)
	}
	return n, nil
}
func (h Handler) list(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r, true)
	if !ok {
		return
	}
	n, e := limit(r)
	if e != nil {
		respond(w, nil, e)
		return
	}
	q := r.URL.Query()
	preview, e := flagRequested(r, "include_reply_preview")
	if e != nil {
		respond(w, nil, e)
		return
	}
	featured, e := optionalBool(r, "featured")
	if e != nil {
		respond(w, nil, e)
		return
	}
	hasTeacherReply, e := optionalBool(r, "has_teacher_reply")
	if e != nil {
		respond(w, nil, e)
		return
	}
	teachers, e := flagRequested(r, "include_teacher_ids")
	if e != nil {
		respond(w, nil, e)
		return
	}
	v, e := h.Service.List(r.Context(), p, reviews.Filter{IncludeTeacherIDs: teachers, IncludeReplyPreview: preview, Featured: featured, HasTeacherReply: hasTeacherReply, RatingGroup: q.Get("rating_group"), Kind: q.Get("kind"), TargetID: q.Get("target_id"), TermID: q.Get("term_id"), TeacherID: q.Get("teacher_id"), Sort: q.Get("sort"), RootID: q.Get("root_id"), After: q.Get("after"), Limit: n})
	respond(w, v, e)
}
func (h Handler) get(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r, true)
	if !ok {
		return
	}
	teachers, e := flagRequested(r, "include_teacher_ids")
	if e != nil {
		respond(w, nil, e)
		return
	}
	v, e := h.Service.GetWithOptions(r.Context(), p, r.PathValue("id"), reviews.ViewOptions{IncludeTeacherIDs: teachers})
	respond(w, v, e)
}
func (h Handler) create(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r, true)
	if !ok {
		return
	}
	var in reviews.Input
	if e := decode(w, r, &in); e != nil {
		respond(w, nil, e)
		return
	}
	v, e := h.Service.Create(r.Context(), p, r.Header.Get("Idempotency-Key"), in)
	respond(w, v, e)
}
func (h Handler) reply(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r, true)
	if !ok {
		return
	}
	var in reviews.Input
	if e := decode(w, r, &in); e != nil {
		respond(w, nil, e)
		return
	}
	v, e := h.Service.Reply(r.Context(), p, r.PathValue("id"), r.Header.Get("Idempotency-Key"), in)
	respond(w, v, e)
}
func (h Handler) action(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r, true)
	if !ok {
		return
	}
	var in reviews.Action
	if e := decode(w, r, &in); e != nil {
		respond(w, nil, e)
		return
	}
	v, e := h.Service.Action(r.Context(), p, r.PathValue("id"), r.Header.Get("Idempotency-Key"), in)
	respond(w, v, e)
}
func (h Handler) statistics(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r, true)
	if !ok {
		return
	}
	q := r.URL.Query()
	var from, until *time.Time
	for key, dest := range map[string]**time.Time{"from": &from, "until": &until} {
		if q.Get(key) != "" {
			v, e := time.Parse(time.RFC3339, q.Get(key))
			if e != nil {
				respond(w, nil, reviews.ErrInput)
				return
			}
			*dest = &v
		}
	}
	n, e := limit(r)
	if e != nil {
		respond(w, nil, e)
		return
	}
	v, e := h.Service.ReadStatsPage(r.Context(), p, q.Get("kind"), q.Get("target_id"), from, until, q.Get("after"), n)
	respond(w, v, e)
}
func (h Handler) mine(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r, true)
	if !ok {
		return
	}
	n, e := limit(r)
	if e != nil {
		respond(w, nil, e)
		return
	}
	q := r.URL.Query()
	preview, e := flagRequested(r, "include_reply_preview")
	if e != nil {
		respond(w, nil, e)
		return
	}
	featured, e := optionalBool(r, "featured")
	if e != nil {
		respond(w, nil, e)
		return
	}
	hasTeacherReply, e := optionalBool(r, "has_teacher_reply")
	if e != nil {
		respond(w, nil, e)
		return
	}
	teachers, e := flagRequested(r, "include_teacher_ids")
	if e != nil {
		respond(w, nil, e)
		return
	}
	v, e := h.Service.List(r.Context(), p, reviews.Filter{IncludeTeacherIDs: teachers, IncludeReplyPreview: preview, Featured: featured, HasTeacherReply: hasTeacherReply, RatingGroup: q.Get("rating_group"), Owner: p.Subject, Kind: q.Get("kind"), TargetID: q.Get("target_id"), TermID: q.Get("term_id"), TeacherID: q.Get("teacher_id"), Sort: q.Get("sort"), After: q.Get("after"), Limit: n})
	respond(w, v, e)
}
func (h Handler) drafts(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r, true)
	if !ok {
		return
	}
	n, e := limit(r)
	if e != nil {
		respond(w, nil, e)
		return
	}
	v, e := h.Service.Drafts(r.Context(), p, r.URL.Query().Get("after"), n)
	respond(w, v, e)
}
func (h Handler) saveDraft(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r, true)
	if !ok {
		return
	}
	var in struct {
		ID       string        `json:"id"`
		Revision int64         `json:"revision"`
		Input    reviews.Input `json:"input"`
	}
	if e := decode(w, r, &in); e != nil {
		respond(w, nil, e)
		return
	}
	v, e := h.Service.SaveDraft(r.Context(), p, in.ID, r.Header.Get("Idempotency-Key"), in.Input, in.Revision)
	respond(w, v, e)
}
func (h Handler) deleteDraft(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Revision int64 `json:"revision"`
	}
	if e := decode(w, r, &in); e != nil {
		respond(w, nil, e)
		return
	}
	v, e := h.Service.DeleteDraft(r.Context(), p, r.PathValue("id"), r.Header.Get("Idempotency-Key"), in.Revision)
	respond(w, v, e)
}
func (h Handler) votes(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r, true)
	if !ok {
		return
	}
	n, e := limit(r)
	if e != nil {
		respond(w, nil, e)
		return
	}
	v, e := h.Service.MyVotes(r.Context(), p, r.URL.Query().Get("after"), n)
	respond(w, v, e)
}

func (h Handler) references(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r, true)
	if !ok {
		return
	}
	out, err := h.Service.MyReferences(r.Context(), p, strings.Split(r.URL.Query().Get("ids"), ","))
	respond(w, out, err)
}

func optionalBool(r *http.Request, name string) (*bool, error) {
	values, ok := r.URL.Query()[name]
	if !ok {
		return nil, nil
	}
	if len(values) != 1 || values[0] != "true" && values[0] != "false" {
		return nil, reviews.ErrInput
	}
	v := values[0] == "true"
	return &v, nil
}

func flagRequested(r *http.Request, name string) (bool, error) {
	v, err := optionalBool(r, name)
	if err != nil || v == nil {
		return false, err
	}
	return *v, nil
}
