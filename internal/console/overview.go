package console

import "net/http"

// part is one thing CairnPost does, and how it is doing.
type part struct {
	Name string
	// State is "ok", "failing" or "off"; Says is that in words.
	State, Says string
}

type overview struct {
	Parts  []part
	Driver string
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

// overview is the first page: whether each part of CairnPost is working.
// It asks nothing of the mail provider and counts no table. That the
// database answers is not said: the page could not be reached if it did
// not.
func (c *console) overview(w http.ResponseWriter, r *http.Request) {
	c.render(w, r, http.StatusOK, "overview", overview{Driver: c.Driver, Parts: []part{
		stateOf("Delivery events", c.Events, "Being read from the mail provider.",
			"Not being read. Mail still goes; bounces are not known until this recovers.", c.EventsOff),
		stateOf("Webhooks", c.Sending, "Being sent.",
			"Not being sent. They are kept and go out when this recovers.", "Off."),
		stateOf("Pruning", c.Pruning, "Old records are being deleted on time.",
			"Not running. Mail still goes; old records stay until this recovers.", "Off."),
	}})
}
