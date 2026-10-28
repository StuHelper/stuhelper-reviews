// Package reviews owns course reviews, mentor comments and discussion interactions.
package reviews

import (
	"context"
	"errors"
	"time"
)

var (
	ErrUnauthenticated = errors.New("unauthenticated")
	ErrInput           = errors.New("invalid_input")
	ErrNotFound        = errors.New("not_found")
	ErrForbidden       = errors.New("forbidden")
	ErrConflict        = errors.New("conflict")
	ErrUnavailable     = errors.New("unavailable")
	ErrWindow          = errors.New("edit_window_closed")
	ErrEnrollment      = errors.New("enrollment_required")
)

type Principal struct{ Subject string }
type Target struct {
	ID, SchoolID, Kind string
	Inactive           bool
}
type Evidence struct {
	ID         string    `json:"id"`
	TermID     string    `json:"term_id"`
	TeacherIDs []string  `json:"teacher_ids"`
	SourceID   string    `json:"source_id"`
	Revision   int64     `json:"revision"`
	FetchedAt  time.Time `json:"fetched_at"`
}

// Catalog returns only evidence for the authenticated subject and requested course/term.
type Catalog interface {
	Target(context.Context, string, string) (Target, error)
	Evidence(context.Context, Principal, string, string) ([]Evidence, error)
}

// Access evaluates the current action configuration and facts on every request.
// EnrollmentRequired is read from the same effective rule version as Authorize.
type Grant struct {
	RuleVersion        string
	EnrollmentRequired bool
	Requirements       []Requirement
}
type Requirement struct {
	Key   string `json:"key"`
	State string `json:"state"`
}
type Composer struct {
	Disclosures  []DisclosureOption `json:"disclosures,omitempty"`
	TeacherReply bool               `json:"teacher_reply,omitempty"`
	Allowed      bool               `json:"allowed"`
	State        string             `json:"state"`
	Requirements []Requirement      `json:"requirements"`
	TeacherIDs   []string           `json:"teacher_ids"`
	Identity     string             `json:"identity"`
	RuleVersion  string             `json:"rule_version"`
}
type PublishDraft struct {
	Revision int64 `json:"revision"`
	Input    Input `json:"input"`
}
type Access interface {
	Authorize(context.Context, Principal, string, string) (Grant, error)
}
type Candidate struct {
	ID, Kind, SchoolID, Subject, Title, Body string
	Revision                                 int64
}
type Moderation interface {
	Check(context.Context, Candidate) (string, error)
}
type Profile struct {
	Name   string `json:"name"`
	Avatar string `json:"avatar,omitempty"`
}
type Profiles interface {
	Lookup(context.Context, string) (Profile, error)
}

type Input struct {
	Disclosures []string `json:"disclosures,omitempty"`
	Kind        string   `json:"kind"`
	TargetID    string   `json:"target_id"`
	TermID      string   `json:"term_id,omitempty"`
	TeacherID   string   `json:"teacher_id,omitempty"`
	Title       string   `json:"title"`
	Body        string   `json:"body"`
	Ratings     [5]int   `json:"ratings"`
	Signed      bool     `json:"signed"`
}

