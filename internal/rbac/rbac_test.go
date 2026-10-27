package rbac

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var testSecret = []byte("test-secret-key-do-not-use-in-prod")

func TestIssueAndVerifyToken(t *testing.T) {
	tok, err := IssueToken(testSecret, "user-1", "tenant-a", time.Hour)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	if tok == "" {
		t.Fatal("expected non-empty token")
	}

	principal, err := VerifyToken(testSecret, tok)
	if err != nil {
		t.Fatalf("VerifyToken: %v", err)
	}
	if principal.UserID != "user-1" || principal.TenantID != "tenant-a" {
		t.Fatalf("unexpected principal: %+v", principal)
	}
}

func TestVerifyTokenExpired(t *testing.T) {
	tok, err := IssueToken(testSecret, "user-1", "tenant-a", -time.Minute)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	_, err = VerifyToken(testSecret, tok)
	if err == nil {
		t.Fatal("expected error for expired token")
	}
	if err != ErrExpiredToken {
		t.Fatalf("expected ErrExpiredToken, got %v", err)
	}
}

func TestVerifyTokenBadSignature(t *testing.T) {
	tok, err := IssueToken(testSecret, "user-1", "tenant-a", time.Hour)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	_, err = VerifyToken([]byte("a-completely-different-secret"), tok)
	if err != ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
}

func TestVerifyTokenMalformed(t *testing.T) {
	cases := []string{"", "not-a-token", "a.b", "a.b.c.d"}
	for _, c := range cases {
		if _, err := VerifyToken(testSecret, c); err != ErrInvalidToken {
			t.Errorf("token %q: expected ErrInvalidToken, got %v", c, err)
		}
	}
}

// buildTestStore constructs:
//
//	tenants: "acme", "globex"
//	teams:   "sre" (log reader role)
//	roles:   "log-reader" {logs:read on *}, "admin" {admin:* on *}
//	users:
//	  "alice" - direct binding to "log-reader" role in "acme"
//	  "bob"   - member of team "sre", which has "log-reader" bound in "acme"
//	  "carol" - direct binding to "admin" role in "acme" (wildcard)
//	  "dave"  - no bindings anywhere
func buildTestStore(t *testing.T) *Store {
	t.Helper()
	s := NewStore()

	s.PutTenant(Tenant{ID: "acme", Name: "Acme Corp"})
	s.PutTenant(Tenant{ID: "globex", Name: "Globex Corp"})

	s.PutTeam(Team{ID: "sre", Name: "SRE"})

	s.PutRole(Role{
		ID:   "log-reader",
		Name: "Log Reader",
		Permissions: []Permission{
			{Action: "logs:read", Resource: "*"},
		},
	})
	s.PutRole(Role{
		ID:   "admin",
		Name: "Admin",
		Permissions: []Permission{
			{Action: "admin:*", Resource: "*"},
		},
	})

	s.PutUser(User{ID: "alice", Email: "alice@acme.com"})
	s.PutUser(User{ID: "bob", Email: "bob@acme.com", Teams: []string{"sre"}})
	s.PutUser(User{ID: "carol", Email: "carol@acme.com"})
	s.PutUser(User{ID: "dave", Email: "dave@acme.com"})

	s.PutBinding(Binding{ID: "b1", UserID: "alice", RoleID: "log-reader", TenantID: "acme"})
	s.PutBinding(Binding{ID: "b2", TeamID: "sre", RoleID: "log-reader", TenantID: "acme"})
	s.PutBinding(Binding{ID: "b3", UserID: "carol", RoleID: "admin", TenantID: "acme"})

	return s
}

func TestAuthorizeDirectBinding(t *testing.T) {
	s := buildTestStore(t)
	ok, err := s.Authorize("alice", Permission{Action: "logs:read", Resource: "ingest"}, "acme")
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !ok {
		t.Fatal("expected alice to be authorized via direct binding")
	}
}

func TestAuthorizeTeamBinding(t *testing.T) {
	s := buildTestStore(t)
	ok, err := s.Authorize("bob", Permission{Action: "logs:read", Resource: "ingest"}, "acme")
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !ok {
		t.Fatal("expected bob to be authorized via team binding")
	}
}

func TestAuthorizeWildcard(t *testing.T) {
	s := buildTestStore(t)
	ok, err := s.Authorize("carol", Permission{Action: "admin:delete-tenant", Resource: "acme"}, "acme")
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !ok {
		t.Fatal("expected carol to be authorized via admin:* wildcard")
	}

	// carol's admin role should NOT satisfy an unrelated action namespace.
	ok, err = s.Authorize("carol", Permission{Action: "billing:read", Resource: "*"}, "acme")
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if ok {
		t.Fatal("admin:* must not grant unrelated billing:read permission")
	}
}

