package schedule_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/driver"
	"github.com/mettjs/cairnpost/internal/mail"
	"github.com/mettjs/cairnpost/internal/schedule"
	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/store/memstore"
)

// upstream answers what it is told to and keeps what it was handed.
type upstream struct {
	mu   sync.Mutex
	err  error
	held []driver.Message
}

func (*upstream) Name() string { return "fake" }

func (u *upstream) Send(_ context.Context, m driver.Message) (string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.held = append(u.held, m)
	return "up-1", u.err
}

type rig struct {
	t     *testing.T
	store *memstore.Store
	drv   *upstream
	mail  *mail.Service
	run   *schedule.Runner
	now   time.Time
	key   store.Key
}

func newRig(t *testing.T, keep time.Duration) *rig {
	r := &rig{t: t, store: memstore.New(), drv: &upstream{}, now: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)}
	r.store.Now = func() time.Time { return r.now }
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var err error
	if r.key, err = r.store.CreateKey(context.Background(), store.Key{Name: "app", Prefix: "cp_app", Scope: store.ScopeSend}, 0); err != nil {
		t.Fatal(err)
	}
	r.mail = mail.New(r.store, r.drv, mail.Options{SendTimeout: time.Second, IdempotencyTTL: 24 * time.Hour, InDoubtWindow: 2 * time.Minute,
		KeepContent: keep, Now: r.store.Now}, log)
	r.run = schedule.New(r.store, r.mail, schedule.Options{Poll: time.Second, Batch: 10, Parallel: 2,
		GiveUp: 3 * time.Hour, Abandoned: 5 * time.Minute}, r.store.Now, log)
	return r
}

// later asks for an email in an hour and answers its id.
func (r *rig) later(to string, files ...mail.Attachment) string {
	r.t.Helper()
	got, err := r.mail.Send(context.Background(), r.key, "cp_app_x", mail.Request{From: "App <app@example.com>", To: []string{to},
		Cc: []string{"cc@example.org"}, Subject: "Later", Text: "Body", Attachments: files, ScheduledAt: r.now.Add(time.Hour)})
	if err != nil || got.ScheduledAt.IsZero() {
		r.t.Fatalf("scheduling: %+v %v", got, err)
	}
	return got.ID
}

func (r *rig) pass() (int, time.Duration) {
	r.t.Helper()
	n, wait, err := r.run.Pass(context.Background())
	if err != nil {
		r.t.Fatal(err)
	}
	return n, wait
}

func (r *rig) state(id string) string {
	r.t.Helper()
	m, _, err := r.mail.MessageFor(context.Background(), r.key, id)
	if err != nil {
		r.t.Fatalf("%s: %v", id, err)
	}
	return m.State
}

func TestAnEmailIsSentWhenItsTimeComesAndNotBefore(t *testing.T) {
	r := newRig(t, 0)
	id := r.later("ana@example.org", mail.Attachment{Filename: "a.txt", Content: []byte("FILEBYTES")})
	if n, _ := r.pass(); n != 0 || len(r.drv.held) != 0 || r.state(id) != store.StateScheduled {
		t.Fatalf("before its time: %d taken, %d sent, %s", n, len(r.drv.held), r.state(id))
	}
	r.now = r.now.Add(time.Hour)
	if n, _ := r.pass(); n != 1 || len(r.drv.held) != 1 || r.state(id) != store.StateSent {
		t.Fatalf("at its time: %d taken, %d sent, %s", n, len(r.drv.held), r.state(id))
	}
	got := r.drv.held[0]
	if got.Subject != "Later" || got.FromName != "App" || got.To[0] != "ana@example.org" || got.Cc[0] != "cc@example.org" ||
		len(got.Attachments) != 1 || string(got.Attachments[0].Content) != "FILEBYTES" || got.Attachments[0].Type != "text/plain" {
		t.Fatalf("what was sent: %+v", got)
	}
	// Sent, it waits no more, and nothing it said is kept: not asked for.
	if _, err := r.store.Held(context.Background(), id); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("it still waits: %v", err)
	}
	if words, files, _ := r.store.HeldWords(context.Background(), id); words != nil || files != nil {
		t.Error("what it said is still kept")
	}
	if c, err := r.mail.Content(context.Background(), id); c != nil || err != nil {
		t.Errorf("content after the send: %+v %v", c, err)
	}
	if n, _ := r.pass(); n != 0 || len(r.drv.held) != 1 {
		t.Error("it was taken a second time")
	}
}

