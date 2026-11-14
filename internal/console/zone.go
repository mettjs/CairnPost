package console

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

// zoneCookie is where a browser's script keeps the name of its own time
// zone, so pages are answered in it. It is no secret and the script
// writes it, so it is not HttpOnly.
const zoneCookie = "cairnpost_console_zone"

// zoneOf is the time zone a request's times are said in: the browser's
// own if it said one that exists, the console's otherwise.
func (c *console) zoneOf(r *http.Request) *time.Location {
	held, err := r.Cookie(zoneCookie)
	if err != nil || !zoneNamed(held.Value) {
		return c.Zone
	}
	if zone := c.zones.named(held.Value); zone != nil {
		return zone
	}
	return c.Zone
}

// zoneNamed reports whether a browser's word could be a time zone's
// name ("Europe/Berlin", "Etc/GMT+5"): anything else is not looked up.
func zoneNamed(name string) bool {
	if name == "" || len(name) > 64 || name == "Local" || strings.Contains(name, "..") {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/_+-", r)) {
			return false
		}
	}
	return true
}

// zones keeps the time zones browsers have named, so a name is looked
// up once. Browsers are in few zones; past zonesKept it starts over
// rather than grow.
type zones struct {
	mu sync.Mutex
	by map[string]*time.Location
}

const zonesKept = 512

// named is the time zone a name names, nil if it names none.
func (z *zones) named(name string) *time.Location {
	z.mu.Lock()
	defer z.mu.Unlock()
	if zone, known := z.by[name]; known {
		return zone
	}
	zone, err := time.LoadLocation(name)
	if err != nil {
		zone = nil // kept too: a name that names none is not looked up again
	}
	if z.by == nil || len(z.by) >= zonesKept {
		z.by = map[string]*time.Location{}
	}
	z.by[name] = zone
	return zone
}
