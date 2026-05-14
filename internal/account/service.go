// Package account owns the business logic for account CRUD and
// lifecycle transitions. It depends only on the store.AccountRepo
// interface, never on a concrete database driver — this is the
// load-bearing seam that lets the storage backend be swapped without
// touching business code.
package account

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/internal/store"
)

// Service implements account operations.
type Service struct {
	repo store.AccountRepo
	now  func() time.Time
}

// NewService wires repo into a Service.
func NewService(repo store.AccountRepo) *Service {
	return &Service{repo: repo, now: func() time.Time { return time.Now().UTC() }}
}

// SetClock overrides the time source. Tests only.
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// validateName checks the name meets our basic rules: non-empty, no
// surrounding whitespace, ≤ 64 chars. We deliberately do *not* enforce
// a regex on contents — names show up in Discord and we want unicode
// support.
func validateName(name string) error {
	if name == "" {
		return errcode.New(errcode.InvalidArgument, "name is empty")
	}
	if strings.TrimSpace(name) != name {
		return errcode.New(errcode.InvalidArgument, "name has leading/trailing whitespace")
	}
	if len(name) > 64 {
		return errcode.New(errcode.InvalidArgument, "name is longer than 64 bytes")
	}
	return nil
}

// Create inserts a new account in the "created" lifecycle state.
func (s *Service) Create(ctx context.Context, name string, role store.Role) (*store.Account, error) {
	a, err := s.Build(name, role)
	if err != nil {
		return nil, err
	}
	if err := s.repo.Create(ctx, a); err != nil {
		return nil, err
	}
	return a, nil
}

// Build validates the name + role and constructs an Account ready to
// be persisted. **No I/O** is performed, so callers that need to
// insert through a transactional store (see store.Bundler.WithTx) can
// do the validation up front and write inside their own tx.
func (s *Service) Build(name string, role store.Role) (*store.Account, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	if !role.Valid() {
		return nil, errcode.New(errcode.InvalidArgument, "invalid role: %q", role)
	}
	u, err := uuid.NewV7()
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "uuidv7")
	}
	now := s.now()
	return &store.Account{
		ID:             u.String(),
		Name:           name,
		Role:           role,
		LifecycleState: store.LifecycleCreated,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

// PlanUpdate is the pure (no-I/O) half of Update. Given the current
// account state and a request, it validates every requested field and
// returns the proposed new state plus the list of fields that would
// change. Callers that wish to run the mutation inside a custom
// transaction call PlanUpdate first, then repo.Update inside their tx.
func (s *Service) PlanUpdate(a *store.Account, req UpdateRequest) (*store.Account, []ChangeRecord, error) {
	if a == nil {
		return nil, nil, errcode.New(errcode.InvalidArgument, "account is nil")
	}
	if req.Name == nil && req.Role == nil {
		return nil, nil, errcode.New(errcode.InvalidArgument, "no fields to update")
	}
	if req.Name != nil {
		if err := validateName(*req.Name); err != nil {
			return nil, nil, err
		}
	}
	if req.Role != nil && !req.Role.Valid() {
		return nil, nil, errcode.New(errcode.InvalidArgument, "invalid role: %q", *req.Role)
	}

	out := *a
	var changes []ChangeRecord
	if req.Name != nil && *req.Name != a.Name {
		changes = append(changes, ChangeRecord{Field: "name", Old: a.Name, New: *req.Name})
		out.Name = *req.Name
	}
	if req.Role != nil && *req.Role != a.Role {
		changes = append(changes, ChangeRecord{
			Field: "role", Old: string(a.Role), New: string(*req.Role),
		})
		out.Role = *req.Role
	}
	if len(changes) > 0 {
		out.UpdatedAt = s.now()
	}
	return &out, changes, nil
}

// Get returns an account by ID.
func (s *Service) Get(ctx context.Context, id string) (*store.Account, error) {
	return s.repo.Get(ctx, id)
}

// GetByName returns an account by exact name.
func (s *Service) GetByName(ctx context.Context, name string) (*store.Account, error) {
	return s.repo.GetByName(ctx, name)
}

// List returns all accounts, oldest first.
func (s *Service) List(ctx context.Context) ([]*store.Account, error) {
	return s.repo.List(ctx)
}

// UpdateRequest captures the optional changes Update may apply. Fields
// are pointers so the caller can distinguish "not provided" from
// "provided as the zero value". All non-nil fields are validated up
// front, then applied atomically (one repo.Update call).
type UpdateRequest struct {
	Name *string
	Role *store.Role
}

// ChangeRecord describes a single field that Update mutated. Returned
// to the caller so audit logs can record exactly what changed.
type ChangeRecord struct {
	Field string `json:"field"`
	Old   string `json:"old"`
	New   string `json:"new"`
}

// Update applies the requested changes to an account atomically. It
// validates every requested field before touching the repository, so a
// later failure cannot leave the account half-updated.
//
// Returns the updated account plus the list of fields that actually
// changed (empty when the request was a no-op).
func (s *Service) Update(ctx context.Context, id string, req UpdateRequest) (*store.Account, []ChangeRecord, error) {
	a, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	newAcc, changes, err := s.PlanUpdate(a, req)
	if err != nil {
		return nil, nil, err
	}
	if len(changes) == 0 {
		return a, nil, nil
	}
	if err := s.repo.Update(ctx, newAcc); err != nil {
		return nil, nil, err
	}
	return newAcc, changes, nil
}

// SetRole is a convenience wrapper around Update for role-only changes.
// Retained because tests and external callers may depend on the
// narrower API.
func (s *Service) SetRole(ctx context.Context, id string, role store.Role) (*store.Account, error) {
	a, _, err := s.Update(ctx, id, UpdateRequest{Role: &role})
	return a, err
}

// Rename is a convenience wrapper around Update for name-only changes.
func (s *Service) Rename(ctx context.Context, id, name string) (*store.Account, error) {
	a, _, err := s.Update(ctx, id, UpdateRequest{Name: &name})
	return a, err
}

// Delete removes an account permanently. Cascade DELETE on the tokens
// table removes all of its API tokens.
func (s *Service) Delete(ctx context.Context, id string) error {
	return s.repo.Delete(ctx, id)
}

// BootstrapRoot guarantees that at least one admin exists. On a fresh
// database it creates an admin named "root" and returns (account,
// true, nil). On a populated database it returns (nil, false, nil).
func (s *Service) BootstrapRoot(ctx context.Context) (*store.Account, bool, error) {
	n, err := s.repo.Count(ctx)
	if err != nil {
		return nil, false, err
	}
	if n > 0 {
		return nil, false, nil
	}
	a, err := s.Create(ctx, "root", store.RoleAdmin)
	if err != nil {
		return nil, false, err
	}
	return a, true, nil
}
