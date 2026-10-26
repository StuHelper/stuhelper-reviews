package reviews

import (
	"bytes"
	"encoding/json"
)

// encoding/json accepts short/long arrays for [5]int; enforce the API's exact shape.
func (v *Input) UnmarshalJSON(raw []byte) error {
	type plain Input
	var a struct {
		Kind      string `json:"kind"`
		TargetID  string `json:"target_id"`
		TermID    string `json:"term_id"`
		TeacherID string `json:"teacher_id"`
		Title     string `json:"title"`
		Body      string `json:"body"`
		Ratings   []int  `json:"ratings"`
		Signed    bool   `json:"signed"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&a) != nil || len(a.Ratings) != 5 {
		return ErrInput
	}
	*v = Input{Kind: a.Kind, TargetID: a.TargetID, TermID: a.TermID, TeacherID: a.TeacherID, Title: a.Title, Body: a.Body, Signed: a.Signed}
	copy(v.Ratings[:], a.Ratings)
	return nil
}
