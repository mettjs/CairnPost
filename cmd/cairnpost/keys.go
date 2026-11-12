package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/mettjs/cairnpost/internal/keys"
	"github.com/mettjs/cairnpost/internal/store"
)

// keysCommand manages API keys straight in the database: it is run by
// whoever operates the server, and is how the first key comes to exist.
func keysCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("say which: keys create, keys list or keys revoke PREFIX")
	}
	// What was asked is understood before the database is opened, so -h
	// and a mistyped command need no database.
	var name, scope, domain string
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("keys create", flag.ContinueOnError)
		fs.StringVar(&name, "name", "", "what will use the key, e.g. \"billing service\" (required)")
		fs.StringVar(&scope, "scope", store.ScopeSend, "send: may send email; admin: may also manage CairnPost")
		fs.StringVar(&domain, "domain", "", "the one sending domain the key may send from (default: any)")
		if err := parse(fs, args[1:]); err != nil {
			return err
		}
	case "list":
	case "revoke":
		if len(args) != 2 {
			return errors.New("give the key's prefix, as keys list shows it: keys revoke cp_1a2b3c4d")
		}
	default:
		return fmt.Errorf("keys %s is not a command; use create, list or revoke", args[0])
	}
	db, err := open(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	svc := keys.New(db)
	switch args[0] {
	case "create":
		full, k, err := svc.Create(ctx, name, scope, domain)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Made the %s key %q (%s). This is the only time it is shown:\n", k.Scope, k.Name, k.Prefix)
		fmt.Println(full)
		return nil
	case "list":
		return listKeys(ctx, svc)
	}
	if err := svc.Revoke(ctx, args[1]); errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("no active key has the prefix %s", args[1])
	} else if err != nil {
		return err
	}
	fmt.Println("revoked", args[1])
	return nil
}

// listKeys prints every key as a table.
func listKeys(ctx context.Context, svc *keys.Service) error {
	list, err := svc.List(ctx)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PREFIX\tNAME\tSCOPE\tDOMAIN\tCREATED\tSTATUS")
	for _, k := range list {
		status, domain := "active", k.Domain
		if k.RevokedAt != nil {
			status = "revoked " + k.RevokedAt.Format("2006-01-02")
		}
		if domain == "" {
			domain = "any"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", k.Prefix, k.Name, k.Scope, domain,
			k.CreatedAt.Format("2006-01-02"), status)
	}
	return w.Flush()
}