func TestAnEmailTheProviderDoesNotTakeIsTriedAgainOnTheSameRecord(t *testing.T) {
	r := newRig(t, 0)
	id := r.later("ana@example.org")
	r.now = r.now.Add(time.Hour)
	r.drv.err = driver.ErrUnavailable
	r.pass()
	// Still to come, as far as anyone asking is told.
	if r.state(id) != store.StateScheduled || len(r.drv.held) != 1 {
		t.Fatalf("after a failed try: %s, %d tries", r.state(id), len(r.drv.held))
	}
	if n, _ := r.pass(); n != 0 {
		t.Fatal("it was tried again at once")
	}
	r.drv.err = nil
	r.now = r.now.Add(time.Minute)
	r.pass()
	if r.state(id) != store.StateSent || len(r.drv.held) != 2 {
		t.Fatalf("after the second try: %s, %d tries", r.state(id), len(r.drv.held))
	}
	// One email, counted once: the rows of the first try were begun again.
	all, _ := r.store.Messages(context.Background(), store.MessageQuery{Limit: 10})
	if len(all) != 1 || len(all[0].Recipients) != 2 {
		t.Fatalf("%d emails on record: %+v", len(all), all)
	}
}

func TestAnEmailIsGivenUpOnAfterItsTime(t *testing.T) {
	r := newRig(t, 0)
	id := r.later("ana@example.org")
	r.drv.err = driver.ErrUnavailable
	r.now = r.now.Add(time.Hour)
	for range 12 {
		r.pass()
		r.now = r.now.Add(time.Hour)
	}
	if r.state(id) != store.StateFailed || len(r.drv.held) < 2 || len(r.drv.held) > 5 {
		t.Fatalf("%s after %d tries", r.state(id), len(r.drv.held))
	}
	if _, err := r.store.Held(context.Background(), id); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("it still waits: %v", err)
	}
}

func TestAnAnswerLostIsNotSentAgain(t *testing.T) {
	r := newRig(t, 0)
	id := r.later("ana@example.org")
	r.drv.err = errors.New("the line dropped")
	r.now = r.now.Add(time.Hour)
	r.pass()
	r.now = r.now.Add(time.Hour)
	r.pass()
	if r.state(id) != store.StateUnknown || len(r.drv.held) != 1 {
		t.Fatalf("%s after %d tries", r.state(id), len(r.drv.held))
	}
}

func TestTheProvidersLimitPausesEverythingAndSpendsNoTry(t *testing.T) {
	r := newRig(t, 0)
	for _, to := range []string{"a@example.org", "b@example.org", "c@example.org", "d@example.org"} {
		r.later(to)
	}
	r.drv.err = &driver.Throttled{After: 10 * time.Minute}
	r.now = r.now.Add(time.Hour)
	n, wait := r.pass()
	if n != 4 || wait != 10*time.Minute || len(r.drv.held) > 2 {
		t.Fatalf("%d taken, wait %s, %d handed to the provider", n, wait, len(r.drv.held))
	}
	r.drv.err = nil
	if n, _ := r.pass(); n != 0 {
		t.Fatal("tried again before the wait was over")
	}
	// Nothing was begun but what was handed to the provider.
	list, _ := r.store.ListHeld(context.Background(), "", time.Time{}, 10)
	tried := 0
	for _, h := range list {
		tried += h.Tries
	}
	if tried != len(r.drv.held) {
		t.Fatalf("%d tries are counted for %d handed to the provider", tried, len(r.drv.held))
	}
	r.now = r.now.Add(10 * time.Minute)
	if n, _ := r.pass(); n != 4 {
		t.Fatalf("%d taken after the wait", n)
	}
	if w, _ := r.store.Waiting(context.Background(), ""); w != 0 {
		t.Fatalf("%d still wait", w)
	}
}

