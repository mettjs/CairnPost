package webhook

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"syscall"
	"time"
)

// Why a try failed. A closed set: an endpoint's own words, and the HTTP
// client's error, which names the URL, are never kept or logged.
const (
	FailTimeout = "timeout" // no answer in time
	FailRefused = "refused" // nothing is listening there
	FailDNS     = "dns"     // the name leads nowhere
	FailTLS     = "tls"     // its certificate does not check
	FailBlocked = "blocked" // its address is not a public one
	FailStatus  = "status"  // it answered, and not with 2xx
	FailOther   = "other"
)

// Failures is every reason a try can fail for.
var Failures = []string{FailTimeout, FailRefused, FailDNS, FailTLS, FailBlocked, FailStatus, FailOther}

// Result is what a try met: the endpoint's status, 0 for none, and why it
// failed, "" when it did not.
type Result struct {
	Status  int
	Failure string
}

// Delivered says the endpoint took the webhook.
func (r Result) Delivered() bool { return r.Failure == "" }

// Poster sends signed webhooks.
type Poster struct {
	client  *http.Client
	agent   string
	timeout time.Duration
	now     func() time.Time
}

// NewPoster returns a poster. timeout bounds one try. anywhere lets it
// reach addresses that are not public, for an endpoint on the same private
// network. perHost is how many webhooks go to one endpoint at once, so
// that many connections to it are kept for the next ones. version is said
// in the User-Agent.
func NewPoster(timeout time.Duration, anywhere bool, perHost int, version string, now func() time.Time) *Poster {
	transport := &http.Transport{
		// No proxy, whatever the environment says: the address check is on
		// the connection made, and must be made on the endpoint's own.
		Proxy:                 nil,
		DialContext:           dialer(anywhere, timeout).DialContext,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		MaxIdleConnsPerHost:   max(perHost, 1),
		IdleConnTimeout:       time.Minute,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	client := &http.Client{Transport: transport,
		// A redirect is not followed: the answer is the endpoint's own.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &Poster{client: client, agent: "CairnPost/" + version, timeout: timeout, now: now}
}

// Timeout is the most one try may take.
func (p *Poster) Timeout() time.Duration { return p.timeout }

// Post sends one webhook, signed now, and answers what it met. id names
// the webhook and is the same on every try; the timestamp is this try's.
func (p *Poster) Post(ctx context.Context, url string, key []byte, id string, body []byte) Result {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Result{Failure: FailOther}
	}
	at := p.now()
	signature, stamp := sign(key, id, at, body), strconv.FormatInt(at.Unix(), 10)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", p.agent)
	// The same three values under both names in use for this scheme.
	for _, prefix := range []string{"webhook-", "svix-"} {
		req.Header.Set(prefix+"id", id)
		req.Header.Set(prefix+"timestamp", stamp)
		req.Header.Set(prefix+"signature", signature)
	}
	res, err := p.client.Do(req)
	if err != nil {
		return Result{Failure: failureOf(err)}
	}
	defer res.Body.Close()
	// The answer's body is not wanted; a little is read so the connection
	// can be used again.
	_, _ = io.CopyN(io.Discard, res.Body, 64<<10)
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return Result{Status: res.StatusCode, Failure: FailStatus}
	}
	return Result{Status: res.StatusCode}
}

// failureOf names why a request got no answer.
func failureOf(err error) string {
	var (
		dns     *net.DNSError
		timeout net.Error
		cert    *tls.CertificateVerificationError
		unknown x509.UnknownAuthorityError
		host    x509.HostnameError
		record  tls.RecordHeaderError
	)
	switch {
	case errors.Is(err, errBlocked):
		return FailBlocked
	case errors.As(err, &dns):
		return FailDNS
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &timeout) && timeout.Timeout():
		return FailTimeout
	case errors.As(err, &cert), errors.As(err, &unknown), errors.As(err, &host), errors.As(err, &record):
		return FailTLS
	case errors.Is(err, syscall.ECONNREFUSED):
		return FailRefused
	}
	return FailOther
}
