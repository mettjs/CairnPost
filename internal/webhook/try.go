package webhook

import (
	"context"

	"github.com/mettjs/cairnpost/internal/store"
)

// released is the state of a try cut short and handed back; it is no
// state of a delivery, which stays pending and uncounted.
const released = "released"

// try sends one delivery and settles it, and answers the state it is left
// in. Settling is not cut short by a shutdown: a webhook the endpoint took
// and nobody recorded would be sent again.
func (w *Worker) try(ctx context.Context, d store.Due) string {
	r := w.poster.Post(ctx, d.Endpoint.URL, keysOf(d.Endpoint), d.Delivery.ID, bodyOf(d))
	settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
	defer cancel()
	state, paused, err := store.DeliveryDelivered, false, error(nil)
	switch {
	case r.Delivered():
		err = w.store.DeliveryDone(settle, d.Delivery.ID, r.Status)
	case ctx.Err() != nil:
		// Cut short by the shutdown, not refused by the endpoint: the try
		// does not count against it.
		state = released
		err = w.store.DeliveryReleased(settle, d.Delivery.ID, d.Delivery.Attempts)
	case d.Delivery.Attempts > len(w.opt.Retries):
		state = store.DeliveryFailed
		paused, err = w.store.DeliveryMissed(settle, d.Delivery.ID, d.Delivery.Attempts, r.Status, r.Failure, 0, true, w.opt.PauseAfter)
		w.log.Warn("webhook: an endpoint did not take a webhook on any try; it is given up",
			"endpoint", d.Endpoint.ID, "delivery", d.Delivery.ID, "tries", d.Delivery.Attempts,
			"last_failure", r.Failure, "last_status", r.Status)
	default:
		state = store.DeliveryPending
		paused, err = w.store.DeliveryMissed(settle, d.Delivery.ID, d.Delivery.Attempts, r.Status, r.Failure,
			w.opt.Retries[d.Delivery.Attempts-1], false, w.opt.PauseAfter)
	}
	if paused {
		// The id only: an endpoint's URL can hold a token.
		w.log.Warn("webhook: an endpoint took nothing for too long and is paused; it is still owed what happens, "+
			"and nothing is sent to it until it is resumed", "endpoint", d.Endpoint.ID, "failing_for", w.opt.PauseAfter.String())
	}
	if err != nil {
		// Unsettled, it comes due again when its lease ends: a webhook the
		// endpoint took may be sent to it twice, never lost.
		w.log.Error("webhook: what a try met could not be recorded", "delivery", d.Delivery.ID, "error", err)
	}
	return state
}
