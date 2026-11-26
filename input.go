package reviews

import (
	"bytes"
	"encoding/json"
	"slices"
)

const maxSelectedTeachers = 100

type Grade struct {
	Scale string          `json:"scale"`
	Value json.RawMessage `json:"value,omitempty"`
}

func (g Grade) valid() bool {
	switch g.Scale {
	case "percent":
		var value *int
		return json.Unmarshal(g.Value, &value) == nil && value != nil && *value >= 0 && *value <= 100
	case "five_level", "two_level":
		var value string
		if json.Unmarshal(g.Value, &value) != nil {
			return false
		}
		return value == "pass" || value == "fail" ||
			g.Scale == "five_level" && slices.Contains([]string{"excellent", "good", "medium"}, value)
	case "withdrawn":
		return len(g.Value) == 0
	default:
		return false
	}
}

func validateTeacherSelection(ids []string) error {
	if len(ids) > maxSelectedTeachers {
		return inputFieldErr("invalid_teacher_ids", "teacher_ids")
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !validID(id) || seen[id] {
			return inputFieldErr("invalid_teacher_ids", "teacher_ids")
		}
		seen[id] = true
	}
	return nil
}

func (v Input) sortedTeachers() Input {
	v.TeacherIDs = slices.Clone(v.TeacherIDs)
	slices.Sort(v.TeacherIDs)
	return v
}

// encoding/json accepts short/long arrays for [5]int; enforce the API's exact shape.
func (v *Input) UnmarshalJSON(raw []byte) error {
	var a struct {
		Disclosures []string        `json:"disclosures"`
		Kind        string          `json:"kind"`
		TargetID    string          `json:"target_id"`
		ParentID    string          `json:"parent_id"`
		TermID      string          `json:"term_id"`
		TeacherIDs  json.RawMessage `json:"teacher_ids"`
		Grade       json.RawMessage `json:"grade"`
		Title       string          `json:"title"`
		Body        string          `json:"body"`
		Ratings     []int           `json:"ratings"`
		Signed      bool            `json:"signed"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&a) != nil || len(a.Ratings) != 5 {
		return ErrInput
	}
	var teacherIDs []string
	if len(a.TeacherIDs) > 0 && (json.Unmarshal(a.TeacherIDs, &teacherIDs) != nil || teacherIDs == nil) {
		return inputFieldErr("invalid_teacher_ids", "teacher_ids")
	}
	var grade *Grade
	if len(a.Grade) > 0 {
		d := json.NewDecoder(bytes.NewReader(a.Grade))
		d.DisallowUnknownFields()
		if d.Decode(&grade) != nil || grade == nil {
			return inputFieldErr("invalid_grade", "grade")
		}
	}
	*v = Input{Disclosures: a.Disclosures, Kind: a.Kind, TargetID: a.TargetID, ParentID: a.ParentID, TermID: a.TermID, TeacherIDs: teacherIDs, Grade: grade, Title: a.Title, Body: a.Body, Signed: a.Signed}
	copy(v.Ratings[:], a.Ratings)
	return nil
}
