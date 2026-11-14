// Package cloudflare delivers through Cloudflare Email Sending's REST API.
// It is the only package that names Cloudflare's host.
//
// What it relies on was measured against the live service, where the
// service and its documentation disagree: a send answers 200 with the
// message's id and the recipient as queued, and whether the email arrived
// is told later by an event, never by this call.
package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/mettjs/cairnpost/internal/driver"
)

// Name is the driver's name.
const Name = "cloudflare"

const host = "https://api.cloudflare.com/client/v4"

// Driver sends as one Cloudflare account.
type Driver struct {
	base  string // the account's API root
	token string
	http  *http.Client
	quota cache
}

// New returns a driver for an account, authenticated by an API token with
// the Email Sending permission. The client carries no timeout of its own:
// each call is bounded by its context.
func New(accountID, token string) *Driver {
	return &Driver{base: host + "/accounts/" + accountID, token: token, http: &http.Client{}}
}

// NewAt is New against another API root: a stand-in for Cloudflare in a
// test, or a proxy in front of it.
func NewAt(root, token string) *Driver {
	return &Driver{base: root, token: token, http: &http.Client{}}
}

var (
	_ driver.Driver  = (*Driver)(nil)
	_ driver.Limited = (*Driver)(nil)
)

// Name implements driver.Driver.
func (*Driver) Name() string { return Name }

type sender struct {
	Address string `json:"address"`
	Name    string `json:"name,omitempty"`
}

type sendBody struct {
	From    sender            `json:"from"`
	To      []string          `json:"to"`
	Cc      []string          `json:"cc,omitempty"`
	Bcc     []string          `json:"bcc,omitempty"`
	ReplyTo string            `json:"reply_to,omitempty"`
	Subject string            `json:"subject"`
	HTML    string            `json:"html,omitempty"`
	Text    string            `json:"text,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// sendResult names the recipient in exactly one of its lists.
type sendResult struct {
	MessageID  string   `json:"message_id"`
	Queued     []string `json:"queued"`
	Delivered  []string `json:"delivered"`
	Bounced    []string `json:"permanent_bounces"`
	Suppressed []string `json:"suppressed_recipients"`
}

// Send implements driver.Driver.
func (d *Driver) Send(ctx context.Context, m driver.Message) (string, error) {
	a, err := d.ask(ctx, http.MethodPost, "/email/sending/send", sendBody{
		From: sender{Address: m.From, Name: m.FromName}, To: m.To, Cc: m.Cc, Bcc: m.Bcc, ReplyTo: m.ReplyTo,
		Subject: m.Subject, HTML: m.HTML, Text: m.Text, Headers: m.Headers,
	})
	if err != nil {
		return "", err
	}
	err = a.refusal()
	var throttled *driver.Throttled
	switch {
	case errors.Is(err, errUnread):
		// Cloudflare said yes and its answer could not be read: the email
		// was taken, and there is no id to match its events with. Saying
		// otherwise would send it twice.
		return "", nil
	case err != nil && !errors.As(err, &throttled) && sentNothing(err):
		// What Cloudflare answers once the day's allowance is spent is not
		// known, so the allowance itself is asked: a spent one is a reason
		// to wait, whatever the refusal looked like.
		if wait, spent := d.spent(ctx); spent {
			return "", fmt.Errorf("cloudflare: the allowance is spent (%v): %w", err, &driver.Throttled{After: wait})
		}
		return "", err
	case err != nil:
		return "", err
	}
	var res sendResult
	if json.Unmarshal(a.result, &res) != nil {
		return "", nil // a yes, unreadable: as above
	}
	// A recipient Cloudflare lists as bounced or suppressed was still taken
	// by this call, and an event says what became of it: only an event is
	// believed about delivery.
	listed := slices.Concat(res.Queued, res.Delivered, res.Bounced, res.Suppressed)
	if len(listed) == 0 {
		// A yes that names nobody: there is no telling what was done.
		return "", errors.New("cloudflare: a success that names no recipient")
	}
	return res.MessageID, nil
}

// sentNothing reports whether an error is one of the two kinds after which
// nothing was sent.
func sentNothing(err error) bool {
	return errors.Is(err, driver.ErrRejected) || errors.Is(err, driver.ErrUnavailable)
}

// spent asks whether the allowance is used up, and for how long.
func (d *Driver) spent(ctx context.Context) (time.Duration, bool) {
	q, err := d.readQuota(ctx)
	if err != nil || !q.Over {
		return 0, false
	}
	return max(0, time.Until(q.ResetsAt)), true
}
