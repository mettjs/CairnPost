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
	to := fs.String("to", "", "who it goes to: an address, or several with commas between (required)")
	cc := fs.String("cc", "", "who gets a copy: an address, or several with commas between")
	bcc := fs.String("bcc", "", "who gets a copy the others do not see: an address, or several with commas between")
	subject := fs.String("subject", "", "the subject (required, unless a template gives it)")
	text := fs.String("text", "", "the plain-text body")
	html := fs.String("html", "", "the HTML body")
	idem := fs.String("idempotency-key", "", "a key that makes sending this twice send it once")
	at := fs.String("at", "", "when to send it, such as 2030-01-31T09:00:00Z; left out, it is sent now")
	template := fs.String("template", "", "the name of a template to make it from, in place of -text and -html")
	var attach, values paths
	fs.Var(&attach, "attach", "a file to send with it; give the flag once for each file")
	fs.Var(&values, "var", "a value for a variable of the template, as name=value; give the flag once for each")
	if err := parse(fs, args); err != nil {
		return err
	}
	key := os.Getenv("CAIRNPOST_API_KEY")
	switch {
	case *url == "":
		return errors.New("say where the server is: -url http://localhost:8080, or set CAIRNPOST_URL")
	case key == "":
		return errors.New("set CAIRNPOST_API_KEY to an API key (make one with: cairnpost keys create)")
	}
	files, err := attached(attach)
	if err != nil {
		return err
	}
	fields := map[string]any{
		"from": *from, "to": several(*to), "cc": several(*cc), "bcc": several(*bcc), "subject": *subject, "text": *text, "html": *html,
	}
	if files != nil {
		fields["attachments"] = files
	}
	if *at != "" {
		fields["scheduled_at"] = *at
	}
	switch use, err := templateOf(*template, values); {
	case err != nil:
		return err
	case use != nil:
		fields["template"] = use
	}
	body, _ := json.Marshal(fields)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(*url, "/")+"/emails", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	if *idem != "" {
		req.Header.Set("Idempotency-Key", *idem)
	}
	res, err := (&http.Client{Timeout: 3 * time.Minute}).Do(req)
	if err != nil {
		return fmt.Errorf("the server at %s did not answer: %w", *url, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var answer struct {
		ID, Name, Message string
		Suppressed        []string
		ScheduledAt       string `json:"scheduled_at"`
	}
	_ = json.Unmarshal(raw, &answer)
	if res.StatusCode != http.StatusOK {
		if answer.Message == "" {
			return fmt.Errorf("the server answered %d", res.StatusCode)
		}
		return fmt.Errorf("%s (%d %s)", answer.Message, res.StatusCode, answer.Name)
	}
	fmt.Println(answer.ID)
	if answer.ScheduledAt != "" {
		fmt.Fprintln(os.Stderr, "not sent yet: it waits to be sent at "+answer.ScheduledAt)
	}
	if len(answer.Suppressed) > 0 {
		fmt.Fprintln(os.Stderr, "not sent to, being suppressed: "+strings.Join(answer.Suppressed, ", "))
	}
	return nil
}

// several is a flag's addresses, split at its commas. A name with a
// comma in it is written as an address alone.
func several(flag string) []string {
	out := []string{}
	for _, one := range strings.Split(flag, ",") {
		if one = strings.TrimSpace(one); one != "" {
			out = append(out, one)
		}
	}
	return out
}

// templateOf is the template a send names and the values it gives its
// variables, as the API takes them; nil for none.
func templateOf(name string, values []string) (map[string]any, error) {
	if name == "" {
		if len(values) > 0 {
			return nil, errors.New("-var gives a template's variable a value: name the template with -template")
		}
		return nil, nil
	}
	given := map[string]string{}
	for _, one := range values {
		variable, value, ok := strings.Cut(one, "=")
		if !ok || variable == "" {
			return nil, errors.New("write -var as name=value, such as -var code=4821")
		}
		given[variable] = value
	}
	return map[string]any{"name": name, "variables": given}, nil
}
