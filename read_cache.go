package reviews

import "context"

type disclosureReadKey struct{ Subject, School, Course, Term string }
type disclosureReadResult struct {
	Facts DisclosureFacts
	Err   error
}
type disclosureReadCache struct {
	provider DisclosureProvider
	results  map[disclosureReadKey]disclosureReadResult
}

func (c *disclosureReadCache) Facts(ctx context.Context, p Principal, school, course, term string) (DisclosureFacts, error) {
	key := disclosureReadKey{p.Subject, school, course, term}
	if v, ok := c.results[key]; ok {
		return v.Facts, v.Err
	}
	facts, err := c.provider.Facts(ctx, p, school, course, term)
	c.results[key] = disclosureReadResult{facts, err}
	return facts, err
}

// Current disclosure results live only for one read. Count and preview use the
// same authorized facts, including when the same author replied many times.
func (s *Service) withReadCache() *Service {
	copy := *s
	if s.Disclosures != nil {
		copy.Disclosures = &disclosureReadCache{s.Disclosures, map[disclosureReadKey]disclosureReadResult{}}
	}
	return &copy
}
