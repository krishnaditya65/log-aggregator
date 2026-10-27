package rbac

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// Principal is the authenticated caller extracted from a verified bearer
// token, injected into the request context by Middleware.
type Principal struct {
	UserID   string
	TenantID string
}

// contextKey is unexported so callers cannot collide with or forge context
// values from outside this package.
type contextKey struct{}

var principalKey = contextKey{}

// PrincipalFromContext extracts the Principal previously injected by
// Middleware, if any.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey).(Principal)
	return p, ok
}

// Middleware parses a Bearer token from the Authorization header, verifies
// its signature and expiry against secret, and — on success — injects the
// resulting Principal{UserID, TenantID} into the request context for
// downstream handlers. Requests with a missing or invalid/expired token are
// rejected with 401 Unauthorized before reaching the wrapped handler.
//
// The store parameter is accepted for symmetry with RequirePermission and
// future extensions (e.g. token revocation lookups); it is not required for
// basic signature/expiry verification.
func Middleware(store *Store, secret []byte) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, err := bearerToken(r)
			if err != nil {
				http.Error(w, "unauthorized: "+err.Error(), http.StatusUnauthorized)
				return
			}

			principal, err := VerifyToken(secret, token)
			if err != nil {
				http.Error(w, "unauthorized: "+err.Error(), http.StatusUnauthorized)
				return
			}

			ctx := context.WithValue(r.Context(), principalKey, principal)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func bearerToken(r *http.Request) (string, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", errors.New("missing Authorization header")
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return "", errors.New("Authorization header must use Bearer scheme")
	}
	token := strings.TrimSpace(strings.TrimPrefix(h, prefix))
	if token == "" {
		return "", errors.New("empty bearer token")
	}
	return token, nil
}

// RequirePermission returns middleware that authorizes the authenticated
// Principal (injected earlier in the chain by Middleware) to perform
// `action` against a tenant, and rejects the request with 403 Forbidden if
// they lack it.
//
// Tenant convention: the tenant to authorize against is resolved, in order
// of precedence, from:
//  1. the "X-Tenant-ID" request header
//  2. the "tenant_id" query parameter
//  3. the tenant claim embedded in the caller's bearer token (Principal.TenantID)
//
// This lets a token scoped to one tenant still be explicitly directed at a
// different tenant it also has bindings for (e.g. a multi-tenant
// service-account token), while defaulting sensibly for the common
// single-tenant-per-token case. Store.Authorize enforces tenant isolation
// regardless of which tenant is resolved here: a caller with no binding for
// that tenant is always denied.
//
// RequirePermission must be mounted after Middleware in the handler chain,
// since it requires a Principal to already be present in the request
// context; if none is found it responds 401.
func RequirePermission(store *Store, action Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := PrincipalFromContext(r.Context())
			if !ok {
				http.Error(w, "unauthorized: no authenticated principal", http.StatusUnauthorized)
				return
			}

			tenantID := resolveTenantID(r, principal)

			allowed, err := store.Authorize(principal.UserID, action, tenantID)
			if err != nil {
				http.Error(w, "forbidden: "+err.Error(), http.StatusForbidden)
				return
			}
			if !allowed {
				http.Error(w, "forbidden: insufficient permissions", http.StatusForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func resolveTenantID(r *http.Request, principal Principal) string {
	if h := r.Header.Get("X-Tenant-ID"); h != "" {
		return h
	}
	if q := r.URL.Query().Get("tenant_id"); q != "" {
		return q
	}
	return principal.TenantID
}
