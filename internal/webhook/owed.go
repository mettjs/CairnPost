package webhook

import (
	"context"
	"fmt"

	"github.com/mettjs/cairnpost/internal/store"
)

// OwedMost is where counting what an endpoint is owed stops: one paused
// for weeks is owed more than is worth counting for a page.
const OwedMost = 10000

// Owed answers how many webhooks each of some endpoints is owed, up to
// OwedMost: those waiting to be tried and, for a paused one, those given
// up while it failed, which are sent when it is resumed. The endpoints
// are ones the caller was given, so it may know of them.
func (s *Service) Owed(ctx context.Context, ends []store.Endpoint) (map[string]int, error) {
	ids := make([]string, len(ends))
	for i, e := range ends {
		ids[i] = e.ID
	}
	owed, err := s.store.Owed(ctx, ids, OwedMost)
	if err != nil {
		return nil, fmt.Errorf("webhook: count what endpoints are owed: %w", err)
	}
	return owed, nil
}
