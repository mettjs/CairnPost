package smtp

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// server is a mail server a test scripts: what it offers, and what it
// answers at each step ("" for yes).
type server struct {
	offers []string // lines after EHLO, such as "AUTH PLAIN"
	// answers are by step: "greeting", "EHLO", "AUTH", "MAIL", "RCPT",
	// "DATA", "end" (the answer once the content is in).
	answers map[string]string
	// hangAtEnd takes the content and closes without an answer.
	hangAtEnd bool
	cert      *tls.Config

	mu       sync.Mutex
	said     []string // every line the client sent, the content apart
	message  string
	secured  bool
	user     string
	password string
	addr     string
}

func (s *server) answer(step, yes string) string {
	if a := s.answers[step]; a != "" {
		return a
	}
	return yes
}

// start listens and answers each connection by the script.
func (s *server) start(t *testing.T) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	s.addr = l.Addr().String()
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go s.serve(conn)
		}
	}()
}

func (s *server) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	say := func(line string) { conn.Write([]byte(line + "\r\n")) }
	read := func() (string, bool) {
		line, err := r.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if err == nil {
			s.mu.Lock()
			s.said = append(s.said, line)
			s.mu.Unlock()
		}
		return line, err == nil
	}
	say(s.answer("greeting", "220 ready"))
	for {
		line, ok := read()
		if !ok {
			return
		}
		verb := strings.ToUpper(strings.Fields(line + " x")[0])
		switch verb {
		case "EHLO", "HELO":
			// A client refused its EHLO tries the older word: the
			// script's answer is for both.
			if a := s.answers["EHLO"]; a != "" {
				say(a)
				continue
			}
			if verb == "HELO" {
				say("250 ok")
				continue
			}
			// The first line is the server's name; what it offers follows.
			for _, o := range append([]string{"mail.test"}, s.offers...) {
				say("250-" + o)
			}
			say("250 ok")
		case "STARTTLS":
			say("220 go")
			secured := tls.Server(conn, s.cert)
			if secured.Handshake() != nil {
				return
			}
			conn, r = secured, bufio.NewReader(secured)
			s.mu.Lock()
			s.secured = true
			s.mu.Unlock()
		case "AUTH":
			if a := s.answers["AUTH"]; a != "" {
				say(a)
				continue
			}
			parts := strings.Fields(line)
			if parts[1] == "PLAIN" {
				raw, _ := base64.StdEncoding.DecodeString(parts[2])
				both := strings.Split(string(raw), "\x00")
				s.mu.Lock()
				s.user, s.password = both[1], both[2]
				s.mu.Unlock()
			} else {
				say("334 " + base64.StdEncoding.EncodeToString([]byte("Username:")))
				u, _ := read()
				say("334 " + base64.StdEncoding.EncodeToString([]byte("Password:")))
				p, _ := read()
				user, _ := base64.StdEncoding.DecodeString(u)
				pass, _ := base64.StdEncoding.DecodeString(p)
				s.mu.Lock()
				s.user, s.password = string(user), string(pass)
				s.mu.Unlock()
			}
			say("235 in")
		case "MAIL":
			say(s.answer("MAIL", "250 ok"))
		case "RCPT":
			say(s.answer("RCPT", "250 ok"))
		case "DATA":
			a := s.answer("DATA", "354 go on")
			if say(a); !strings.HasPrefix(a, "354") {
				continue
			}
			var b strings.Builder
			for {
				line, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if line == ".\r\n" {
					break
				}
				b.WriteString(line)
			}
			s.mu.Lock()
			s.message = b.String()
			s.mu.Unlock()
			if s.hangAtEnd {
				return
			}
			say(s.answer("end", "250 taken"))
		case "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

// lines is what the client said, as one text.
func (s *server) lines() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.said, "\n")
}

// driverFor is a driver that speaks to the server.
func driverFor(t *testing.T, s *server, security, user, password string) *Driver {
	t.Helper()
	s.start(t)
	host, port, _ := net.SplitHostPort(s.addr)
	n := 0
	for _, c := range port {
		n = n*10 + int(c-'0')
	}
	d := New(Options{Host: host, Port: n, Security: security, Username: user, Password: password, Hello: "cairnpost.test"},
		func() time.Time { return time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC) })
	if s.cert != nil {
		d.tls = s.cert.Clone()
		d.tls.ServerName = "example.com"
	}
	return d
}

// certified is a certificate a test's server shows and its client
// trusts, borrowed from the standard library's test server.
func certified(t *testing.T) *tls.Config {
	t.Helper()
	h := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(h.Close)
	cfg := h.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	cfg.Certificates = h.TLS.Certificates
	return cfg
}

// seen is what the server was left with, read once it is at rest.
type seen struct {
	content, user, password string
	secured                 bool
}

func (s *server) seen() seen {
	s.mu.Lock()
	defer s.mu.Unlock()
	return seen{s.message, s.user, s.password, s.secured}
}
