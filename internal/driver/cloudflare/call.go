package cloudflare

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/mettjs/cairnpost/internal/driver"
)

// answer is what Cloudflare said to one call.
type answer struct {
	status int
	// readable: the body was the JSON envelope every answer of the API's
	// own is wrapped in. An edge's or a gateway's page is not.
	readable bool
	success  bool
	code     int // Cloudflare's first error number, 0 for none
	result   json.RawMessage
	after    time.Duration // Retry-After, 0 for none
}

// ask makes one API call. Its error is a failure to reach Cloudflare,
// already one of the driver's kinds; what Cloudflare answered is the
// answer's to judge.
func (d *Driver) ask(ctx context.Context, method, path string, body any) (answer, error) {
	return d.askUpTo(ctx, 1<<20, method, path, body)
}

// askUpTo is ask for a call whose answer can be large: limit is the most
// of it that is read.
func (d *Driver) askUpTo(ctx context.Context, limit int64, method, path string, body any) (answer, error) {
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return answer{}, fmt.Errorf("cloudflare: encode: %w: %w", err, driver.ErrRejected)
		}
		payload = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, d.base+path, payload)
	if err != nil {
		return answer{}, fmt.Errorf("cloudflare: build the request: %w: %w", err, driver.ErrUnavailable)
	}
	req.Header.Set("Authorization", "Bearer "+d.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := d.http.Do(req)
	if err != nil {
		if neverLeft(err) {
			return answer{}, fmt.Errorf("cloudflare: could not connect: %w: %w", err, driver.ErrUnavailable)
		}
		// Sent and never answered, for all anyone knows.
		return answer{}, fmt.Errorf("cloudflare: no answer: %w", err)
	}
	defer res.Body.Close()
	a := answer{status: res.StatusCode}
	if n, err := strconv.Atoi(res.Header.Get("Retry-After")); err == nil && n > 0 {
		a.after = time.Duration(n) * time.Second
	}
	var envelope struct {
		Success *bool           `json:"success"`
		Result  json.RawMessage `json:"result"`
		Errors  []struct {
			Code int `json:"code"`
		} `json:"errors"`
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, limit))
	if err == nil && json.Unmarshal(raw, &envelope) == nil && envelope.Success != nil {
		a.readable, a.success, a.result = true, *envelope.Success, envelope.Result
		if len(envelope.Errors) > 0 {
			a.code = envelope.Errors[0].Code
		}
	}
	return a, nil
}

// neverLeft reports whether a transport error happened before any byte of
// the request could leave: no name, no connection, no trusted TLS session.
func neverLeft(err error) bool {
	var (
		dns  *net.DNSError
		op   *net.OpError
		cert *tls.CertificateVerificationError
	)
	return errors.As(err, &dns) || errors.As(err, &cert) || (errors.As(err, &op) && op.Op == "dial")
}

// errUnread is a success whose answer could not be read.
var errUnread = errors.New("cloudflare: a success whose answer could not be read")

// messageAtFault are the error numbers Cloudflare gives a 400 whose cause
// is the email itself, as measured: 10001 a malformed request, 10200 too
// big, 10201 no content length, 10202 an invalid email (a bad address, a
// forbidden header, a sender domain the account has not onboarded).
var messageAtFault = []int{10001, 10200, 10201, 10202}

// signInDown is the error number of a 503 that means Cloudflare's own
// sign-in is unreachable: the request was not carried out.
const signInDown = 10100

// refusal is what an answer means for the message: nil for a success, else
// one of the driver's three kinds. It says a status and Cloudflare's error
// number, never its text, which can echo an address.
//
//   - Refused for good: a 400 with one of messageAtFault, a 413, a 422.
//   - Nothing sent, try later: 429; any other 4xx (the token, the
//     entitlement, the account, the route: an operator's to fix, never a
//     reason to drop the message); a 503 from Cloudflare's sign-in; a 2xx
//     whose own body says no.
//   - In doubt: every other 5xx, which does not say whether the email was
//     queued.
func (a answer) refusal() error {
	said := fmt.Sprintf("cloudflare: status %d, error %d", a.status, a.code)
	switch {
	case a.status >= 200 && a.status <= 299 && !a.readable:
		return errUnread
	case a.status >= 200 && a.status <= 299 && a.success:
		return nil
	case a.status >= 200 && a.status <= 299:
		return fmt.Errorf("%s: a success that says it failed: %w", said, driver.ErrUnavailable)
	case a.status == http.StatusTooManyRequests:
		return fmt.Errorf("%s: %w", said, &driver.Throttled{After: a.after})
	case a.status == http.StatusBadRequest && slices.Contains(messageAtFault, a.code),
		a.status == http.StatusRequestEntityTooLarge, a.status == http.StatusUnprocessableEntity:
		return fmt.Errorf("%s: %w", said, driver.ErrRejected)
	case a.status >= 400 && a.status <= 499,
		a.status == http.StatusServiceUnavailable && a.code == signInDown:
		return fmt.Errorf("%s: %w", said, driver.ErrUnavailable)
	}
	return errors.New(said)
}
