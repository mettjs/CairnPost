package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/suppress"
)

// suppressionsCommand manages the suppression list straight in the
// database, as keys does: it is run by whoever operates the server.
func suppressionsCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("say which: suppressions list, suppressions add or suppressions remove")
	}
	if args[0] != "list" && args[0] != "add" && args[0] != "remove" {
		return fmt.Errorf("suppressions %s is not a command; use list, add or remove", args[0])
	}
	fs := flag.NewFlagSet("suppressions "+args[0], flag.ContinueOnError)
	domain := fs.String("domain", "", "the sending domain, e.g. mail.example.com (list: optional; add, remove: required)")
	var (
		address, note, reason string
		all                   bool
	)
	switch args[0] {
	case "add":
		fs.StringVar(&address, "address", "", "the recipient's address (required)")
		fs.StringVar(&note, "note", "", "why, for whoever reads the list later")
	case "remove":
		fs.StringVar(&address, "address", "", "the recipient's address (or -all)")
		fs.BoolVar(&all, "all", false, "every suppressed address of the domain, instead of -address")
		fs.StringVar(&reason, "reason", "", "with -all: only those suppressed for bounce, complaint or manual")
	}
	if err := parse(fs, args[1:]); err != nil {
		return err
	}
	db, err := open(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	svc := suppress.New(db)
	switch args[0] {
	case "add":
		s, added, err := svc.Add(ctx, *domain, address, note)
		if err != nil {
			return plain(err)
		}
		if !added {
			fmt.Printf("%s was already suppressed for %s; nothing changed\n", s.Address, s.Domain)
			return nil
		}
		fmt.Printf("suppressed %s for %s\n", s.Address, s.Domain)
		return nil
	case "remove":
		if all == (address != "") || (!all && reason != "") {
			return errors.New("give -address, or -all for every suppressed address of the domain; -reason goes with -all")
		}
		if all {
			n, err := svc.LiftAll(ctx, *domain, reason)
			if err != nil {
				return plain(err)
			}
			if n == 1 {
				fmt.Printf("1 suppression lifted for %s\n", *domain)
				return nil
			}
			fmt.Printf("%d suppressions lifted for %s\n", n, *domain)
			return nil
		}
		err := svc.Lift(ctx, *domain, address)
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("%s is not suppressed for %s", address, *domain)
		}
		if err != nil {
			return plain(err)
		}
		fmt.Printf("%s will be sent to again from %s\n", address, *domain)
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "DOMAIN\tADDRESS\tREASON\tSINCE (UTC)\tNOTE")
	cursor := ""
	for {
		page, next, err := svc.List(ctx, *domain, cursor, suppress.MaxLimit)
		if err != nil {
			return plain(err)
		}
		for _, s := range page {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", s.Domain, s.Address, s.Reason, s.CreatedAt.UTC().Format("2006-01-02"), s.Note)
		}
		if cursor = next; cursor == "" {
			return w.Flush()
		}
	}
}

// plain is an error as a person reads it: a request's own fault is its
// sentence, without the package's name in front.
func plain(err error) error {
	var invalid *suppress.Invalid
	if errors.As(err, &invalid) {
		return errors.New(invalid.Reason)
	}
	return err
}