// Entry is a private persistence model. Public responses always use View.
type Entry struct {
	ChangeReason     string      `json:"change_reason,omitempty"`
	ChangeOrigin     string      `json:"change_origin,omitempty"`
	Featured         bool        `json:"featured,omitempty"`
	TeacherReply     bool        `json:"teacher_reply,omitempty"`
	DisclosureCourse string      `json:"disclosure_course,omitempty"`
	DisclosureTerm   string      `json:"disclosure_term,omitempty"`
	ID               string      `json:"id"`
	RootID           string      `json:"root_id"`
	ParentID         string      `json:"parent_id,omitempty"`
	Owner            string      `json:"owner"`
	SchoolID         string      `json:"school_id"`
	Input            Input       `json:"input"`
	TeacherIDs       []string    `json:"teacher_ids"`
	Evidence         []Evidence  `json:"evidence"`
	State            string      `json:"state"`
	Revision         int64       `json:"revision"`
	FirstPublishedAt *time.Time  `json:"first_published_at"`
	CreatedAt        time.Time   `json:"created_at"`
	UpdatedAt        time.Time   `json:"updated_at"`
	ExpiresAt        *time.Time  `json:"expires_at"`
	RuleVersion      string      `json:"rule_version"`
	Projection       *Projection `json:"-"`
	ReviewRequested  bool        `json:"review_requested"`
}
type Projection struct {
	Identity                   Identity
	Upvotes, Downvotes, MyVote int
}
type Identity struct {
	RootID, Subject, Handle string
	Signed                  bool
}
type Author struct {
	Disclosures []DisclosureOption `json:"disclosures,omitempty"`
	Anonymous   bool               `json:"anonymous"`
	Handle      string             `json:"handle"`
	Profile     *Profile           `json:"profile,omitempty"`
}
type View struct {
	Featured    bool       `json:"featured,omitempty"`
	CanEdit     bool       `json:"can_edit"`
	State       string     `json:"state"`
	ID          string     `json:"id"`
	RootID      string     `json:"root_id"`
	ParentID    string     `json:"parent_id,omitempty"`
	Kind        string     `json:"kind"`
	SchoolID    string     `json:"school_id"`
	TargetID    string     `json:"target_id"`
	TermID      string     `json:"term_id,omitempty"`
	TeacherID   string     `json:"teacher_id,omitempty"`
	Title       string     `json:"title"`
	Body        string     `json:"body"`
	Ratings     [5]int     `json:"ratings"`
	Preview     bool       `json:"preview"`
	Author      Author     `json:"author"`
	Revision    int64      `json:"revision"`
	PublishedAt *time.Time `json:"published_at"`
	Upvotes     int        `json:"upvotes"`
	Downvotes   int        `json:"downvotes"`
	MyVote      int        `json:"my_vote"`
	Mine        bool       `json:"mine"`
}
type Result struct {
	ID        string     `json:"id"`
	State     string     `json:"state"`
	Revision  int64      `json:"revision"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}
type Action struct {
	Action   string `json:"action"`
	Revision int64  `json:"revision"`
	Input    *Input `json:"input,omitempty"`
	Vote     int    `json:"vote,omitempty"`
}
type Draft struct {
	ID        string    `json:"id"`
	Revision  int64     `json:"revision"`
	Owner     string    `json:"owner"`
	Input     Input     `json:"input"`
	UpdatedAt time.Time `json:"updated_at"`
}
type DraftView struct {
	ID        string     `json:"id"`
	Revision  int64      `json:"revision"`
	Input     Input      `json:"input"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	Withdrawn bool       `json:"withdrawn"`
	RootID    string     `json:"root_id,omitempty"`
	ParentID  string     `json:"parent_id,omitempty"`
}
type Filter struct {
	EquivalentTargets, EquivalentTeachers                []string
	Kind, TargetID, TermID, RootID, Owner, Viewer, After string
	TeacherID, Sort, CursorID                            string
	CursorTime                                           *time.Time
	Now                                                  time.Time
	Limit                                                int
}
type Page struct {
	Items []View `json:"items"`
	Next  string `json:"next"`
}
type AdminEntry struct {
	ChangeReason string     `json:"change_reason,omitempty"`
	ChangeOrigin string     `json:"change_origin,omitempty"`
	Featured     bool       `json:"featured,omitempty"`
	ID           string     `json:"id"`
	RootID       string     `json:"root_id"`
	SchoolID     string     `json:"school_id"`
	Input        Input      `json:"input"`
	AuthorHandle string     `json:"author_handle"`
	Anonymous    bool       `json:"anonymous"`
	State        string     `json:"state"`
	Revision     int64      `json:"revision"`
	CreatedAt    time.Time  `json:"created_at"`
	PublishedAt  *time.Time `json:"published_at"`
}
type AdminPage struct {
	Items []AdminEntry `json:"items"`
	Next  string       `json:"next"`
}
type Band struct {
	Level   int `json:"level"`
	Samples int `json:"samples"`
	Courses int `json:"courses"`
}
type CourseStatistic struct {
	CourseID   string  `json:"course_id"`
	Dimensions [5]Band `json:"dimensions"`
}
type Statistics struct {
	Courses    []CourseStatistic `json:"courses"`
	Next       string            `json:"next"`
	Dimensions [5]Band           `json:"dimensions"`
	From       *time.Time        `json:"from"`
	Until      *time.Time        `json:"until"`
}
type Aggregate struct {
	CourseID string
	Sums     [5]float64
	Counts   [5]int
}
type Replay struct {
	Digest   string
	Result   Result
	Until    time.Time
	ObjectID string
}
type Event struct {
	ID, Type, Subject, SchoolID, Actor string
	Revision                           int64
	At                                 time.Time
}
type DeliveryEvent struct {
	ID      string `json:"id"`
	Subject string `json:"subject"`
	Type    string `json:"type"`
	Data    struct {
		Revision int64 `json:"revision"`
	} `json:"data"`
	Attempts     int        `json:"-"`
	FirstFailure *time.Time `json:"-"`
}
type Inspection struct {
	ID, Kind, SchoolID, Owner string
	Revision                  int64
	Visible                   bool
}
type Decision struct {
	TeacherID        string  `json:"teacher_id,omitempty"`
	EvidenceRevision string  `json:"evidence_revision,omitempty"`
	ID               string  `json:"id"`
	ObjectID         string  `json:"object_id"`
	Revision         int64   `json:"revision"`
	Action           string  `json:"action"`
	Body             *string `json:"body,omitempty"`
	Subject          string  `json:"subject,omitempty"`
	AuthorHandle     string  `json:"author_handle,omitempty"`
	Signed           bool    `json:"signed"`
	Reason           string  `json:"reason"`
}
type Repository interface {
	Transaction(context.Context, func(Transaction) error) error
}
type Transaction interface {
	History(string, int64, int) ([]Entry, error)
	Presentation(string) (Presentation, error)
	SavePresentation(Presentation) error
	Lock(string) error
	Entry(string) (Entry, error)
	SaveEntry(Entry) error
	DeleteEntry(Entry) error
	List(Filter) ([]Entry, error)
	Expired(time.Time, int) ([]Entry, error)
	Identity(root, subject string) (Identity, error)
	IdentityByHandle(root, handle string) (Identity, error)
	SaveIdentity(Identity) error
	Vote(id, subject string) (int, error)
	SetVote(id, subject string, value int) error
	VoteCounts(id string) (int, int, error)
	Voted(subject, after string, limit int, now time.Time) ([]Entry, error)
	Bookmark(subject, target string, set bool) error
	Bookmarks(subject, after string, limit int) ([]string, error)
	Draft(string) (Draft, error)
	SaveDraft(Draft) error
	DeleteDraft(string) error
	Drafts(subject, after string, limit int) ([]Draft, error)
	Withdrawals(subject, after string, limit int) ([]Entry, error)
	Replay(string) (Replay, error)
	SaveReplay(string, Replay) error
	Aggregates(kind string, targets []string, from, until *time.Time) ([]Aggregate, error)
	Event(Event) error
	AuditRead(Event) error
	AdminEntries(school, kind, state, after string, limit int, now time.Time) ([]Entry, error)
	Events(time.Time, int) ([]DeliveryEvent, error)
	CompleteEvent(string) error
	FailEvent(string, int, time.Time, time.Time, bool) error
}
