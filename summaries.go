package reviews

import (
	"context"
	"iter"
	"maps"
	"math"
	"slices"
	"strings"
	"time"
)

// RecommendationPolicy contains the accepted product defaults. The five input dimensions
// and the existing equal-course teacher statistic remain unchanged.
type RecommendationPolicy struct {
	Status                string  `json:"status"`
	MinimumSamples        int     `json:"minimum_samples"`
	StronglyRecommendedAt float64 `json:"strongly_recommended_at"`
	RecommendedAt         float64 `json:"recommended_at"`
	AverageAt             float64 `json:"average_at"`
}

func DefaultRecommendationPolicy() RecommendationPolicy {
	return RecommendationPolicy{"Accepted", 3, 4.5, 3.5, 2.5}
}

func (p RecommendationPolicy) valid() bool {
	return p.Status == "Accepted" && p.MinimumSamples >= 1 && p.MinimumSamples <= 1000 &&
		p.AverageAt >= 1 && p.AverageAt < p.RecommendedAt && p.RecommendedAt < p.StronglyRecommendedAt && p.StronglyRecommendedAt <= 5
}

func (p RecommendationPolicy) Validate() error {
	if !p.valid() {
		return ErrInput
	}
	return nil
}

func (p RecommendationPolicy) level(mean float64, samples int) string {
	if samples < p.MinimumSamples {
		return "unrated"
	}
	switch {
	case mean >= p.StronglyRecommendedAt:
		return "strongly_recommended"
	case mean >= p.RecommendedAt:
		return "recommended"
	case mean >= p.AverageAt:
		return "average"
	default:
		return "not_recommended"
	}
}

type RatingDistribution struct {
	Samples int    `json:"samples"`
	Weights [5]int `json:"weights"`
}
type TermSummary struct {
	TermOrderKey   int     `json:"term_order_key"`
	TermID         string  `json:"term_id"`
	TermName       string  `json:"term_name"`
	ReviewCount    int     `json:"review_count"`
	Dimensions     [5]Band `json:"dimensions"`
	Recommendation string  `json:"recommendation"`
}
type ReviewSummary struct {
	TargetID       string               `json:"target_id"`
	Kind           string               `json:"kind"`
	ReviewCount    int                  `json:"review_count"`
	Dimensions     [5]Band              `json:"dimensions"`
	Recommendation string               `json:"recommendation"`
	Distribution   []RatingDistribution `json:"distribution,omitempty"`
	Terms          []TermSummary        `json:"terms,omitempty"`
}
type ReviewSummaries struct {
	Items       []ReviewSummary      `json:"items"`
	Policy      RecommendationPolicy `json:"policy"`
	GeneratedAt time.Time            `json:"generated_at"`
}

// SummaryAggregate is already aggregated by the owning repository. No author,
// review body or enrollment evidence crosses this read boundary.
type SummaryAggregate struct {
	TargetID, CourseID, TermID string
	ReviewCount                int64
	Sums, Counts               [5]int64
	Histogram                  [5][5]int64
}
type SummaryReference struct {
	TermOrderKey                          int
	ID, CanonicalID, SchoolID, Kind, Name string
	Inactive                              bool
}
type SummaryCatalog interface {
	SummaryTargets(context.Context, string, []string) (map[string]SummaryReference, error)
	SummaryAliases(context.Context, string, string) (map[string]string, error)
}
type SummaryTransaction interface {
	SummaryAggregates(SummaryQuery) ([]SummaryAggregate, error)
	OwnReviewOverview(string, time.Time) (OwnOverview, error)
	PendingReviewCourses(string, []PendingCourse, map[string]string, map[string]string, time.Time) (PendingCourses, error)
}

type SummaryQuery struct {
	Kind, SchoolID                            string
	Targets, ExpandedTargets                  []string
	Charts                                    bool
	Now                                       time.Time
	CourseAliases, TargetAliases, TermAliases map[string]string
}

func validSummaryKind(kind string) bool { return kind == "course" || kind == "teacher" }
func canonicalSummaryID(aliases map[string]string, id string) string {
	if canonical := aliases[id]; canonical != "" {
		return canonical
	}
	return id
}

