package authz

import "github.com/riverqueue/river"

// RoleChangedArgs is the outbox job emitted when a user's roles change.
type RoleChangedArgs struct {
	CompanyID  string   `json:"company_id"`
	UserID     string   `json:"user_id"`
	Roles      []string `json:"roles"`
	ApprovalID string   `json:"approval_id,omitempty"`
}

// Kind is the River job name identity and notifications consume.
func (RoleChangedArgs) Kind() string { return "role.changed" }

// UserDisabledArgs is the outbox job emitted when a user is disabled.
type UserDisabledArgs struct {
	CompanyID string `json:"company_id"`
	UserID    string `json:"user_id"`
}

// Kind is the River job name.
func (UserDisabledArgs) Kind() string { return "user.disabled" }

var (
	_ river.JobArgs = RoleChangedArgs{}
	_ river.JobArgs = UserDisabledArgs{}
)
