package domains

import (
	"strings"

	"github.com/mettjs/cairnpost/internal/dnscheck"
	"github.com/mettjs/cairnpost/internal/driver"
)

// Words is one DNS record's result as a person reads it: on a page and
// on the command line alike.
type Words struct {
	// Title names the record; For says what it is for; Label and Tone
	// say how it stands; Says is what was found and what to do.
	Title, For, Label, Tone, Says string
}

// uses names each kind of record and what it is for.
var uses = map[string][2]string{
	driver.UseDKIM:    {"DKIM key", "Receivers check each email's signature against this key."},
	driver.UseSPF:     {"SPF", "Says which servers may send for the address bounces come back to."},
	driver.UseBounce:  {"Bounce address", "Where a receiving server sends a bounce, so that this server hears of it."},
	dnscheck.UseDMARC: {"DMARC policy", "Tells receivers what to do with mail that fails the checks above."},
}

// policies says what each DMARC policy tells receivers.
var policies = map[string]string{
	"none":       "Receivers are asked to do nothing special with mail that fails. Reports can still be asked for.",
	"quarantine": "Receivers are asked to treat mail that fails as suspicious, which usually means the spam folder.",
	"reject":     "Receivers are asked to refuse mail that fails.",
}

// WordsFor says how a record stands. Every kind of record in every state
// has words: a kind or a state not known here is named plainly and said
// to be unknown. Tone is "ok", "failing" or "off".
func WordsFor(r dnscheck.Result) Words {
	w := Words{Title: r.Use, Label: "Not known", Tone: "off",
		Says: "It could not be looked up just now, so nothing is known either way. Look again in a moment."}
	if use, known := uses[r.Use]; known {
		w.Title, w.For = use[0], use[1]
	}
	switch r.State {
	case dnscheck.OK:
		w.Label, w.Tone, w.Says = "In place", "ok", found(r)
	case dnscheck.Missing:
		w.Label, w.Tone, w.Says = "Missing", "failing", missing(r)
		if allowed(r) {
			w.Label, w.Tone = "None", "off"
		}
	case dnscheck.Wrong:
		w.Label, w.Tone, w.Says = "Not right", "failing", wrong(r)
	}
	return w
}

func found(r dnscheck.Result) string {
	switch r.Use {
	case driver.UseDKIM:
		return "A key is published there."
	case driver.UseSPF:
		if r.Include != "" {
			return "It names " + r.Include + ", the mail provider's servers."
		}
		return "An SPF record is published there."
	case driver.UseBounce:
		if r.Under != "" {
			return "Bounces go to the mail provider's servers."
		}
		return "Bounces have somewhere to go."
	case dnscheck.UseDMARC:
		says := policies[r.Policy]
		if r.Inherited != "" {
			return "This domain has no record of its own. The one of " + r.Inherited + " may speak for it. " + says
		}
		return says
	}
	return "It is published."
}

func missing(r dnscheck.Result) string {
	switch r.Use {
	case driver.UseDKIM:
		return "No record was found where the mail provider puts its key. Without it, receivers cannot check a signature, and mail is likely to be refused or marked as spam."
	case driver.UseSPF:
		return "No SPF record was found there. Add the one the mail provider gives for this domain."
	case driver.UseBounce:
		return "No MX record was found there, so a bounce has nowhere to go and this server never hears of it."
	case dnscheck.UseDMARC:
		return "Mail still goes: receivers decide for themselves. A record lets you say what happens to mail that pretends to be from this domain."
	}
	return "It was not found."
}

func wrong(r dnscheck.Result) string {
	switch r.Use {
	case driver.UseDKIM:
		return "A record is there, but it holds no key. A key that was withdrawn looks like this."
	case driver.UseSPF:
		if len(r.Found) > 1 {
			return "More than one SPF record is there. Receivers then apply none of them: keep one."
		}
		return "An SPF record is there, but it does not name " + r.Include + ", so the mail provider's servers are not allowed to send."
	case dnscheck.UseDMARC:
		if len(r.Found) > 1 {
			return "More than one DMARC record is there. Receivers then apply none of them: keep one."
		}
		return "A record is there, but it names no policy that can be read: it needs p=none, p=quarantine or p=reject."
	}
	if r.Use == driver.UseBounce {
		for _, host := range r.Found {
			if strings.Trim(host, ".") == "" {
				return "The record there says the name takes no mail, so a bounce has nowhere to go."
			}
		}
		return "The MX record there does not point at the mail provider's servers (" + r.Under + "), so a bounce goes somewhere else and this server never hears of it."
	}
	return "Something is there, and it is not what is needed."
}
