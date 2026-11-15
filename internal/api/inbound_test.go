package api_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"context"

	"github.com/mettjs/cairnpost/internal/inbound"
	"github.com/mettjs/cairnpost/internal/store"
)

const cameMail = "From: Ana <ana@example.org>\r\nTo: help@mail.example.com\r\nSubject: INSUBJECTQ\r\n" +
	"Content-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\nContent-Type: text/plain\r\n\r\nINBODYQ\r\n" +
	"--b\r\nContent-Type: application/pdf; name=in.pdf\r\nContent-Transfer-Encoding: base64\r\n\r\nJVBERi0x\r\n--b--\r\n"

// handOn posts an email as whoever holds the secret would, changed by
// change before it is signed and by after once it is.
func (r *rig) handOn(to, from, raw string, change, after func(*inbound.Envelope)) (int, map[string]any) {
	sum := sha256.Sum256([]byte(raw))
	e := inbound.Envelope{Timestamp: strconv.FormatInt(time.Now().Unix(), 10), To: to, From: from, Digest: hex.EncodeToString(sum[:])}
	if change != nil {
		change(&e)
	}
	e.Signature = inbound.Sign(inboundSecret, e)
	if after != nil {
		after(&e)
	}
	req := httptest.NewRequest("POST", "/inbound", bytes.NewReader([]byte(raw)))
	for name, v := range map[string]string{inbound.HeaderTimestamp: e.Timestamp, inbound.HeaderTo: e.To, inbound.HeaderFrom: e.From,
		inbound.HeaderDigest: e.Digest, inbound.HeaderSignature: e.Signature} {
		req.Header.Set(name, v)
	}
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestAnEmailHandedOnIsKeptOnceAndReadByAnAdminKey(t *testing.T) {
	r := newRig(t)
	code, made := r.handOn("Help@Mail.Example.com", "ana@example.org", cameMail, nil, nil)
	id, _ := made["id"].(string)
	if code != 201 || id == "" {
		t.Fatalf("%d %v", code, made)
	}
	// Handed on again, it is the same email.
	if code, again := r.handOn("help@mail.example.com", "ana@example.org", cameMail, nil, nil); code != 200 || again["id"] != id {
		t.Fatalf("handed on twice: %d %v", code, again)
	}
	_, _, got := r.do("GET", "/inbound/"+id, r.admin, "", "")
	content, _ := got["content"].(map[string]any)
	files, _ := content["attachments"].([]any)
	if got["to"] != "help@mail.example.com" || got["from"] != "ana@example.org" || content["subject"] != "INSUBJECTQ" ||
		content["text"] != "INBODYQ" || content["from"] != "Ana <ana@example.org>" || len(files) != 1 || got["kept_until"] == nil {
		t.Fatalf("%v", got)
	}
	if rec := r.file("/inbound/"+id+"/attachments/1", r.admin); rec.Code != 200 || rec.Body.String() != "%PDF-1" ||
		!strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment; filename=in.pdf") {
		t.Errorf("the file: %d %q %v", rec.Code, rec.Body, rec.Header())
	}
	if rec := r.file("/inbound/"+id+"/raw", r.admin); rec.Code != 200 || rec.Body.String() != cameMail ||
		rec.Header().Get("Content-Type") != "application/octet-stream" {
		t.Errorf("as it came: %d", rec.Code)
	}
	// A list says who and when, and nothing of what it says.
	code, _, list := r.do("GET", "/inbound", r.admin, "", "")
	if raw, _ := json.Marshal(list); code != 200 || len(list["data"].([]any)) != 1 || strings.Contains(string(raw), "INSUBJECTQ") {
		t.Errorf("%d %s", code, raw)
	}
	// Nobody else reads it.
	other, _, err := r.keys.Create(context.Background(), "other", store.ScopeAdmin, "other.example")
	if err != nil {
		t.Fatal(err)
	}
	own, _, _ := r.keys.Create(context.Background(), "own", store.ScopeAdmin, "mail.example.com")
	for _, path := range []string{"/inbound/" + id, "/inbound/" + id + "/raw", "/inbound/" + id + "/attachments/1"} {
		if rec := r.file(path, r.send); rec.Code != 403 {
			t.Errorf("%s by a send key: %d", path, rec.Code)
		}
		if rec := r.file(path, other); rec.Code != 404 || strings.Contains(rec.Body.String(), "INBODYQ") {
			t.Errorf("%s by another domain's admin: %d", path, rec.Code)
		}
		if rec := r.file(path, own); rec.Code != 200 {
			t.Errorf("%s by its domain's admin: %d", path, rec.Code)
		}
	}
	if _, _, none := r.do("GET", "/inbound", other, "", ""); len(none["data"].([]any)) != 0 {
		t.Errorf("another domain's list: %v", none)
	}
	for _, path := range []string{"/inbound/nope", "/inbound/" + id + "/attachments/2", "/inbound/" + id + "/attachments/0"} {
		if rec := r.file(path, r.admin); rec.Code != 404 {
			t.Errorf("%s: %d", path, rec.Code)
		}
	}
	if code, _, _ := r.do("DELETE", "/inbound/"+id, other, "", ""); code != 404 {
		t.Errorf("deleted by another domain: %d", code)
	}
	if code, _, _ := r.do("DELETE", "/inbound/"+id, r.admin, "", ""); code != 204 {
		t.Errorf("deleted: %d", code)
	}
	if code, _, _ := r.do("GET", "/inbound/"+id, r.admin, "", ""); code != 404 {
		t.Errorf("read once deleted: %d", code)
	}
}

