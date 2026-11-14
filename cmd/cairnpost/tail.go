package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/mettjs/cairnpost/internal/address"
	"github.com/mettjs/cairnpost/internal/store"
)

// tailCommand shows what becomes of emails as it is recorded: the last
// few events, then each new one, until it is stopped. It reads the
// database, as keys does: it is run by whoever operates the server.
func tailCommand(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("tail", flag.ContinueOnError)
	domain := fs.String("domain", "", "only one sending domain's emails, e.g. mail.example.com")
	last := fs.Int("last", 10, "how many of the events already recorded to show first")
	every := fs.Duration("every", time.Second, "how often to look for new events")
	once := fs.Bool("once", false, "show the last events and stop")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *last < 0 || *last > 1000 || *every < 100*time.Millisecond {
		return errors.New("-last is 0 to 1000, and -every 100ms or longer")
	}
	of := ""
	if *domain != "" {
		host, ok := address.Host(*domain)
		if !ok {
			return fmt.Errorf("%q is not a domain, such as mail.example.com", *domain)
		}
		of = host
	}
	db, err := open(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	return tail(ctx, db, os.Stdout, of, *last, *every, *once)
}

// tailPage is how many new events one look reads.
const tailPage = 200

// tail writes the last events and then each new one. A look that fails
// is said and tried again: a database away for a moment does not end it.
func tail(ctx context.Context, events store.Events, w io.Writer, domain string, last int, every time.Duration, once bool) error {
	after := ""
	if last > 0 {
		shown, err := events.EventsAfter(ctx, "", domain, last)
		if err != nil {
			return err
		}
		after = told(w, shown, after)
	}
	if after == "" {
		// Nothing shown: follow from the newest event there is.
		newest, err := events.EventsAfter(ctx, "", domain, 1)
		if err != nil {
			return err
		}
		if len(newest) == 1 {
			after = newest[0].ID
		}
	}
	if once {
		return nil
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
		// With none recorded yet, the first that comes is the last one.
		var (
			page []store.Event
			err  error
		)
		if after == "" {
			page, err = events.EventsAfter(ctx, "", domain, tailPage)
		} else {
			page, err = events.EventsAfter(ctx, after, domain, tailPage)
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			fmt.Fprintln(os.Stderr, "cairnpost: the events could not be read; trying again")
			continue
		}
		after = told(w, page, after)
	}
}

// told writes events, one a line, and answers the last one's id, or the
// one before if there were none.
func told(w io.Writer, events []store.Event, after string) string {
	for _, e := range events {
		what := e.Kind
		if e.Kind == store.EventBounced && e.Permanent {
			what += " for good"
		}
		fmt.Fprintf(w, "%s  %-18s %-7s %s  from %s  email %s\n", e.At.UTC().Format("2006-01-02 15:04:05Z"), what, e.Code, e.Recipient, e.Sender, e.MessageID)
		after = e.ID
	}
	return after
}
