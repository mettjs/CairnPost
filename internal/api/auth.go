package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/mettjs/cairnpost/internal/keys"
	"github.com/mettjs/cairnpost/internal/store"
)

// caller is who a request is from: the key as it is kept, and as it came.
type caller struct {
	key       store.Key
	presented string
}

type callerKey struct{}

func callerOf(r *http.Request) caller { return r.Context().Value(callerKey{}).(caller) }

// auth lets a request through only with a live API key in its
// Authorization header, an admin's when admin is set.
func (a *app) auth(admin bool, next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || presented == "" {
			fail(w, http.StatusUnauthorized, "missing_api_key",
				`Send your API key in the Authorization header: "Authorization: Bearer cp_...".`)
			return
		}
		key, err := a.Keys.Authenticate(r.Context(), presented)
		switch {
		case errors.Is(err, keys.ErrInvalid):
			fail(w, http.StatusUnauthorized, "invalid_api_key", "This API key is not valid, or was revoked.")
			return
		case err != nil:
			a.Log.Error("api: a key could not be checked", "error", err)
			fail(w, http.StatusServiceUnavailable, "unavailable", "The key could not be checked. Try again shortly.")
			return
		case admin && key.Scope != store.ScopeAdmin:
			fail(w, http.StatusForbidden, "admin_key_required", "This needs an API key with the admin scope.")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), callerKey{}, caller{key, presented})))
	})
}
