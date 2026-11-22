package mail_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/mettjs/cairnpost/internal/mail"
)

// The idempotency keys every client is held to
// (client-cases/idempotency.json) are held to what this server does with
// one. A key a client must send reaches a server as it was sent, and a
// send and a batch here both take it. A key a client must refuse does not
// reach a server as it was sent, or is refused here as a bad key. The
// empty key is in neither list, since the clients do not read it alike.
func TestTheClientCasesAreIdempotencyKeysAsTheyAreTaken(t *testing.T) {
	var file struct {
		Taken   []string `json:"taken"`
		Refused []string `json:"refused"`
	}
	raw, err := os.ReadFile("../../client-cases/idempotency.json")
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		t.Fatal(err)
	}
	if len(file.Taken) == 0 || len(file.Refused) == 0 {
		t.Fatal("the file holds no key to send, or none to refuse")
	}
	// arrives says a key crosses HTTP as it is: sent in the header, and
	// read out of it the same on the other side.
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Idempotency-Key")
	}))
	t.Cleanup(srv.Close)
	arrives := func(key string) bool {
		req, _ := http.NewRequest(http.MethodPost, srv.URL, nil)
		req.Header.Set("Idempotency-Key", key)
		got = "not this"
		res, err := srv.Client().Do(req)
		if err != nil {
			return false
		}
		res.Body.Close()
		return got == key
	}
	// badKey says a refusal is one of the key, and of nothing else.
	badKey := func(err error) bool {
		var invalid *mail.Invalid
		return errors.As(err, &invalid) && invalid.Field == "Idempotency-Key"
	}
	send := func(key string) error {
		_, err := newRig(t).send(request(key))
		return err
	}
	batch := func(key string) error {
		svc, _, _ := batchRig(mail.Options{})
		_, err := svc.SendBatch(context.Background(), sender, "cp_x", key, to("ana", "ben"))
		return err
	}
	for _, key := range append(file.Taken, file.Refused...) {
		if key == "" {
			t.Error("the empty key is in a list: the clients do not read it alike")
		}
	}
	for _, key := range file.Taken {
		if !arrives(key) {
			t.Errorf("%q is to be sent as it is, and does not arrive so", key)
		}
		if one, several := send(key), batch(key); one != nil || several != nil {
			t.Errorf("%q is to be sent as it is, and this server refuses it: a send %v, a batch %v", key, one, several)
		}
	}
	for _, key := range file.Refused {
		if !arrives(key) {
			continue // what arrived would be another key, or nothing went
		}
		if one, several := send(key), batch(key); !badKey(one) || !badKey(several) {
			t.Errorf("%q is to be refused by a client, and this server takes it as it is: a send %v, a batch %v", key, one, several)
		}
	}
}
