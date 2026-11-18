package httpapi

import (
	r "github.com/StuHelper/stuhelper-reviews"
	"net/http"
	"strconv"
)

func (h Handler) managementRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/v1/reviews/admin/entries/{id}/history", func(w http.ResponseWriter, req *http.Request) {
		p, ok := h.principal(w, req, true)
		if !ok {
			return
		}
		n, e := limit(req)
		if e != nil {
			respond(w, nil, e)
			return
		}
		var before int64
		if v := req.URL.Query().Get("before"); v != "" {
			before, e = strconv.ParseInt(v, 10, 64)
			if e != nil {
				respond(w, nil, r.ErrInput)
				return
			}
		}
		includeDetails, e := flagRequested(req, "include_moderation_details")
		if e != nil {
			respond(w, nil, e)
			return
		}
		v, e := h.Service.History(req.Context(), p, req.PathValue("id"), before, n)
		if !includeDetails {
			for i := range v {
				omitModerationDetails(&v[i])
			}
		}
		respond(w, v, e)
	})

	m.HandleFunc("GET /api/v1/reviews/presentation", func(w http.ResponseWriter, req *http.Request) {
		p, ok := h.principal(w, req, true)
		if !ok {
			return
		}
		v, e := h.Service.ReadPresentation(req.Context(), p, req.URL.Query().Get("school_id"))
		respond(w, v, e)
	})
	m.HandleFunc("GET /api/v1/reviews/admin/presentation", func(w http.ResponseWriter, req *http.Request) {
		p, ok := h.principal(w, req, true)
		if !ok {
			return
		}
		v, e := h.Service.AdminPresentation(req.Context(), p, req.URL.Query().Get("school_id"))
		respond(w, v, e)
	})
	m.HandleFunc("PUT /api/v1/reviews/admin/presentation", func(w http.ResponseWriter, req *http.Request) {
		p, ok := h.principal(w, req, true)
		if !ok {
			return
		}
		var in r.Presentation
		if e := decode(w, req, &in); e != nil {
			respond(w, nil, e)
			return
		}
		if schools, present := req.URL.Query()["school_id"]; present && (len(schools) != 1 || schools[0] != in.SchoolID) {
			respond(w, nil, r.ErrInput)
			return
		}
		v, e := h.Service.SavePresentation(req.Context(), p, req.Header.Get("Idempotency-Key"), in)
		respond(w, v, e)
	})
	m.HandleFunc("POST /api/v1/reviews/admin/entries/{id}/attribution", func(w http.ResponseWriter, req *http.Request) {
		p, ok := h.principal(w, req, true)
		if !ok {
			return
		}
		if !r.ValidIdempotencyKey(req.Header.Get("Idempotency-Key")) {
			respond(w, nil, r.ErrInput)
			return
		}
		var in struct {
			TeacherID *string `json:"teacher_id"`
		}
		if e := decode(w, req, &in); e != nil {
			respond(w, nil, e)
			return
		}
		if in.TeacherID == nil {
			respond(w, nil, r.ErrInput)
			return
		}
		v, e := h.Service.PreviewAttribution(req.Context(), p, req.PathValue("id"), *in.TeacherID)
		respond(w, v, e)
	})
}
