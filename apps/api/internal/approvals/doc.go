// Package approvals is the approval engine (P1.7, R2.1 to R2.11, tests D1 to D14).
//
// Document modules call Service. Identity and posting stay behind StepUpVerifier
// and PostingGate so this package does not import them. HTTP covers the OpenAPI
// routes inbox, get, approve and reject. Delegate and snooze stay on the service
// until a contracts-only PR adds those paths.
package approvals
