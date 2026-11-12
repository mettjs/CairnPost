// Command cairnpost is the server and its command line, in one binary.
package main

import (
	"context"
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
  cairnpost send                  send an email through a running server
  cairnpost version               print the version

Each command explains its flags with -h. The server is configured by
CAIRNPOST_* environment variables; see the README.`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "cairnpost:", err)
		os.Exit(1)
	}
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
