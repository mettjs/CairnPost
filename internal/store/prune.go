package store

import (
	"context"
	"time"
)

// Pruning forgets what has been kept long enough. Each call deletes at most
// limit rows, oldest first, skipping any row another transaction holds, and
// answers how many went: a caller repeats it until fewer than limit do.
type Pruning interface {
	// PruneContents deletes what emails said, once the time each was kept
	// for is up. That time was fixed when it was kept, so a pruner needs
	// no setting of its own, and one process never deletes what another
	// was told to keep.
	PruneContents(ctx context.Context, limit int) (int, error)
	// PruneDetails does the same for what receiving servers said.
	PruneDetails(ctx context.Context, limit int) (int, error)
	// PruneSessions deletes console sessions whose time is up.
	PruneSessions(ctx context.Context, limit int) (int, error)
	// PruneKeys forgets Idempotency-Keys remembered for ttl or longer: the
	// ones a new request would take over anyway. A message one of them
	// left with no answer is recorded as unknown, as a takeover would.
	PruneKeys(ctx context.Context, ttl time.Duration, limit int) (int, error)
	// Horizon is the store's own time, keep ago: the instant the other
	// prunes of one pass are measured against, so they agree.
	Horizon(ctx context.Context, keep time.Duration) (time.Time, error)
	// PruneMessages deletes messages nothing has happened to since a time
	// (sent, or an event recorded), with their events and the webhook
	// deliveries owed for them. A message an Idempotency-Key still points
	// at is left until the key is gone. A suppression one of them caused
	// stays, no longer naming it.
	PruneMessages(ctx context.Context, before time.Time, limit int) (int, error)
	// PruneLifted deletes suppressions lifted at or before a time. A lifted
	// row is what stops a late event about an email sent before the lift
	// from suppressing again, so none goes while a message made at or
	// before its lift is still kept.
	PruneLifted(ctx context.Context, before time.Time, limit int) (int, error)
}