func (s *Service) ReadSummaries(ctx context.Context, p Principal, kind string, ids []string, charts bool) (ReviewSummaries, error) {
	out := ReviewSummaries{Items: []ReviewSummary{}, Policy: s.Config.Recommendations, GeneratedAt: s.now()}
	school, err := s.contentScope(ctx, p, "")
	if err != nil {
		return out, err
	}
	if !validSummaryKind(kind) || len(ids) < 1 || len(ids) > 100 {
		return out, ErrInput
	}
	ids = slices.Clone(ids)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	for _, id := range ids {
		if !validID(id) {
			return out, ErrInput
		}
	}
	catalog, ok := s.Catalog.(SummaryCatalog)
	if !ok {
		return out, ErrUnavailable
	}
	targets, err := s.catalogSummaryTargets(ctx, catalog, kind, ids)
	if err != nil {
		return out, err
	}
	canonical := []string{}
	for _, id := range ids {
		ref, ok := targets[id]
		if !ok || ref.Inactive {
			return out, ErrNotFound
		}
		if ref.SchoolID != school {
			return out, ErrForbidden
		}
		if ref.Kind != kind || ref.ID != id {
			return out, ErrInput
		}
		if ref.CanonicalID != "" {
			id = ref.CanonicalID
		}
		canonical = append(canonical, id)
	}
	slices.Sort(canonical)
	canonical = slices.Compact(canonical)
	metrics, err := s.summaryMetrics(ctx, kind, school, canonical, charts)
	if err != nil {
		return out, err
	}
	byID := map[string]ReviewSummary{}
	for _, metric := range metrics {
		byID[metric.TargetID] = metric
	}
	for _, id := range canonical {
		v, ok := byID[id]
		if !ok {
			v = ReviewSummary{TargetID: id, Kind: kind, Recommendation: "unrated"}
			if charts {
				v.Distribution = make([]RatingDistribution, 5)
				v.Terms = []TermSummary{}
			}
		}
		out.Items = append(out.Items, v)
	}
	return out, nil
}

// DirectoryMetrics reads all nonzero review aggregates for one currently
// authorized school in one query, bounded at 100000 course/target groups. This
// supports sorting an entire catalog without an N+1 read for each visible row.
func (s *Service) DirectoryMetrics(ctx context.Context, p Principal, kind, school string) ([]ReviewSummary, error) {
	if !validSummaryKind(kind) {
		return nil, ErrInput
	}
	scope, err := s.contentScope(ctx, p, school)
	if err != nil {
		return nil, err
	}
	return s.summaryMetrics(ctx, kind, scope, nil, false)
}

func (s *Service) summaryMetrics(ctx context.Context, kind, school string, targets []string, charts bool) ([]ReviewSummary, error) {
	port, ok := s.Catalog.(SummaryCatalog)
	if !ok || !s.Config.Recommendations.valid() {
		return nil, ErrUnavailable
	}
	query := SummaryQuery{Kind: kind, SchoolID: school, Targets: targets, Charts: charts, Now: s.now()}
	targeted := len(targets) > 0
	var err error
	if targeted {
		query.TargetAliases, err = s.requestedAliases(ctx, port, school, kind, targets)
		if err != nil {
			return nil, err
		}
		query.ExpandedTargets = append(slices.Clone(targets), slices.Collect(maps.Keys(query.TargetAliases))...)
		if kind == "course" {
			query.CourseAliases = query.TargetAliases
		}
	} else {
		query.CourseAliases, err = s.catalogSummaryAliases(ctx, port, school, "course")
		if err != nil {
			return nil, err
		}
		query.TargetAliases = query.CourseAliases
		if kind == "teacher" {
			query.TargetAliases, err = s.catalogSummaryAliases(ctx, port, school, "teacher")
			if err != nil {
				return nil, err
			}
		}
		if charts {
			query.TermAliases, err = s.catalogSummaryAliases(ctx, port, school, "term")
			if err != nil {
				return nil, err
			}
		}
	}
	var rows []SummaryAggregate
	err = s.Repo.Transaction(ctx, func(tx Transaction) error {
		reader, ok := tx.(SummaryTransaction)
		if !ok {
			return ErrUnavailable
		}
		var err error
		rows, err = reader.SummaryAggregates(query)
		return err
	})
	if err != nil {
		return nil, err
	}
	if targeted && kind == "teacher" {
		if err = s.canonicalizeSummaryRows(ctx, port, school, "course", rows); err != nil {
			return nil, err
		}
	}
	if targeted && charts {
		if err = s.canonicalizeSummaryRows(ctx, port, school, "term", rows); err != nil {
			return nil, err
		}
	}
	return s.foldSummaries(ctx, port, kind, school, rows, charts, targeted && kind == "teacher")
}

