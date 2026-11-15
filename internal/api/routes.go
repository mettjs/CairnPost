package api

import "net/http"

// What key a call needs.
const (
	// KeyNone: no API key. The call is open, or signed some other way.
	KeyNone = "none"
	// KeyAny: any API key.
	KeyAny = "any"
	// KeyAdmin: an API key with the admin scope.
	KeyAdmin = "admin"
)

// route is one call of the API.
type route struct {
	// pattern is its method and path, as the mux takes them.
	pattern string
	key     string
	serve   func(*app) http.HandlerFunc
	// when says it is there only where a part is in use; nil for always.
	when func(Deps) bool
}

func takesInbound(d Deps) bool { return d.Inbound != nil }
func captures(d Deps) bool     { return d.Captured != nil }

// routes is every call of the API. The file that describes the API to
// other programs (openapi.json) is held to this list by a test.
var routes = []route{
	{pattern: "GET /health", key: KeyNone, serve: func(a *app) http.HandlerFunc { return a.health }},
	{pattern: "POST /emails", key: KeyAny, serve: func(a *app) http.HandlerFunc { return a.sendEmail }},
	{pattern: "POST /emails/batch", key: KeyAny, serve: func(a *app) http.HandlerFunc { return a.sendBatch }},
	{pattern: "GET /emails/{id}", key: KeyAny, serve: func(a *app) http.HandlerFunc { return a.getEmail }},
	{pattern: "GET /emails/{id}/attachments/{place}", key: KeyAdmin, serve: func(a *app) http.HandlerFunc { return a.getAttachment }},
	{pattern: "PATCH /emails/{id}", key: KeyAny, serve: func(a *app) http.HandlerFunc { return a.rescheduleEmail }},
	{pattern: "POST /emails/{id}/cancel", key: KeyAny, serve: func(a *app) http.HandlerFunc { return a.cancelEmail }},
	{pattern: "GET /limits", key: KeyAny, serve: func(a *app) http.HandlerFunc { return a.limits }},
	{pattern: "GET /metrics", key: KeyAdmin, serve: func(a *app) http.HandlerFunc { return a.metrics }},
	{pattern: "GET /suppressions", key: KeyAdmin, serve: func(a *app) http.HandlerFunc { return a.listSuppressions }},
	{pattern: "POST /suppressions", key: KeyAdmin, serve: func(a *app) http.HandlerFunc { return a.addSuppression }},
	{pattern: "POST /suppressions/lift", key: KeyAdmin, serve: func(a *app) http.HandlerFunc { return a.liftSuppressions }},
	{pattern: "POST /inbound", key: KeyNone, serve: func(a *app) http.HandlerFunc { return a.receive }, when: takesInbound},
	{pattern: "GET /inbound", key: KeyAdmin, serve: func(a *app) http.HandlerFunc { return a.listInbound }, when: takesInbound},
	{pattern: "GET /inbound/{id}", key: KeyAdmin, serve: func(a *app) http.HandlerFunc { return a.getInbound }, when: takesInbound},
	{pattern: "GET /inbound/{id}/raw", key: KeyAdmin, serve: func(a *app) http.HandlerFunc { return a.getInboundRaw }, when: takesInbound},
	{pattern: "GET /inbound/{id}/attachments/{place}", key: KeyAdmin, serve: func(a *app) http.HandlerFunc { return a.getInboundFile }, when: takesInbound},
	{pattern: "DELETE /inbound/{id}", key: KeyAdmin, serve: func(a *app) http.HandlerFunc { return a.deleteInbound }, when: takesInbound},
	{pattern: "POST /templates", key: KeyAdmin, serve: func(a *app) http.HandlerFunc { return a.createTemplate }},
	{pattern: "GET /templates", key: KeyAdmin, serve: func(a *app) http.HandlerFunc { return a.listTemplates }},
	{pattern: "GET /templates/{id}", key: KeyAdmin, serve: func(a *app) http.HandlerFunc { return a.getTemplate }},
	{pattern: "PATCH /templates/{id}", key: KeyAdmin, serve: func(a *app) http.HandlerFunc { return a.updateTemplate }},
	{pattern: "DELETE /templates/{id}", key: KeyAdmin, serve: func(a *app) http.HandlerFunc { return a.deleteTemplate }},
	{pattern: "POST /webhooks", key: KeyAdmin, serve: func(a *app) http.HandlerFunc { return a.createWebhook }},
	{pattern: "GET /webhooks", key: KeyAdmin, serve: func(a *app) http.HandlerFunc { return a.listWebhooks }},
	{pattern: "GET /webhooks/{id}", key: KeyAdmin, serve: func(a *app) http.HandlerFunc { return a.getWebhook }},
	{pattern: "PATCH /webhooks/{id}", key: KeyAdmin, serve: func(a *app) http.HandlerFunc { return a.updateWebhook }},
	{pattern: "DELETE /webhooks/{id}", key: KeyAdmin, serve: func(a *app) http.HandlerFunc { return a.deleteWebhook }},
	{pattern: "GET /webhooks/{id}/deliveries", key: KeyAdmin, serve: func(a *app) http.HandlerFunc { return a.listDeliveries }},
	{pattern: "POST /webhooks/{id}/deliveries/{delivery}/retry", key: KeyAdmin, serve: func(a *app) http.HandlerFunc { return a.retryDelivery }},
	{pattern: "POST /webhooks/{id}/test", key: KeyAdmin, serve: func(a *app) http.HandlerFunc { return a.testWebhook }},
	{pattern: "POST /webhooks/{id}/secret", key: KeyAdmin, serve: func(a *app) http.HandlerFunc { return a.rotateWebhookSecret }},
	{pattern: "POST /webhooks/{id}/resume", key: KeyAdmin, serve: func(a *app) http.HandlerFunc { return a.resumeWebhook }},
	{pattern: "GET /capture/emails", key: KeyAdmin, serve: func(a *app) http.HandlerFunc { return a.captured }, when: captures},
}

// Route is a call of the API, for whoever describes it.
type Route struct {
	// Pattern is its method and path: "GET /emails/{id}".
	Pattern string
	// Key is the key it needs: KeyNone, KeyAny or KeyAdmin.
	Key string
}

// Routes answers every call the API can have, whatever parts are in use.
func Routes() []Route {
	out := make([]Route, 0, len(routes))
	for _, r := range routes {
		out = append(out, Route{r.pattern, r.key})
	}
	return out
}
