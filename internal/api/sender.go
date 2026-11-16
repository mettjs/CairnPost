package api

import (
	"fmt"

	"github.com/mettjs/cairnpost/internal/mail"
)

// senderWords says a refusal that is most likely the sending domain's:
// what was looked for, and what to do. It says "likely" because DNS is
// all that was read; the mail provider did not say why.
func senderWords(e *mail.SenderDomain) string {
	return fmt.Sprintf("The mail provider refused this email, and %s is likely not set up to send with it: "+
		"none of the DNS records the provider asks of the domain is in place (%s, a %s record, among them). "+
		"Until the domain is set up the provider refuses every email from it, whatever the email says. "+
		"Nothing was sent. Set the domain up with the mail provider, check it with \"cairnpost domains check %s\", and send again; "+
		"the same Idempotency-Key may be used. If the domain is set up and this server's DNS resolver cannot see its records, "+
		"the fault is the email's: check the addresses and the headers.",
		e.Domain, e.Record.Name, e.Record.Type, e.Domain)
}
