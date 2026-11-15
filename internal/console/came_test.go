package console_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/inbound"
	"github.com/mettjs/cairnpost/internal/store"
)

// handedOn keeps an email as the mail provider's side would hand it on.
func (r *rig) handedOn(to, raw string) string {
	r.t.Helper()
	sum := sha256.Sum256([]byte(raw))
	e := inbound.Envelope{Timestamp: strconv.FormatInt(r.now.Unix(), 10), To: to, From: "ana@example.org", Digest: hex.EncodeToString(sum[:])}
	e.Signature = inbound.Sign(cameSecret, e)
	id, _, err := inbound.New(r.s, inbound.Options{Secret: cameSecret, Now: func() time.Time { return r.now }}).Receive(context.Background(), e, []byte(raw))
	if err != nil {
		r.t.Fatal(err)
	}
	return id
}

const cameRaw = "From: Ana <ana@example.org>\r\nTo: help@a.example\r\nSubject: A question <b>\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n" +
	"--b\r\nContent-Type: text/html\r\n\r\n<script>alert(1)</script>\r\n--b\r\nContent-Type: image/svg+xml; name=x.svg\r\n\r\n<svg onload=alert(1)>\r\n--b--\r\n"

func TestAnEmailThatCameIsListedShownAndDeleted(t *testing.T) {
	r := newRig(t, nil)
	b := r.signedIn(r.admin)
	if page := b.do("GET", "/emails?tab=received", nil).Body.String(); !strings.Contains(page, "No email that came is kept.") || !strings.Contains(page, "7 days") {
		t.Fatalf("the empty list: %s", page)
	}
	id := r.handedOn("help@a.example", cameRaw)
	list := b.do("GET", "/emails?tab=received", nil).Body.String()
	for _, want := range []string{`/console/inbound/` + id + `">help@a.example</a>`, "Ana &lt;ana@example.org&gt;", "A question &lt;b&gt;", "1 file", `Received <span class="n">1</span>`} {
		if !strings.Contains(list, want) {
			t.Errorf("the list lacks %q", want)
		}
	}
	// Shown as written: what a stranger sent is never markup here.
	page := b.do("GET", "/inbound/"+id, nil).Body.String()
	for _, want := range []string{"Email for help@a.example", "&lt;script&gt;alert(1)&lt;/script&gt;", `/console/inbound/` + id + `/files/1" download>x.svg</a>`,
		`/console/inbound/` + id + `/raw" download`, "Subject: A question &lt;b&gt;"} {
		if !strings.Contains(page, want) {
			t.Errorf("the email's page lacks %q", want)
		}
	}
	if strings.Contains(page, "<script>alert") || strings.Contains(page, "<svg onload") {
		t.Error("what a stranger sent is in the page as markup")
	}
	// A file, and the email as it came, are only ever saved.
	for path, holds := range map[string]string{"/inbound/" + id + "/files/1": "<svg onload=alert(1)>", "/inbound/" + id + "/raw": cameRaw} {
		rec := b.do("GET", path, nil)
		if h := rec.Header(); rec.Code != 200 || rec.Body.String() != holds || h.Get("Content-Type") != "application/octet-stream" ||
			!strings.HasPrefix(h.Get("Content-Disposition"), "attachment") || !strings.HasPrefix(h.Get("Content-Security-Policy"), "sandbox") {
			t.Errorf("%s: %d %v", path, rec.Code, h)
		}
	}
	if rec := b.do("GET", "/inbound/"+id+"/files/2", nil); rec.Code != 404 {
		t.Errorf("a file that is not there: %d", rec.Code)
	}
	// Another domain's admin is not told it is there; its own is.
	other, _, _ := r.ks.Create(context.Background(), "o", store.ScopeAdmin, "b.example")
	own, _, _ := r.ks.Create(context.Background(), "a", store.ScopeAdmin, "a.example")
	ob := r.signedIn(other)
	for _, path := range []string{"/inbound/" + id, "/inbound/" + id + "/raw", "/inbound/" + id + "/files/1", "/inbound/" + id + "/delete"} {
		if rec := ob.do("GET", path, nil); rec.Code != 404 {
			t.Errorf("%s by another domain's admin: %d", path, rec.Code)
		}
	}
	if rec := ob.do("POST", "/inbound/"+id+"/delete", url.Values{}); rec.Code != 404 {
		t.Errorf("deleted by another domain's admin: %d", rec.Code)
	}
	if page := ob.do("GET", "/emails?tab=received", nil).Body.String(); strings.Contains(page, id) {
		t.Error("listed for another domain's admin")
	}
	if rec := r.signedIn(own).do("GET", "/inbound/"+id, nil); rec.Code != 200 {
		t.Errorf("its own domain's admin: %d", rec.Code)
	}
	// Its time up, it is not there.
	r.now = r.now.Add(8 * 24 * time.Hour)
	if rec := r.signedIn(r.admin).do("GET", "/inbound/"+id, nil); rec.Code != 404 {
		t.Errorf("past its time: %d", rec.Code)
	}
	r.now = r.now.Add(-8 * 24 * time.Hour)
	b = r.signedIn(r.admin)
	// Deleted after asking.
	if page := b.do("GET", "/inbound/"+id+"/delete", nil).Body.String(); !strings.Contains(page, "Delete the email for help@a.example?") {
		t.Errorf("asked first: %s", page)
	}
	if page := b.follow("POST", "/inbound/"+id+"/delete", url.Values{}).Body.String(); !strings.Contains(page, "The email was deleted.") ||
		!strings.Contains(page, "No email that came is kept.") {
		t.Errorf("deleted: %s", page)
	}
	for _, mark := range []string{"A question", "alert(1)", "ana@example.org", "help@a.example"} {
		if strings.Contains(r.logged.String(), mark) {
			t.Errorf("the log holds %q", mark)
		}
	}
}
