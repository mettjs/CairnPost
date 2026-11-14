package console

import (
	"context"
	"time"
)

// Quota is the mail provider's sending allowance and how much is used.
type Quota struct {
	Limit, Used int
	// Per is the period the limit covers, as the provider names it ("day").
	Per string
	// Over is the provider saying the allowance is spent.
	Over bool
	// ResetsAt is when the period turns over; zero when it did not say.
	ResetsAt time.Time
}

// quotaWait is how long a page waits for the provider's answer: the
// page is about more than the allowance, and is not held up for it.
const quotaWait = 3 * time.Second

// quotaView is the allowance as the first page shows it.
type quotaView struct {
	// Unread says the provider did not answer.
	Unread            bool
	Used, Limit, Left string
	Per, Resets       string
	Over              bool
	// Fill is how much of the bar is filled, of 100. High and Wide are
	// the bar's colour and size.
	Fill       float64
	High, Wide bool
}

// quota is the provider's allowance; nil for a provider that has none
// to tell. How often the provider is really asked is its driver's rule.
func (c *console) quota(ctx context.Context) *quotaView {
	if c.Quota == nil {
		return nil
	}
	bounded, cancel := context.WithTimeout(ctx, quotaWait)
	defer cancel()
	q, err := c.Quota(bounded)
	if err != nil {
		// A reader who left is nothing wrong. The provider's answer is
		// not logged: only that there was none.
		if ctx.Err() == nil {
			c.Log.Warn("console: the mail provider's allowance could not be read")
		}
		return &quotaView{Unread: true}
	}
	return quotaShown(q)
}

func quotaShown(q Quota) *quotaView {
	v := &quotaView{Used: count(q.Used), Limit: count(q.Limit), Left: count(max(q.Limit-q.Used, 0)), Per: q.Per,
		Over: q.Over || (q.Limit > 0 && q.Used >= q.Limit), Wide: true}
	if q.Limit > 0 {
		v.Fill = round(min(100, 100*float64(q.Used)/float64(q.Limit)))
	}
	v.High = v.Over
	if !q.ResetsAt.IsZero() {
		v.Resets = "at " + when(q.ResetsAt)
	}
	return v
}
