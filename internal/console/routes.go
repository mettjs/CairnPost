package console

import "net/http"

// routes is every page and form of the console, and what answers it.
func (c *console) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("GET "+Root+"/assets/{file}", c.assetServer())
	mux.HandleFunc("GET "+Root+"/signin", c.signinPage)
	mux.HandleFunc("POST "+Root+"/signin", c.signin)
	mux.Handle("POST "+Root+"/signout", c.signed(c.signout))
	mux.Handle("GET "+Root+"/{$}", c.signed(c.overview))
	mux.Handle("GET "+Root+"/emails", c.signed(c.emailsPage))
	mux.Handle("POST "+Root+"/emails/find", c.signed(c.findEmails))
	// A refused search leaves this address in the browser: asked for
	// again, it is the list.
	mux.Handle("GET "+Root+"/emails/find", c.signed(back("/emails")))
	mux.Handle("GET "+Root+"/emails/{id}", c.signed(c.emailPage))
	mux.Handle("GET "+Root+"/suppressions", c.signed(c.suppressionsPage))
	mux.Handle("POST "+Root+"/suppressions", c.signed(c.addSuppression))
	mux.Handle("POST "+Root+"/suppressions/find", c.signed(c.findSuppressions))
	mux.Handle("POST "+Root+"/suppressions/lift", c.signed(c.liftSuppression))
	mux.Handle("POST "+Root+"/suppressions/lift-all", c.signed(c.liftSuppressions))
	for _, formOnly := range []string{"find", "lift", "lift-all"} {
		mux.Handle("GET "+Root+"/suppressions/"+formOnly, c.signed(back("/suppressions")))
	}
	mux.Handle("GET "+Root+"/webhooks", c.signed(c.webhooksPage))
	mux.Handle("POST "+Root+"/webhooks", c.signed(c.createWebhook))
	mux.Handle("GET "+Root+"/webhooks/{id}", c.signed(c.endpointPage))
	mux.Handle("POST "+Root+"/webhooks/{id}", c.signed(c.updateEndpoint))
	mux.Handle("POST "+Root+"/webhooks/{id}/enabled", c.signed(c.enableEndpoint))
	mux.Handle("POST "+Root+"/webhooks/{id}/test", c.signed(c.testEndpoint))
	mux.Handle("POST "+Root+"/webhooks/{id}/deliveries/{delivery}/retry", c.signed(c.retryDelivery))
	mux.Handle("GET "+Root+"/webhooks/{id}/delete", c.signed(c.removePage))
	mux.Handle("POST "+Root+"/webhooks/{id}/delete", c.signed(c.removeEndpoint))
	// A form's own address, asked for, is the endpoint's page.
	toEndpoint := c.signed(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, endpointURL(r.PathValue("id"), "", ""), http.StatusSeeOther)
	})
	mux.Handle("GET "+Root+"/webhooks/{id}/enabled", toEndpoint)
	mux.Handle("GET "+Root+"/webhooks/{id}/test", toEndpoint)
	mux.Handle("GET "+Root+"/webhooks/{id}/deliveries/{delivery}/retry", toEndpoint)
	mux.Handle("GET "+Root+"/domains", c.signed(c.domainsPage))
	mux.Handle("GET "+Root+"/domains/{name}", c.signed(c.domainPage))
	mux.Handle("GET "+Root+"/keys", c.signed(c.keysPage))
	mux.Handle("POST "+Root+"/keys", c.signed(c.createKey))
	mux.Handle("GET "+Root+"/keys/{prefix}/revoke", c.signed(c.revokePage))
	mux.Handle("POST "+Root+"/keys/{prefix}/revoke", c.signed(c.revokeKey))
	mux.HandleFunc("GET "+Root, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, Root+"/", http.StatusSeeOther)
	})
	mux.Handle(Root+"/", c.signed(func(w http.ResponseWriter, r *http.Request) {
		c.fail(w, r, http.StatusNotFound, "There is no such page.")
	}))
	return mux
}
