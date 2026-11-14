package console_test

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/mettjs/cairnpost/internal/mail"
	"github.com/mettjs/cairnpost/internal/store"
)

// sent sends an email through the service, as an application would, and
// answers its record.
func (r *rig) sent(from, to, subject string) store.Message {
	r.t.Helper()
	ctx := context.Background()
	receipt, err := r.mail.Send(ctx, r.sendKey, r.send, mail.Request{From: from, To: []string{to}, Subject: subject,
		Text: "the body of " + subject, HTML: "<script>alert('" + subject + "')</script>", Headers: map[string]string{"X-Entity": "e-" + subject}})
	if err != nil {
		r.t.Fatal(err)
	}
	m, err := r.s.Message(ctx, receipt.ID)
	if err != nil {
		r.t.Fatal(err)
	}
	return m
}

// heard records that something happened to an email.
func (r *rig) heard(m store.Message, kind, code, detail string) {
	r.t.Helper()
	_, err := r.s.RecordEvent(context.Background(), store.Event{Driver: m.Driver, UpstreamID: m.UpstreamID,
		UpstreamEventID: kind + m.ID, Kind: kind, Permanent: kind == store.EventBounced, Code: code, Reason: "mailbox-full",
		At: r.now, Detail: detail, KeepDetail: 24 * 60 * 60 * 1e9})
	if err != nil {
		r.t.Fatal(err)
	}
}

// order answers the position of each word in a page, -1 for one not there.
func order(page string, words ...string) []int {
	out := make([]int, len(words))
	for i, w := range words {
		out[i] = strings.Index(page, w)
	}
	return out
}

