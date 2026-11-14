package dnscheck_test

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/dnscheck"
	"github.com/mettjs/cairnpost/internal/driver"
)

// A checker given a DNS server asks that server: every question of a
// check arrives there, and its answer is the one believed. The server
// here says of every name that there is none such.
func TestTheNamedServerIsTheOneAsked(t *testing.T) {
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var asked atomic.Int64
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			if n < 12 {
				continue
			}
			asked.Add(1)
			// The question sent back as its own answer: a response, with
			// recursion available, saying "no such name", and no records.
			answer := append([]byte(nil), buf[:n]...)
			answer[2], answer[3] = answer[2]|0x80, 0x80|3
			answer[6], answer[7], answer[8], answer[9], answer[10], answer[11] = 0, 0, 0, 0, 0, 0
			_, _ = conn.WriteTo(answer, from)
		}
	}()
	checker := dnscheck.New(5*time.Second, conn.LocalAddr().String())
	results, err := checker.Check(context.Background(), "mail.example.com", []driver.Record{
		{Use: driver.UseDKIM, Name: "key._domainkey.mail.example.com", Type: "TXT"},
		{Use: driver.UseBounce, Name: "bounce.mail.example.com", Type: "MX", Under: "provider.example"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		// Missing, not unknown: the server's own answer was read. The
		// system's resolver, asked instead, could not say this of a
		// made-up name so fast, or at all.
		if r.State != dnscheck.Missing {
			t.Errorf("%s at %s is %q, want missing", r.Use, r.Name, r.State)
		}
	}
	if asked.Load() < 3 {
		t.Fatalf("the server was asked %d questions, want one for each record at least", asked.Load())
	}
}
