//go:build integration

package postgres_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
)

// An email to several addresses is one email with a row for each: an
// event moves the address it names, a suppressed address is left out,
// lists find the email by any of its addresses, and it is pruned whole.
func TestAnEmailToSeveralAddressesIsOneEmail(t *testing.T) {
	ctx := context.Background()
	db := open(t)
	k := key(t, db)
	keyID, domain := k.ID, k.ID+".example.com"
	tag := k.ID
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
	exec(t, `UPDATE messages SET updated_at = now() - interval '50 years' WHERE email_id = $1::uuid AND kind <> 'bcc'`, claim.ID)
	if n, err := db.PruneMessages(ctx, time.Now().Add(-time.Hour), 1000); err != nil {
		t.Fatal(n, err)
	}
	if _, err := db.Message(ctx, claim.ID); err != nil {
		t.Fatalf("pruned while something came of one address: %v", err)
	}
	exec(t, `UPDATE messages SET updated_at = now() - interval '50 years' WHERE email_id = $1::uuid`, claim.ID)
	if _, err := db.PruneMessages(ctx, time.Now().Add(-time.Hour), 1000); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Message(ctx, claim.ID); err != store.ErrNotFound {
		t.Fatalf("after pruning: %v", err)
	}
}

// What follows from an email to several: a hidden copy never takes the
// place of "to", who was left out is said again on a replay, a key is
// freed only once the email failed at every address, an event that names
// no address moves none, and a suppression names the email.
func TestWhatFollowsFromAnEmailToSeveral(t *testing.T) {
	ctx := context.Background()
	db := open(t)
	k := key(t, db)
	keyID, domain, tag := k.ID, k.ID+".example.com", k.ID
	gone := "gone-" + tag + "@example.org"
	at := func(name string) string { return name + "-" + tag + "@example.org" }
	if _, err := db.Suppress(ctx, store.Suppression{Domain: domain, Address: gone, Reason: store.ReasonManual}); err != nil {
		t.Fatal(err)
	}
	send := func(idem string, rs ...store.Addressee) store.Claim {
		t.Helper()
		c, err := db.Start(ctx, store.Start{KeyID: keyID, Sender: "app@" + domain, Driver: "test", TTL: time.Hour, InDoubt: time.Hour,
			IdemKey: idem, Fingerprint: []byte("f"), Recipients: rs})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	event := func(id, up, kind, recipient string) store.Recorded {
		t.Helper()
		got, err := db.RecordEvent(ctx, store.Event{Driver: "test", UpstreamEventID: id + tag, UpstreamID: up, Kind: kind,
			Permanent: kind == store.EventBounced, Code: "5.1.1", At: time.Now(), Sender: "app@" + domain, Recipient: recipient})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	to, cc, bcc := store.KindTo, store.KindCc, store.KindBcc
	// With nobody left in the open, nothing is sent: a hidden copy sent
	// as "to" would be shown to the other hidden ones.
	if c := send("", store.Addressee{Address: gone, Kind: to}, store.Addressee{Address: at("b1"), Kind: bcc},
		store.Addressee{Address: at("b2"), Kind: bcc}); c.Outcome != store.Suppressed || c.ID != "" {
		t.Fatalf("with hidden copies alone left: %+v", c)
	}
	// A copy takes the place of "to", and is kept as what it went as.
	idem := "k-" + tag
	first := send(idem, store.Addressee{Address: gone, Kind: to}, store.Addressee{Address: at("y"), Kind: cc}, store.Addressee{Address: at("z"), Kind: bcc})
	if first.Outcome != store.Started || len(first.Recipients) != 2 || first.Recipients[0] != (store.Addressee{Address: at("y"), Kind: to}) ||
		first.Recipients[1].Kind != bcc || len(first.Dropped) != 1 {
		t.Fatalf("a copy in the place of to: %+v", first)
	}
	up := "<follows-" + tag + ">"
	if err := db.Sent(ctx, first.ID, up); err != nil {
		t.Fatal(err)
	}
	m, err := db.Message(ctx, first.ID)
	if err != nil || m.Kind != to || len(m.LeftOut) != 1 || m.LeftOut[0] != gone {
		t.Fatalf("what is kept: %+v %v", m, err)
	}
	// The same request again says again who was left out.
	if again := send(idem, store.Addressee{Address: gone, Kind: to}); again.Outcome != store.Replay || again.ID != first.ID ||
		len(again.Dropped) != 1 || again.Dropped[0] != gone {
		t.Fatalf("a replay: %+v", again)
	}
	// Failed at one address, the email still went to the other: its key
	// is kept. Failed at both, it went to nobody, and the key is free.
	if got := event("f1", up, store.EventFailed, at("y")); got != store.Kept {
		t.Fatalf("a failure at the first address: %v", got)
	}
	if again := send(idem, store.Addressee{Address: at("y"), Kind: to}); again.Outcome != store.Replay {
		t.Fatalf("the key was freed while one address was sent to: %+v", again)
	}
	if got := event("f2", up, store.EventFailed, at("z")); got != store.Kept {
		t.Fatalf("a failure at the other address: %v", got)
	}
	if fresh := send(idem, store.Addressee{Address: at("y"), Kind: to}); fresh.Outcome != store.Started || fresh.ID == first.ID {
		t.Fatalf("after it failed everywhere: %+v", fresh)
	}
	// An event that names no address is about none of several.
	two := send("", store.Addressee{Address: at("p"), Kind: to}, store.Addressee{Address: at("q"), Kind: cc})
	up2 := "<unnamed-" + tag + ">"
	if err := db.Sent(ctx, two.ID, up2); err != nil {
		t.Fatal(err)
	}
	if got := event("u1", up2, store.EventBounced, ""); got != store.NoMessage {
		t.Fatalf("an event naming nobody: %v", got)
	}
	if m, _ := db.Message(ctx, two.ID); m.State != store.StateSent || m.Recipients[1].State != store.StateSent {
		t.Fatalf("it moved an address: %+v", m)
	}
	// A bounce at a copy suppresses that address, and names the email.
	if got := event("u2", up2, store.EventBounced, at("q")); got != store.Kept {
		t.Fatalf("a bounce at the copy: %v", got)
	}
	list, err := db.ListSuppressions(ctx, domain, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range list {
		if s.Address == at("q") {
			found = s.MessageID == two.ID
		}
	}
	if !found {
		t.Fatalf("the suppression does not name the email %s: %+v", two.ID, list)
	}
	// An id is the same email however it is cased.
	if m, err := db.Message(ctx, strings.ToUpper(two.ID)); err != nil || len(m.Recipients) != 2 {
		t.Fatalf("by an id in capitals: %+v %v", m, err)
	}
}
