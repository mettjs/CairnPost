// Command cairnpost is the server and its command line, in one binary.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

// version is set at build time from the release's tag.
var version = "dev"

const usage = `CairnPost: a small transactional email service.

  cairnpost serve                 run the HTTP API (applies migrations first)
  cairnpost migrate               apply migrations and stop
  cairnpost keys create           make an API key (shown once)
  cairnpost keys list             list API keys
  cairnpost keys revoke PREFIX    end an API key
  cairnpost suppressions list     list the addresses not sent to
  cairnpost suppressions add      stop sending to an address
  cairnpost suppressions remove   send to an address again
  cairnpost webhooks create       have a URL told what becomes of emails
  cairnpost webhooks list         list the webhook endpoints
  cairnpost webhooks remove ID    remove a webhook endpoint
  cairnpost domains check DOMAIN  see whether a domain's DNS is ready to send from
  cairnpost send                  send an email through a running server
  cairnpost version               print the version

Each command explains its flags with -h. The server is configured by
CAIRNPOST_* environment variables; see the README.`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := run(ctx, os.Args[1:])
	var bad *badFlags
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
		// -h printed the flags: that is what was asked for.
	case errors.As(err, &bad):
		os.Exit(2) // the flag package has already said what was wrong
	default:
		fmt.Fprintln(os.Stderr, "cairnpost:", err)
		os.Exit(1)
	}
}

// badFlags is a command line the flag package refused and explained.
type badFlags struct{ err error }

func (b *badFlags) Error() string { return b.err.Error() }

// parse reads a command's flags. -h answers flag.ErrHelp; anything the
// flag package refused answers a *badFlags, already reported.
func parse(fs *flag.FlagSet, args []string) error {
	err := fs.Parse(args)
	if err == nil || errors.Is(err, flag.ErrHelp) {
		return err
	}
	return &badFlags{err}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Println(usage)
		return nil
	}
	switch args[0] {
	case "serve":
		return serve(ctx)
	case "migrate":
		return migrate(ctx)
	case "keys":
		return keysCommand(ctx, args[1:])
	case "suppressions":
		return suppressionsCommand(ctx, args[1:])
	case "webhooks":
		return webhooksCommand(ctx, args[1:])
	case "domains":
		return domainsCommand(ctx, args[1:])
	case "send":
		return sendCommand(ctx, args[1:])
	case "version":
		fmt.Println(version)
		return nil
	case "help", "-h", "--help":
		fmt.Println(usage)
		return nil
	}
	return fmt.Errorf("%q is not a command; run cairnpost help", args[0])
}
