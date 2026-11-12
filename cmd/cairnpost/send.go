package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// sendCommand sends one email through a running server's API, as any
// program would.
func sendCommand(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	url := fs.String("url", os.Getenv("CAIRNPOST_URL"), "the server's address (or CAIRNPOST_URL), e.g. http://localhost:8080")
	from := fs.String("from", "", "the sender, \"Name <you@example.com>\" (required)")
	to := fs.String("to", "", "the recipient's address (required)")
	subject := fs.String("subject", "", "the subject (required)")
	text := fs.String("text", "", "the plain-text body")
	html := fs.String("html", "", "the HTML body")
	idem := fs.String("idempotency-key", "", "a key that makes sending this twice send it once")
	if err := fs.Parse(args); err != nil {
		return err
	}
	key := os.Getenv("CAIRNPOST_API_KEY")
	switch {
	case *url == "":
		return errors.New("say where the server is: -url http://localhost:8080, or set CAIRNPOST_URL")
	case key == "":
		return errors.New("set CAIRNPOST_API_KEY to an API key (make one with: cairnpost keys create)")
	}
	body, _ := json.Marshal(map[string]any{
		"from": *from, "to": *to, "subject": *subject, "text": *text, "html": *html,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(*url, "/")+"/emails", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	if *idem != "" {
		req.Header.Set("Idempotency-Key", *idem)
	}
	res, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("the server at %s did not answer: %w", *url, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var answer struct{ ID, Name, Message string }
	_ = json.Unmarshal(raw, &answer)
	if res.StatusCode != http.StatusOK {
		if answer.Message == "" {
			return fmt.Errorf("the server answered %d", res.StatusCode)
		}
		return fmt.Errorf("%s (%d %s)", answer.Message, res.StatusCode, answer.Name)
	}
	fmt.Println(answer.ID)
	return nil
}
