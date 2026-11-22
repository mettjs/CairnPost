package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
)

// when is a time as the client cases write one: "" for none.
type when string

func (w when) time() time.Time {
	at, _ := time.Parse(time.RFC3339, string(w))
	return at
}

// clientEvent is a webhook as a client must read it, in the form the
// client cases write one.
type clientEvent struct {
	Type  string `json:"type"`
	At    when   `json:"at"`
	Email *struct {
		ID         string   `json:"id"`
		From       string   `json:"from"`
		To         []string `json:"to"`
		CreatedAt  when     `json:"created_at"`
		Code       string   `json:"code"`
		Reason     string   `json:"reason"`
		Bounce     string   `json:"bounce"`
		Suppressed bool     `json:"suppressed"`
	} `json:"email"`
	Suppression *struct {
		Domain, Address, Reason string
	} `json:"suppression"`
	Received *struct {
		ID         string   `json:"id"`
		From       string   `json:"from"`
		To         []string `json:"to"`
		ReceivedAt when     `json:"received_at"`
	} `json:"received"`
}

// written is the body this package writes for what a client is to read;
// nil for a type this server does not send.
func (e clientEvent) written() []byte {
	switch {
	case e.Type == TypeTest:
		return testBody(e.At.time())
	case e.Type == TypeSuppressed && e.Suppression != nil:
		s := e.Suppression
		return suppressedBody(store.Suppression{Domain: s.Domain, Address: s.Address, Reason: s.Reason, CreatedAt: e.At.time()})
	case e.Type == TypeReceived && e.Received != nil && len(e.Received.To) == 1:
		r := e.Received
		return receivedBody(store.Inbound{ID: r.ID, Sender: r.From, Recipient: r.To[0], ReceivedAt: r.ReceivedAt.time()})
	case kindOf(e.Type) != "" && e.Email != nil:
		m := e.Email
		d := &data{EmailID: m.ID, From: m.From, To: m.To, CreatedAt: m.CreatedAt.time(), Code: m.Code, Reason: m.Reason, Suppressed: m.Suppressed}
		if m.Bounce != "" {
			d.Bounce = &bounce{Type: m.Bounce}
		}
		return encode(payload{Type: e.Type, CreatedAt: e.At.time(), Data: d})
	}
	return nil
}

// around is what a client drops around a secret and around the value of a
// signature header, and what stands between two signatures: a space, a
// tab and a line break. Nothing else, so that every client reads one
// value the same way.
const around = " \t\r\n"

// seconds is a time as a signature header carries it.
var seconds = regexp.MustCompile(`^[0-9]{1,15}$`)

// signedAs reads a case's signature headers as a client does: the names
// in any case, the three of one set (webhook-, then svix-) and never a
// mix of the two, each without what is dropped around a value. A case
// names a header once: under two spellings each client reads the one it
// meets first, and they do not meet them in the same order.
func signedAs(t *testing.T, headers map[string]string) (id, stamp, signatures string) {
	said := map[string]string{}
	for name, value := range headers {
		if _, twice := said[strings.ToLower(name)]; twice {
			t.Fatalf("a case names the header %s twice, in two spellings", strings.ToLower(name))
		}
		said[strings.ToLower(name)] = strings.Trim(value, around)
	}
	for _, set := range []string{"webhook-", "svix-"} {
		id, stamp, signatures = said[set+"id"], said[set+"timestamp"], said[set+"signature"]
		if id != "" && stamp != "" && signatures != "" {
			break
		}
	}
	return id, stamp, signatures
}

// written signs as a receiver checks: over the time as the header has it.
// The test holds it to sign wherever the two can be compared.
func written(key []byte, id, stamp string, body []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id + "." + stamp + "."))
	mac.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// The webhooks every client is held to (client-cases/webhooks.json) are
