package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/mettjs/cairnpost/internal/config"
	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/webhook"
)

// webhooksCommand manages webhook endpoints straight in the database, as
// keys does: it is run by whoever operates the server.
func webhooksCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("say which: webhooks create, webhooks list or webhooks remove ID")
	}
	var n webhook.New
	var events string
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("webhooks create", flag.ContinueOnError)
		fs.StringVar(&n.URL, "url", "", "where to send the webhooks, e.g. https://app.example.com/hooks/email (required)")
		fs.StringVar(&n.Domain, "domain", "", "the one sending domain to hear about (default: all)")
		fs.StringVar(&events, "events", "", "the event types to hear about, separated by commas: "+
			strings.Join(webhook.Types(), ", ")+" (default: all)")
		fs.StringVar(&n.Description, "description", "", "what the endpoint is, for whoever reads the list later")
		if err := parse(fs, args[1:]); err != nil {
			return err
		}
		for _, t := range strings.Split(events, ",") {
			if t = strings.TrimSpace(t); t != "" {
				n.Types = append(n.Types, t)
			}
		}
	case "list":
	case "remove":
		if len(args) != 2 {
			return errors.New("give the endpoint's id, as webhooks list shows it: webhooks remove ID")
		}
	default:
		return fmt.Errorf("webhooks %s is not a command; use create, list or remove", args[0])
	}
	db, err := open(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	// The server's own setting decides whether an endpoint may be plain
	// http. Nothing is sent from here, so the poster is never used.
	insecure, err := config.AllowPrivate(os.Getenv)
	if err != nil {
		return err
	}
	most, err := config.MaxEndpoints(os.Getenv)
	if err != nil {
		return err
	}
	svc := webhook.NewService(db, nil, insecure, most, 0, time.Now)
	switch args[0] {
	case "create":
		e, err := svc.Create(ctx, "", n)
		if err != nil {
			return plain(err)
		}
		fmt.Fprintf(os.Stderr, "Made the endpoint %s. Its signing secret, shown only now:\n", e.ID)
		fmt.Println(webhook.Reveal(e.Secret))
		return nil
	case "list":
		return listWebhooks(ctx, svc)
	}
	if err := svc.Delete(ctx, "", args[1]); errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("no endpoint has the id %s", args[1])
	} else if err != nil {
		return err
	}
	fmt.Println("removed", args[1])
	return nil
}

// listWebhooks prints every endpoint as a table.
func listWebhooks(ctx context.Context, svc *webhook.Service) error {
	list, err := svc.List(ctx, "")
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tURL\tDOMAIN\tEVENTS\tSTATUS\tDESCRIPTION")
	for _, e := range list {
		domain, events, status := e.Domain, "all", "enabled"
		if domain == "" {
			domain = "all"
		}
		if len(e.Kinds) > 0 {
			types := make([]string, 0, len(e.Kinds))
			for _, kind := range e.Kinds {
				types = append(types, webhook.TypeOf(kind))
			}
			events = strings.Join(types, ",")
		}
		switch webhook.StateOf(e) {
		case webhook.StateOff:
			status = "disabled"
		case webhook.StatePaused:
			status = "paused"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", e.ID, e.URL, domain, events, status, e.Description)
	}
	return w.Flush()
}
