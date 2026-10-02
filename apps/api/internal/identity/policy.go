package identity

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// Sentinel errors from a factor broker. Handlers map every one of them onto
// the same wire failure (R1.7); the audit row keeps the real cause.
var (
	ErrUnknown     = errors.New("unknown user")
	ErrFactor      = errors.New("factor rejected")
	ErrLocked      = errors.New("account locked")
	ErrUnavailable = errors.New("broker unavailable")
)

// Factors is what Zitadel's session reports after a check.
type Factors struct {
	UserID      string
	LoginName   string
	DisplayName string
	Password    bool
	TOTP        bool
	WebAuthN    bool
}

// Broker is Zitadel's Session API as this module uses it. The gRPC client and
// tests both implement it; the service never talks to Zitadel directly.
type Broker interface {
	Create(ctx context.Context, loginName string) (sessionID, sessionToken string, err error)
	Password(ctx context.Context, sessionID, password string) (Factors, error)
	TOTP(ctx context.Context, sessionID, code string) (Factors, error)
	WebAuthN(ctx context.Context, sessionID string, assertion map[string]any) (Factors, error)
}

// Account is the ERP directory record that rides on a verified Zitadel user.
type Account struct {
	ID            string
	LoginName     string
	Name          string
	CompanyID     uuid.UUID
	Roles         []string
	Personas      []string
	Disabled      bool
	StepUpMethods []string
}

// Directory resolves ERP users. Zitadel does not own roles (that is A2); identity
// reads them through this interface so it never imports internal/authz.
type Directory interface {
	ByLogin(ctx context.Context, loginName string) (Account, error)
	ByID(ctx context.Context, id string) (Account, error)
}

// PushUnregistrar drops a device push token on logout. Notifications owns the
// implementation; a nil registrar is a no-op.
type PushUnregistrar interface {
	Unregister(ctx context.Context, userID, deviceID string) error
}

// Policy is the login and token configuration. AccessTTL is capped at 15 minutes (R1.5).
type Policy struct {
	AccessTTL          time.Duration
	RefreshSliding     time.Duration
	RefreshAbsolute    time.Duration
	ConsoleIdle        time.Duration
	MaxSessions        int
	FailuresBeforeLock int
	LockFor            time.Duration
	RatePerLogin       int
	RatePerIP          int
	RateWindow         time.Duration
	LoginTTL           time.Duration
	DPoPSkew           time.Duration
}

// DefaultPolicy matches docs/spec/05-security-and-audit.md.
func DefaultPolicy() Policy {
	return Policy{
		AccessTTL:          15 * time.Minute,
		RefreshSliding:     30 * 24 * time.Hour,
		RefreshAbsolute:    90 * 24 * time.Hour,
		ConsoleIdle:        8 * time.Hour,
		MaxSessions:        5,
		FailuresBeforeLock: 5,
		LockFor:            15 * time.Minute,
		RatePerLogin:       30,
		RatePerIP:          60,
		RateWindow:         time.Minute,
		LoginTTL:           10 * time.Minute,
		DPoPSkew:           5 * time.Minute,
	}
}

func (p Policy) accessTTL() time.Duration {
	if p.AccessTTL <= 0 || p.AccessTTL > 15*time.Minute {
		return 15 * time.Minute
	}
	return p.AccessTTL
}

// stepUpLifetime is fixed by A6: single-use and two minutes.
const stepUpLifetime = 2 * time.Minute

// platformCompanyID is the chain company for failures that have no tenant yet.
var platformCompanyID = uuid.MustParse("00000000-0000-4000-8000-000000000001")

func mfaRequired(roles []string) bool {
	for _, role := range roles {
		switch role {
		case "Approver", "Stakeholder", "Accountant", "System Manager":
			return true
		}
	}
	return false
}

func isSystemManager(roles []string) bool {
	for _, role := range roles {
		if role == "System Manager" {
			return true
		}
	}
	return false
}

// sqlPrincipal drops role names the kit refuses to put in a session variable
// (spaces are part of the spec names, and kit/rls rejects them). Identity does
// not rely on those SQL roles; callers still see the real names on the token.
func sqlPrincipal(p rls.Principal) rls.Principal {
	kept := make([]string, 0, len(p.Roles))
	for _, role := range p.Roles {
		if strings.ContainsAny(role, ", '\"") {
			continue
		}
		kept = append(kept, role)
	}
	p.Roles = kept
	return p
}
