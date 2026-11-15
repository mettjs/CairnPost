package console_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/mail"
)

// An email that waits to be sent is listed apart from those on record,
// shown with what it says, and cancelled only after being asked.
func TestAWaitingEmailIsShownAndCancelled(t *testing.T) {
	r := newRig(t, nil)
	b := r.signedIn(r.admin)
	if page := b.do("GET", "/emails?tab=scheduled", nil).Body.String(); !strings.Contains(page, "No email is waiting to be sent.") {
		t.Fatalf("with none waiting: %s", page)
	}
	got, err := r.mail.Send(context.Background(), r.sendKey, r.send, mail.Request{From: "App <app@example.com>",
		To: []string{"ana@example.org"}, Cc: []string{"cy@example.org"}, Subject: "subject-later", Text: "body-later",
		Attachments: []mail.Attachment{{Filename: "later.pdf", Content: []byte("%PDF")}}, ScheduledAt: r.now.Add(48 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	// Not among the emails on record; counted on its own part.
	if page := b.do("GET", "/emails", nil).Body.String(); strings.Contains(page, "ana@example.org") ||
		!strings.Contains(page, `Scheduled <span class="n">1</span>`) {
		t.Fatalf("the emails on record: %s", page)
	}
	list := b.do("GET", "/emails?tab=scheduled", nil).Body.String()
	for _, want := range []string{`href="/console/emails/` + got.ID + `"`, "ana@example.org", "and 1 more", "app@example.com", "Goes out", "3 Jan 2030, 00:00"} {
		if !strings.Contains(list, want) {
			t.Errorf("the list lacks %q", want)
		}
	}
	page := b.do("GET", "/emails/"+got.ID, nil).Body.String()
	for _, want := range []string{">Scheduled</span>", "It waits to be sent", "Goes out", "Who it goes to", "cy@example.org", "Copy",
		"/emails/" + got.ID + `/cancel">Cancel this email</a>`, "subject-later", "body-later", "later.pdf",
		"Kept until the email is sent.", "The files are held until the email is sent.", `/files/1" download>later.pdf</a>`} {
		if !strings.Contains(page, want) {
			t.Errorf("the email's page lacks %q", want)
		}
	}
	for _, not := range []string{"What happened to it", "Last heard of", "Mail provider"} {
		if strings.Contains(page, not) {
			t.Errorf("the page of an email not yet sent says %q", not)
		}
	}
	// Asked first, then cancelled: what it said goes with it.
	if ask := b.do("GET", "/emails/"+got.ID+"/cancel", nil).Body.String(); !strings.Contains(ask, "Cancel the email to ana@example.org and 1 more?") ||
		!strings.Contains(ask, ">Keep it</a>") {
		t.Fatalf("the question: %s", ask)
	}
	after := b.follow("POST", "/emails/"+got.ID+"/cancel", nil).Body.String()
	for _, want := range []string{"The email was cancelled.", ">Cancelled</span>", "Was to go out", "What this email said was deleted when it was cancelled."} {
		if !strings.Contains(after, want) {
			t.Errorf("once cancelled, the page lacks %q", want)
		}
	}
	if strings.Contains(after, "subject-later") || strings.Contains(after, "Cancel this email") {
		t.Error("once cancelled, the page still says what it said, or offers to cancel it")
	}
	// Cancelled twice, or asked about again, changes nothing and says so.
	if again := b.follow("POST", "/emails/"+got.ID+"/cancel", nil).Body.String(); !strings.Contains(again, "That email was no longer waiting") {
		t.Errorf("cancelled twice: %s", again)
	}
	if again := b.follow("GET", "/emails/"+got.ID+"/cancel", nil).Body.String(); !strings.Contains(again, "That email was no longer waiting") {
		t.Errorf("asked about again: %s", again)
	}
	if page := b.do("GET", "/emails?tab=scheduled", nil).Body.String(); !strings.Contains(page, "No email is waiting to be sent.") {
		t.Errorf("the list once it is cancelled: %s", page)
	}
	if code := b.do("POST", "/emails/nope/cancel", nil).Code; code != 404 {
		t.Errorf("cancelling an email there is not: %d", code)
	}
}
