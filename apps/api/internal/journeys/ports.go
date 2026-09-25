package journeys

import "context"

// Decision is an approval outcome read from the approvals module.
// Pending is the honest answer when the decision does not exist yet.
type Decision string

const (
	// Pending means the awaited decision has not been made.
	Pending Decision = "pending"
	// Approved means the awaited decision passed.
	Approved Decision = "approved"
	// Rejected means the awaited decision went against the run.
	Rejected Decision = "rejected"
)

// Personas is the identity view this engine needs. Implementations live in
// identity; this package only sees the interface.
type Personas interface {
	// List returns the personas the server has evaluated for the user.
	List(ctx context.Context, userID string) ([]string, error)
}

// Permissions is the authorization view this engine needs. Implementations
// live in authz; a client body is never a Permissions.
type Permissions interface {
	// Allowed reports whether the user holds permission in the company right now.
	Allowed(ctx context.Context, userID, companyID, permission string) (bool, error)
}

// Approvals is the approval view this engine needs. Implementations live in
// approvals. The engine stores a server-generated reference and reads the
// decision; it never accepts the decision from the client.
type Approvals interface {
	// Status returns the decision for a server-generated reference.
	Status(ctx context.Context, companyID, ref string) (Decision, error)
}

// Books is the ledger view the Go-Live gate needs. Implementations live in
// the ledger module. The boolean is re-read on every step.
type Books interface {
	// TrialBalanceNetsToZero reports whether the opening trial balance nets to zero.
	TrialBalanceNetsToZero(ctx context.Context, companyID string) (bool, error)
}

// Providers are the neighbouring modules, supplied per request.
// A zero field falls closed: no personas, no permissions, pending approvals,
// and a trial balance that does not net to zero.
type Providers struct {
	Personas    Personas
	Permissions Permissions
	Approvals   Approvals
	Books       Books
	// ActorName returns the display name for an audit and outbox actor.
	// Nil falls back to the user id.
	ActorName func(ctx context.Context, userID string) (string, error)
}

type providerKey struct{}

func providersPresent(ctx context.Context) bool {
	_, ok := ctx.Value(providerKey{}).(Providers)
	return ok
}

// WithProviders attaches neighbouring-module ports to the request context.
func WithProviders(ctx context.Context, p Providers) context.Context {
	return context.WithValue(ctx, providerKey{}, p)
}

func providersFrom(ctx context.Context) Providers {
	p, _ := ctx.Value(providerKey{}).(Providers)
	if p.Personas == nil {
		p.Personas = denyPersonas{}
	}
	if p.Permissions == nil {
		p.Permissions = denyPermissions{}
	}
	if p.Approvals == nil {
		p.Approvals = pendingApprovals{}
	}
	if p.Books == nil {
		p.Books = lockedBooks{}
	}
	return p
}

type denyPersonas struct{}

// List returns no personas, so an unwired engine shows no journeys.
func (denyPersonas) List(context.Context, string) ([]string, error) { return nil, nil }

type denyPermissions struct{}

// Allowed refuses every permission.
func (denyPermissions) Allowed(context.Context, string, string, string) (bool, error) {
	return false, nil
}

type pendingApprovals struct{}

// Status reports pending so an unwired await never counts as approved.
func (pendingApprovals) Status(context.Context, string, string) (Decision, error) {
	return Pending, nil
}

type lockedBooks struct{}

// TrialBalanceNetsToZero reports false so the company stays locked until the ledger says otherwise.
func (lockedBooks) TrialBalanceNetsToZero(context.Context, string) (bool, error) {
	return false, nil
}
