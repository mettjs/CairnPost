package console

import (
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/webhook"
)

// What an endpoint is owed is said to fit how it stands: one that is
// paused is owed it until resumed, off or not.
func TestWhatAnEndpointIsOwedIsSaidAsItStands(t *testing.T) {
	then := time.Now()
	on, off := store.Endpoint{Enabled: true}, store.Endpoint{}
	paused, both := store.Endpoint{Enabled: true, PausedAt: &then}, store.Endpoint{PausedAt: &then}
	for _, c := range []struct {
		n    int
		e    store.Endpoint
		want string
	}{
		{0, on, ""}, {0, paused, ""},
		{1, on, "1 webhook waits to be sent."}, {1234, on, "1,234 webhooks wait to be sent."},
		{1, off, "1 webhook waits until it is on."}, {2, off, "2 webhooks wait until it is on."},
		{1, paused, "It is owed 1 webhook."}, {5, both, "It is owed 5 webhooks."},
		{webhook.OwedMost, paused, "It is owed 10,000+ webhooks."},
	} {
		if got := owedSays(c.n, c.e); got != c.want {
			t.Errorf("%d owed to %+v: %q, want %q", c.n, c.e, got, c.want)
		}
	}
}
