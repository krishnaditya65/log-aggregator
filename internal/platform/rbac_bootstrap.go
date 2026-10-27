package platform

import "custom-log-aggregator/internal/rbac"

// bootstrapRBAC seeds a default tenant, an admin role with full wildcard
// permissions, and an admin user/binding so operators have something to
// mint tokens against immediately. Real deployments should call
// Platform.RBAC.Save/Load against a persistent path and manage
// users/teams/tenants through the /v2/rbac endpoints instead of relying on
// these seed values in production.
func bootstrapRBAC(store *rbac.Store) {
	const (
		defaultTenantID = "default"
		adminRoleID     = "admin"
		adminUserID     = "admin"
	)

	store.PutTenant(rbac.Tenant{ID: defaultTenantID, Name: "Default"})
	store.PutRole(rbac.Role{
		ID:   adminRoleID,
		Name: "Admin",
		Permissions: []rbac.Permission{
			{Action: "admin:*", Resource: "*"},
			{Action: "logs:read", Resource: "*"},
			{Action: "logs:write", Resource: "*"},
			{Action: "metrics:read", Resource: "*"},
			{Action: "metrics:write", Resource: "*"},
			{Action: "alerts:read", Resource: "*"},
			{Action: "alerts:write", Resource: "*"},
		},
	})
	store.PutRole(rbac.Role{
		ID:   "viewer",
		Name: "Viewer",
		Permissions: []rbac.Permission{
			{Action: "logs:read", Resource: "*"},
			{Action: "metrics:read", Resource: "*"},
			{Action: "alerts:read", Resource: "*"},
		},
	})
	store.PutUser(rbac.User{ID: adminUserID, Email: "admin@localhost"})
	store.PutBinding(rbac.Binding{ID: "admin-binding", UserID: adminUserID, RoleID: adminRoleID, TenantID: defaultTenantID})
}