// Emails are listed newest first, by state and by whom they went to, and
// the list never says what an email said.
func TestEmailsAreListedAndNarrowed(t *testing.T) {
	r := newRig(t, nil)
	b := &browser{r: r, secure: true, jar: map[string]string{}}
	b.follow("POST", "/signin", url.Values{"key": {r.admin}})
	if page := b.do("GET", "/emails", nil).Body.String(); !strings.Contains(page, "No email is on record") {
		t.Fatalf("an empty list: %s", page)
	}
	first := r.sent("App <app@example.com>", "ana@example.org", "subject-one")
	second := r.sent("app@example.com", "Bo@Example.org", "subject-two")
	third := r.sent("app@other.example.com", "ana@example.org", "subject-three")
	r.heard(second, store.EventBounced, "550", "said-of-two")

	page := b.do("GET", "/emails", nil).Body.String()
	at := order(page, "/emails/"+third.ID, "/emails/"+second.ID, "/emails/"+first.ID)
	if at[0] < 0 || at[0] > at[1] || at[1] > at[2] {
		t.Fatalf("not newest first: %v", at)
	}
	for _, never := range []string{"subject-", "the body of", "said-of-two", "X-Entity"} {
		if strings.Contains(page, never) {
			t.Errorf("the list says %q", never)
		}
	}
	if !strings.Contains(page, ">Bounced</span>") || !strings.Contains(page, ">Sent</span>") {
		t.Error("states are not named in words")
	}

	// By state, in the URL.
	page = b.do("GET", "/emails?state=bounced", nil).Body.String()
	if at := order(page, "/emails/"+second.ID, "/emails/"+first.ID); at[0] < 0 || at[1] >= 0 {
		t.Fatalf("by state: %v", at)
	}
	// A state that is none is said, beside the list as it is without it.
	rec := b.do("GET", "/emails?state=nonsense", nil)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "There is no such state") ||
		!strings.Contains(rec.Body.String(), "/emails/"+first.ID) || strings.Contains(rec.Body.String(), "No email") {
		t.Fatalf("a state that is none: %d", rec.Code)
	}

	// By address: typed in any case, with a name or spaces around it, and
	// never put in a URL.
	for _, typed := range []string{"  ANA@example.org ", "Ana <ana@example.org>"} {
		// The address is kept with the session: the browser is given
		// a handle for it, and nothing that holds it.
		narrowed, _ := b.narrow("/emails/find", url.Values{"to": {typed}, "state": {""}})
		page = b.do("GET", narrowed, nil).Body.String()
		at := order(page, "/emails/"+third.ID, "/emails/"+first.ID, "/emails/"+second.ID)
		if at[0] < 0 || at[1] < 0 || at[2] >= 0 {
			t.Fatalf("%q: by address: %v", typed, at)
		}
	}
	// An address kept as it was typed is found however it is typed now.
	narrowed, bo := b.narrow("/emails/find", url.Values{"to": {"bo@example.ORG"}, "state": {"bounced"}})
	if page = b.do("GET", narrowed, nil).Body.String(); !strings.HasSuffix(narrowed, "&state=bounced") || !strings.Contains(page, "/emails/"+second.ID) {
		t.Fatalf("an address was not found in another case, at %s", narrowed)
	}
	if page = b.do("GET", "/emails?state=delivered&for="+bo, nil).Body.String(); !strings.Contains(page, "No email on record matches") {
		t.Fatal("nothing matching is not said")
	}
	for _, typed := range []string{"ana", "ana@", "a@b.example, c@d.example", "<script>"} {
		rec := b.do("POST", "/emails/find", url.Values{"to": {typed}})
		if rec.Code != 422 || !strings.Contains(rec.Body.String(), "Give one whole address") {
			t.Errorf("%q: %d", typed, rec.Code)
		}
	}
	// A refused search keeps what the list was narrowed to, says the
	// address is at fault even beside a state that is none, and leaves
	// an address in the browser that is the list when asked for again.
	rec = b.do("POST", "/emails/find", url.Values{"to": {"ana"}, "state": {"nonsense"}, "for": {bo}})
	if page = rec.Body.String(); rec.Code != 422 || !strings.Contains(page, "Give one whole address") || strings.Contains(page, "no such state") ||
		!strings.Contains(page, "/emails/"+second.ID) || strings.Contains(page, "/emails/"+first.ID) {
		t.Fatalf("a refused search: %d", rec.Code)
	}
	if rec = b.do("GET", "/emails/find", nil); rec.Code != 303 || rec.Header().Get("Location") != "/console/emails" {
		t.Fatalf("the refused search's address, asked for again: %d", rec.Code)
	}
	// Too much to be an address is refused before it is read or echoed.
	huge := strings.Repeat("a", 1<<20)
	if rec = b.do("POST", "/emails/find", url.Values{"to": {huge + "@example.org"}}); rec.Code != 422 || rec.Body.Len() > 20000 {
		t.Fatalf("a megabyte of address: %d, %d bytes answered", rec.Code, rec.Body.Len())
	}
	if rec = b.do("POST", "/emails/find", url.Values{"to": {strings.Repeat("a", 330) + "@example.org"}}); rec.Code != 422 || strings.Contains(rec.Body.String(), "aaaaaaaaaa") == false {
		t.Fatalf("an address too long to be one: %d", rec.Code)
	}
	// Show all forgets the address.
	if rec = b.do("POST", "/emails/find", url.Values{"to": {"bo@example.org"}, "clear": {"1"}}); rec.Code != 303 {
		t.Fatalf("show all: %d", rec.Code)
	}
	if page = b.do("GET", "/emails", nil).Body.String(); !strings.Contains(page, "/emails/"+first.ID) {
		t.Fatal("show all did not show all")
	}
	// What one session looks for is its own: it is not another's, and it
	// ends with the session however that ends.
	b.do("POST", "/emails/find", url.Values{"to": {"bo@example.org"}})
	other := &browser{r: r, secure: true, jar: map[string]string{}}
	other.follow("POST", "/signin", url.Values{"key": {r.admin}})
	if page = other.do("GET", "/emails", nil).Body.String(); !strings.Contains(page, "/emails/"+first.ID) {
		t.Fatal("another session's list is narrowed")
	}
	r.now = r.now.Add(2 * 60 * 60 * 1e9)
	b.follow("GET", "/emails", nil)
	b.follow("POST", "/signin", url.Values{"key": {r.admin}})
	if page = b.do("GET", "/emails", nil).Body.String(); !strings.Contains(page, "/emails/"+first.ID) || strings.Contains(page, `value="bo@example.org"`) {
		t.Fatal("an address outlived the session that looked for it")
	}
	if strings.Contains(r.logged.String(), "example.org") {
		t.Fatal("the log holds an address")
	}
}

