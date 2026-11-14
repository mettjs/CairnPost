package console

import (
	"context"

	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/webhook"
)

// owed sets, on the endpoints listed, how many webhooks each is owed.
// Where that cannot be read the rows say all but it.
func (c *console) owed(ctx context.Context, rows []hookRow) {
	ends := make([]store.Endpoint, len(rows))
	for i, row := range rows {
		ends[i] = row.Endpoint
	}
	owed, err := c.Webhooks.Owed(ctx, ends)
	if err != nil {
		c.Log.Error("console: what the endpoints are owed could not be read", "error", err)
		return
	}
	for i := range rows {
		if n := owed[rows[i].ID]; n > 0 {
			rows[i].Owed = owedCount(n)
		}
	}
}

// owedCount is how many webhooks are owed, as a number: counting stops
// at webhook.OwedMost, and the number says so.
func owedCount(n int) string {
	if n >= webhook.OwedMost {
		return count(webhook.OwedMost) + "+"
	}
	return count(n)
}

// owedSays is how many webhooks an endpoint is owed, as a sentence that
// fits how it stands; "" for none. One that is paused is owed them until
// it is resumed, whether it is on or off.
func owedSays(n int, e store.Endpoint) string {
	many, wait := owedCount(n)+" webhooks", "wait"
	if n == 1 {
		many, wait = "1 webhook", "waits"
	}
	switch {
	case n == 0:
		return ""
	case e.PausedAt != nil:
		return "It is owed " + many + "."
	case !e.Enabled:
		return many + " " + wait + " until it is on."
	}
	return many + " " + wait + " to be sent."
}
