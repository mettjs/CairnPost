package schedule

import (
	"time"

	"github.com/mettjs/cairnpost/internal/driver"
)

// hold is how long everything waits when the mail provider says its
// limit is reached: as long as it asked and MaxWait at most, or a pause
// when it did not say.
//
// The provider's word alone is no bound. Every email that is due is put
// off by this wait, in the store, where a restart does not undo it, and
// nothing is looked for until it is over.
func (r *Runner) hold(t *driver.Throttled) time.Duration {
	if wait := min(t.After, r.opt.MaxWait); wait > 0 {
		return wait
	}
	return pause
}
