package memstore_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/store/memstore"
)

// An email to several addresses is one email with a row for each: an
// event moves the address it names, a suppressed address is left out,
// lists find the email by any of its addresses, and it is pruned whole.
func TestAnEmailToSeveralAddressesIsOneEmail(t *testing.T) {
	ctx := context.Background()
	db := memstore.New()
	now := time.Date(2030, 3, 1, 10, 20, 0, 0, time.UTC)
	db.Now = func() time.Time { return now }
	keyID, domain := "k", "example.com"
	tag := "t"
	a, b, c, gone := "ana-"+tag+"@example.org", "Bo-"+tag+"@example.org", "cy-"+tag+"@example.org", "di-"+tag+"@example.org"
	if _, err := db.Suppress(ctx, store.Suppression{Domain: domain, Address: gone, Reason: store.ReasonManual}); err != nil {
		t.Fatal(err)
	}
	claim, err := db.Start(ctx, store.Start{KeyID: keyID, Sender: "app@" + domain, Driver: "test", TTL: time.Hour, InDoubt: time.Hour,
		Recipients: []store.Addressee{{Address: a, Kind: store.KindTo}, {Address: gone, Kind: store.KindTo},
			{Address: b, Kind: store.KindCc}, {Address: c, Kind: store.KindBcc}}})
	if err != nil || claim.Outcome != store.Started || len(claim.Dropped) != 1 || claim.Dropped[0] != gone {
		t.Fatalf("the send: %+v %v", claim, err)
	}
	up := "<several-" + tag + ">"
	if err := db.Sent(ctx, claim.ID, up); err != nil {
		t.Fatal(err)
	}
	states := func() string {
		t.Helper()
		m, err := db.Message(ctx, claim.ID)
		if err != nil || m.ID != claim.ID || m.EmailID != claim.ID || m.Recipient != a {
			t.Fatalf("the email: %+v %v", m, err)
		}
		out := ""
		for _, r := range m.Recipients {
			out += r.Kind + ":" + r.State + " "
		}
		return out
	}
	if got := states(); got != "to:sent cc:sent bcc:sent " {
		t.Fatalf("once sent: %s", got)
	}
	// An event moves the address it names, however that is cased.
	event := func(id, kind, recipient string) store.Recorded {
		t.Helper()
		got, err := db.RecordEvent(ctx, store.Event{Driver: "test", UpstreamEventID: id + tag, UpstreamID: up, Kind: kind,
			Permanent: kind == store.EventBounced, Code: "5.1.1", At: time.Now(), Sender: "app@" + domain, Recipient: recipient})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if event("e1", store.EventBounced, strings.ToLower(b)) != store.Kept || event("e2", store.EventDelivered, c) != store.Kept {
		t.Fatal("an event about one of the addresses was not kept")
	}
	if got := states(); got != "to:sent cc:bounced bcc:delivered " {
		t.Fatalf("after two events: %s", got)
	}
	// One about an address the email did not go to is about no email.
	if event("e3", store.EventDelivered, "nobody-"+tag+"@example.org") != store.NoMessage {
		t.Fatal("an event about another address was taken for one of this email's")
	}
	// The bounce suppressed that address alone.
	for address, want := range map[string]bool{b: true, a: false, c: false} {
		list, err := db.SuppressionsOf(ctx, domain, address)
		if err != nil || (len(list) == 1) != want {
			t.Errorf("%s suppressed: %v %v, want %v", address, list, err, want)
		}
	}
	// Its events say which address each is about, under the email's id.
	_, events, err := db.Record(ctx, claim.ID)
	if err != nil || len(events) != 2 || !strings.EqualFold(events[0].Recipient, b) || events[1].Recipient != c {
		t.Fatalf("the record: %+v %v", events, err)
	}
	// The email is found by any address of it and by any state one of
	// them is in, once, and not by an address in another state.
	for _, q := range []store.MessageQuery{{Recipient: c}, {Recipient: strings.ToUpper(b[:1]) + b[1:], State: store.StateBounced},
		{Domain: domain, State: store.StateDelivered}, {Domain: domain}} {
		q.Limit = 10
		list, err := db.Messages(ctx, q)
		if err != nil || len(list) != 1 || list[0].ID != claim.ID || len(list[0].Recipients) != 3 {
			t.Errorf("listed by %+v: %+v %v", q, list, err)
		}
	}
	if list, _ := db.Messages(ctx, store.MessageQuery{Recipient: a, State: store.StateBounced, Limit: 10}); len(list) != 0 {
		t.Errorf("found by an address in a state it is not in: %+v", list)
	}
	// It is pruned whole, and not while anything came of one address.
	now = now.Add(time.Hour)
	if n, err := db.PruneMessages(ctx, now.Add(-2*time.Hour), 10); err != nil || n != 0 {
		t.Fatalf("pruned %d while it is new: %v", n, err)
	}
	if n, err := db.PruneMessages(ctx, now, 10); err != nil || n != 3 {
		t.Fatalf("pruned %d rows, want its 3: %v", n, err)
	}
	if _, err := db.Message(ctx, claim.ID); err != store.ErrNotFound {
		t.Fatalf("after pruning: %v", err)
	}
}
