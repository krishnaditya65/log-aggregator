package rbac

import "fmt"

// ErrTenantAccessDenied is returned by TenantScoper when a caller has no
// binding within the requested tenant.
var ErrTenantAccessDenied = fmt.Errorf("rbac: tenant access denied")

// TenantScoper enforces tenant isolation: a caller may only read or write
// data belonging to tenants they have an explicit role binding in (directly
// or via team membership). Access is deny-by-default — the absence of a
// binding for a tenant, or an unknown user/tenant, always denies.
type TenantScoper struct {
	store *Store
}

// NewTenantScoper builds a TenantScoper backed by store.
func NewTenantScoper(store *Store) *TenantScoper {
	return &TenantScoper{store: store}
}

// CanAccessTenant reports whether userID may read/write data scoped to
// tenantID at all (i.e. has any role binding there). This is a coarser
// check than Store.Authorize, which additionally requires a specific
// permission to match; use CanAccessTenant for a plain tenant-boundary gate
// and Authorize when a specific action/resource permission also matters.
func (ts *TenantScoper) CanAccessTenant(userID, tenantID string) bool {
	if userID == "" || tenantID == "" {
		return false
	}
	return ts.store.UserBelongsToTenant(userID, tenantID)
}

// EnforceTenant returns nil if userID may access tenantID, or
// ErrTenantAccessDenied otherwise.
func (ts *TenantScoper) EnforceTenant(userID, tenantID string) error {
	if ts.CanAccessTenant(userID, tenantID) {
		return nil
	}
	return fmt.Errorf("user %q, tenant %q: %w", userID, tenantID, ErrTenantAccessDenied)
}