// this package's own. One a client must read, or must refuse for what it
// says or for its age, carries the signature made here; one it must
// refuse as forged has an id, a time and a signature and does not, and
// one it must refuse as unsigned lacks an id, a signature or a time in
// seconds. A case with another signature says here which it should
// carry. And one a client must read, of a type this server sends, has the
// body this package writes for it.
func TestTheClientCasesAreWebhooksAsTheyAreSent(t *testing.T) {
	var file struct {
		Secret  string `json:"secret"`
		Secrets struct {
			Taken, Refused []string
		} `json:"secrets"`
		Cases []struct {
			Name    string            `json:"name"`
			Headers map[string]string `json:"headers"`
			Body    string            `json:"body"`
			Want    struct {
				Refused string       `json:"refused"`
				Event   *clientEvent `json:"event"`
			} `json:"want"`
		} `json:"cases"`
	}
	raw, err := os.ReadFile("../../client-cases/webhooks.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(file.Secret, secretPrefix))
	if err != nil || Reveal(key) != file.Secret || len(file.Cases) == 0 {
		t.Fatalf("the secret is %q, of %d cases: %v", file.Secret, len(file.Cases), err)
	}
	// A secret a client must take is one this server gives, but for a
	// space, a tab or a line break around it. Those are all a client
	// drops, so one it must refuse is none this server gives even with
	// them dropped: the two lists are told apart by one rule.
	given := func(secret string) bool {
		encoded, ok := strings.CutPrefix(secret, secretPrefix)
		made, err := base64.StdEncoding.DecodeString(encoded)
		return ok && err == nil && len(made) > 0 && Reveal(made) == secret
	}
	for _, secret := range file.Secrets.Taken {
		if !given(strings.Trim(secret, around)) {
			t.Errorf("%q is to be taken as a secret, and is none this server gives", secret)
		}
	}
	for _, secret := range file.Secrets.Refused {
		if given(strings.Trim(secret, around)) {
			t.Errorf("%q is to be refused as a secret, and is one this server gives", secret)
		}
	}
	if len(file.Secrets.Taken) == 0 || len(file.Secrets.Refused) == 0 {
		t.Error("the file holds no secret to take, or none to refuse")
	}
	signed, ours := 0, map[string]bool{TypeTest: false}
	for _, name := range Types() {
		ours[name] = false
	}
	for _, c := range file.Cases {
		id, stamp, signatures := signedAs(t, c.Headers)
		whole := id != "" && signatures != "" && seconds.MatchString(stamp)
		// A receiver signs the time's digits as they are written, zeros in
		// front and all. Wherever they are written as this server writes
		// them, that is the signature this server makes.
		made := written(key, id, stamp, []byte(c.Body))
		at, _ := strconv.ParseInt(stamp, 10, 64)
		asNumber := sign(key, id, time.Unix(at, 0), []byte(c.Body))
		plain := strconv.FormatInt(at, 10) == stamp
		if whole && plain && asNumber != made {
			t.Fatalf("%s: this test signs as %s, and the server as %s", c.Name, made, asNumber)
		}
		between := func(r rune) bool { return strings.ContainsRune(around, r) }
		given := strings.FieldsFunc(signatures, between)
		carries := whole && slices.Contains(given, made)
		switch c.Want.Refused {
		case "unsigned":
			if whole {
				t.Errorf("%s: is to be refused as unsigned, and has an id, a time in seconds and a signature", c.Name)
			}
		case "signature":
			if !whole {
				t.Errorf("%s: is to be refused as forged, and lacks an id, a signature or a time in seconds: that is unsigned", c.Name)
			}
			if carries {
				t.Errorf("%s: is to be refused as forged, and carries the signature this server makes", c.Name)
			}
			// A time not written as this server writes it, refused as
			// forged, is there to catch a client that signs the number in
			// place of the digits: it carries that signature or catches none.
			if whole && !plain && !slices.Contains(given, asNumber) {
				t.Errorf("%s: its signature should be %s, the one over the time as a number", c.Name, asNumber)
			}
		case "", "stale", "payload":
			if signed++; !carries {
				t.Errorf("%s: its signature should be %s", c.Name, made)
			}
		default:
			t.Errorf("%s: %q is no reason to refuse a webhook", c.Name, c.Want.Refused)
		}
		if e := c.Want.Event; e != nil {
			if _, sent := ours[e.Type]; !sent {
				continue // a type of a newer server's, which a client passes on
			}
			ours[e.Type] = true
			var got, want any
			if err := json.Unmarshal([]byte(c.Body), &got); err != nil {
				t.Fatalf("%s: %v", c.Name, err)
			}
			_ = json.Unmarshal(e.written(), &want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s: its body should be %s", c.Name, e.written())
			}
		}
	}
	if signed == 0 {
		t.Fatal("no case carries a signature of this server's")
	}
	for name, read := range ours {
		if !read {
			t.Errorf("no case has a client read %s", name)
		}
	}
}
