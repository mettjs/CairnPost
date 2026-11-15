// Package inbound takes the emails a mail provider hands on for the
// addresses of a sending domain: it checks who hands one on, keeps it
// as it came for a time, and reads it for whoever may.
package inbound

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The headers an email is handed on with. The email itself is the body,
// as it came. Signature is "v1=" and the HMAC-SHA256, in hex, under the
// secret both sides hold, of Timestamp, To, From and Digest, each behind
// its length as eight bytes, big end first.
const (
	// HeaderTimestamp is when it was handed on, in seconds since 1970.
	HeaderTimestamp = "X-CairnPost-Timestamp"
	// HeaderTo is the address it came for, and HeaderFrom the one it
	// came from, as the sending server gave them: From is empty for a
	// bounce. Whatever in one is not a printable ASCII character, and
	// "%", is written as its UTF-8 bytes, each "%" and two hex digits:
	// a header carries nothing else safely. They are signed as written.
	HeaderTo   = "X-CairnPost-To"
	HeaderFrom = "X-CairnPost-From"
	// HeaderDigest is the SHA-256 of the body, in hex.
	HeaderDigest    = "X-CairnPost-Digest"
	HeaderSignature = "X-CairnPost-Signature"
)

const version = "v1="

// ErrUnsigned is a hand-over that whoever holds the secret did not
// make, or made too long ago.
var ErrUnsigned = errors.New("inbound: not signed by the holder of the secret")

// Envelope is what an email is handed on with.
type Envelope struct {
	Timestamp, To, From, Digest, Signature string
}

// Sign answers the signature of an envelope under a secret.
func Sign(secret string, e Envelope) string {
	mac := hmac.New(sha256.New, []byte(secret))
	for _, part := range []string{e.Timestamp, e.To, e.From, e.Digest} {
		mac.Write(binary.BigEndian.AppendUint64(nil, uint64(len(part))))
		mac.Write([]byte(part))
	}
	return version + hex.EncodeToString(mac.Sum(nil))
}

// Verify says whether an envelope was signed under the secret within
// tolerance of now: ErrUnsigned otherwise. It reads no body, so a
// stranger is refused before one is taken.
func Verify(secret string, e Envelope, now time.Time, tolerance time.Duration) error {
	at, err := strconv.ParseInt(e.Timestamp, 10, 64)
	if err != nil || secret == "" || len(e.Digest) != hex.EncodedLen(sha256.Size) {
		return ErrUnsigned
	}
	if age := now.Sub(time.Unix(at, 0)); age > tolerance || age < -tolerance {
		return ErrUnsigned
	}
	if subtle.ConstantTimeCompare([]byte(Sign(secret, e)), []byte(e.Signature)) != 1 {
		return ErrUnsigned
	}
	return nil
}

// Matches says whether a body is the one an envelope's digest names.
func Matches(e Envelope, digest [sha256.Size]byte) bool {
	return subtle.ConstantTimeCompare([]byte(strings.ToLower(e.Digest)), []byte(hex.EncodeToString(digest[:]))) == 1
}

// Said reads an address as a header carries it (see HeaderTo); false
// for one that is not written so.
func Said(written string) (string, bool) {
	said, err := url.PathUnescape(written)
	return said, err == nil && utf8.ValidString(said)
}
