package httpapi

import (
	"net/http"

	reviews "github.com/StuHelper/stuhelper-reviews"
)

func parseOptionalBool(r *http.Request, name string) (*bool, error) {
	values, ok := r.URL.Query()[name]
	if !ok {
		return nil, nil
	}
	if len(values) != 1 || (values[0] != "true" && values[0] != "false") {
		return nil, reviews.ErrInput
	}
	v := values[0] == "true"
	return &v, nil
}

func (h Handler) adminGet(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r, true)
	if !ok {
		return
	}
	includeDetails, e := flagRequested(r, "include_moderation_details")
	if e != nil {
		respond(w, nil, e)
		return
	}
	v, e := h.Service.AdminGet(r.Context(), p, r.PathValue("id"))
	if !includeDetails {
		omitModerationDetails(&v)
	}
	respond(w, v, e)
}
func (h Handler) adminList(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r, true)
	if !ok {
		return
	}
	n, e := limit(r)
	if e != nil {
		respond(w, nil, e)
		return
	}
	reviewRequested, e := parseOptionalBool(r, "review_requested")
	if e != nil {
		respond(w, nil, e)
		return
	}
	featured, e := parseOptionalBool(r, "featured")
	if e != nil {
		respond(w, nil, e)
		return
	}
	q := r.URL.Query()
	includeDetails, e := flagRequested(r, "include_moderation_details")
	if e != nil {
		respond(w, nil, e)
		return
	}
	v, e := h.Service.AdminListFiltered(r.Context(), p, reviews.AdminFilter{
		SchoolID:        q.Get("school_id"),
		Kind:            q.Get("kind"),
		State:           q.Get("state"),
		TargetID:        q.Get("target_id"),
		RootID:          q.Get("root_id"),
		Sort:            q.Get("sort"),
		ReviewRequested: reviewRequested,
		Featured:        featured,
		After:           q.Get("after"),
		Limit:           n,
	})
	if !includeDetails {
		for i := range v.Items {
			omitModerationDetails(&v.Items[i])
		}
	}
	respond(w, v, e)
}
func (h Handler) adminBatchApprove(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r, true)
	if !ok {
		return
	}
	var in reviews.AdminBatchApproveRequest
	if e := decode(w, r, &in); e != nil {
		respond(w, nil, e)
		return
	}
	v, e := h.Service.BatchApprove(r.Context(), p, r.Header.Get("Idempotency-Key"), in)
	respond(w, v, e)
}

// Older clients validate AdminEntry against a closed response schema.
func omitModerationDetails(v *reviews.AdminEntry) {
	v.ModerationReasonCode = ""
	v.ReviewRequested = false
	v.ParentID = ""
	v.UpdatedAt = nil
}
