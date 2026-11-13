package mail_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/driver"
	"github.com/mettjs/cairnpost/internal/mail"
	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/store/memstore"
)

// fakeDriver answers what it is told to and counts what it was handed.
type fakeDriver struct {
	err    error
	sends  int
	last   driver.Message
	ctxErr error // the context's state when Send was called
}

func (*fakeDriver) Name() string { return "fake" }

func (f *fakeDriver) Send(ctx context.Context, m driver.Message) (string, error) {
	f.sends++
	f.last, f.ctxErr = m, ctx.Err()
	if f.err != nil {
		return "", f.err
	}
	return "upstream-1", nil
}

type rig struct {
	svc   *mail.Service
	store *memstore.Store
	drv   *fakeDriver
	now   time.Time
	key   store.Key
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{store: memstore.New(), drv: &fakeDriver{}, now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	r.store.Now = func() time.Time { return r.now }
	r.key = store.Key{ID: "key-1", Scope: store.ScopeSend}
	r.svc = mail.New(r.store, r.drv, mail.Options{SendTimeout: time.Second,
		IdempotencyTTL: 24 * time.Hour, InDoubtWindow: 2 * time.Minute}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return r
}

func request(idem string) mail.Request {
	return mail.Request{From: "App <app@example.com>", To: []string{"ana@example.org"},
		Subject: "Your code", Text: "123456", IdemKey: idem}
}

func (r *rig) send(req mail.Request) (string, error) {
	return r.svc.Send(context.Background(), r.key, "cp_presented", req)
}

func (r *rig) state(t *testing.T, id string) string {
	t.Helper()
	m, err := r.store.Message(context.Background(), id)
	if err != nil {
		t.Fatalf("message %s: %v", id, err)
	}
	return m.State
}

func TestSendRecordsWhatBecameOfIt(t *testing.T) {
	r := newRig(t)
	id, err := r.send(request(""))
	if err != nil || r.drv.sends != 1 || r.state(t, id) != store.StateSent {
		t.Fatalf("a plain send: id %q, err %v, sends %d", id, err, r.drv.sends)
	}
}

func TestSameKeySameRequestIsOneEmail(t *testing.T) {
	r := newRig(t)
	first, _ := r.send(request("k"))
	second, err := r.send(request("k"))
	if err != nil || second != first || r.drv.sends != 1 {
		t.Fatalf("a replay: first %q, second %q, err %v, sends %d", first, second, err, r.drv.sends)
	}
}

func TestSameKeyOtherRequestIsRefused(t *testing.T) {
	r := newRig(t)
	r.send(request("k"))
	other := request("k")
	other.Text = "654321"
	if _, err := r.send(other); !errors.Is(err, mail.ErrMismatch) || r.drv.sends != 1 {
		t.Fatalf("a changed request under a used key: err %v, sends %d", err, r.drv.sends)
	}
}

func TestAKeyIsNewAgainOnceForgotten(t *testing.T) {
	r := newRig(t)
	r.send(request("k"))
	r.now = r.now.Add(24 * time.Hour)
	if _, err := r.send(request("k")); err != nil || r.drv.sends != 2 {
		t.Fatalf("after the key's lifetime: err %v, sends %d", err, r.drv.sends)
	}
}

func TestALostAnswerBlocksThenLetsARetryThrough(t *testing.T) {
	r := newRig(t)
	r.drv.err = errors.New("connection reset")
	first, err := r.send(request("k"))
	if !errors.Is(err, mail.ErrInDoubt) || first == "" || r.state(t, first) != store.StateUnknown {
		t.Fatalf("a lost answer: id %q, err %v, state %s", first, err, r.state(t, first))
	}
	r.drv.err = nil
	r.now = r.now.Add(30 * time.Second)
	var busy *mail.Busy
	if _, err := r.send(request("k")); !errors.As(err, &busy) || busy.After != 90*time.Second || r.drv.sends != 1 {
		t.Fatalf("a retry inside the window: err %v, sends %d", err, r.drv.sends)
	}
	// A different request under the key is in flight too: nothing was sent
	// under it yet, so it cannot be told that something was.
	changed := request("k")
	changed.Text = "654321"
	if _, err := r.send(changed); !errors.Is(err, mail.ErrInFlight) {
		t.Fatalf("a changed request while in doubt: %v", err)
	}
	r.now = r.now.Add(90 * time.Second)
	second, err := r.send(request("k"))
	if err != nil || r.drv.sends != 2 || second == first {
		t.Fatalf("a retry after the window: err %v, sends %d", err, r.drv.sends)
	}
	if r.state(t, first) != store.StateUnknown || r.state(t, second) != store.StateSent {
		t.Fatalf("states: first %s, second %s", r.state(t, first), r.state(t, second))
	}
}

// A key's day runs from the try that sent, not from a first try that was
// lost: a send that succeeds late is still remembered for its whole life.
func TestAKeysLifeRunsFromTheTryThatSent(t *testing.T) {
	r := newRig(t)
	r.drv.err = errors.New("connection reset")
	r.send(request("k"))
	r.drv.err = nil
	r.now = r.now.Add(23 * time.Hour)
	sent, err := r.send(request("k"))
	if err != nil || r.drv.sends != 2 {
		t.Fatalf("the late retry: err %v, sends %d", err, r.drv.sends)
	}
	r.now = r.now.Add(2 * time.Hour)
	if again, err := r.send(request("k")); err != nil || again != sent || r.drv.sends != 2 {
		t.Fatalf("a repeat 25 hours after the first try: id %q (sent %q), err %v, sends %d", again, sent, err, r.drv.sends)
	}
}

// The upstream call does not stop because the caller hung up.
func TestACallerHangingUpDoesNotCutTheSendShort(t *testing.T) {
	r := newRig(t)
	ctx, hangUp := context.WithCancel(context.Background())
	hangUp()
	id, err := r.svc.Send(ctx, r.key, "cp_presented", request("k"))
	if err != nil || r.drv.ctxErr != nil || r.state(t, id) != store.StateSent {
		t.Fatalf("err %v, the driver's context: %v", err, r.drv.ctxErr)
	}
}

func TestTheDriverGetsBareAddressesAndTheNameApart(t *testing.T) {
	r := newRig(t)
	req := request("")
	req.From, req.To = "José Pérez <app@example.com>", []string{`"a b"@example.org`}
	if _, err := r.send(req); err != nil {
		t.Fatal(err)
	}
	if m := r.drv.last; m.From != "app@example.com" || m.FromName != "José Pérez" || m.To != `"a b"@example.org` {
		t.Fatalf("the driver was handed %+v", m)
	}
}

func TestASendersNameCannotCarryALineBreak(t *testing.T) {
	r := newRig(t)
	req := request("")
	// An encoded word that decodes to "Evil\r\nBcc: x@example.net".
	req.From = "=?utf-8?b?RXZpbA0KQmNjOiB4QGV4YW1wbGUubmV0?= <app@example.com>"
	var invalid *mail.Invalid
	if _, err := r.send(req); !errors.As(err, &invalid) || invalid.Field != "from" || r.drv.sends != 0 {
		t.Fatalf("err %v, sends %d", err, r.drv.sends)
	}
}

// A suppressed recipient is not sent to. A request that was sent before the
// address was suppressed still answers its id: it is the same email.
func TestASuppressedRecipientIsNotSentTo(t *testing.T) {
	r := newRig(t)
	before, _ := r.send(request("earlier"))
	r.store.Suppress(context.Background(), store.Suppression{Domain: "example.com", Address: "ANA@example.org", Reason: store.ReasonBounce})
	for _, idem := range []string{"", "new"} {
		if _, err := r.send(request(idem)); !errors.Is(err, mail.ErrSuppressed) || r.drv.sends != 1 {
			t.Fatalf("key %q: err %v, sends %d", idem, err, r.drv.sends)
		}
	}
	if again, err := r.send(request("earlier")); err != nil || again != before {
		t.Fatalf("a replay of what was sent before: %q %v", again, err)
	}
	// Another sending domain has its own list.
	other := request("")
	other.From = "app@other.example"
	if _, err := r.send(other); err != nil {
		t.Fatalf("from another domain: %v", err)
	}
	r.store.Unsuppress(context.Background(), "example.com", "ana@example.org")
	if _, err := r.send(request("new")); err != nil {
		t.Fatalf("once lifted: %v", err)
	}
}

func TestARefusalFreesTheKey(t *testing.T) {
	for _, refusal := range []error{driver.ErrRejected, driver.ErrUnavailable, &driver.Throttled{After: time.Minute}} {
		r := newRig(t)
		r.drv.err = refusal
		id, err := r.send(request("k"))
		if !errors.Is(err, refusal) || r.state(t, id) != store.StateFailed {
			t.Fatalf("%v: err %v, state %s", refusal, err, r.state(t, id))
		}
		r.drv.err = nil
		if _, err := r.send(request("k")); err != nil || r.drv.sends != 2 {
			t.Fatalf("%v: the next try: err %v, sends %d", refusal, err, r.drv.sends)
		}
	}
}

func TestAKeyBoundToADomainSendsOnlyFromIt(t *testing.T) {
	r := newRig(t)
	r.key.Domain = "other.example"
	if _, err := r.send(request("")); !errors.Is(err, mail.ErrDomain) || r.drv.sends != 0 {
		t.Fatalf("err %v, sends %d", err, r.drv.sends)
	}
	r.key.Domain = "example.com"
	if _, err := r.send(request("")); err != nil {
		t.Fatalf("its own domain: %v", err)
	}
}

func TestRequestsThatCanNeverBeSent(t *testing.T) {
	cases := map[string]func(*mail.Request){
		"from":            func(r *mail.Request) { r.From = "not an address" },
		"to":              func(r *mail.Request) { r.To = []string{"a@example.org", "b@example.org"} },
		"reply_to":        func(r *mail.Request) { r.ReplyTo = "nope" },
		"subject":         func(r *mail.Request) { r.Subject = "two\nlines" },
		"html":            func(r *mail.Request) { r.Text = "" },
		"headers":         func(r *mail.Request) { r.Headers = map[string]string{"Subject": "x"} },
		"Idempotency-Key": func(r *mail.Request) { r.IdemKey = "caf\xe9" },
	}
	for field, breakIt := range cases {
		r := newRig(t)
		req := request("")
		breakIt(&req)
		var invalid *mail.Invalid
		if _, err := r.send(req); !errors.As(err, &invalid) || invalid.Field != field || r.drv.sends != 0 {
			t.Errorf("%s: err %v, sends %d", field, err, r.drv.sends)
		}
	}
}

// A key whose earlier try got no answer, retried once its recipient is
// suppressed: nothing is sent, and the refusal carries the earlier try's
// id, because that email may have gone. The key is then free.
func TestASuppressedRetryOfATryInDoubtSaysWhichTry(t *testing.T) {
	r := newRig(t)
	r.drv.err = errors.New("connection reset")
	first, err := r.send(request("k"))
	if !errors.Is(err, mail.ErrInDoubt) || first == "" {
		t.Fatalf("the first try: %q %v", first, err)
	}
	r.drv.err = nil
	r.store.Suppress(context.Background(), store.Suppression{Domain: "example.com", Address: "ana@example.org", Reason: store.ReasonBounce})
	r.now = r.now.Add(3 * time.Minute)
	id, err := r.send(request("k"))
	if !errors.Is(err, mail.ErrSuppressed) || id != first || r.drv.sends != 1 || r.state(t, first) != store.StateUnknown {
		t.Fatalf("the retry: %q (want %q) %v, sends %d, state %s", id, first, err, r.drv.sends, r.state(t, first))
	}
	r.store.Unsuppress(context.Background(), "example.com", "ana@example.org")
	if id, err := r.send(request("k")); err != nil || id == first {
		t.Fatalf("once lifted, the key sends anew: %q %v", id, err)
	}
}

// An address with a local part that needs quotes is suppressed and looked
// up under one form, whichever path wrote it.
func TestAQuotedAddressIsSuppressedUnderTheFormItIsSentIn(t *testing.T) {
	r := newRig(t)
	req := request("")
	req.To = []string{`"Ana Maria"@example.org`}
	id, err := r.send(req)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := r.store.Message(context.Background(), id)
	domain, address := store.SuppressionKey(m.Sender, m.Recipient)
	r.store.Suppress(context.Background(), store.Suppression{Domain: domain, Address: address, Reason: store.ReasonBounce})
	if _, err := r.send(req); !errors.Is(err, mail.ErrSuppressed) {
		t.Fatalf("a send to it: %v", err)
	}
}

// An address longer than any server takes is refused, in either place.
func TestAnAddressHasALongestLength(t *testing.T) {
	r := newRig(t)
	long := strings.Repeat("a", 300) + "@" + strings.Repeat("b", 30) + ".example.com"
	for field, req := range map[string]mail.Request{
		"from": {From: long, To: []string{"ana@example.org"}, Subject: "s", Text: "t"},
		"to":   {From: "app@example.com", To: []string{long}, Subject: "s", Text: "t"},
	} {
		var invalid *mail.Invalid
		if _, err := r.send(req); !errors.As(err, &invalid) || invalid.Field != field {
			t.Errorf("%s: %v", field, err)
		}
	}
	if r.drv.sends != 0 {
		t.Fatal("something was sent")
	}
	if _, err := mail.Recipient(long); err == nil {
		t.Fatal("an address too long to be one is looked for")
	}
}
