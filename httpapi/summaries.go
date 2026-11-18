package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	r "github.com/StuHelper/stuhelper-reviews"
)

func (h Handler) summaries(w http.ResponseWriter, req *http.Request) {
	p, ok := h.principal(w, req, true)
	if !ok {
		return
	}
	query := req.URL.Query()
	charts := false
	if value := query.Get("include_charts"); value != "" {
		var err error
		charts, err = strconv.ParseBool(value)
		if err != nil {
			respond(w, nil, r.ErrInput)
			return
		}
	}
	v, err := h.Service.ReadSummaries(req.Context(), p, query.Get("kind"), strings.Split(query.Get("ids"), ","), charts)
	respond(w, v, err)
}

func (h Handler) overview(w http.ResponseWriter, req *http.Request) {
	p, ok := h.principal(w, req, true)
	if !ok {
		return
	}
	q := req.URL.Query()
	details, err := flagRequested(req, "include_own_context")
	q.Del("include_own_context")
	if err != nil || len(q) != 0 {
		respond(w, nil, r.ErrInput)
		return
	}
	v, err := h.Service.OwnOverview(req.Context(), p)
	if !details {
		for i := range v.PendingCourses.Items {
			v.PendingCourses.Items[i].DraftID = ""
		}
	}
	respond(w, v, err)
}
