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
	db, err := open(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	svc := keys.New(db)
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("keys create", flag.ContinueOnError)
		name := fs.String("name", "", "what will use the key, e.g. \"billing service\" (required)")
		scope := fs.String("scope", store.ScopeSend, "send: may send email; admin: may also manage CairnPost")
		domain := fs.String("domain", "", "the one sending domain the key may send from (default: any)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		full, k, err := svc.Create(ctx, *name, *scope, *domain)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Made the %s key %q (%s). This is the only time it is shown:\n", k.Scope, k.Name, k.Prefix)
		fmt.Println(full)
		return nil
	case "list":
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
	case "revoke":
		if len(args) != 2 {
			return errors.New("give the key's prefix, as keys list shows it: keys revoke cp_1a2b3c4d")
		}
		if err := svc.Revoke(ctx, args[1]); errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("no active key has the prefix %s", args[1])
		} else if err != nil {
			return err
		}
		fmt.Println("revoked", args[1])
		return nil
	}
	return fmt.Errorf("keys %s is not a command; use create, list or revoke", args[0])
}
