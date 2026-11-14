package console

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
)

// tabs are the parts of a page, as links: one is shown, the others are
// a link away. Each keeps what the page is narrowed to.
type tabs struct {
	// Label says what they are the parts of.
	Label string
	Parts []chip
}

// tab is one part of a page: the word in its address ("" for the
// first), what it is called, and how many things it lists, 0 for not said.
type tab struct {
	Key, Label string
	N          int
}

// tabsOf are a page's parts as links, with the one shown marked. q is
// what the page is narrowed to, kept by every link.
func tabsOf(label, page string, q url.Values, on string, parts ...tab) tabs {
	t := tabs{Label: label}
	for _, p := range parts {
		to := url.Values{}
		for k, v := range q {
			to[k] = v
		}
		if to.Del("tab"); p.Key != "" {
			to.Set("tab", p.Key)
		}
		c := chip{Label: p.Label, URL: at(page, to), On: p.Key == on}
		if p.N > 0 {
			c.N = strconv.Itoa(p.N)
		}
		t.Parts = append(t.Parts, c)
	}
	return t
}

// tabOf is the part of a page a request asks for: forced if a form of
// that part was refused, else the one its address names, if it is one.
func tabOf(r *http.Request, forced string, known ...string) string {
	if forced != "" {
		return forced
	}
	if asked := r.URL.Query().Get("tab"); slices.Contains(known, asked) {
		return asked
	}
	return ""
}
