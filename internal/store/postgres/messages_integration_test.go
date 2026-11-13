//go:build integration

package postgres_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mettjs/cairnpost/internal/store"
)

// Messages are listed newest first, and narrowed by sending domain, by
// recipient in any case, by state and by where the last page ended.
func TestMessagesAreListedNewestFirstAndNarrowed(t *testing.T) {
	ctx, db := context.Background(), open(t)
	k := key(t, db)
	// The test's own domain and addresses: the database is shared.
	tail := k.ID[len(k.ID)-12:]
	domain, to := "d"+tail+".example.com", "\u00c4na-"+tail+"@Example.org"
	var ids []string
	for i, sender := range []string{"app@" + domain, "app@" + strings.ToUpper(domain), "app@x" + domain, "a@b@" + domain} {
		s := start(k, "", "")
		s.Sender, s.Recipient = sender, to
		if i == 3 {
			s.Recipient = "bo-" + tail + "@example.org"
		}
		claim, err := db.Start(ctx, s)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, claim.ID)
	}
	if err := db.Sent(ctx, ids[0], "up-"+tail); err != nil {
		t.Fatal(err)
	}
	list := func(q store.MessageQuery) []string {
		t.Helper()
		if q.Limit == 0 {
			q.Limit = 50
		}
		got, err := db.Messages(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, m := range got {
			out = append(out, m.ID)
		}
		return out
	}
	same := func(what string, got []string, want ...string) {
		t.Helper()
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("%s: %v, want %v", what, got, want)
		}
	}
	// The domain is what follows the last "@", in any case; a domain
	// that only ends alike is another.
	same("by domain", list(store.MessageQuery{Domain: domain}), ids[3], ids[1], ids[0])
	// Only A to Z are folded, in the database as in the server: the
	// address is found with those in another case, and not with its
	// other letter in another case.
	same("by recipient", list(store.MessageQuery{Recipient: store.FoldASCII(to)}), ids[2], ids[1], ids[0])
	same("by recipient, in capitals", list(store.MessageQuery{Recipient: "\u00c4NA-" + strings.ToUpper(tail) + "@EXAMPLE.ORG"}), ids[2], ids[1], ids[0])
	same("a letter that is not A to Z", list(store.MessageQuery{Recipient: strings.ToLower(to)}))
	same("by recipient and domain", list(store.MessageQuery{Recipient: store.FoldASCII(to), Domain: domain}), ids[1], ids[0])
	same("by state", list(store.MessageQuery{Domain: domain, State: store.StateSent}), ids[0])
	same("older than one", list(store.MessageQuery{Domain: domain, Before: ids[3]}), ids[1], ids[0])
	same("the newest one", list(store.MessageQuery{Domain: domain, Limit: 1}), ids[3])
	same("older than the oldest", list(store.MessageQuery{Domain: domain, Before: ids[0]}))
	same("an id that is none", list(store.MessageQuery{Domain: domain, Before: "'; --"}))
	if all := list(store.MessageQuery{Limit: 4}); len(all) != 4 || all[0] != ids[3] {
		t.Errorf("every message, newest first: %v", all)
	}
	got, err := db.Messages(ctx, store.MessageQuery{Domain: domain, State: store.StateSent, Limit: 1})
	if err != nil || len(got) != 1 || got[0].UpstreamID != "up-"+tail || got[0].KeyID != k.ID || got[0].Sender != "app@"+domain ||
		got[0].Recipient != to || got[0].CreatedAt.IsZero() {
		t.Fatalf("a listed message's record: %+v %v", got, err)
	}
	// The domain a list is narrowed to is the one the rule for reading
	// one email goes by.
	for _, id := range list(store.MessageQuery{Domain: domain}) {
		if m, err := db.Message(ctx, id); err != nil || store.DomainOf(m.Sender) != domain {
			t.Errorf("%s is listed for %s but is of %s", id, domain, store.DomainOf(m.Sender))
		}
	}
	// Every state there is can be asked for.
	for _, state := range store.States {
		exec(t, `UPDATE messages SET state = $1 WHERE id = $2::uuid`, state, ids[1])
		same("in state "+state, list(store.MessageQuery{Recipient: to, Domain: domain, State: state, Before: ids[2]})[:1], ids[1])
	}
}
