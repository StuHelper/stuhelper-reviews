package httpapi

import (
	reviews "github.com/StuHelper/stuhelper-reviews"
	"net/http"
)

func (h Handler) compose(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r, true)
	if !ok {
		return
	}
	q := r.URL.Query()
	v, e := h.Service.Composer(r.Context(), p, q.Get("kind"), q.Get("target_id"), q.Get("term_id"), q.Get("parent_id"))
	respond(w, v, e)
}
func (h Handler) ownEntry(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r, true)
	if !ok {
		return
	}
	teachers, e := flagRequested(r, "include_teacher_ids")
	if e != nil {
		respond(w, nil, e)
		return
	}
	includeDetails, e := flagRequested(r, "include_moderation_details")
	if e != nil {
		respond(w, nil, e)
		return
	}
	v, e := h.Service.GetWithOptions(r.Context(), p, r.PathValue("id"), reviews.ViewOptions{Own: true, IncludeTeacherIDs: teachers})
	if !includeDetails {
		v.ModerationReasonCode = ""
	}
	respond(w, v, e)
}
func (h Handler) getDraft(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r, true)
	if !ok {
		return
	}
	v, e := h.Service.GetDraft(r.Context(), p, r.PathValue("id"))
	respond(w, v, e)
}
func (h Handler) publishDraft(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r, true)
	if !ok {
		return
	}
	var in reviews.PublishDraft
	if e := decode(w, r, &in); e != nil {
		respond(w, nil, e)
		return
	}
	v, e := h.Service.PublishDraft(r.Context(), p, r.PathValue("id"), r.Header.Get("Idempotency-Key"), in)
	respond(w, v, e)
}
