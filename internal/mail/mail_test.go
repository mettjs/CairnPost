package mail_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/driver"
	"github.com/mettjs/cairnpost/internal/mail"
	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/store/memstore"
)

// fakeDriver answers what it is told to and counts what it was handed.
type fakeDriver struct {
	err   error
	sends int
}

func (*fakeDriver) Name() string { return "fake" }

func (f *fakeDriver) Send(context.Context, driver.Message) (string, error) {
	f.sends++
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
	if err == nil || errors.Is(err, driver.ErrUnavailable) || r.state(t, first) != store.StateSending {
		t.Fatalf("a lost answer: err %v, state %s", err, r.state(t, first))
	}
	r.drv.err = nil
	if _, err := r.send(request("k")); !errors.Is(err, mail.ErrInFlight) || r.drv.sends != 1 {
		t.Fatalf("a retry inside the window: err %v, sends %d", err, r.drv.sends)
	}
	r.now = r.now.Add(2 * time.Minute)
	second, err := r.send(request("k"))
	if err != nil || r.drv.sends != 2 || second == first {
		t.Fatalf("a retry after the window: err %v, sends %d", err, r.drv.sends)
	}
	if r.state(t, first) != store.StateUnknown || r.state(t, second) != store.StateSent {
		t.Fatalf("states: first %s, second %s", r.state(t, first), r.state(t, second))
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
		"Idempotency-Key": func(r *mail.Request) { r.IdemKey = string(make([]byte, 257)) },
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
