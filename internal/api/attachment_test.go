package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/api"
	"github.com/mettjs/cairnpost/internal/mail"
	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/store/memstore"
)

// filesRig keeps what emails say for a day, with or without their files.
func filesRig(t *testing.T, files bool, now *time.Time) *rig {
	s := memstore.New()
	s.Now = func() time.Time { return *now }
	return filesRigOn(t, s, files)
}

// filesRigOn is a server over a store another may have written to.
func filesRigOn(t *testing.T, s *memstore.Store, files bool) *rig {
	return newRigBuilt(t, s, func(d *api.Deps) {
		d.Mail = mail.New(s, &fake{}, mail.Options{SendTimeout: time.Second, IdempotencyTTL: time.Hour, InDoubtWindow: time.Minute,
			KeepContent: 24 * time.Hour, KeepFiles: files, Now: s.Now}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	})
}

func (r *rig) file(path, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	return rec
}

// Where what emails say is kept with their files, an admin key reads
// what a file held, as a file to save, for as long as the words are
// kept, and nobody else does.
func TestAKeptFileIsReadByAnAdminWhileItIsKept(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	r := filesRig(t, true, &now)
	two := strings.Replace(worded, `}]}`, `},{"filename":"second.txt","content":"dHdv"}]}`, 1)
	_, _, sent := r.do("POST", "/emails", r.send, "", two)
	path := "/emails/" + sent["id"].(string)
	_, _, got := r.do("GET", path, r.admin, "", "")
	files, _ := got["content"].(map[string]any)["attachments"].([]any)
	if file, _ := files[0].(map[string]any); len(files) != 2 || file["kept"] != true || file["content"] != nil {
		t.Fatalf("the content says %v", files)
	}
	rec := r.file(path+"/attachments/1", r.admin)
	if rec.Code != 200 || rec.Body.String() != "%PDF-1" || rec.Header().Get("Content-Type") != "application/octet-stream" ||
		!strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment; filename=receipt.pdf") ||
		rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("the file: %d %v %q", rec.Code, rec.Header(), rec.Body)
	}
	bound, _, err := r.keys.Create(context.Background(), "other", store.ScopeAdmin, "other.example")
	if err != nil {
		t.Fatal(err)
	}
	for name, try := range map[string][2]string{"another domain's admin": {path + "/attachments/1", bound},
		"no such place": {path + "/attachments/3", r.admin}, "place zero": {path + "/attachments/0", r.admin},
		"no number": {path + "/attachments/first", r.admin}, "a signed place": {path + "/attachments/+1", r.admin},
		"a place past any": {path + "/attachments/99999999999", r.admin}, "no such email": {"/emails/nope/attachments/1", r.admin}} {
		if rec := r.file(try[0], try[1]); rec.Code != 404 || strings.Contains(rec.Body.String(), "%PDF") {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := r.file(path+"/attachments/1", r.send); rec.Code != 403 {
		t.Errorf("a send key: %d", rec.Code)
	}
	// Each file by its own place, and to the admin key of its domain.
	own, _, err := r.keys.Create(context.Background(), "own", store.ScopeAdmin, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if rec := r.file(path+"/attachments/2", own); rec.Code != 200 || rec.Body.String() != "two" ||
		!strings.Contains(rec.Header().Get("Content-Disposition"), "second.txt") {
		t.Errorf("the second file: %d %q", rec.Code, rec.Body)
	}
	// Past the words' time it is gone with them.
	now = now.Add(24 * time.Hour)
	if rec := r.file(path+"/attachments/1", r.admin); rec.Code != 404 {
		t.Errorf("past its time: %d", rec.Code)
	}
}

// With files not kept, the words are and the files are not; and a file
// of an email that waits is read until it is sent, whatever is kept.
func TestFilesAreKeptOnlyWhereAsked(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	r := filesRig(t, false, &now)
	_, _, sent := r.do("POST", "/emails", r.send, "", worded)
	path := "/emails/" + sent["id"].(string)
	_, _, got := r.do("GET", path, r.admin, "", "")
	files, _ := got["content"].(map[string]any)["attachments"].([]any)
	if file, _ := files[0].(map[string]any); file["filename"] != "receipt.pdf" || file["kept"] != nil {
		t.Fatalf("the content says %v", files)
	}
	if rec := r.file(path+"/attachments/1", r.admin); rec.Code != 404 {
		t.Fatalf("a file not kept: %d", rec.Code)
	}
	later := strings.Replace(worded, `"subject"`, `"scheduled_at":"2030-01-01T09:00:00Z","subject"`, 1)
	_, _, waits := r.do("POST", "/emails", r.send, "", later)
	if rec := r.file("/emails/"+waits["id"].(string)+"/attachments/1", r.admin); rec.Code != 200 || rec.Body.String() != "%PDF-1" {
		t.Fatalf("the file of an email that waits: %d %q", rec.Code, rec.Body)
	}
	if rec := r.file("/emails/"+waits["id"].(string)+"/attachments/1", r.send); rec.Code != 403 {
		t.Fatalf("read by a send key: %d", rec.Code)
	}
}

// A server set to keep files no longer hides at once the files it kept:
// they are not read, and are not said to be kept.
func TestFilesKeptBeforeAreHiddenOnceFilesAreNotKept(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	s := memstore.New()
	s.Now = func() time.Time { return now }
	before := filesRigOn(t, s, true)
	_, _, sent := before.do("POST", "/emails", before.send, "", worded)
	path := "/emails/" + sent["id"].(string)
	if rec := before.file(path+"/attachments/1", before.admin); rec.Code != 200 {
		t.Fatalf("kept: %d", rec.Code)
	}
	after := filesRigOn(t, s, false)
	if rec := after.file(path+"/attachments/1", after.admin); rec.Code != 404 {
		t.Errorf("with files no longer kept: %d", rec.Code)
	}
	_, _, got := after.do("GET", path, after.admin, "", "")
	files, _ := got["content"].(map[string]any)["attachments"].([]any)
	if file, _ := files[0].(map[string]any); file["filename"] != "receipt.pdf" || file["kept"] != nil {
		t.Errorf("the content says %v", files)
	}
}
