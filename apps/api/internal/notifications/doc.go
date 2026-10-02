// Package notifications consumes the outbox and delivers events to WebSocket
// subscribers, FCM HTTP v1, APNs, and email (R13, ADR-04, ADR-07).
//
// Identity and approvals stay behind Directory, MasterApproval, and Approvals.
// This package does not import another module.
package notifications
