package keys

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/store/memstore"
)

// The keys every client is held to (client-cases/keys.json) are told
// apart by one rule. A key to take is, with the space, tabs and line
// breaks around it dropped, not empty and printable ASCII with no space,
// and that is what a client sends; a key to refuse is not. A client does
// not ask more of a key, so that a key of a form a later server makes is
// still taken. The keys to take are held to this package too: among them
// is one of the form a key is made in here, read off a key made here.
var keyLike = regexp.MustCompile(`^[!-~]+$`)

func TestTheClientCasesAreKeysAsTheyAreMade(t *testing.T) {
	const around = " \t\r\n"
	var file struct {
		Taken []struct {
			Given string `json:"given"`
			Sends string `json:"sends"`
		} `json:"taken"`
		Refused []string `json:"refused"`
	}
	raw, err := os.ReadFile("../../client-cases/keys.json")
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		t.Fatal(err)
	}
	if len(file.Taken) == 0 || len(file.Refused) == 0 {
		t.Fatal("the file holds no key to take, or none to refuse")
	}
	// made says a text has the form of a key made here.
	made := func(key string) bool {
		prefix, ok := prefixOf(key)
		if !ok || prefix != strings.ToLower(prefix) {
			return false
		}
		// Written back, since the decoder alone skips a line break.
		rest := key[len(prefix)+1:]
		secret, err := base64.RawURLEncoding.DecodeString(rest)
		return err == nil && len(secret) == 32 && base64.RawURLEncoding.EncodeToString(secret) == rest
	}
	// The form is the maker's own, not this test's idea of it.
	fresh, _, err := New(memstore.New()).Create(context.Background(), "a client", store.ScopeSend, "")
	if err != nil || !made(fresh) {
		t.Fatalf("a key made here does not have the form the cases are held to: %v", err)
	}
	ours := 0
	for _, k := range file.Taken {
		if k.Sends != strings.Trim(k.Given, around) {
			t.Errorf("%q is to be sent as %q, which is not it without the space around it", k.Given, k.Sends)
		}
		if !keyLike.MatchString(k.Sends) {
			t.Errorf("%q is to be taken as a key, and is empty or holds more than printable ASCII", k.Given)
		}
		if made(k.Sends) {
			ours++
		}
	}
	if ours == 0 {
		t.Error("no key to take has the form of one this server makes")
	}
	for _, key := range file.Refused {
		if keyLike.MatchString(strings.Trim(key, around)) {
			t.Errorf("%q is to be refused as a key, and is one a client takes", key)
		}
	}
}
