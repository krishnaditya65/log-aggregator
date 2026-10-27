package rbac

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
)

// ErrNotFound is returned when a lookup fails to find the requested entity.
var ErrNotFound = errors.New("rbac: not found")

// snapshot is the JSON-serializable representation of a Store's contents,
// used by Load and Save.
type snapshot struct {
	Users    []User    `json:"users"`
	Teams    []Team    `json:"teams"`
	Roles    []Role    `json:"roles"`
	Tenants  []Tenant  `json:"tenants"`
	Bindings []Binding `json:"bindings"`
}

// Store is a thread-safe, in-memory RBAC data store with optional JSON file
// persistence via Load/Save.
type Store struct {
	mu sync.RWMutex

	users    map[string]User
	teams    map[string]Team
	roles    map[string]Role
	tenants  map[string]Tenant
	bindings map[string]Binding
}

// NewStore creates an empty Store.
func NewStore() *Store {
	return &Store{
		users:    make(map[string]User),
		teams:    make(map[string]Team),
		roles:    make(map[string]Role),
		tenants:  make(map[string]Tenant),
		bindings: make(map[string]Binding),
	}
}

// --- mutation methods -------------------------------------------------

// PutUser inserts or replaces a user.
func (s *Store) PutUser(u User) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[u.ID] = u
}

// PutTeam inserts or replaces a team.
func (s *Store) PutTeam(t Team) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.teams[t.ID] = t
}

// PutRole inserts or replaces a role.
func (s *Store) PutRole(r Role) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.roles[r.ID] = r
}

// PutTenant inserts or replaces a tenant.
func (s *Store) PutTenant(t Tenant) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tenants[t.ID] = t
}

// PutBinding inserts or replaces a role binding.
func (s *Store) PutBinding(b Binding) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bindings[b.ID] = b
}

// RemoveBinding removes a role binding by ID.
func (s *Store) RemoveBinding(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.bindings, id)
}

// --- read methods -------------------------------------------------

// GetUser returns the user with the given ID.
func (s *Store) GetUser(id string) (User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[id]
	if !ok {
		return User{}, fmt.Errorf("user %q: %w", id, ErrNotFound)
	}
	return u, nil
}

// GetTenant returns the tenant with the given ID.
func (s *Store) GetTenant(id string) (Tenant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tenants[id]
	if !ok {
		return Tenant{}, fmt.Errorf("tenant %q: %w", id, ErrNotFound)
	}
	return t, nil
}

// UserBelongsToTenant reports whether the given user has any role binding
// (direct or via team membership) scoped to the given tenant. This is the
// basis of tenant isolation: a caller with no binding in a tenant has no
// access to it, regardless of what permissions they hold elsewhere.
func (s *Store) UserBelongsToTenant(userID, tenantID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.userBelongsToTenantLocked(userID, tenantID)
}

func (s *Store) userBelongsToTenantLocked(userID, tenantID string) bool {
	u, ok := s.users[userID]
	if !ok {
		return false
	}
	teamSet := make(map[string]bool, len(u.Teams))
	for _, t := range u.Teams {
		teamSet[t] = true
	}
	for _, b := range s.bindings {
		if b.TenantID != tenantID {
			continue
		}
		if b.UserID == userID {
			return true
		}
		if b.TeamID != "" && teamSet[b.TeamID] {
			return true
		}
	}
	return false
}

// Authorize resolves whether the given user is permitted to perform `action`
// within `tenantID`. Deny-by-default: it returns (false, nil) unless there is
// an explicit role binding (direct-to-user or via team membership) scoped to
// that tenant whose role grants a permission matching `action` (wildcards on
// Action/Resource are honored, see Permission.Matches). This also enforces
// tenant isolation, since bindings are always tenant-scoped: a user with
// permissions in tenant A cannot use them against tenant B.
func (s *Store) Authorize(userID string, action Permission, tenantID string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	u, ok := s.users[userID]
	if !ok {
		return false, fmt.Errorf("user %q: %w", userID, ErrNotFound)
	}
	if _, ok := s.tenants[tenantID]; !ok {
		return false, fmt.Errorf("tenant %q: %w", tenantID, ErrNotFound)
	}

	teamSet := make(map[string]bool, len(u.Teams))
	for _, t := range u.Teams {
		teamSet[t] = true
	}

	for _, b := range s.bindings {
		if b.TenantID != tenantID {
			continue
		}
		isDirect := b.UserID == userID
		isViaTeam := b.TeamID != "" && teamSet[b.TeamID]
		if !isDirect && !isViaTeam {
			continue
		}
		role, ok := s.roles[b.RoleID]
		if !ok {
			continue
		}
		for _, perm := range role.Permissions {
			if perm.Matches(action) {
				return true, nil
			}
		}
	}
	return false, nil
}

// --- persistence -------------------------------------------------

// Save writes the store's contents to path as JSON.
func (s *Store) Save(path string) error {
	s.mu.RLock()
	snap := snapshot{
		Users:    make([]User, 0, len(s.users)),
		Teams:    make([]Team, 0, len(s.teams)),
		Roles:    make([]Role, 0, len(s.roles)),
		Tenants:  make([]Tenant, 0, len(s.tenants)),
		Bindings: make([]Binding, 0, len(s.bindings)),
	}
	for _, u := range s.users {
		snap.Users = append(snap.Users, u)
	}
	for _, t := range s.teams {
		snap.Teams = append(snap.Teams, t)
	}
	for _, r := range s.roles {
		snap.Roles = append(snap.Roles, r)
	}
	for _, t := range s.tenants {
		snap.Tenants = append(snap.Tenants, t)
	}
	for _, b := range s.bindings {
		snap.Bindings = append(snap.Bindings, b)
	}
	s.mu.RUnlock()

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("rbac: marshal snapshot: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("rbac: write %s: %w", path, err)
	}
	return nil
}

// Load reads a JSON snapshot previously written by Save from path and
// replaces the store's contents with it.
func (s *Store) Load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("rbac: read %s: %w", path, err)
	}
	var snap snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return fmt.Errorf("rbac: unmarshal %s: %w", path, err)
	}

	users := make(map[string]User, len(snap.Users))
	for _, u := range snap.Users {
		users[u.ID] = u
	}
	teams := make(map[string]Team, len(snap.Teams))
	for _, t := range snap.Teams {
		teams[t.ID] = t
	}
	roles := make(map[string]Role, len(snap.Roles))
	for _, r := range snap.Roles {
		roles[r.ID] = r
	}
	tenants := make(map[string]Tenant, len(snap.Tenants))
	for _, t := range snap.Tenants {
		tenants[t.ID] = t
	}
	bindings := make(map[string]Binding, len(snap.Bindings))
	for _, b := range snap.Bindings {
		bindings[b.ID] = b
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.users = users
	s.teams = teams
	s.roles = roles
	s.tenants = tenants
	s.bindings = bindings
	return nil
}
