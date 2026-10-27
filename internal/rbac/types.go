// Package rbac provides role-based access control primitives for the
// custom-log-aggregator: users, teams, roles, tenants, role bindings, a
// thread-safe in-memory store (with optional JSON file persistence), tenant
// isolation enforcement, HMAC-signed bearer tokens, and HTTP middleware for
// authentication and per-route authorization.
package rbac

// User is a principal that can be granted permissions directly or via team
// membership.
type User struct {
	ID    string   `json:"id"`
	Email string   `json:"email"`
	Teams []string `json:"teams"` // Team IDs this user belongs to.
}

// Team groups users together so roles can be granted to the whole group at
// once.
type Team struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Permission is a fine-grained capability, optionally scoped to a resource
// (e.g. a service or tenant). Both Action and Resource support the wildcard
// "*" which matches any value.
//
// Examples:
//
//	Permission{Action: "logs:read", Resource: "*"}
//	Permission{Action: "logs:write", Resource: "ingest-service"}
//	Permission{Action: "admin:*", Resource: "*"}
type Permission struct {
	Action   string `json:"action"`
	Resource string `json:"resource"`
}

// Matches reports whether p (a permission granted by a role) satisfies the
// requested permission `req`. Wildcard segments in the granted permission's
// Action match any action; "admin:*" matches "admin:anything" as well as any
// action for which the prefix before ':' is "admin" combined with a "*"
// suffix. Resource wildcards ("*") match any resource.
func (p Permission) Matches(req Permission) bool {
	return matchAction(p.Action, req.Action) && matchResource(p.Resource, req.Resource)
}

func matchAction(granted, requested string) bool {
	if granted == "*" || granted == requested {
		return true
	}
	// Support "namespace:*" wildcards, e.g. "admin:*" matches "admin:anything".
	gi := indexColon(granted)
	if gi >= 0 && granted[gi+1:] == "*" {
		ri := indexColon(requested)
		if ri >= 0 {
			return granted[:gi] == requested[:ri]
		}
		return granted[:gi] == requested
	}
	return false
}

func matchResource(granted, requested string) bool {
	if granted == "" || granted == "*" || requested == "" {
		return true
	}
	return granted == requested
}

func indexColon(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			return i
		}
	}
	return -1
}

// Role is a named bundle of permissions.
type Role struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Permissions []Permission `json:"permissions"`
}

// Tenant represents an isolated customer/organization boundary. Every
// ingested record and every query is associated with a tenant ID.
type Tenant struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Binding grants a Role to either a User or a Team, scoped to a single
// tenant. Exactly one of UserID / TeamID should be set.
type Binding struct {
	ID       string `json:"id"`
	UserID   string `json:"user_id,omitempty"`
	TeamID   string `json:"team_id,omitempty"`
	RoleID   string `json:"role_id"`
	TenantID string `json:"tenant_id"`
}
