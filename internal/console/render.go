package console

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"html/template"
	"io/fs"
	"net/http"
	"time"

	"github.com/mettjs/cairnpost/internal/domains"
	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/webhook"
)

//go:embed templates/*.html
var templateFiles embed.FS

//go:embed assets
var assetFiles embed.FS

// pages is each page's template, parsed with the layout it sits in.
type pages map[string]*template.Template

// helpers are what a template may call. A time is said in the zone the
// page hands over with it.
var helpers = template.FuncMap{
	// when is a time as a person reads it, in a time zone, which it
	// names.
	"when": func(zone *time.Location, t time.Time) string { return when(t.In(zone)) },
	"plus": func(a, b int) int { return a + b },
	// day is the date alone, where the hour is in the way; stamp the
	// time as a machine reads it.
	"day":   func(zone *time.Location, t time.Time) string { return t.In(zone).Format("2 Jan 2006") },
	"stamp": func(t time.Time) string { return t.UTC().Format(time.RFC3339) },
	// An email's state, as a person reads it.
	"label":    func(state string) string { return wordsOf(state).Label },
	"meaning":  func(state string) string { return wordsOf(state).Meaning },
	"tone":     func(state string) string { return wordsOf(state).Tone },
	"happened": happened,
	// An email that went to several: what became of it at each, and
	// what a kind of recipient is called.
	"outcomes": outcomes,
	"kind":     func(kind string) string { return kindWords[kind] },
	"minus":    func(a, b int) int { return a - b },
	"size":     size,
	// A reason an address is suppressed for, as a label and in a sentence.
	"why":     why,
	"because": because,
	// A webhook: what a try met, and a delivery's state.
	"met":           met,
	"deliveryLabel": func(state string) string { label, _ := deliveryWords(state); return label },
	"deliveryTone":  func(state string) string { _, tone := deliveryWords(state); return tone },
	"typeOf":        webhook.TypeOf,
	"endpointState": webhook.StateOf,
	"endpointLabel": func(e store.Endpoint) string { return endpointStates[webhook.StateOf(e)][0] },
	"endpointTone":  func(e store.Endpoint) string { return endpointStates[webhook.StateOf(e)][1] },
	// Whether a sending domain is a name that can be looked up.
	"isHost":  func(name string) bool { _, ok := domains.Host(name); return ok },
	"stateOf": func(kind string) string { return store.StateOf[kind] },
}

// when is a time as a person reads it, in the time zone it is in, which
// it names.
func when(t time.Time) string { return t.Format("2 Jan 2006, 15:04 ") + zoneName(t) }

// zoneName is what a time's zone is called: its own short name ("CET"),
// or how far it is from UTC ("UTC+05:30") where it has none.
func zoneName(t time.Time) string {
	if name := t.Format("MST"); name[0] != '+' && name[0] != '-' {
		return name
	}
	return "UTC" + t.Format("-07:00")
}

// parse reads every page at start: a template that does not parse stops
// the server, not a request.
func parse() pages {
	out := pages{}
	names, err := fs.Glob(templateFiles, "templates/*.html")
	if err != nil {
		panic(err)
	}
	for _, name := range names {
		if name == "templates/layout.html" || name == "templates/hookform.html" || name == "templates/templateform.html" || name == "templates/parts.html" {
			continue // parts of pages, not pages
		}
		page := name[len("templates/") : len(name)-len(".html")]
		out[page] = template.Must(template.New("layout.html").Funcs(helpers).ParseFS(templateFiles, "templates/layout.html", "templates/hookform.html", "templates/templateform.html", "templates/parts.html", name))
	}
	return out
}

// view is what every page is given.
type view struct {
	// Page names the page, for the navigation to mark.
	Page string
	// Who is the key signed in as; nil on the sign-in page.
	Who *store.Key
	// Notice is a sentence about what the last request did, "" for none.
	Notice string
	// Root is where the console is mounted, for links.
	Root    string
	Version string
	// Assets is the word in the stylesheet's and the script's URLs.
	Assets string
	// Zone is the time zone the page's times are said in.
	Zone *time.Location
	// Pulse is how the server stands, said on every page.
	Pulse pulse
	Data  any
}

// render writes a page. It is rendered whole before any of it is sent, so
// a page that fails is an error page and not half a page.
func (c *console) render(w http.ResponseWriter, r *http.Request, code int, page string, data any) {
	v := view{Page: page, Root: Root, Version: c.Version, Assets: c.assets, Pulse: pulseOf(c.parts()), Zone: c.zoneOf(r), Data: data}
	// What the request before asked this page to say is said by a page
	// that was asked for, not by the answer to a form.
	if r.Method == http.MethodGet && code < 400 {
		v.Notice = c.heard(w, r)
	}
	if key, signedIn := r.Context().Value(whoKey{}).(store.Key); signedIn {
		v.Who = &key
	}
	var out bytes.Buffer
	if err := c.pages[page].Execute(&out, v); err != nil {
		c.Log.Error("console: a page could not be rendered", "page", page, "error", err)
		http.Error(w, "The page could not be shown.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_, _ = out.WriteTo(w)
}

// fail answers with a page that says what went wrong and what to do.
func (c *console) fail(w http.ResponseWriter, r *http.Request, code int, message string) {
	c.render(w, r, code, "error", message)
}

// assetsVersion is a word that changes when any asset does.
func assetsVersion() string {
	sum := sha256.New()
	names, err := fs.Glob(assetFiles, "assets/*")
	if err != nil {
		panic(err)
	}
	for _, name := range names {
		raw, err := assetFiles.ReadFile(name)
		if err != nil {
			panic(err)
		}
		sum.Write(raw)
	}
	return hex.EncodeToString(sum.Sum(nil))[:12]
}

// assetServer serves the console's own stylesheet and script. Asked for
// under the word that names their content, they may be kept for good:
// when they change, so does the word.
func (c *console) assetServer() http.Handler {
	files, err := fs.Sub(assetFiles, "assets")
	if err != nil {
		panic(err)
	}
	server := http.FileServerFS(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + r.PathValue("file")
		w.Header().Set("Cache-Control", "no-cache")
		if r.URL.Query().Get("v") == c.assets {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		server.ServeHTTP(w, r2)
	})
}
