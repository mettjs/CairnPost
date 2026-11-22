package webhook

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"reflect"
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

// The webhooks every client is held to (client-cases/webhooks.json) are
// this package's own. One a client must read, or must refuse for what it
// says or for its age, carries the signature made here; one it must
// refuse as forged does not. A case with another signature says here
// which it should carry. And one a client must read, of a type this
// server sends, has the body this package writes for it.
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
		if !given(strings.Trim(secret, " \t\r\n")) {
			t.Errorf("%q is to be taken as a secret, and is none this server gives", secret)
		}
	}
	for _, secret := range file.Secrets.Refused {
		if given(strings.Trim(secret, " \t\r\n")) {
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
		said := map[string]string{}
		for name, value := range c.Headers {
			name = strings.ToLower(name)
			said[strings.TrimPrefix(strings.TrimPrefix(name, "webhook-"), "svix-")] = value
		}
		seconds, err := strconv.ParseInt(said["timestamp"], 10, 64)
		made := sign(key, said["id"], time.Unix(seconds, 0), []byte(c.Body))
		carries := err == nil && slices.Contains(strings.Fields(said["signature"]), made)
		switch c.Want.Refused {
		case "unsigned":
		case "signature":
			if carries {
				t.Errorf("%s: is to be refused as forged, and carries the signature this server makes", c.Name)
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
