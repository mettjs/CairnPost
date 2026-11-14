package console_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/mettjs/cairnpost/internal/console"
	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/store/memstore"
	"github.com/mettjs/cairnpost/internal/suppress"
)

// signedIn is a browser signed in with a key.
func (r *rig) signedIn(key string) *browser {
	b := &browser{r: r, secure: true, jar: map[string]string{}}
	b.follow("POST", "/signin", url.Values{"key": {key}})
	return b
}

// An address that bounced for good is found on the list however it is
// typed, with why and the email it happened to; one that is not
// suppressed is said to be sent to.
func TestASuppressedAddressIsFound(t *testing.T) {
	r := newRig(t, nil)
	b := r.signedIn(r.admin)
	if page := b.do("GET", "/suppressions", nil).Body.String(); !strings.Contains(page, "No address is suppressed.") {
		t.Fatalf("an empty list: %s", page)
	}
	plain := r.sent("app@example.com", "Ana@Example.org", "one")
	quoted := r.sent("app@example.com", `"Ana B"@example.org`, "two")
	r.heard(plain, store.EventBounced, "5.1.1", "")
	r.heard(quoted, store.EventComplained, "", "")
	page := b.do("GET", "/suppressions", nil).Body.String()
	for _, want := range []string{"ana@example.org", "&#34;ana b&#34;@example.org", "Bounced for good", "Marked as spam",
		"/emails/" + plain.ID, "/emails/" + quoted.ID} {
		if !strings.Contains(page, want) {
			t.Errorf("the list lacks %q", want)
		}
	}
	for typed, want := range map[string]string{"ANA@EXAMPLE.ORG": plain.ID, "Ana <ana@example.org>": plain.ID, `"ANA B"@Example.org`: quoted.ID} {
		narrowed, _ := b.narrow("/suppressions/find", url.Values{"to": {typed}})
		page = b.do("GET", narrowed, nil).Body.String()
		if !strings.Contains(page, "/emails/"+want) || strings.Count(page, `name="address"`) != 1 || !strings.Contains(page, "Suppressions of <code>") {
			t.Errorf("%q: the address's suppression is not what is shown", typed)
		}
	}
	// The page leads to the emails sent to the same address, and those to
	// its suppressions, by the handle alone.
	narrowed, quotedHandle := b.narrow("/suppressions/find", url.Values{"to": {`"ana b"@example.org`}})
	if page = b.do("GET", narrowed, nil).Body.String(); !strings.Contains(page, `href="/console/emails?for=`+quotedHandle+`"`) {
		t.Fatalf("the suppressions do not lead to the address's emails: %s", page)
	}
	page = b.do("GET", "/emails?for="+quotedHandle, nil).Body.String()
	if !strings.Contains(page, "/emails/"+quoted.ID) || strings.Contains(page, "/emails/"+plain.ID) ||
		!strings.Contains(page, `href="/console/suppressions?for=`+quotedHandle+`"`) {
		t.Fatal("the emails are not narrowed to the address looked for, or do not lead back")
	}
	narrowed, _ = b.narrow("/suppressions/find", url.Values{"to": {"bo@example.org"}, "domain": {"Example.com"}})
	page = b.do("GET", narrowed, nil).Body.String()
	if !strings.HasPrefix(narrowed, "/suppressions?domain=example.com&for=") ||
		!strings.Contains(page, "This address is not suppressed for that domain: emails to it are sent.") {
		t.Fatalf("an address that is not suppressed, at %s: %s", narrowed, page)
	}
	// Show all leads to the whole list, and a page with no handle is it.
	if rec := b.do("POST", "/suppressions/find", url.Values{"clear": {"1"}, "to": {"bo@example.org"}, "for": {quotedHandle}}); rec.Header().Get("Location") != "/console/suppressions" {
		t.Fatalf("show all leads to %s", rec.Header().Get("Location"))
	}
	if page = b.do("GET", "/suppressions", nil).Body.String(); !strings.Contains(page, "ana@example.org") || !strings.Contains(page, "Suppressed addresses") {
		t.Fatal("the page with no handle is not the whole list")
	}
	for _, typed := range []string{"ana", "<script>", strings.Repeat("a", 1<<20) + "@example.org"} {
		if rec := b.do("POST", "/suppressions/find", url.Values{"to": {typed}}); rec.Code != 422 || !strings.Contains(rec.Body.String(), "Give one whole address") ||
			!strings.Contains(rec.Body.String(), "ana@example.org") || rec.Body.Len() > 30000 {
			t.Errorf("%.20q: %d", typed, rec.Code)
		}
	}
	// An address typed where the domain goes never reaches a URL.
	rec := b.do("POST", "/suppressions/find", url.Values{"domain": {"ana@example.org"}})
	if rec.Code != 422 || rec.Header().Get("Location") != "" || !strings.Contains(rec.Body.String(), "not a sending domain") {
		t.Fatalf("an address as a domain: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	// An address a send was let through with is found and lifted as
	// any other, however odd: what the list holds, it shows.
	odd := r.sent("app@example.com", "\"a\tb\"@example.org", "three")
	r.heard(odd, store.EventBounced, "5.1.1", "")
	narrowed, oddHandle := b.narrow("/suppressions/find", url.Values{"to": {"\"A\tB\"@Example.org"}})
	if rec = b.do("GET", narrowed, nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), "/emails/"+odd.ID) ||
		!strings.Contains(rec.Body.String(), `name="for" value="`+oddHandle+`"`) {
		t.Fatalf("an odd address that is suppressed: %d %s", rec.Code, rec.Body)
	}
	// Lifting from the narrowed list leads back to it, narrowed still.
	lift := url.Values{"domain": {"example.com"}, "address": {"\"a\tb\"@example.org"}, "for": {oddHandle}}
	if rec = b.do("POST", "/suppressions/lift", lift); rec.Code != 200 || !strings.Contains(rec.Body.String(), `name="confirm"`) ||
		!strings.Contains(rec.Body.String(), `name="for" value="`+oddHandle+`"`) || !strings.Contains(rec.Body.String(), `href="`+console.Root+narrowed+`"`) {
		t.Fatalf("lifting it does not ask, or forgets where it came from: %d %s", rec.Code, rec.Body)
	}
	lift.Set("confirm", "1")
	if page := b.follow("POST", "/suppressions/lift", lift).Body.String(); !strings.Contains(page, "The suppression was lifted.") ||
		!strings.Contains(page, "This address is not suppressed") {
		t.Fatalf("an odd address was not lifted: %s", page)
	}
	for _, formOnly := range []string{"find", "lift", "lift-all"} {
		if rec := b.do("GET", "/suppressions/"+formOnly, nil); rec.Code != 303 || rec.Header().Get("Location") != "/console/suppressions" {
			t.Errorf("GET %s: %d", formOnly, rec.Code)
		}
	}
	if strings.Contains(r.logged.String(), "example.org") || strings.Contains(r.logged.String(), "level=ERROR") {
		t.Fatalf("the log holds an address, or a failure that was none: %s", r.logged.String())
	}
}

// failing is a store whose suppressions cannot be read.
type failing struct{ *memstore.Store }

func (failing) SuppressionsOf(context.Context, string, string) ([]store.Suppression, error) {
	return nil, errors.New("the database is away")
}

// A store that fails is said to have failed, and logged: never passed
// off as a suppression that is not the key's.
func TestAFailingStoreIsNotARefusal(t *testing.T) {
	r := newRig(t, func(d *console.Deps) { d.Suppress = suppress.New(failing{memstore.New()}) })
	b := r.signedIn(r.admin)
	rec := b.do("POST", "/suppressions/lift", url.Values{"domain": {"a.example.com"}, "address": {"ana@example.org"}})
	if rec.Code != 503 || !strings.Contains(r.logged.String(), "a suppression could not be read") {
		t.Fatalf("%d %s", rec.Code, r.logged.String())
	}
}

// An address is suppressed by hand and lifted again, each said by the
// next page; a lift asks first; the log says who and for which domain,
// never to whom.
func TestAnAddressIsSuppressedAndLiftedByHand(t *testing.T) {
	r := newRig(t, nil)
	b := r.signedIn(r.admin)
	add := url.Values{"address": {"Cy@Example.org"}, "domain": {" Mail.Example.com "}, "note": {"asked to stop"}}
	if page := b.follow("POST", "/suppressions", add).Body.String(); !strings.Contains(page, "The address is suppressed.") ||
		!strings.Contains(page, "cy@example.org") || !strings.Contains(page, "asked to stop") || !strings.Contains(page, "Added by hand") {
		t.Fatalf("after adding: %s", page)
	}
	if page := b.follow("POST", "/suppressions", add).Body.String(); !strings.Contains(page, "suppressed for that domain already") {
		t.Fatal("adding it twice is not said")
	}
	for what, c := range map[string]struct {
		form url.Values
		want string
	}{
		"not an address":  {url.Values{"address": {"cy"}, "domain": {"mail.example.com"}}, "Give one address with no name beside it"},
		"no domain":       {url.Values{"address": {"cy@example.org"}}, "Name the sending domain the address is suppressed for"},
		"a note of lines": {url.Values{"address": {"cy@example.org"}, "domain": {"mail.example.com"}, "note": {"a\nb"}}, "The note must be one line"},
		"too much":        {url.Values{"address": {"cy@example.org"}, "domain": {"mail.example.com"}, "note": {strings.Repeat("n", 1<<20)}}, "more than this form takes"},
	} {
		rec := b.do("POST", "/suppressions", c.form)
		if rec.Code != 422 || !strings.Contains(rec.Body.String(), c.want) || rec.Body.Len() > 30000 {
			t.Errorf("%s: %d", what, rec.Code)
		}
	}
	// A lift asks first, and lifts nothing until it is confirmed.
	lift := url.Values{"domain": {"mail.example.com"}, "address": {"cy@example.org"}}
	rec := b.do("POST", "/suppressions/lift", lift)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Send to cy@example.org again?") || !strings.Contains(rec.Body.String(), `name="confirm"`) {
		t.Fatalf("a lift does not ask: %d", rec.Code)
	}
	if page := b.do("GET", "/suppressions", nil).Body.String(); !strings.Contains(page, "cy@example.org") {
		t.Fatal("asking lifted it")
	}
	// What is done from a narrowed list leads back to it.
	lift.Set("confirm", "1")
	lift.Set("in", "Mail.Example.com")
	if rec = b.do("POST", "/suppressions/lift", lift); rec.Code != 303 || rec.Header().Get("Location") != "/console/suppressions?domain=mail.example.com" {
		t.Fatalf("a lift from a narrowed list: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if page := b.do("GET", "/suppressions?domain=mail.example.com", nil).Body.String(); !strings.Contains(page, "The suppression was lifted.") || strings.Contains(page, "cy@example.org") {
		t.Fatalf("after lifting: %s", page)
	}
	lift.Set("in", "ana@example.org")
	if rec = b.do("POST", "/suppressions/lift", lift); rec.Header().Get("Location") != "/console/suppressions" {
		t.Fatalf("an address where the list's domain goes reached a URL: %s", rec.Header().Get("Location"))
	}
	lift.Del("in")
	// One not suppressed any more is not asked about.
	ask := url.Values{"domain": {"mail.example.com"}, "address": {"cy@example.org"}}
	if page := b.follow("POST", "/suppressions/lift", ask).Body.String(); !strings.Contains(page, "no longer suppressed") {
		t.Fatal("a lift asks about an address that is not suppressed")
	}
	if page := b.follow("POST", "/suppressions/lift", lift).Body.String(); !strings.Contains(page, "no longer suppressed") {
		t.Fatal("lifting what was lifted meanwhile is not said")
	}
	if rec := b.do("POST", "/suppressions/lift", url.Values{"domain": {""}, "address": {"cy@example.org"}}); rec.Code != 422 {
		t.Fatalf("a lift of no domain: %d", rec.Code)
	}
	logged := r.logged.String()
	for _, want := range []string{"an address was suppressed", "a suppression was lifted", "by=" + r.adminKey.Prefix, "domain=mail.example.com"} {
		if !strings.Contains(logged, want) {
			t.Errorf("the log lacks %q", want)
		}
	}
	if strings.Contains(logged, "example.org") || strings.Contains(logged, "asked to stop") {
		t.Fatalf("the log holds an address or a note: %s", logged)
	}
}

// Every suppression of a domain, or those of one reason, is lifted after
// asking, and the page says how many.
func TestADomainsSuppressionsAreLiftedTogether(t *testing.T) {
	r := newRig(t, nil)
	b := r.signedIn(r.admin)
	sup, ctx := suppress.New(r.s), context.Background()
	for i := range 3 {
		sup.Add(ctx, "a.example.com", fmt.Sprintf("u%d@example.org", i), "")
	}
	sup.Add(ctx, "b.example.com", "u0@example.org", "")
	form := url.Values{"domain": {"A.example.com"}, "reason": {"manual"}}
	rec := b.do("POST", "/suppressions/lift-all", form)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Lift the suppressions of a.example.com?") || !strings.Contains(rec.Body.String(), "someone added it by hand") {
		t.Fatalf("lifting a domain's does not ask: %d %s", rec.Code, rec.Body)
	}
	if left, _, _ := sup.List(ctx, "", "", 10); len(left) != 4 {
		t.Fatal("asking lifted them")
	}
	// Done, it leads to the list, which says how many once: its own
	// answer asked for again lifts nothing.
	form.Set("confirm", "1")
	form.Set("in", "a.example.com")
	rec = b.do("POST", "/suppressions/lift-all", form)
	if rec.Code != 303 || rec.Header().Get("Location") != "/console/suppressions?domain=a.example.com" {
		t.Fatalf("after lifting: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if page := b.do("GET", "/suppressions?domain=a.example.com", nil).Body.String(); !strings.Contains(page, "3 addresses can be sent to again.") {
		t.Fatalf("after lifting: %s", page)
	}
	if page := b.do("GET", "/suppressions", nil).Body.String(); strings.Contains(page, "can be sent to again") {
		t.Fatal("it is said twice")
	}
	if page := b.follow("POST", "/suppressions/lift-all", form).Body.String(); !strings.Contains(page, "Nothing was lifted") {
		t.Fatal("lifting none is not said")
	}
	// The count is a count: nothing else rides in its place.
	for _, forged := range []string{"lifted-all:<b>", "lifted-all:-4", "revoked:3", "lifted-all:9999999999"} {
		b.jar["cairnpost_console_said"] = forged
		if page := b.do("GET", "/suppressions", nil).Body.String(); strings.Contains(page, `role="status"`) {
			t.Errorf("%q made the page say something", forged)
		}
	}
	// A refused form keeps what was asked.
	if page := b.do("POST", "/suppressions/lift-all", url.Values{"domain": {"a@b"}, "reason": {"bounce"}}).Body.String(); !strings.Contains(page, `value="a@b"`) ||
		!strings.Contains(page, `value="bounce" checked`) {
		t.Fatal("a refused form came back empty")
	}
	if left, _, _ := sup.List(ctx, "", "", 10); len(left) != 1 || left[0].Domain != "b.example.com" {
		t.Fatalf("what is left: %+v", left)
	}
	for what, f := range map[string]url.Values{"no domain": {"reason": {""}}, "no such reason": {"domain": {"a.example.com"}, "reason": {"spam"}, "confirm": {"1"}},
		"not a domain": {"domain": {"a@b"}}} {
		if rec := b.do("POST", "/suppressions/lift-all", f); rec.Code != 422 || !strings.Contains(rec.Body.String(), `role="alert"`) {
			t.Errorf("%s: %d", what, rec.Code)
		}
	}
	if logged := r.logged.String(); !strings.Contains(logged, "a domain's suppressions were lifted") || !strings.Contains(logged, "lifted=3") ||
		!strings.Contains(logged, "reason=manual") || strings.Contains(logged, "example.org") {
		t.Fatalf("the log: %s", logged)
	}
}

// A key that manages one sending domain sees and changes that domain's
// suppressions, whatever a form or a URL says.
func TestABoundKeyManagesItsDomainsSuppressions(t *testing.T) {
	r := newRig(t, nil)
	sup, ctx := suppress.New(r.s), context.Background()
	sup.Add(ctx, "a.example.com", "ours@example.org", "")
	sup.Add(ctx, "b.example.com", "theirs@example.org", "")
	sup.Add(ctx, "b.example.com", "ours@example.org", "")
	bound, _, err := r.ks.Create(ctx, "a's admin", store.ScopeAdmin, "a.example.com")
	if err != nil {
		t.Fatal(err)
	}
	b := r.signedIn(bound)
	page := b.do("GET", "/suppressions", nil).Body.String()
	if !strings.Contains(page, "ours@example.org") || strings.Contains(page, "theirs@example.org") || strings.Contains(page, "b.example.com") {
		t.Fatalf("the list: %s", page)
	}
	b.do("POST", "/suppressions/find", url.Values{"to": {"ours@example.org"}})
	if page = b.do("GET", "/suppressions", nil).Body.String(); strings.Contains(page, "b.example.com") || !strings.Contains(page, "<code>a.example.com</code>") {
		t.Fatal("an address's suppressions for another domain are shown")
	}
	b.do("POST", "/suppressions/find", url.Values{"clear": {"1"}})
	rec := b.do("GET", "/suppressions?domain=b.example.com", nil)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "not a sending domain this key manages") || !strings.Contains(rec.Body.String(), "ours@example.org") ||
		strings.Contains(rec.Body.String(), "theirs@example.org") {
		t.Fatalf("another domain in the URL: %d", rec.Code)
	}
	if rec = b.do("POST", "/suppressions", url.Values{"address": {"x@example.org"}, "domain": {"b.example.com"}}); rec.Code != 403 {
		t.Fatalf("adding for another domain: %d", rec.Code)
	}
	for _, confirm := range []string{"", "1"} {
		if rec = b.do("POST", "/suppressions/lift", url.Values{"domain": {"b.example.com"}, "address": {"theirs@example.org"}, "confirm": {confirm}}); rec.Code != 422 {
			t.Fatalf("lifting another domain's (confirm %q): %d", confirm, rec.Code)
		}
		if rec = b.do("POST", "/suppressions/lift-all", url.Values{"domain": {"b.example.com"}, "confirm": {confirm}}); rec.Code != 403 {
			t.Fatalf("lifting all of another domain's (confirm %q): %d", confirm, rec.Code)
		}
	}
	if left, _, _ := sup.List(ctx, "b.example.com", "", 10); len(left) != 2 {
		t.Fatalf("another domain's suppressions were changed: %+v", left)
	}
	// Its own domain needs no naming, and is the one written down.
	if rec = b.do("POST", "/suppressions/lift", url.Values{"address": {"ours@example.org"}, "confirm": {"1"}}); rec.Code != 303 ||
		!strings.Contains(r.logged.String(), `a suppression was lifted" by=`) || !strings.Contains(r.logged.String(), "domain=a.example.com") {
		t.Fatalf("a lift naming no domain: %d %s", rec.Code, r.logged.String())
	}
	if page = b.follow("POST", "/suppressions", url.Values{"address": {"new@example.org"}}).Body.String(); !strings.Contains(page, "new@example.org") {
		t.Fatal("it could not add for its own domain")
	}
}

