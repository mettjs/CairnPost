package main

import (
	"strings"
	"testing"

	"github.com/mettjs/cairnpost/internal/dnscheck"
	"github.com/mettjs/cairnpost/internal/domains"
	"github.com/mettjs/cairnpost/internal/driver"
)

// A policy taken from a parent domain is printed at the parent's name and
// said to be the parent's, though it is in place; a record that is simply
// in place gets no sentence.
func TestPrintReportNamesAnInheritedPolicy(t *testing.T) {
	report := domains.Report{Domain: "mail.example.com", Provider: "cloudflare", Asks: true, Results: []dnscheck.Result{
		{Record: driver.Record{Use: driver.UseDKIM, Name: "k._domainkey.mail.example.com", Type: "TXT"}, State: dnscheck.OK},
		{Record: driver.Record{Use: dnscheck.UseDMARC, Name: "_dmarc.mail.example.com", Type: "TXT"},
			State: dnscheck.OK, Inherited: "example.com", Policy: "reject"},
	}}
	var out strings.Builder
	if err := printReport(&out, report); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "TXT _dmarc.example.com\n") || strings.Contains(got, "TXT _dmarc.mail.example.com") {
		t.Errorf("the policy is not printed at the parent's name:\n%s", got)
	}
	if !strings.Contains(got, "DMARC policy: This domain has no record of its own. The one of example.com may speak for it.") {
		t.Errorf("the policy is not said to be the parent's:\n%s", got)
	}
	if strings.Contains(got, "DKIM key:") {
		t.Errorf("a record simply in place got a sentence:\n%s", got)
	}
}
