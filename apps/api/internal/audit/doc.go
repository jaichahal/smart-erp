// Package audit anchors the per-company hash chain, verifies it against those
// anchors, and runs backup and restore (P1.6, P1.15).
//
// The hourly anchor is {company_id, chain_seq, head_hash, row_count, at},
// signed by an off-prem key and written to the on-prem bucket and the off-site
// compliance-mode bucket. In dev the off-site bucket is offsite-sim and the
// key is a local simulation; Sign is still denied to the database operator.
// Anchors and verification runs are immutable. Backup logic lives here so
// deploy/scripts can stay thin wrappers.
package audit