// A long list shows its first rows and says there are more.
func TestALongListOfSuppressionsSaysThereIsMore(t *testing.T) {
	r := newRig(t, nil)
	sup, ctx := suppress.New(r.s), context.Background()
	for i := range 501 {
		sup.Add(ctx, "a.example.com", fmt.Sprintf("u%03d@example.org", i), "")
	}
	page := r.call("GET", "/suppressions", r.signIn(r.admin), nil).Body.String()
	if strings.Count(page, `name="address" value="u`) != 500 || !strings.Contains(page, "These are the first 500") || strings.Contains(page, "u500@example.org") {
		t.Fatalf("%d rows", strings.Count(page, `name="address" value="u`))
	}
}

// Two tabs of one sign-in each keep what they looked for: the address is
// named in each page's own URL by a handle, so narrowing one list does
// not narrow the other's. A handle is the session's own, and a session
// keeps only so many.
func TestTwoTabsKeepTheirOwnSearch(t *testing.T) {
	r := newRig(t, nil)
	b := r.signedIn(r.admin)
	ana := r.sent("app@example.com", "ana@example.org", "one")
	bo := r.sent("app@example.com", "bo@example.org", "two")
	first, anaHandle := b.narrow("/emails/find", url.Values{"to": {"ana@example.org"}})
	second, boHandle := b.narrow("/emails/find", url.Values{"to": {"bo@example.org"}})
	for at, want := range map[string][2]string{first: {ana.ID, bo.ID}, second: {bo.ID, ana.ID}} {
		// Each tab, asked for again after the other searched, is as it was.
		page := b.do("GET", at, nil).Body.String()
		if !strings.Contains(page, "/emails/"+want[0]) || strings.Contains(page, "/emails/"+want[1]) {
			t.Errorf("the tab at %s does not show its own search", at)
		}
	}
	// The same address looked for again has the same handle.
	if _, again := b.narrow("/suppressions/find", url.Values{"to": {"ana@example.org"}}); again != anaHandle || anaHandle == boHandle {
		t.Fatalf("handles: %s, again %s, the other %s", anaHandle, again, boHandle)
	}
	// A handle means nothing to another sign-in, or made up: the list is
	// shown whole, and nothing is said of the address.
	other := r.signedIn(r.admin)
	for who, page := range map[string]string{
		"another sign-in": other.do("GET", "/emails?for="+anaHandle, nil).Body.String(),
		"a made-up one":   b.do("GET", "/emails?for=0123456789abcdef", nil).Body.String(),
		"no handle":       b.do("GET", "/emails?for=ana@example.org", nil).Body.String(),
	} {
		if !strings.Contains(page, "/emails/"+ana.ID) || !strings.Contains(page, "/emails/"+bo.ID) || !strings.Contains(page, "All emails") {
			t.Errorf("%s: the list is not whole", who)
		}
	}
	// Opening an email from a narrowed list, and coming back from it,
	// keeps the search; so does a form's own address asked for again.
	page := b.do("GET", first, nil).Body.String()
	if !strings.Contains(page, `href="/console/emails/`+ana.ID+`?for=`+anaHandle+`"`) ||
		!strings.Contains(page, `action="/console/emails/find?for=`+anaHandle+`"`) {
		t.Fatalf("the narrowed list's links and form do not carry its handle: %s", page)
	}
	if page = b.do("GET", "/emails/"+ana.ID+"?for="+anaHandle, nil).Body.String(); !strings.Contains(page, `href="/console/emails?for=`+anaHandle+`"`) {
		t.Fatal("the email's way back does not lead to the narrowed list")
	}
	if page = b.do("GET", "/emails/"+ana.ID+"?for=ana@example.org", nil).Body.String(); !strings.Contains(page, `href="/console/emails"`) {
		t.Fatal("what is no handle is passed on by the email's page")
	}
	for _, form := range []string{"/emails/find", "/suppressions/find", "/suppressions/lift", "/suppressions/lift-all"} {
		want := "/console" + strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(form, "/find"), "/lift-all"), "/lift") + "?for=" + anaHandle
		if rec := b.do("GET", form+"?for="+anaHandle, nil); rec.Code != 303 || rec.Header().Get("Location") != want {
			t.Errorf("GET %s leads to %s, want %s", form, rec.Header().Get("Location"), want)
		}
	}
	// A session keeps the addresses looked for or shown last. A tab in
	// use stays narrowed however many others are looked for; one left
	// alone says its search is no longer kept, and shows the whole list.
	for i := range 25 {
		b.narrow("/emails/find", url.Values{"to": {fmt.Sprintf("someone%d@example.org", i)}})
		if page := b.do("GET", first, nil).Body.String(); !strings.Contains(page, "Emails to <code>ana@example.org</code>") {
			t.Fatalf("a tab in use lost its search after %d others", i+1)
		}
	}
	if page = b.do("GET", second, nil).Body.String(); !strings.Contains(page, "All emails") ||
		!strings.Contains(page, "The address this page was narrowed to is no longer kept") {
		t.Fatalf("a search the session no longer keeps: %s", page)
	}
	if page = b.do("GET", "/suppressions?for="+boHandle, nil).Body.String(); !strings.Contains(page, "is no longer kept") {
		t.Fatal("the suppressions do not say a search is no longer kept")
	}
	if page = b.do("GET", "/emails", nil).Body.String(); strings.Contains(page, "no longer kept") {
		t.Fatal("a list never narrowed says a search is no longer kept")
	}
	// Show all forgets the address on the server, not only on the page.
	if rec := b.do("POST", "/emails/find", url.Values{"clear": {"1"}, "for": {anaHandle}}); rec.Header().Get("Location") != "/console/emails" {
		t.Fatalf("show all leads to %s", rec.Header().Get("Location"))
	}
	if page = b.do("GET", first, nil).Body.String(); !strings.Contains(page, "is no longer kept") {
		t.Fatal("show all did not forget the address")
	}
	if strings.Contains(r.logged.String(), "example.org") {
		t.Fatalf("the log holds an address: %s", r.logged.String())
	}
}