// A long list comes a page at a time, each page where the last ended.
func TestEmailsComeAPageAtATime(t *testing.T) {
	r := newRig(t, nil)
	session := r.signIn(r.admin)
	var all []store.Message
	for i := range 205 {
		all = append(all, r.sent("app@example.com", fmt.Sprintf("p%d@example.org", i), "s"))
	}
	page := r.call("GET", "/emails", session, nil).Body.String()
	older := "/console/emails?before=" + all[105].ID
	if strings.Count(page, "<tr>") != 101 || !strings.Contains(page, `href="`+older+`"`) || strings.Contains(page, ">Newest<") {
		t.Fatalf("the first page: %d rows", strings.Count(page, "<tr>"))
	}
	page = r.call("GET", strings.TrimPrefix(older, "/console"), session, nil).Body.String()
	if !strings.Contains(page, "/emails/"+all[104].ID) || strings.Contains(page, "/emails/"+all[105].ID) || !strings.Contains(page, ">Newest<") {
		t.Fatal("the second page does not begin where the first ended")
	}
	page = r.call("GET", "/emails?before="+all[5].ID, session, nil).Body.String()
	if strings.Count(page, "<tr>") != 6 || strings.Contains(page, ">Older<") {
		t.Fatalf("the last page: %d rows", strings.Count(page, "<tr>"))
	}
	for _, before := range []string{all[0].ID, "nonsense", "'; DROP TABLE messages; --"} {
		rec := r.call("GET", "/emails?before="+url.QueryEscape(before), session, nil)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "There are no older emails") {
			t.Errorf("before %q: %d", before, rec.Code)
		}
	}
	if strings.Contains(r.logged.String(), "level=ERROR") {
		t.Fatalf("a hand-made URL was logged as a failure: %s", r.logged.String())
	}
}

// One email shows its record, what happened to it, and what it said
// where that is kept: as text, never as a page of its own, and never in
// the log.
func TestAnEmailIsShownAsItWasWritten(t *testing.T) {
	r := newRig(t, nil)
	session := r.signIn(r.admin)
	m := r.sent("App <app@example.com>", "ana@example.org", "subject-one")
	r.heard(m, store.EventDeferred, "451", "said-first")
	r.heard(m, store.EventBounced, "550", "said-<b>last</b>")
	rec := r.call("GET", "/emails/"+m.ID, session, nil)
	page := rec.Body.String()
	if rec.Code != 200 {
		t.Fatalf("%d", rec.Code)
	}
	for _, want := range []string{"ana@example.org", "app@example.com", ">Bounced</span>", "The receiving server refused it.",
		">Delayed</span>", "<code>451</code>", "for good", "said-first", "said-&lt;b&gt;last&lt;/b&gt;",
		"subject-one", "the body of subject-one", "&lt;script&gt;alert(", "X-Entity: e-subject-one", "Kept until", "UTC"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if strings.Contains(page, "<script>alert") || strings.Contains(page, "<b>last") {
		t.Fatal("what an email said was put in the page as markup")
	}
	if at := order(page, ">Delayed</span>", "said-first"); at[0] > at[1] {
		t.Error("events are not oldest first")
	}
	for _, never := range []string{"subject-one", "the body of", "said-first", "ana@example.org"} {
		if strings.Contains(r.logged.String(), never) {
			t.Errorf("the log holds %q", never)
		}
	}
	// Past its time, what it said is no longer shown; the record is.
	r.now = r.now.Add(25 * 60 * 60 * 1e9)
	session = r.signIn(r.admin)
	page = r.call("GET", "/emails/"+m.ID, session, nil).Body.String()
	if strings.Contains(page, "subject-one") || strings.Contains(page, "said-first") || !strings.Contains(page, "Nothing this email said is kept") ||
		!strings.Contains(page, ">Bounced</span>") {
		t.Fatal("what an email said outlived its time, or its record did not")
	}
	for _, id := range []string{"msg-9999", "nonsense", "00000000-0000-7000-8000-000000000000"} {
		if rec := r.call("GET", "/emails/"+id, session, nil); rec.Code != 404 || !strings.Contains(rec.Body.String(), "There is no such email") {
			t.Errorf("%s: %d", id, rec.Code)
		}
	}
}

// A key that manages one sending domain reads that domain's emails: each
// one listed opens, and no other does.
func TestABoundKeyReadsItsDomainsEmails(t *testing.T) {
	r := newRig(t, nil)
	ours := r.sent("app@A.example.com", "ana@example.org", "ours")
	theirs := r.sent("app@b.example.com", "ana@example.org", "theirs")
	near := r.sent("app@xa.example.com", "ana@example.org", "near")
	bound, _, err := r.ks.Create(context.Background(), "a's admin", store.ScopeAdmin, "a.example.com")
	if err != nil {
		t.Fatal(err)
	}
	session := r.signIn(bound)
	page := r.call("GET", "/emails", session, nil).Body.String()
	if at := order(page, "/emails/"+ours.ID, "/emails/"+theirs.ID, "/emails/"+near.ID); at[0] < 0 || at[1] >= 0 || at[2] >= 0 {
		t.Fatalf("the list: %v", at)
	}
	if rec := r.call("GET", "/emails/"+ours.ID, session, nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), "ours") {
		t.Fatalf("its own domain's email: %d", rec.Code)
	}
	for _, m := range []store.Message{theirs, near} {
		if rec := r.call("GET", "/emails/"+m.ID, session, nil); rec.Code != 404 || strings.Contains(rec.Body.String(), "ana@example.org") {
			t.Fatalf("another domain's email: %d", rec.Code)
		}
	}
}

