package reviews

import (
	"context"
	"encoding/base64"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/StuHelper/stuhelper-reviews/internal/activityday"
)

const ActivityEventType = "discovery.activity.v2"

type ActivityTarget struct {
	Source string `json:"source"`
	Kind   string `json:"kind"`
	ID     string `json:"id"`
}

// Activity is a complete provider-owned Beijing daily snapshot, including zeros.
type Activity struct {
	Source   string           `json:"source"`
	ID       string           `json:"id"`
	Target   ActivityTarget   `json:"target"`
	Day      string           `json:"day"`
	Revision int64            `json:"revision"`
	Visible  bool             `json:"visible"`
	Metrics  map[string]int64 `json:"metrics"`
}
type ActivityReader interface {
	// ActivitySnapshot uses an inclusive start date and an exclusive end date
	// in Asia/Shanghai. The time of day within each supplied date is ignored.
	ActivitySnapshot(context.Context, time.Time, time.Time, string, int) ([]Activity, string, error)
	ReadActivity(context.Context, string) (Activity, error)
}

// ActivityID uses the Beijing date containing the supplied instant.
func ActivityID(kind, id string, day time.Time) string {
	return kind + "/" + base64.RawURLEncoding.EncodeToString([]byte(id)) + "/" + activityday.Date(day).Format(time.DateOnly)
}

// ParseActivityID returns the target and the start of its Beijing activity day.
func ParseActivityID(id string) (ActivityTarget, time.Time, error) {
	parts := strings.Split(id, "/")
	if len(parts) != 3 || (parts[0] != "course" && parts[0] != "teacher") {
		return ActivityTarget{}, time.Time{}, ErrInput
	}
	raw, e := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if e != nil || len(raw) == 0 || len(raw) > 200 || !utf8.Valid(raw) {
		return ActivityTarget{}, time.Time{}, ErrInput
	}
	day, e := time.ParseInLocation(time.DateOnly, parts[2], activityday.Location)
	if e != nil || day.Year() < 1 || id != ActivityID(parts[0], string(raw), day) {
		return ActivityTarget{}, time.Time{}, ErrInput
	}
	return ActivityTarget{Source: "catalog", Kind: parts[0], ID: string(raw)}, day, nil
}

// ActivityDay reports whether an instant is in the supported Beijing date range.
func ActivityDay(t time.Time) bool {
	date := activityday.Date(t)
	return !t.IsZero() && date.Year() >= 1 && date.Year() <= 9999
}

// ActivityDisclosureReader limits live checks for replies whose visibility needs
// current public identity proof; IDs are internal and never enter activity events.
type ActivityDisclosureReader interface {
	ActivityDisclosureEntries(context.Context, string, int) ([]string, error)
}