// unread fails the test if any of it is read.
type unread struct{ t *testing.T }

func (u unread) Read([]byte) (int, error) {
	u.t.Error("the body of an unsigned hand-over was read")
	return 0, io.EOF
}

func TestOnlyTheHolderOfTheSecretHandsAnEmailOn(t *testing.T) {
	r := newRig(t)
	// A stranger is refused on the headers alone: nothing of what was
	// sent is read.
	req := httptest.NewRequest("POST", "/inbound", unread{t})
	req.Header.Set(inbound.HeaderTo, "help@mail.example.com")
	req.Header.Set(inbound.HeaderSignature, "v1=00")
	rec := httptest.NewRecorder()
	if r.h.ServeHTTP(rec, req); rec.Code != 401 {
		t.Errorf("unsigned: %d", rec.Code)
	}
	old := func(e *inbound.Envelope) { e.Timestamp = strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10) }
	for name, try := range map[string]struct {
		change, after func(*inbound.Envelope)
		code          int
	}{
		"signed long ago":                       {old, nil, 401},
		"another address":                       {nil, func(e *inbound.Envelope) { e.To = "other@mail.example.com" }, 401},
		"another sender":                        {nil, func(e *inbound.Envelope) { e.From = "boss@example.org" }, 401},
		"another body":                          {func(e *inbound.Envelope) { e.Digest = hex.EncodeToString(make([]byte, 32)) }, nil, 401},
		"no signature":                          {nil, func(e *inbound.Envelope) { e.Signature = "" }, 401},
		"another secret":                        {nil, func(e *inbound.Envelope) { e.Signature = inbound.Sign("another secret of the same length 0123", *e) }, 401},
		"for what is no address":                {func(e *inbound.Envelope) { e.To = "not%20an%20address" }, nil, 422},
		"for a domain that only looks like one": {func(e *inbound.Envelope) { e.To = "x@ban%E2%84%AA.example" }, nil, 422},
	} {
		if code, out := r.handOn("help@mail.example.com", "ana@example.org", cameMail, try.change, try.after); code != try.code {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	// A sender that is no address is still said, as the server said it.
	code, odd := r.handOn("help@mail.example.com", "a..b@x.example", cameMail+"y", nil, nil)
	if _, _, got := r.do("GET", "/inbound/"+odd["id"].(string), r.admin, "", ""); code != 201 || got["from"] != "a..b@x.example" {
		t.Errorf("an odd sender: %d %v", code, got)
	}
	r.do("DELETE", "/inbound/"+odd["id"].(string), r.admin, "", "")
	// A bounce comes from nobody, and is taken.
	if code, out := r.handOn("help@mail.example.com", "", cameMail+"x", nil, nil); code != 201 {
		t.Errorf("from nobody: %d %v", code, out)
	}
	// The emails kept have only so much room: the rig's holds 4000 bytes.
	if code, out := r.handOn("help@mail.example.com", "", strings.Repeat("x", 3900), nil, nil); code != 503 || out["name"] != "inbound_full" {
		t.Errorf("past the room: %d %v", code, out)
	}
	if _, _, list := r.do("GET", "/inbound", r.admin, "", ""); len(list["data"].([]any)) != 1 {
		t.Errorf("%v", list)
	}
}