// An email that went to several addresses is one row of the list, and
// its page says what became of it at each.
func TestAnEmailToSeveralIsOneRowAndShowsEachAddress(t *testing.T) {
	r := newRig(t, nil)
	b := r.signedIn(r.admin)
	if _, err := r.s.Suppress(context.Background(), store.Suppression{Domain: "example.com", Address: "gone@example.org", Reason: store.ReasonManual}); err != nil {
		t.Fatal(err)
	}
	sent, err := r.mail.Send(context.Background(), r.sendKey, r.send, mail.Request{From: "app@example.com",
		To: []string{"ana@example.org", "gone@example.org"}, Cc: []string{"bo@example.org"}, Bcc: []string{"cy@example.org"}, Subject: "s", Text: "t"})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := r.s.Message(context.Background(), sent.ID)
	r.s.Sent(context.Background(), sent.ID, "up-several")
	if _, err := r.s.RecordEvent(context.Background(), store.Event{Driver: m.Driver, UpstreamEventID: "e-several", UpstreamID: "up-several",
		Kind: store.EventBounced, Permanent: true, Code: "5.1.1", At: r.now, Recipient: "bo@example.org"}); err != nil {
		t.Fatal(err)
	}
	list := b.do("GET", "/emails", nil).Body.String()
	if strings.Count(list, "/emails/"+sent.ID) != 1 || !strings.Contains(list, "and 2 more") || !strings.Contains(list, "</svg>1 Bounced</span>") ||
		!strings.Contains(list, "</svg>2 Sent</span>") {
		t.Fatalf("the list: %s", list)
	}
	// It is found by an address it was copied to.
	narrowed, _ := b.narrow("/emails/find", url.Values{"to": {"cy@example.org"}})
	if page := b.do("GET", narrowed, nil).Body.String(); !strings.Contains(page, "/emails/"+sent.ID) {
		t.Error("not found by a hidden copy's address")
	}
	page := b.do("GET", "/emails/"+sent.ID, nil).Body.String()
	for _, want := range []string{"Email to ana@example.org and 2 more", "Who it went to", ">Copy<", ">Hidden copy<", "cy@example.org",
		`<span class="why">bo@example.org</span>`, "Each address has its own outcome.", "<dt>Not sent to</dt>", "gone@example.org"} {
		if !strings.Contains(page, want) {
			t.Errorf("the email's page lacks %q", want)
		}
	}
}
