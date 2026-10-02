package authz

import (
	"context"

	"github.com/google/uuid"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// LoginGate is what identity (P1.2) calls before it mints a token. A disabled
// user stays in the directory and returns false from CanAuthenticate (A15, R1.9).
// Identity cannot import this package; cmd wires the call. The matching issue
// records the method signatures.
type LoginGate interface {
	CanAuthenticate(ctx context.Context, userID string) (bool, error)
	ResolveUser(ctx context.Context, actor rls.Principal, userID string) (DirectoryUser, error)
}

// OverrideApprover is the approvals module (P1.7). Granted reports whether an
// approval allows one user to hold the proposed roles. The default looks up a
// sod.override.<approval_id> grant and otherwise denies the override.
type OverrideApprover interface {
	Granted(ctx context.Context, approvalID, userID string, roles []string) (bool, error)
}

// MatrixApprover is the approvals module (P1.7). Submit records that a sensitive
// master mutation needs a decision. The matrix row stays pending either way.
type MatrixApprover interface {
	Submit(ctx context.Context, companyID uuid.UUID, kind string, payload []byte) (requestID string, err error)
}

type pendingMatrix struct{}

// Submit returns a local request id. The matrix row stays pending.
func (pendingMatrix) Submit(context.Context, uuid.UUID, string, []byte) (string, error) {
	return uuid.NewString(), nil
}
