package httpapi

import "net/http"

func (h Handler) adminGet(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r, true)
	if !ok {
		return
	}
	v, e := h.Service.AdminGet(r.Context(), p, r.PathValue("id"))
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
	q := r.URL.Query()
	v, e := h.Service.AdminList(r.Context(), p, q.Get("school_id"), q.Get("kind"), q.Get("state"), q.Get("after"), n)
	respond(w, v, e)
}
