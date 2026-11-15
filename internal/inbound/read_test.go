package inbound

import (
	"strings"
	"testing"
)

func crlf(s string) []byte { return []byte(strings.ReplaceAll(s, "\n", "\r\n")) }

var mixed = crlf(`From: =?UTF-8?Q?Ana_Mar=C3=ADa?= <ana@example.org>
To: Help <help@mail.example.com>, second@mail.example.com
Cc: bo@example.org
Reply-To: replies@example.org
Subject: =?ISO-8859-1?Q?Caf=E9_question?=
Message-ID: <abc@example.org>
Date: Tue, 01 Jan 2030 10:00:00 +0100
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary="outer"

--outer
Content-Type: multipart/alternative; boundary="inner"

--inner
Content-Type: text/plain; charset=iso-8859-1
Content-Transfer-Encoding: quoted-printable

Caf=E9 au lait =93now=94
--inner
Content-Type: text/html; charset=utf-8
Content-Transfer-Encoding: base64

PHA+Q2Fmw6k8L3A+
--inner--
--outer
Content-Type: image/png
Content-ID: <logo>
Content-Transfer-Encoding: base64

iVBORw0K
--outer
Content-Type: application/pdf; name="=?UTF-8?Q?r=C3=A9sum=C3=A9.pdf?="
Content-Disposition: attachment; filename="../../etc/r.pdf"
Content-Transfer-Encoding: base64

JVBERi0x
--outer
Content-Type: text/plain

A second text is a file.
--outer--
`)

func TestAMessageIsReadWithItsBodiesAndFiles(t *testing.T) {
	got := Parse(mixed)
	if got.Partial || got.Subject != "Café question" || got.From != "Ana María <ana@example.org>" || got.ReplyTo != "replies@example.org" ||
		got.MessageID != "<abc@example.org>" || got.Date.Hour() != 9 || len(got.To) != 2 || got.To[0] != "help@mail.example.com" ||
		len(got.Cc) != 1 {
		t.Fatalf("%+v", got)
	}
	if got.Text != "Café au lait “now”" || got.HTML != "<p>Café</p>" {
		t.Fatalf("%q %q", got.Text, got.HTML)
	}
	want := []Part{{"file-1", "image/png", "logo", 6}, {"r.pdf", "application/pdf", "", 6}, {"file-3", "text/plain", "", 24}}
	if len(got.Attachments) != 3 {
		t.Fatalf("%+v", got.Attachments)
	}
	for i, w := range want {
		if got.Attachments[i] != w {
			t.Errorf("file %d: %+v", i+1, got.Attachments[i])
		}
		part, content, ok := File(mixed, i+1)
		if !ok || part != w || len(content) != w.Size {
			t.Errorf("file %d read back: %+v %q %v", i+1, part, content, ok)
		}
	}
	if _, content, _ := File(mixed, 2); string(content) != "%PDF-1" {
		t.Errorf("%q", content)
	}
	for _, place := range []int{0, -1, 4} {
		if _, _, ok := File(mixed, place); ok {
			t.Errorf("a file at place %d", place)
		}
	}
	var subject bool
	for _, h := range got.Headers {
		subject = subject || h.Name == "Subject" && h.Value == "Café question"
	}
	if !subject {
		t.Errorf("%+v", got.Headers)
	}
}

