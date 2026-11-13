package api

import "net/http"

// liftSuppressions is POST /suppressions/lift: send to an address again,
// or to every suppressed address of a domain. The address is in the body,
// never the URL: a URL is what proxies and access logs keep.
func (a *app) liftSuppressions(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Domain  string `json:"domain"`
		Address string `json:"address"`
		All     bool   `json:"all"`
		Reason  string `json:"reason"`
	}
	const shape = `The body must be JSON with "domain" and either "address", or "all": true to lift every suppression ` +
		`of the domain; with "all", "reason" narrows it to "bounce", "complaint" or "manual".`
	if !a.body(w, r, &body, shape) {
		return
	}
	if body.All == (body.Address != "") || (!body.All && body.Reason != "") {
		fail(w, http.StatusUnprocessableEntity, "validation_error", shape)
		return
	}
	lifted, list := 1, a.suppressions(r)
	var err error
	if body.All {
		lifted, err = list.LiftAll(r.Context(), body.Domain, body.Reason)
	} else {
		err = list.Lift(r.Context(), body.Domain, body.Address)
	}
	if err != nil {
		a.failSuppress(w, err)
		return
	}
	write(w, http.StatusOK, map[string]int{"lifted": lifted})
}