func TestWhatCannotBeSentAnyMoreIsCancelledWithItsReason(t *testing.T) {
	r := newRig(t, 0)
	gone, revoked := r.later("gone@example.org"), ""
	// Everyone it names in the open is suppressed by the time it is due.
	for _, a := range []string{"gone@example.org", "cc@example.org"} {
		if _, err := r.store.Suppress(context.Background(), store.Suppression{Domain: "example.com", Address: a, Reason: store.ReasonManual}); err != nil {
			t.Fatal(err)
		}
	}
	r.now = r.now.Add(time.Hour)
	r.pass()
	m, _, err := r.mail.MessageFor(context.Background(), r.key, gone)
	if err != nil || m.State != store.StateCancelled || m.Reason != store.HeldSuppressed || len(r.drv.held) != 0 {
		t.Fatalf("%+v %v, %d sent", m, err, len(r.drv.held))
	}
	// And one whose key was revoked while it waited: at once, not when
	// its time comes.
	revoked = r.later("ok@example.net")
	if err := r.store.RevokeKey(context.Background(), "cp_app"); err != nil {
		t.Fatal(err)
	}
	h, err := r.store.Held(context.Background(), revoked)
	if err != nil || h.State != store.StateCancelled || h.Reason != store.HeldKeyRevoked || len(r.drv.held) != 0 {
		t.Fatalf("%+v %v, %d sent", h, err, len(r.drv.held))
	}
	if words, _, _ := r.store.HeldWords(context.Background(), revoked); words != nil {
		t.Error("what a cancelled email said is still kept")
	}
}

