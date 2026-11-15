package webhook

import (
	"context"
	"errors"
	"fmt"
	neturl "net/url"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mettjs/cairnpost/internal/store"
)

// Invalid is a request that cannot be carried out as it is. Reason is a
// whole sentence saying what it must be, naming the part at fault as the
// API names it. Field is that part, and Must the sentence after its
// name ("must be ..."), where the sentence begins with it: for whoever
// names the part in other words.
type Invalid struct{ Field, Reason, Must string }

// must is an Invalid whose sentence is the field's name and what it must
// be.
func must(field, be string) *Invalid {
	return &Invalid{Field: field, Reason: `"` + field + `" ` + be, Must: be}
}

func (e *Invalid) Error() string { return "webhook: " + e.Reason }

// ErrDomain is a caller bound to one sending domain asking for an
// endpoint of another.
var ErrDomain = errors.New("webhook: the caller manages another sending domain's endpoints")

// Limits of an endpoint.
const (
	maxURL         = 2048
	MaxDescription = 200
	maxDomain      = 253
)

// Service manages the endpoints.
type Service struct {
	store  store.Webhooks
	poster *Poster
	// insecure lets an endpoint be plain http, for one on a private network.
	insecure bool
	// perDomain is how many endpoints may hear about one sending domain,
	// and how many about all.
	perDomain int
	// overlap is how long, after an endpoint's signing key is changed,
	// its webhooks are signed with the key before as well.
	overlap time.Duration
	now     func() time.Time
}

// NewService returns the service. insecure allows http endpoints as well
// as https; perDomain is how many endpoints one sending domain may have;
// overlap is how long a signing key that was changed goes on signing.
func NewService(s store.Webhooks, p *Poster, insecure bool, perDomain int, overlap time.Duration, now func() time.Time) *Service {
	return &Service{store: s, poster: p, insecure: insecure, perDomain: perDomain, overlap: overlap, now: now}
}

// Overlap is how long a signing key that was changed goes on signing
// beside the new one.
func (s *Service) Overlap() time.Duration { return s.overlap }

// States an endpoint can be in, as StateOf answers them.
const (
	// StateOn: it is owed what happens, and sent it.
	StateOn = "on"
	// StateOff: it is owed nothing new, and what it was owed waits.
	StateOff = "off"
	// StatePaused: it failed for too long. It is owed what happens, and
	// nothing is sent until it is resumed.
	StatePaused = "paused"
)

// States is every state an endpoint can be in.
var States = []string{StateOn, StateOff, StatePaused}

// StateOf is the one place that says how an endpoint stands. Off comes
// first: one that is off is owed nothing, paused or not.
func StateOf(e store.Endpoint) string {
	switch {
	case !e.Enabled:
		return StateOff
	case e.PausedAt != nil:
		return StatePaused
	}
	return StateOn
}

func plain(s string) bool { return utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl) }

// checkURL says whether an endpoint can be sent to. A space anywhere in
// it is refused: a URL is kept and posted to as it is written.
func (s *Service) checkURL(raw string) error {
	u, err := neturl.Parse(raw)
	if strings.ContainsFunc(raw, unicode.IsSpace) {
		return must("url", "must not have a space in it.")
	}
	schemes := `"https://"`
	if s.insecure {
		schemes = `"https://" or "http://"`
	}
	switch {
	case err != nil || len(raw) > maxURL || !plain(raw) || u.Host == "" || u.Hostname() == "",
		u.Scheme != "https" && !(s.insecure && u.Scheme == "http"):
		return must("url", fmt.Sprintf(`must be an address starting with %s, of at most %d bytes.`, schemes, maxURL))
	case u.User != nil:
		return must("url", `must not carry a user name or a password. A webhook is proven by its signature.`)
	case u.Fragment != "":
		return must("url", `must not have a fragment (the part after "#"): it is never sent.`)
	}
	return nil
}

// kindsOf turns the event types asked for into kinds, in the order kept.
func kindsOf(types []string) ([]string, error) {
	var kinds []string
	for _, t := range types {
		kind := kindOf(t)
		if kind == "" {
			return nil, &Invalid{Field: "events", Reason: fmt.Sprintf(`"events" must be a list of %s, or left out for all.`, strings.Join(Types(), ", "))}
		}
		if !slices.Contains(kinds, kind) {
			kinds = append(kinds, kind)
		}
	}
	return kinds, nil
}

func checkDescription(d string) error {
	if len(d) > MaxDescription || !plain(d) {
		return must("description", fmt.Sprintf(`must be one line of at most %d bytes.`, MaxDescription))
	}
	return nil
}

// New is an endpoint to make.
type New struct {
	URL         string
	Domain      string // the sending domain it hears about, "" for all
	Types       []string
	Description string
}

// Create makes an endpoint, with a signing key of its own. scope is the
// sending domain the caller is bound to, "" for none: a bound caller's
// endpoint hears about its own domain.
func (s *Service) Create(ctx context.Context, scope string, n New) (store.Endpoint, error) {
	domain := store.Fold(n.Domain)
	switch {
	case scope != "" && domain != "" && domain != scope:
		return store.Endpoint{}, ErrDomain
	case scope != "":
		domain = scope
	}
	if len(domain) > maxDomain || !plain(n.Domain) || strings.ContainsAny(domain, "@ /") {
		return store.Endpoint{}, &Invalid{Field: "domain", Reason: `"domain" must be a sending domain, such as "mail.example.com", or left out for all.`}
	}
	n.URL = strings.TrimSpace(n.URL)
	if err := s.checkURL(n.URL); err != nil {
		return store.Endpoint{}, err
	}
	kinds, err := kindsOf(n.Types)
	if err != nil {
		return store.Endpoint{}, err
	}
	description := strings.TrimSpace(n.Description)
	if err := checkDescription(description); err != nil {
		return store.Endpoint{}, err
	}
	e, err := s.store.CreateEndpoint(ctx, store.Endpoint{URL: n.URL, Domain: domain, Kinds: kinds,
		Description: description, Secret: newSecret()}, s.perDomain)
	if errors.Is(err, store.ErrLimit) {
		// Counted for the domain it hears about, so one domain's
		// endpoints never stand in another's way.
		of := "That sending domain has"
		if domain == "" {
			of = "Every sending domain together has"
		}
		many := "endpoints"
		if s.perDomain == 1 {
			many = "endpoint"
		}
		return store.Endpoint{}, &Invalid{Field: "endpoints", Reason: fmt.Sprintf("%s %d %s already, which is the most "+
			"(CAIRNPOST_WEBHOOK_MAX_ENDPOINTS). Remove one first.", of, s.perDomain, many)}
	}
	if err != nil {
		return store.Endpoint{}, fmt.Errorf("webhook: create an endpoint: %w", err)
	}
	return e, nil
}
