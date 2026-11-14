package console

import "github.com/mettjs/cairnpost/internal/store"

// chip is one way to narrow a list, as a link: what it is called, where
// it leads, and whether the list is narrowed to it now.
type chip struct {
	Label, URL string
	On         bool
	// N is how many things it leads to, "" for not said.
	N string
}

// emailChips are the states a list of emails can be narrowed to, and
// all of them first; each keeps the address the list is narrowed to.
func emailChips(state, handle string) []chip {
	chips := []chip{{Label: "All", URL: emailsAt("", "", handle), On: state == ""}}
	for _, s := range store.States {
		chips = append(chips, chip{Label: wordsOf(s).Label, URL: emailsAt(s, "", handle), On: s == state})
	}
	return chips
}

// deliveryChips are the states an endpoint's deliveries can be narrowed to.
func deliveryChips(id, state string) []chip {
	chips := []chip{{Label: "Everything", URL: endpointURL(id, "", ""), On: state == ""}}
	for _, s := range store.DeliveryStates {
		label, _ := deliveryWords(s)
		chips = append(chips, chip{Label: label, URL: endpointURL(id, s, ""), On: s == state})
	}
	return chips
}
