package address_test

import (
	"testing"

	"github.com/mettjs/cairnpost/internal/address"
)

func TestBareIsAnAddressAndNothingElse(t *testing.T) {
	for in, want := range map[string]string{
		"ana@example.org":              "ana@example.org",
		"  Ana@Example.org ":           "Ana@Example.org",
		"<ana@example.org>":            "ana@example.org",
		`"ana maria"@example.org`:      `"ana maria"@example.org`,
		"Ana <ana@example.org>":        "",
		"nobody":                       "",
		"":                             "",
		"a@example.org, b@example.org": "",
	} {
		got, ok := address.Bare(in)
		if got != want || ok != (want != "") {
			t.Errorf("%q: %q %v, want %q", in, got, ok, want)
		}
	}
}
