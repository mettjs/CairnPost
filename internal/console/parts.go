package console

import "strings"

// part is one thing CairnPost does, and how it is doing.
type part struct {
	Name string
	// State is "ok", "failing" or "off"; Says is that in words.
	State, Says string
}

// stateOf is a part's state: healthy is nil for one that is off.
func stateOf(name string, healthy func() bool, ok, failing, off string) part {
	switch {
	case healthy == nil:
		return part{name, "off", off}
	case healthy():
		return part{name, "ok", ok}
	}
	return part{name, "failing", failing}
}

// parts is each part of the server and whether it is working. It asks
// nothing of the mail provider and counts no table. That the database
// answers is not said: no page could be reached if it did not.
func (c *console) parts() []part {
	return []part{
		stateOf("Delivery events", c.Events, "Being read from the mail provider.",
			"Not being read. Mail still goes; bounces are not known until this recovers.", c.EventsOff),
		stateOf("Webhooks", c.Sending, "Being sent.",
			"Not being sent. They are kept and go out when this recovers.", "Off."),
		stateOf("Scheduled emails", c.Scheduling, "Sent when their time comes.",
			"Not being sent. They wait, and go out when this recovers.", "Off."),
		stateOf("Pruning", c.Pruning, "Old records are being deleted on time.",
			"Not running. Mail still goes; old records stay until this recovers.", "Off."),
	}
}

// pulse is how the server stands, in the one sentence every page carries.
type pulse struct {
	Failing bool
	Says    string
}

func pulseOf(parts []part) pulse {
	var failing, off []string
	for _, p := range parts {
		switch p.State {
		case "failing":
			failing = append(failing, p.Name)
		case "off":
			off = append(off, p.Name)
		}
	}
	switch {
	case len(failing) > 0:
		return pulse{Failing: true, Says: "Not working: " + strings.Join(failing, ", ")}
	case len(off) > 0:
		// Off is how the server was set up, not a fault; it is still said.
		return pulse{Says: "Working. Off: " + strings.Join(off, ", ")}
	}
	return pulse{Says: "Everything is working"}
}
