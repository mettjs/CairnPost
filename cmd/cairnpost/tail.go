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
	return tail(ctx, db, os.Stdout, os.Stderr, of, *last, *every, *once)
}

// How tail reads: tailPage events a read; tailBack is how far before
// the newest event it looks again, for one that came into view late;
// after tailTries looks in a row that failed it gives up.
const (
	tailPage  = 200
	tailBack  = 10 * time.Second
	tailTries = 10
)

// tail writes the last events and then each new one. A look that fails
// is said on errw and tried again: a database away for a moment does
// not end it, one that stays away does.
func tail(ctx context.Context, events store.Events, w, errw io.Writer, domain string, last int, every time.Duration, once bool) error {
	f := &follower{events: events, w: w, domain: domain}
	if err := f.begin(ctx, last); err != nil || once {
		return err
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for failed := 0; ; {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
		err := f.look(ctx)
		switch {
		case err == nil:
			failed = 0
		case ctx.Err() != nil:
			return nil
		case failed+1 == tailTries:
			return fmt.Errorf("the events could not be read %d times in a row: %w", tailTries, err)
		default:
			failed++
			fmt.Fprintf(errw, "cairnpost: the events could not be read (%v); trying again\n", err)
		}
	}
}
