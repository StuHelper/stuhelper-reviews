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
		v, e := h.Service.History(req.Context(), p, req.PathValue("id"), before, n)
		respond(w, v, e)
	})

	m.HandleFunc("GET /api/v1/reviews/presentation", func(w http.ResponseWriter, req *http.Request) {
		v, e := h.Service.Presentation(req.Context(), req.URL.Query().Get("school_id"))
		respond(w, v, e)
	})
	m.HandleFunc("GET /api/v1/reviews/admin/presentation", func(w http.ResponseWriter, req *http.Request) {
		p, ok := h.principal(w, req, true)
		if !ok {
			return
		}
		school := req.URL.Query().Get("school_id")
		if _, e := h.Service.Access.Authorize(req.Context(), p, "configure", school); e != nil {
			respond(w, nil, e)
			return
		}
		v, e := h.Service.Presentation(req.Context(), school)
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
		v, e := h.Service.SavePresentation(req.Context(), p, req.Header.Get("Idempotency-Key"), in)
		respond(w, v, e)
	})
	m.HandleFunc("POST /api/v1/reviews/admin/entries/{id}/attribution", func(w http.ResponseWriter, req *http.Request) {
		p, ok := h.principal(w, req, true)
		if !ok {
			return
		}
		var in struct {
			TeacherID string `json:"teacher_id"`
		}
		if e := decode(w, req, &in); e != nil {
			respond(w, nil, e)
			return
		}
		v, e := h.Service.PreviewAttribution(req.Context(), p, req.PathValue("id"), in.TeacherID)
		respond(w, v, e)
	})
}
