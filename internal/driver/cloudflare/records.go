package cloudflare

import "github.com/mettjs/cairnpost/internal/driver"

// Records names the DNS records Cloudflare adds when a domain or a
// subdomain is onboarded for sending. All sit under one label of their
// own, so they stand beside another provider's. It reads nothing.
func Records(domain string) []driver.Record {
	return []driver.Record{
		{Use: driver.UseDKIM, Name: "cf-bounce._domainkey." + domain, Type: "TXT"},
		{Use: driver.UseSPF, Name: "cf-bounce." + domain, Type: "TXT", Include: "_spf.mx.cloudflare.net"},
		{Use: driver.UseBounce, Name: "cf-bounce." + domain, Type: "MX"},
	}
}
