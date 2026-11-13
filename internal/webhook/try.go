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
	r := w.poster.Post(ctx, d.Endpoint.URL, d.Endpoint.Secret, d.Delivery.ID, body(d.Event, d.Message))
	settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
	defer cancel()
	state, err := store.DeliveryDelivered, error(nil)
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
		err = w.store.DeliveryMissed(settle, d.Delivery.ID, d.Delivery.Attempts, r.Status, r.Failure, 0, true)
		w.log.Warn("webhook: an endpoint did not take a webhook on any try; it is given up",
			"endpoint", d.Endpoint.ID, "delivery", d.Delivery.ID, "tries", d.Delivery.Attempts,
			"last_failure", r.Failure, "last_status", r.Status)
	default:
		state = store.DeliveryPending
		err = w.store.DeliveryMissed(settle, d.Delivery.ID, d.Delivery.Attempts, r.Status, r.Failure,
			w.opt.Retries[d.Delivery.Attempts-1], false)
	}
	if err != nil {
		// Unsettled, it comes due again when its lease ends: a webhook the
		// endpoint took may be sent to it twice, never lost.
		w.log.Error("webhook: what a try met could not be recorded", "delivery", d.Delivery.ID, "error", err)
	}
	return state
}
