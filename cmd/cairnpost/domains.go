package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/mettjs/cairnpost/internal/config"
	"github.com/mettjs/cairnpost/internal/dnscheck"
	"github.com/mettjs/cairnpost/internal/domains"
	"github.com/mettjs/cairnpost/internal/driver"
	"github.com/mettjs/cairnpost/internal/driver/cloudflare"
)

// errNotReady is a domain whose DNS is not ready: the command says what
// is wanting and ends as a failure, so a script can tell.
var errNotReady = errors.New("the domain's DNS is not ready to be sent from")

// recordsFor answers how a mail provider names the DNS records it asks
// of a sending domain, by the provider's name; nil for one that asks
// none. It is the one place that says which provider asks what, for the
// server and the command alike.
func recordsFor(provider string) func(string) []driver.Record {
	if provider == cloudflare.Name {
		return cloudflare.Records
	}
	return nil
}

// domainsCommand checks a domain's DNS. It reads public DNS and needs
// neither the database nor the mail provider, so it can be run before
// anything else is set up.
func domainsCommand(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "check" {
		return errors.New("say which: domains check DOMAIN")
	}
	fs := flag.NewFlagSet("domains check", flag.ContinueOnError)
	asked := fs.String("provider", os.Getenv("CAIRNPOST_DRIVER"),
		"the mail provider whose records to look for: "+strings.Join(config.Drivers, " or ")+" (default: CAIRNPOST_DRIVER)")
	if err := parse(fs, args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("name one domain: domains check DOMAIN")
	}
	// A provider that is none, or mistyped, is not one that asks nothing:
	// the check would then pass a domain it never looked at.
	provider := strings.ToLower(strings.TrimSpace(*asked))
	if !slices.Contains(config.Drivers, provider) {
		return fmt.Errorf("name the mail provider whose records to look for, with -provider or CAIRNPOST_DRIVER: %s (got %q)",
			strings.Join(config.Drivers, " or "), *asked)
	}
	timeout, err := config.DNSTimeout(os.Getenv)
	if err != nil {
		return err
	}
	server, err := config.DNSServer(os.Getenv)
	if err != nil {
		return err
	}
	report, err := domains.New(nil, dnscheck.New(timeout, server), provider, recordsFor(provider)).Check(ctx, "", fs.Arg(0))
	if errors.Is(err, domains.ErrNotAHost) {
		return fmt.Errorf("%q is not a domain name, such as mail.example.com", fs.Arg(0))
	}
	if err != nil {
		return err
	}
	if err := printReport(os.Stdout, report); err != nil {
		return err
	}
	if !report.Ready() {
		return errNotReady
	}
	return nil
}

// printReport writes a domain's records, one a line at the name each was
// read at, then what to know about any that is not simply in place: one
// that is wanting, and a policy that is a parent domain's.
func printReport(out io.Writer, report domains.Report) error {
	if !report.Asks {
		fmt.Fprintf(out, "The mail provider %q asks no DNS records of a domain, so only the DMARC policy is looked at.\n\n", report.Provider)
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	rows := report.Rows()
	for _, row := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s %s\n", row.Words.Title, strings.ToLower(row.Words.Label), row.Type, row.At())
	}
	if err := w.Flush(); err != nil {
		return err
	}
	for _, row := range rows {
		if row.Words.Tone != "ok" || row.Inherited != "" {
			fmt.Fprintf(out, "\n%s: %s\n", row.Words.Title, row.Words.Says)
		}
	}
	return nil
}
