package inbound

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"
)

// The one signature every Worker and every server must agree on: the
// same values are written beside the Worker's code.
func TestTheSignatureIsTheOneTheWorkerMakes(t *testing.T) {
	sum := sha256.Sum256([]byte("Subject: hi\r\n\r\nhello\r\n"))
	e := Envelope{Timestamp: "1893456000", To: "help@mail.example.com", From: "ana@example.org", Digest: hex.EncodeToString(sum[:])}
	got := Sign("inbound-secret-for-tests-0123456789", e)
	if e.Digest != "152f17ac7a38f5d25376dd86328a96a17551fa061a49aa32649ee50c07d9c7c2" ||
		got != "v1=dc8231a625ddbd1fbcd83ad55995dac3ad63c7c46a6e1df33474467e40daff28" {
		t.Fatalf("digest %s signature %s", e.Digest, got)
	}
	e.Signature = got
	now := time.Unix(1893456000, 0)
	if err := Verify("inbound-secret-for-tests-0123456789", e, now.Add(4*time.Minute), 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	if !Matches(e, sum) || Matches(e, sha256.Sum256([]byte("other"))) {
		t.Error("the digest does not tell its body from another")
	}
	for name, change := range map[string]func(*Envelope){
		"another address":     func(e *Envelope) { e.To = "other@mail.example.com" },
		"another sender":      func(e *Envelope) { e.From = "" },
		"another body":        func(e *Envelope) { e.Digest = hex.EncodeToString(make([]byte, 32)) },
		"another time":        func(e *Envelope) { e.Timestamp = "1893456001" },
		"no signature":        func(e *Envelope) { e.Signature = "" },
		"a time that is none": func(e *Envelope) { e.Timestamp = "soon" },
		// What one part loses the next cannot gain: each is behind its length.
		"parts run together": func(e *Envelope) { e.To, e.From = e.To+"a", e.From[1:] },
	} {
		changed := e
		change(&changed)
		if err := Verify("inbound-secret-for-tests-0123456789", changed, now, 5*time.Minute); !errors.Is(err, ErrUnsigned) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, at := range map[string]time.Time{"too old": now.Add(6 * time.Minute), "from ahead": now.Add(-6 * time.Minute)} {
		if err := Verify("inbound-secret-for-tests-0123456789", e, at, 5*time.Minute); !errors.Is(err, ErrUnsigned) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := Verify("another secret", e, now, 5*time.Minute); !errors.Is(err, ErrUnsigned) {
		t.Error("another secret verified")
	}
	if err := Verify("", Envelope{Timestamp: e.Timestamp, Digest: e.Digest, Signature: Sign("", Envelope{Timestamp: e.Timestamp, Digest: e.Digest})}, now, time.Minute); !errors.Is(err, ErrUnsigned) {
		t.Error("no secret verified")
	}
}

func TestAnAddressIsCarriedAsAHeaderCan(t *testing.T) {
	for written, want := range map[string]string{"help@mail.example.com": "help@mail.example.com", "": "", "a%20b%25%C3%A9@x.example": "a b%é@x.example"} {
		if got, ok := Said(written); !ok || got != want {
			t.Errorf("%q reads %q %v", written, got, ok)
		}
	}
	for _, written := range []string{"a%zz@x.example", "a%@x.example", "a%FF@x.example"} {
		if got, ok := Said(written); ok {
			t.Errorf("%q read as %q", written, got)
		}
	}
}

func TestWhoAnEmailIsForAndFromIsReadWithCare(t *testing.T) {
	for written, want := range map[string][2]string{
		"Help@Mail.Example.COM":        {"help@mail.example.com", "mail.example.com"},
		"%22a%20b%22@mail.example.com": {`"a b"@mail.example.com`, "mail.example.com"},
	} {
		if to, domain, ok := recipient(written); !ok || to != want[0] || domain != want[1] {
			t.Errorf("%q: %q %q %v", written, to, domain, ok)
		}
	}
	// The Kelvin sign folds to "k": it is no letter of a domain here.
	for _, written := range []string{"x@ban%E2%84%AA.example", "not an address", "Ana <ana@example.org>", "a@b", "a@-x.example",
		strings.Repeat("a", 400) + "@x.example", "a@x.example%0d%0aBcc: b@x.example"} {
		if to, domain, ok := recipient(written); ok {
			t.Errorf("%q was taken as %q of %q", written, to, domain)
		}
	}
	for written, want := range map[string]string{"": "", "<>": "", " ": "", "Ana@Example.org": "Ana@Example.org", "a..b@x.org": "a..b@x.org",
		"a.@x.org": "a.@x.org", "odd%0d%0aname": "odd  name", strings.Repeat("a", 400): strings.Repeat("a", 320)} {
		if got := sender(written); got != want {
			t.Errorf("%q came from %q", written, got)
		}
	}
}
