package memstore_test

import (
	"context"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/store/memstore"
)

// The list folds a recipient as the database does: A to Z and nothing
// else.
func TestARecipientIsMatchedWithAToZFolded(t *testing.T) {
	ctx, s := context.Background(), memstore.New()
	_, err := s.Start(ctx, store.Start{KeyID: "k", Sender: "app@example.com", Recipients: store.To("\u00c4na@Example.org"), Driver: "test",
		TTL: time.Hour, InDoubt: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	for address, want := range map[string]int{"\u00c4NA@EXAMPLE.ORG": 1, "\u00c4na@example.org": 1, "\u00e4na@example.org": 0, "ana@example.org": 0} {
		got, err := s.Messages(ctx, store.MessageQuery{Recipient: address, Limit: 10})
		if err != nil || len(got) != want {
			t.Errorf("%q: %d found, want %d: %v", address, len(got), want, err)
		}
	}
}
