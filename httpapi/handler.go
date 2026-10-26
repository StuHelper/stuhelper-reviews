// Package httpapi exposes the review contract without trusting client identity fields.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	reviews "github.com/StuHelper/stuhelper-reviews"
)

type Authenticate func(*http.Request) (reviews.Principal, error)
type Handler struct {
	Service      *reviews.Service
	Authenticate Authenticate
}

func (h Handler) Routes() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /api/v1/reviews", h.list)
	m.HandleFunc("POST /api/v1/reviews", h.create)
	m.HandleFunc("GET /api/v1/reviews/{id}", h.get)
	m.HandleFunc("POST /api/v1/reviews/{id}/actions", h.action)
	m.HandleFunc("POST /api/v1/reviews/{id}/replies", h.reply)
	m.HandleFunc("GET /api/v1/reviews/statistics", h.statistics)
	m.HandleFunc("GET /api/v1/reviews/me/entries", h.mine)
	m.HandleFunc("GET /api/v1/reviews/me/drafts", h.drafts)
	m.HandleFunc("POST /api/v1/reviews/me/drafts", h.saveDraft)
	m.HandleFunc("DELETE /api/v1/reviews/me/drafts/{id}", h.deleteDraft)
	m.HandleFunc("GET /api/v1/reviews/me/bookmarks", h.bookmarks)
	m.HandleFunc("POST /api/v1/reviews/me/bookmarks", h.setBookmark)
	m.HandleFunc("GET /api/v1/reviews/me/votes", h.votes)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method != "GET" && r.Header.Get("Origin") != "" {
			u, e := url.Parse(r.Header.Get("Origin"))
			if e != nil || u.Host != r.Host {
				respond(w, nil, reviews.ErrForbidden)
				return
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		m.ServeHTTP(w, r.WithContext(ctx))
	})
}
func respond(w http.ResponseWriter, v any, e error) {
	w.Header().Set("Content-Type", "application/json")
	if e != nil {
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
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": code})
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		return reviews.ErrInput
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return reviews.ErrInput
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
		return 0, reviews.ErrInput
	}
	return n, nil
}
func (h Handler) list(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r, false)
	if !ok {
		return
	}
	n, e := limit(r)
	if e != nil {
		respond(w, nil, e)
		return
	}
	q := r.URL.Query()
	v, e := h.Service.List(r.Context(), p, reviews.Filter{Kind: q.Get("kind"), TargetID: q.Get("target_id"), TermID: q.Get("term_id"), RootID: q.Get("root_id"), After: q.Get("after"), Limit: n})
	respond(w, v, e)
}
func (h Handler) get(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r, false)
	if !ok {
		return
	}
	v, e := h.Service.Get(r.Context(), p, r.PathValue("id"))
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
	v, e := h.Service.StatsPage(r.Context(), q.Get("kind"), q.Get("target_id"), from, until, q.Get("after"), n)
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
	v, e := h.Service.List(r.Context(), p, reviews.Filter{Owner: p.Subject, After: r.URL.Query().Get("after"), Limit: n})
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
func (h Handler) bookmarks(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r, true)
	if !ok {
		return
	}
	n, e := limit(r)
	if e != nil {
		respond(w, nil, e)
		return
	}
	v, e := h.Service.Bookmarks(r.Context(), p, r.URL.Query().Get("after"), n)
	respond(w, v, e)
}
func (h Handler) setBookmark(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r, true)
	if !ok {
		return
	}
	var in struct {
		TargetID string `json:"target_id"`
		Set      bool   `json:"set"`
	}
	if e := decode(w, r, &in); e != nil {
		respond(w, nil, e)
		return
	}
	v, e := h.Service.SetBookmark(r.Context(), p, in.TargetID, r.Header.Get("Idempotency-Key"), in.Set)
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