func TestWhatCannotBeReadIsSaidAndNeverFails(t *testing.T) {
	for name, raw := range map[string]string{
		"no message at all":   "\x00\x01 not mail",
		"empty":               "",
		"a charset not known": "Subject: s\nContent-Type: text/plain; charset=shift_jis\n\n\x83\x65\x83\x58\x83\x67",
		"base64 that is not":  "Content-Type: application/pdf\nContent-Transfer-Encoding: base64\n\n!!!not base64!!!",
		"a type that is none": "Content-Type: ;;;\n\nbody",
		"nested too deep":     strings.Repeat("Content-Type: multipart/mixed; boundary=b\n\n--b\n", 12) + "Content-Type: text/plain\n\ndeep\n",
	} {
		got := Parse(crlf(raw))
		if !got.Partial {
			t.Errorf("%s: read as whole: %+v", name, got)
		}
		if strings.ContainsRune(got.Text+got.Subject, 0) {
			t.Errorf("%s: %q", name, got.Text)
		}
	}
	// A message with no type is plain text; a text in UTF-8 that says
	// nothing of it reads as it is.
	if got := Parse(crlf("Subject: plain\n\nhé")); got.Partial || got.Text != "hé" || got.Subject != "plain" {
		t.Errorf("%+v", got)
	}
	// A message cut short, as mail sometimes is, reads as far as it goes.
	if got := Parse(crlf("Content-Type: multipart/mixed; boundary=b\n\n--b\nContent-Type: text/plain\n\nhello\n--b\nContent-Type: te")); got.Text != "hello" {
		t.Errorf("%+v", got)
	}
	// Many parts are read up to a point, and no further.
	many := "Content-Type: multipart/mixed; boundary=b\n\n" + strings.Repeat("--b\nContent-Type: application/x\n\nx\n", maxParts+5) + "--b--\n"
	if got := Parse(crlf(many)); !got.Partial || len(got.Attachments) >= maxParts {
		t.Errorf("%d files, partial %v", len(got.Attachments), got.Partial)
	}
}

// A stranger wrote it, and it is read on every look: what a reading
// holds is bounded, the same each time, and says when it is not all.
func TestAReadingIsBoundedAndTheSameEachTime(t *testing.T) {
	long := Parse(crlf("Subject: s\nContent-Type: text/plain; charset=windows-1252\n\n" + strings.Repeat("\x81", maxBody+100)))
	if !long.Partial || len(long.Text) > maxBody {
		t.Errorf("%d bytes of text, partial %v", len(long.Text), long.Partial)
	}
	var b strings.Builder
	for i := range 300 {
		b.WriteString("X-Stuffed-" + strings.Repeat("a", i%7) + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ": v\n")
	}
	raw := crlf(b.String() + "Authentication-Results: mx.example; spf=pass\nReceived-SPF: pass\nX-Long: " + strings.Repeat("v", maxValue+50) + "\n\nbody")
	first := Parse(raw)
	if !first.Partial || len(first.Headers) != maxHeaders || first.Headers[0].Name != "Authentication-Results" || first.Headers[1].Name != "Received-Spf" {
		t.Fatalf("%d headers, first %+v, partial %v", len(first.Headers), first.Headers[:2], first.Partial)
	}
	for range 5 {
		if again := Parse(raw); len(again.Headers) != len(first.Headers) || again.Headers[maxHeaders-1] != first.Headers[maxHeaders-1] {
			t.Fatal("the same message read twice shows other headers")
		}
	}
	if cutAt := Parse(crlf("X-Long: " + strings.Repeat("v", maxValue+50) + "\n\nbody")); !cutAt.Partial || len(cutAt.Headers[0].Value) != maxValue {
		t.Errorf("a long header: %d bytes, partial %v", len(cutAt.Headers[0].Value), cutAt.Partial)
	}
}

func TestAFilesNameIsWhatCanBeSeenAndSavedUnder(t *testing.T) {
	for given, want := range map[string]string{
		"report.pdf": "report.pdf", "../../etc/passwd": "passwd", `C:\Users\a\b.txt`: "b.txt", "a/..": "", "..": "", ".": "", "   ": "",
		"in\u202Evoice\u200B.pdf": "invoice.pdf", "a\u2028b\x85c\x00.txt": "abc.txt", `q"uote.txt`: "quote.txt",
		"=?UTF-8?Q?r=C3=A9sum=C3=A9.pdf?=": "résumé.pdf", strings.Repeat("é", 400) + ".pdf": strings.Repeat("é", 127),
	} {
		if got := named(given); got != want {
			t.Errorf("%q is named %q", given, got)
		}
	}
}
