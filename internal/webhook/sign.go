// Package webhook tells an application what became of its emails: it keeps
// the endpoints, signs each event and delivers it, trying again when an
// endpoint does not take it.
package webhook

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
)

// secretPrefix starts a signing secret as it is shown.
const secretPrefix = "whsec_"

// newSecret makes a signing key.
func newSecret() []byte {
	key := make([]byte, 32)
	_, _ = rand.Read(key) // never fails; it panics if the system has no randomness
	return key
}

// Reveal is a signing key as its endpoint's owner is given it: the form
// verifying libraries take.
func Reveal(key []byte) string { return secretPrefix + base64.StdEncoding.EncodeToString(key) }

// sign is the signature of a webhook: HMAC-SHA256, under the endpoint's
// key, of "id.timestamp.body", the timestamp in Unix seconds. This is the
// Standard Webhooks scheme, which is also the one Svix uses, so a receiver
// verifies with any library for either. The timestamp is part of what is
// signed so that a webhook caught on the way cannot be replayed later.
func sign(key []byte, id string, at time.Time, body []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id + "." + strconv.FormatInt(at.Unix(), 10) + "."))
	mac.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// signatures is a webhook's signature under each key in use, the current
// one first, as the scheme carries several: separated by a space. A
// receiver accepts a webhook when any of them matches.
func signatures(keys [][]byte, id string, at time.Time, body []byte) string {
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		if len(key) > 0 {
			out = append(out, sign(key, id, at, body))
		}
	}
	return strings.Join(out, " ")
}

// keysOf is the keys an endpoint's webhooks are signed with now: its
// own, and those it had before that are still within their time.
func keysOf(e store.Endpoint) [][]byte { return append([][]byte{e.Secret}, e.Previous...) }