func TestWhatAStoppedServerHadTakenIsRecovered(t *testing.T) {
	r := newRig(t, 0)
	notHanded, handed := r.later("a@example.org"), r.later("b@example.org")
	r.now = r.now.Add(time.Hour)
	taken, err := r.store.TakeDue(context.Background(), 10)
	if err != nil || len(taken) != 2 {
		t.Fatalf("%d taken: %v", len(taken), err)
	}
	// One had been begun when the server stopped; the other had not.
	for _, h := range taken {
		if h.ID == handed {
			if _, err := r.store.Start(context.Background(), store.Start{ID: h.ID, Claim: h.Claim, KeyID: h.KeyID, Sender: h.Sender, Recipients: h.Recipients, Driver: "fake"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	r.now = r.now.Add(5 * time.Minute)
	if n, err := r.store.Recover(context.Background(), 5*time.Minute, 10); n != 2 || err != nil {
		t.Fatalf("%d recovered: %v", n, err)
	}
	r.pass()
	if len(r.drv.held) != 1 || r.drv.held[0].To[0] != "a@example.org" || r.state(notHanded) != store.StateSent {
		t.Fatalf("%d sent after recovery: %+v", len(r.drv.held), r.drv.held)
	}
	if got := r.state(handed); got != store.StateSending {
		t.Fatalf("the one that may have gone is %q", got)
	}
}

func TestWithContentKeptTheWordsMoveToTheMessage(t *testing.T) {
	r := newRig(t, 24*time.Hour)
	id := r.later("ana@example.org")
	before, err := r.mail.Content(context.Background(), id)
	if err != nil || before == nil || before.Subject != "Later" || !before.KeptUntil.Equal(r.now.Add(time.Hour)) {
		t.Fatalf("while it waits: %+v %v", before, err)
	}
	r.now = r.now.Add(time.Hour)
	r.pass()
	after, err := r.mail.Content(context.Background(), id)
	if err != nil || after == nil || after.Subject != "Later" || after.FromName != "App" {
		t.Fatalf("after the send: %+v %v", after, err)
	}
}

// An email is one taker's: whoever took it before, and was held to have
// stopped, neither sends it nor changes what the next taker does.
func TestAnEmailTakenAgainIsNotSentByWhoTookItFirst(t *testing.T) {
	r := newRig(t, 0)
	id := r.later("ana@example.org")
	r.now = r.now.Add(time.Hour)
	first, err := r.store.TakeDue(context.Background(), 10)
	if err != nil || len(first) != 1 {
		t.Fatalf("%d taken: %v", len(first), err)
	}
	// The first taker is slow; its email is recovered and taken again.
	r.now = r.now.Add(5 * time.Minute)
	if n, _ := r.store.Recover(context.Background(), 5*time.Minute, 10); n != 1 {
		t.Fatalf("%d recovered", n)
	}
	second, _ := r.store.TakeDue(context.Background(), 10)
	if len(second) != 1 || second[0].Claim == first[0].Claim {
		t.Fatalf("taken again: %+v", second)
	}
	if err := r.mail.SendHeld(context.Background(), second[0], true); err != nil {
		t.Fatal(err)
	}
	// Now the first one wakes up.
	if err := r.mail.SendHeld(context.Background(), first[0], true); !errors.Is(err, mail.ErrGone) {
		t.Fatalf("the first taker's send: %v", err)
	}
	if err := r.store.Postpone(context.Background(), id, first[0].Claim, 0); err == nil {
		t.Fatal("the first taker put it back to wait")
	}
	if len(r.drv.held) != 1 || r.state(id) != store.StateSent {
		t.Fatalf("%d sent, %s", len(r.drv.held), r.state(id))
	}
	if n, _ := r.pass(); n != 0 {
		t.Fatal("it waits again")
	}
}

// An email the provider refused for good, or took, waits no more from
// that moment: a server that stops right then leaves nothing to send.
func TestASettledEmailIsNotRecoveredAndSentAgain(t *testing.T) {
	for name, answer := range map[string]error{"refused": driver.ErrRejected, "taken": nil, "lost": errors.New("dropped")} {
		r := newRig(t, 0)
		id := r.later("ana@example.org")
		r.now = r.now.Add(time.Hour)
		taken, _ := r.store.TakeDue(context.Background(), 10)
		r.drv.err = answer
		_ = r.mail.SendHeld(context.Background(), taken[0], true)
		// The server stops here, before it lets the email go.
		r.drv.err = nil
		r.now = r.now.Add(10 * time.Minute)
		if n, _ := r.store.Recover(context.Background(), 5*time.Minute, 10); n != 0 {
			t.Errorf("%s: %d recovered", name, n)
		}
		if n, _ := r.pass(); n != 0 || len(r.drv.held) != 1 {
			t.Errorf("%s: taken again (%d), %d handed to the provider", name, n, len(r.drv.held))
		}
		if _, err := r.store.Held(context.Background(), id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: it still waits: %v", name, err)
		}
	}
}

// Between two tries the email goes to whom the first try was to go to:
// an address taken off the suppression list since has no record, and is
// not sent to without one.
func TestAnAddressLiftedBetweenTriesIsNotSentToWithoutARecord(t *testing.T) {
	r := newRig(t, 0)
	ctx := context.Background()
	if _, err := r.store.Suppress(ctx, store.Suppression{Domain: "example.com", Address: "cc@example.org", Reason: store.ReasonManual}); err != nil {
		t.Fatal(err)
	}
	id := r.later("ana@example.org")
	r.now = r.now.Add(time.Hour)
	r.drv.err = driver.ErrUnavailable
	r.pass()
	if err := r.store.Unsuppress(ctx, "example.com", "cc@example.org"); err != nil {
		t.Fatal(err)
	}
	r.drv.err = nil
	r.now = r.now.Add(time.Minute)
	r.pass()
	last := r.drv.held[len(r.drv.held)-1]
	m, _ := r.store.Message(ctx, id)
	if len(last.Cc) != 0 || len(last.To) != 1 || len(m.Recipients) != 1 || len(m.LeftOut) != 1 || m.State != store.StateSent {
		t.Fatalf("sent to %v and %v; on record %+v", last.To, last.Cc, m)
	}
}

// A send that breaks in our own code ends neither the server nor the
// other emails, and is not handed to the provider a second time.
type breaks struct{ *mail.Service }

func (b breaks) SendHeld(ctx context.Context, h store.Held, again bool) error {
	if h.Recipients[0].Address == "bad@example.org" {
		panic("broken")
	}
	return b.Service.SendHeld(ctx, h, again)
}

func TestASendThatPanicsEndsNothingElse(t *testing.T) {
	r := newRig(t, 0)
	r.run = schedule.New(r.store, breaks{r.mail}, schedule.Options{Poll: time.Second, Batch: 10, Parallel: 1,
		GiveUp: 3 * time.Hour, Abandoned: 5 * time.Minute}, r.store.Now, slog.New(slog.NewTextHandler(io.Discard, nil)))
	bad, good := r.later("bad@example.org"), r.later("ok@example.org")
	r.now = r.now.Add(time.Hour)
	r.pass()
	if r.state(good) != store.StateSent || r.state(bad) != store.StateScheduled || len(r.drv.held) != 1 {
		t.Fatalf("%s, %s, %d sent", r.state(good), r.state(bad), len(r.drv.held))
	}
	// Long past its time, it is given up on and said to be.
	r.now = r.now.Add(4 * time.Hour)
	r.store.Recover(context.Background(), 5*time.Minute, 10)
	r.pass()
	if h, err := r.store.Held(context.Background(), bad); err != nil || h.State != store.StateCancelled || h.Reason != store.HeldFailed {
		t.Fatalf("%+v %v", h, err)
	}
}

// When the server stops, what it took and did not begin waits again.
func TestStoppingHandsBackWhatWasNotBegun(t *testing.T) {
	r := newRig(t, 0)
	r.later("a@example.org")
	r.later("b@example.org")
	r.now = r.now.Add(time.Hour)
	stopped, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := r.run.Pass(stopped); err == nil {
		// Taking them may fail on a context that is done; if it did
		// not, none was begun and all wait again.
		if n, _ := r.pass(); n != 2 || len(r.drv.held) != 2 {
			t.Fatalf("%d taken afterwards, %d sent", n, len(r.drv.held))
		}
	}
}

// Cancelled between two tries, nothing it said is left: not what waited,
// and not what the failed try kept of its words.
func TestCancelledBetweenTriesItsWordsAreGone(t *testing.T) {
	r := newRig(t, 24*time.Hour)
	id := r.later("ana@example.org")
	r.now = r.now.Add(time.Hour)
	r.drv.err = driver.ErrUnavailable
	r.pass()
	if c, err := r.mail.Content(context.Background(), id); c == nil || err != nil {
		t.Fatalf("between tries: %+v %v", c, err)
	}
	if err := r.mail.Cancel(context.Background(), r.key, id); err != nil {
		t.Fatal(err)
	}
	if c, err := r.mail.Content(context.Background(), id); c != nil || err != nil {
		t.Fatalf("once cancelled: %+v %v", c, err)
	}
	if raw, _, _ := r.store.Content(context.Background(), id, 24*time.Hour); raw != nil {
		t.Fatal("the failed try's copy of its words is still kept")
	}
	if r.state(id) != store.StateCancelled {
		t.Fatalf("it reads %s", r.state(id))
	}
}

// forgetful is a store that cannot write down, once, that a try was not
// taken.
type forgetful struct {
	*memstore.Store
	failed bool
}

func (f *forgetful) Unsent(ctx context.Context, id string) error {
	if !f.failed {
		f.failed = true
		return errors.New("the database is slow")
	}
	return f.Store.Unsent(ctx, id)
}

// A try the provider did not take is tried again even when that could
// not be written down: putting the email back to wait says it.
func TestATryThatCouldNotBeRecordedIsStillTriedAgain(t *testing.T) {
	r := newRig(t, 0)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	r.mail = mail.New(&forgetful{Store: r.store}, r.drv, mail.Options{SendTimeout: time.Second, IdempotencyTTL: 24 * time.Hour,
		InDoubtWindow: 2 * time.Minute, Now: r.store.Now}, log)
	r.run = schedule.New(r.store, r.mail, schedule.Options{Poll: time.Second, Batch: 10, Parallel: 2,
		GiveUp: 3 * time.Hour, Abandoned: 5 * time.Minute}, r.store.Now, log)
	id := r.later("ana@example.org")
	r.now = r.now.Add(time.Hour)
	r.drv.err = driver.ErrUnavailable
	r.pass()
	r.drv.err = nil
	r.now = r.now.Add(time.Minute)
	r.pass()
	if r.state(id) != store.StateSent || len(r.drv.held) != 2 {
		t.Fatalf("%s after %d tries", r.state(id), len(r.drv.held))
	}
}

// A try under way is not taken for a send left unanswered because its
// email was first tried long ago.
func TestALaterTryIsNotSweptAsAbandoned(t *testing.T) {
	r := newRig(t, 0)
	id := r.later("ana@example.org")
	r.now = r.now.Add(time.Hour)
	r.drv.err = driver.ErrUnavailable
	r.pass()
	r.now = r.now.Add(30 * time.Minute)
	taken, _ := r.store.TakeDue(context.Background(), 10)
	if _, err := r.store.Start(context.Background(), store.Start{ID: id, Claim: taken[0].Claim, KeyID: taken[0].KeyID,
		Sender: taken[0].Sender, Recipients: taken[0].Recipients, Driver: "fake"}); err != nil {
		t.Fatal(err)
	}
	if n, _ := r.store.MarkUnanswered(context.Background(), 5*time.Minute, 10); n != 0 {
		t.Fatalf("%d rows of a try just begun were swept", n)
	}
}