func (s *Service) requestedAliases(ctx context.Context, port SummaryCatalog, school, kind string, targets []string) (map[string]string, error) {
	out := map[string]string{}
	if _, ok := s.Catalog.(ReferenceCatalog); ok {
		for _, canonical := range targets {
			ids, err := s.equivalentIDs(ctx, kind, canonical)
			if err != nil {
				return nil, err
			}
			for _, id := range ids {
				if id != canonical {
					out[id] = canonical
				}
			}
		}
		return out, nil
	}
	// Older adapters expose only the school alias map. Keep just this request's targets.
	aliases, err := s.catalogSummaryAliases(ctx, port, school, kind)
	if err != nil {
		return nil, err
	}
	for alias, canonical := range aliases {
		if _, found := slices.BinarySearch(targets, canonical); found && validID(alias) {
			out[alias] = canonical
		}
	}
	return out, nil
}

// Resolve course and term IDs only after the repository has filtered and
// aggregated the requested targets. Historical IDs absent from catalog stay distinct.
func (s *Service) canonicalizeSummaryRows(ctx context.Context, port SummaryCatalog, school, kind string, rows []SummaryAggregate) error {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		id := row.CourseID
		if kind == "term" {
			id = row.TermID
		}
		if id != "" {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	aliases := map[string]string{}
	for batch := range slices.Chunk(slices.Compact(ids), 100) {
		refs, err := s.catalogSummaryTargets(ctx, port, kind, batch)
		if err != nil {
			return err
		}
		for _, id := range batch {
			ref, ok := refs[id]
			if !ok {
				continue
			}
			if ref.ID != id || ref.Kind != kind || ref.SchoolID != school || ref.CanonicalID != "" && !validID(ref.CanonicalID) {
				return ErrUnavailable
			}
			aliases[id] = ref.CanonicalID
		}
	}
	for i := range rows {
		if kind == "term" {
			rows[i].TermID = canonicalSummaryID(aliases, rows[i].TermID)
		} else {
			rows[i].CourseID = canonicalSummaryID(aliases, rows[i].CourseID)
		}
	}
	return nil
}

type summaryAccumulator struct {
	courses map[string]SummaryAggregate
	terms   map[string]map[string]SummaryAggregate
}

func addSummary(a, b SummaryAggregate) SummaryAggregate {
	a.ReviewCount += b.ReviewCount
	for dimension := range 5 {
		a.Sums[dimension] += b.Sums[dimension]
		a.Counts[dimension] += b.Counts[dimension]
		for level := range 5 {
			a.Histogram[dimension][level] += b.Histogram[dimension][level]
		}
	}
	return a
}

func (s *Service) summarizeCourses(courses iter.Seq[SummaryAggregate], charts bool) ReviewSummary {
	out := ReviewSummary{Recommendation: "unrated"}
	var means [5]float64
	var histogram [5][5]int64
	for course := range courses {
		out.ReviewCount += int(course.ReviewCount)
		for dimension := range 5 {
			if course.Counts[dimension] > 0 {
				means[dimension] += float64(course.Sums[dimension]) / float64(course.Counts[dimension])
				out.Dimensions[dimension].Courses++
				out.Dimensions[dimension].Samples += int(course.Counts[dimension])
			}
			if charts {
				for level := range 5 {
					histogram[dimension][level] += course.Histogram[dimension][level]
				}
			}
		}
	}
	if charts {
		out.Distribution = make([]RatingDistribution, 5)
	}
	for dimension := range 5 {
		band := &out.Dimensions[dimension]
		if charts {
			out.Distribution[dimension].Samples = band.Samples
		}
		if band.Courses > 0 {
			means[dimension] /= float64(band.Courses)
		}
		if band.Samples >= s.Config.Recommendations.MinimumSamples {
			band.Level = int(math.Floor(means[dimension] + 0.5))
			if charts {
				maxCount := slices.Max(histogram[dimension][:])
				if maxCount > 0 {
					for level := range 5 {
						out.Distribution[dimension].Weights[level] = int(math.Round(20 * float64(histogram[dimension][level]) / float64(maxCount)))
					}
				}
			}
		}
	}
	out.Recommendation = s.Config.Recommendations.level(means[0], out.Dimensions[0].Samples)
	return out
}

func (s *Service) foldSummaries(ctx context.Context, catalog SummaryCatalog, kind, school string, rows []SummaryAggregate, charts, mergeCourses bool) ([]ReviewSummary, error) {
	if !charts && !mergeCourses {
		// Repository groups by canonical target/course with an empty term, so
		// each row is one course's complete aggregate. Group contiguous targets
		// without allocating per-target maps or unused chart distributions.
		slices.SortFunc(rows, func(a, b SummaryAggregate) int { return strings.Compare(a.TargetID, b.TargetID) })
		out := make([]ReviewSummary, 0, len(rows))
		for start := 0; start < len(rows); {
			end := start + 1
			for end < len(rows) && rows[end].TargetID == rows[start].TargetID {
				end++
			}
			summary := s.summarizeCourses(slices.Values(rows[start:end]), false)
			summary.TargetID, summary.Kind = rows[start].TargetID, kind
			out = append(out, summary)
			start = end
		}
		return out, nil
	}
	grouped := map[string]*summaryAccumulator{}
	termIDs := []string{}
	for _, row := range rows {
		group := grouped[row.TargetID]
		if group == nil {
			group = &summaryAccumulator{courses: map[string]SummaryAggregate{}, terms: map[string]map[string]SummaryAggregate{}}
			grouped[row.TargetID] = group
		}
		group.courses[row.CourseID] = addSummary(group.courses[row.CourseID], row)
		if charts {
			if group.terms[row.TermID] == nil {
				group.terms[row.TermID] = map[string]SummaryAggregate{}
				termIDs = append(termIDs, row.TermID)
			}
			group.terms[row.TermID][row.CourseID] = addSummary(group.terms[row.TermID][row.CourseID], row)
		}
	}
	termNames := map[string]SummaryReference{}
	if charts && len(termIDs) > 0 {
		slices.Sort(termIDs)
		termIDs = slices.Compact(termIDs)
		if len(termIDs) > 100 {
			return nil, ErrUnavailable
		}
		var err error
		termNames, err = s.catalogSummaryTargets(ctx, catalog, "term", termIDs)
		if err != nil {
			return nil, err
		}
	}
	out := make([]ReviewSummary, 0, len(grouped))
	for id, group := range grouped {
		summary := s.summarizeCourses(maps.Values(group.courses), charts)
		summary.TargetID, summary.Kind = id, kind
		if !charts {
			summary.Distribution = nil
		} else {
			summary.Terms = []TermSummary{}
			for termID, courses := range group.terms {
				v := s.summarizeCourses(maps.Values(courses), false)
				name := ""
				if term := termNames[termID]; term.Kind == "term" && term.SchoolID == school {
					name = term.Name
				}
				summary.Terms = append(summary.Terms, TermSummary{TermOrderKey: termNames[termID].TermOrderKey, TermID: termID, TermName: name, ReviewCount: v.ReviewCount, Dimensions: v.Dimensions, Recommendation: v.Recommendation})
			}
			slices.SortFunc(summary.Terms, func(a, b TermSummary) int {
				if a.TermOrderKey != b.TermOrderKey {
					return b.TermOrderKey - a.TermOrderKey
				}
				return strings.Compare(a.TermID, b.TermID)
			})
		}
		out = append(out, summary)
	}
	slices.SortFunc(out, func(a, b ReviewSummary) int { return strings.Compare(a.TargetID, b.TargetID) })
	return out, nil
}
