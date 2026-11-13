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
// whole sentence saying what it must be.
type Invalid struct{ Reason string }

func (e *Invalid) Error() string { return "webhook: " + e.Reason }

// ErrDomain is a caller bound to one sending domain asking for an
// endpoint of another.
var ErrDomain = errors.New("webhook: the caller manages another sending domain's endpoints")

// Limits of an endpoint and of a list.
const (
	maxURL         = 2048
	maxDescription = 200
	maxDomain      = 253
	DefaultLimit   = 50
	MaxLimit       = 500
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
	now       func() time.Time
}

// NewService returns the service. insecure allows http endpoints as well
// as https; perDomain is how many endpoints one sending domain may have.
func NewService(s store.Webhooks, p *Poster, insecure bool, perDomain int, now func() time.Time) *Service {
	return &Service{store: s, poster: p, insecure: insecure, perDomain: perDomain, now: now}
}

func plain(s string) bool { return utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl) }

// checkURL says whether an endpoint can be sent to.
func (s *Service) checkURL(raw string) error {
	u, err := neturl.Parse(raw)
	schemes := `"https://"`
	if s.insecure {
		schemes = `"https://" or "http://"`
	}
	switch {
	case err != nil || len(raw) > maxURL || !plain(raw) || u.Host == "" || u.Hostname() == "",
		u.Scheme != "https" && !(s.insecure && u.Scheme == "http"):
		return &Invalid{fmt.Sprintf(`"url" must be an address starting with %s, of at most %d bytes.`, schemes, maxURL)}
	case u.User != nil:
		return &Invalid{`"url" must not carry a user name or a password. A webhook is proven by its signature.`}
	case u.Fragment != "":
		return &Invalid{`"url" must not have a fragment (the part after "#"): it is never sent.`}
	}
	return nil
}

// kindsOf turns the event types asked for into kinds, in the order kept.
func kindsOf(types []string) ([]string, error) {
	var kinds []string
	for _, t := range types {
		kind := kindOf(t)
		if kind == "" {
			return nil, &Invalid{fmt.Sprintf(`"events" must be a list of %s, or left out for all.`, strings.Join(Types(), ", "))}
		}
		if !slices.Contains(kinds, kind) {
			kinds = append(kinds, kind)
		}
	}
	return kinds, nil
}

func checkDescription(d string) error {
	if len(d) > maxDescription || !plain(d) {
		return &Invalid{fmt.Sprintf(`"description" must be one line of at most %d bytes.`, maxDescription)}
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
		return store.Endpoint{}, &Invalid{`"domain" must be a sending domain, such as "mail.example.com", or left out for all.`}
	}
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
		return store.Endpoint{}, &Invalid{fmt.Sprintf("That sending domain has %d endpoints already, which is the most "+
			"(CAIRNPOST_WEBHOOK_MAX_ENDPOINTS). Remove one first.", s.perDomain)}
	}
	if err != nil {
		return store.Endpoint{}, fmt.Errorf("webhook: create an endpoint: %w", err)
	}
	return e, nil
}
