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

// holding says, once for a look, that nothing that is due goes out and
// for how long: whoever runs the server is not left to find that out
// from the emails that did not come. off is how many this look put off;
// more may be due, which are not looked for until the wait is over.
func (r *Runner) holding(off int, wait, asked time.Duration) {
	if asked > wait {
		r.log.Warn("schedule: the mail provider says its sending limit is reached, and asks for a longer wait than CAIRNPOST_MAX_PROVIDER_WAIT allows; no email that is due is sent until it is tried again, sooner than it asked",
			"put_off", off, "asked", asked.String(), "in", wait.String())
		return
	}
	r.log.Warn("schedule: the mail provider says its sending limit is reached; no email that is due is sent until it is tried again",
		"put_off", off, "in", wait.String())
}
