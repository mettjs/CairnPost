package suppress

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/mettjs/cairnpost/internal/store"
)

// List answers a page of the suppressions in force, of one domain or of
// all (""), and the cursor of the page after it, "" when there is none.
func (s *Service) List(ctx context.Context, domain, cursor string, limit int) ([]store.Suppression, string, error) {
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		return nil, "", &Invalid{fmt.Sprintf(`"limit" must be at most %d.`, MaxLimit)}
	}
	if domain != "" {
		var err error
		if domain, err = domainOf(domain); err != nil {
			return nil, "", err
		}
	}
	var after *store.Suppression
	if cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		d, a, cut := strings.Cut(string(raw), "\x00")
		if err != nil || !cut || !plain(d) || !plain(a) {
			return nil, "", &Invalid{`"after" is not a cursor this list gave.`}
		}
		after = &store.Suppression{Domain: d, Address: a}
	}
	// One more than asked for says whether another page follows.
	page, err := s.store.ListSuppressions(ctx, domain, after, limit+1)
	if err != nil {
		return nil, "", fmt.Errorf("suppress: list: %w", err)
	}
	if len(page) <= limit {
		return page, "", nil
	}
	last := page[limit-1]
	return page[:limit], base64.RawURLEncoding.EncodeToString([]byte(last.Domain + "\x00" + last.Address)), nil
}
