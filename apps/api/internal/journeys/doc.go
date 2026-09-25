// Package journeys is the journey engine (P1.14).
//
// Definitions are data: a slug, the personas who may see them, and ordered steps
// with a kind, an input schema, and a guard. Instances persist every transition.
// The engine keeps no in-memory instance state, so a new process resumes from
// the database after a restart or after days.
//
// Client input never supplies permissions, roles, personas, or workflow state.
// Those are re-read from the interfaces in ports.go on every step. Approvals,
// identity, and authorization stay behind those interfaces; this package does
// not import them.
package journeys
