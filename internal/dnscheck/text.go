package dnscheck

import (
	"context"
	"strings"
	"sync"
)

// The most of an answer that is kept to be shown.
const (
	longest = 300
	most    = 8
)

// shown cuts what DNS answered to what a page can hold.
func shown(texts []string) []string {
	if len(texts) > most {
		texts = texts[:most]
	}
	out := make([]string, 0, len(texts))
	for _, t := range texts {
		t = strings.ToValidUTF8(t, "?")
		if len(t) > longest {
			t = strings.ToValidUTF8(t[:longest], "") + "..."
		}
		out = append(out, t)
	}
	return out
}

// tag answers the value of a tag in a record written as "a=1; b=2", ""
// for none: the form DKIM and DMARC records share.
func tag(record, name string) string {
	for _, part := range strings.Split(record, ";") {
		key, value, found := strings.Cut(part, "=")
		if found && strings.EqualFold(strings.TrimSpace(key), name) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// only keeps the records that begin with a version, as "v=spf1" does: a
// name can hold other text records beside the one looked for.
func only(texts []string, version string) []string {
	var out []string
	for _, t := range texts {
		lead := strings.ToLower(strings.TrimSpace(t))
		if lead == version || strings.HasPrefix(lead, version+" ") || strings.HasPrefix(lead, version+";") {
			out = append(out, t)
		}
	}
	return out
}

// pick answers the records that pass a test, if any does, and all of
// them if none: what is shown is then what stands in the way.
func pick(texts []string, good func(string) bool) ([]string, bool) {
	var passed []string
	for _, t := range texts {
		if good(t) {
			passed = append(passed, t)
		}
	}
	if len(passed) > 0 {
		return passed, true
	}
	return texts, false
}

// includes says whether an SPF record names a host in an include: with
// or without a qualifier before it or a dot after it, in any case.
func includes(record, host string) bool {
	for _, term := range strings.Fields(strings.ToLower(record)) {
		term = strings.TrimLeft(term, "+-~?")
		if name, found := strings.CutPrefix(term, "include:"); found && strings.TrimSuffix(name, ".") == strings.ToLower(host) {
			return true
		}
	}
	return false
}

// policies is what a DMARC record can tell receivers to do.
var policies = map[string]bool{"none": true, "quarantine": true, "reject": true}

// parentsAsked is the most parents of a domain asked for a DMARC record:
// the shortest ones, where an organisation's own record is.
const parentsAsked = 4

// dmarcAt asks one name for its DMARC record. More than one is an error
// to a receiver, which then applies none: Wrong.
func (c *Checker) dmarcAt(ctx context.Context, name string) (state string, texts []string) {
	texts, err := c.Resolver.LookupTXT(ctx, rooted("_dmarc."+name))
	texts = only(texts, "v=dmarc1")
	return stateOf(err, len(texts) > 0, len(texts) == 1), texts
}

// dmarc looks for a domain's DMARC record. Receivers read the domain's
// own. Failing that they read its organisational domain's, where the
// "sp" tag speaks for subdomains if it is set. Which parent that is
// cannot be known without a list of public suffixes, so the shortest
// few parents are asked, at once, and the answer is read as receivers
// that walk the tree read it: a record marked "psd=n" is the
// organisation's; one marked "psd=y" is a registry's, above any
// organisation, and speaks for nobody below; otherwise the shortest
// name with a record is taken. That parent is named as one that may
// apply, not as one that does.
//
// A parent whose record cannot be read is passed over, as a receiver
// passes it over. A parent that could not be asked leaves the answer
// Unknown, unless a record was found without it.
func (c *Checker) dmarc(ctx context.Context, domain string) Result {
	res := Result{State: Missing}
	res.Use, res.Name, res.Type = UseDMARC, "_dmarc."+domain, "TXT"
	labels := strings.Split(domain, ".")
	var parents []string // from the shortest
	for i := len(labels) - 2; i >= 1 && len(parents) < parentsAsked; i-- {
		parents = append(parents, strings.Join(labels[i:], "."))
	}
	states, texts := make([]string, len(parents)+1), make([][]string, len(parents)+1)
	var wg sync.WaitGroup
	for i, name := range append([]string{domain}, parents...) {
		wg.Go(func() { states[i], texts[i] = c.dmarcAt(ctx, name) })
	}
	wg.Wait()
	switch states[0] {
	case Unknown:
		res.State = Unknown
		return res
	case OK, Wrong:
		// Its own record, which no parent's stands in for.
		res.Found = shown(texts[0])
		if res.Policy = strings.ToLower(tag(texts[0][0], "p")); states[0] == Wrong || !policies[res.Policy] {
			res.Policy, res.State = "", Wrong
			return res
		}
		res.State = OK
		return res
	}
	chosen, unasked := -1, false
	for i := 1; i < len(states); i++ { // from the shortest parent
		if unasked = unasked || states[i] == Unknown; states[i] != OK {
			continue
		}
		policy := tag(texts[i][0], "sp")
		if policy == "" {
			policy = tag(texts[i][0], "p")
		}
		switch psd := strings.ToLower(tag(texts[i][0], "psd")); {
		case psd == "y", !policies[strings.ToLower(policy)]:
			continue
		case psd == "n":
			chosen = i // the organisation says so itself: the longest such wins
		case chosen < 0:
			chosen = i
		}
	}
	switch {
	case chosen < 0 && unasked:
		res.State = Unknown
	case chosen >= 0:
		policy := tag(texts[chosen][0], "sp")
		if policy == "" {
			policy = tag(texts[chosen][0], "p")
		}
		res.State, res.Found, res.Inherited, res.Policy = OK, shown(texts[chosen]), parents[chosen-1], strings.ToLower(policy)
	}
	return res
}