func TestAuthorizeDeniesWithoutBinding(t *testing.T) {
	s := buildTestStore(t)
	ok, err := s.Authorize("dave", Permission{Action: "logs:read", Resource: "*"}, "acme")
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if ok {
		t.Fatal("expected dave to be denied: no bindings")
	}
}

func TestAuthorizeDeniesCrossTenant(t *testing.T) {
	s := buildTestStore(t)
	// Alice has logs:read in "acme" but no binding in "globex" -> deny.
	ok, err := s.Authorize("alice", Permission{Action: "logs:read", Resource: "*"}, "globex")
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if ok {
		t.Fatal("expected alice to be denied in globex tenant (tenant isolation)")
	}
}

func TestTenantScoperDeniesByDefault(t *testing.T) {
	s := buildTestStore(t)
	scoper := NewTenantScoper(s)

	if !scoper.CanAccessTenant("alice", "acme") {
		t.Fatal("expected alice to have access to acme")
	}
	if scoper.CanAccessTenant("alice", "globex") {
		t.Fatal("expected alice to be denied access to globex")
	}
	if scoper.CanAccessTenant("dave", "acme") {
		t.Fatal("expected dave (no bindings) to be denied access to acme")
	}
	if err := scoper.EnforceTenant("dave", "acme"); err == nil {
		t.Fatal("expected EnforceTenant to return an error for dave")
	}
	if err := scoper.EnforceTenant("alice", "acme"); err != nil {
		t.Fatalf("expected EnforceTenant to allow alice in acme, got %v", err)
	}
}

func TestStoreSaveLoadRoundTrip(t *testing.T) {
	s := buildTestStore(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "rbac.json")

	if err := s.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file to exist: %v", err)
	}

	loaded := NewStore()
	if err := loaded.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}

	ok, err := loaded.Authorize("bob", Permission{Action: "logs:read", Resource: "*"}, "acme")
	if err != nil {
		t.Fatalf("Authorize after load: %v", err)
	}
	if !ok {
		t.Fatal("expected bob's team-based authorization to survive save/load round trip")
	}
}

// --- middleware end-to-end tests -------------------------------------------------

func newProtectedHandler(t *testing.T, store *Store) http.Handler {
	t.Helper()
	final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFromContext(r.Context())
		if !ok {
			t.Fatal("expected Principal to be present in context")
		}
		w.Header().Set("X-Principal-User", p.UserID)
		w.WriteHeader(http.StatusOK)
	})
	protected := RequirePermission(store, Permission{Action: "logs:read", Resource: "*"})(final)
	return Middleware(store, testSecret)(protected)
}

func TestMiddlewareValidTokenPasses(t *testing.T) {
	s := buildTestStore(t)
	handler := newProtectedHandler(t, s)

	tok, err := IssueToken(testSecret, "alice", "acme", time.Hour)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/logs", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Principal-User"); got != "alice" {
		t.Fatalf("expected principal user 'alice', got %q", got)
	}
}

func TestMiddlewareMissingTokenRejected(t *testing.T) {
	s := buildTestStore(t)
	handler := newProtectedHandler(t, s)

	req := httptest.NewRequest(http.MethodGet, "/logs", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestMiddlewareInvalidTokenRejected(t *testing.T) {
	s := buildTestStore(t)
	handler := newProtectedHandler(t, s)

	req := httptest.NewRequest(http.MethodGet, "/logs", nil)
	req.Header.Set("Authorization", "Bearer garbage.token.here")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestMiddlewareExpiredTokenRejected(t *testing.T) {
	s := buildTestStore(t)
	handler := newProtectedHandler(t, s)

	tok, err := IssueToken(testSecret, "alice", "acme", -time.Minute)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/logs", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for expired token, got %d", rec.Code)
	}
}

func TestMiddlewareInsufficientPermissionRejected(t *testing.T) {
	s := buildTestStore(t)
	handler := newProtectedHandler(t, s)

	// dave has no bindings at all -> should be 403 once authenticated.
	tok, err := IssueToken(testSecret, "dave", "acme", time.Hour)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/logs", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMiddlewareCrossTenantRejected(t *testing.T) {
	s := buildTestStore(t)
	handler := newProtectedHandler(t, s)

	// alice's token is scoped to "acme" but she requests tenant "globex"
	// explicitly via header; she has no binding there so must be denied.
	tok, err := IssueToken(testSecret, "alice", "acme", time.Hour)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/logs", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("X-Tenant-ID", "globex")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for cross-tenant request, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMiddlewareTenantIDQueryParamConvention(t *testing.T) {
	s := buildTestStore(t)
	handler := newProtectedHandler(t, s)

	tok, err := IssueToken(testSecret, "alice", "acme", time.Hour)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/logs?tenant_id=acme", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 via tenant_id query param, got %d: %s", rec.Code, rec.Body.String())
	}
}
