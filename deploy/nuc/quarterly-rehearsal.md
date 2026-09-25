# Quarterly rehearsal checklist

Every quarter the System Manager and one Stakeholder spend half a day proving that the single-NUC model still holds (ADR-16). Everything below produces a signed report or an audit event; the quarter is not closed until the checklist is filed in the console under Admin, Operations, Rehearsals.

Weekly drills (`smart-erp-drill.timer`) and monthly off-site drills already run; this rehearsal is the one time a quarter the whole chain of custody is exercised by people, on purpose, with the clock running.

## Before the day

- [ ] Last four weekly drill reports present and green (`ls /var/lib/smart-erp/reports/drill-*.json`).
- [ ] Both drives accounted for: custodian names match `backup-drives.yaml`, hand-over events in the console for the quarter.
- [ ] Spare NUC boots and has run `bootstrap.sh` this quarter (`spare-nuc-swap.md`).
- [ ] Off-site bucket and KMS credentials not expiring within 90 days.

## On the day

1. Drive check: `just drive-check` on the production NUC, then swap drives with the custodian and record the hand-over. Both drives must show allow-listed yes, encrypted yes.
2. Restore from drive onto the spare: `just drill --target drive` on the spare with the incoming drive. Note the elapsed time.
3. Restore from off-site onto the spare: `just drill --target offsite`. The two chain heads must be identical and must match the latest anchor (I17, I34). If they differ, stop the rehearsal and open `anchor-failure.md`.
4. Production restore rehearsal, dry: obtain a real Stakeholder approval token and run `just restore --target offsite --backup <id> --approval <token>` on the spare (the spare is not production, so this is safe, but the approval path is the real one). Confirm it refuses without the token first.
5. UPS: unplug mains with the Stakeholder watching; confirm the status page turns amber, note battery runtime at five minutes, plug back in. Full-drain test once a year.
6. Update rollback: on the spare, pin the previous image digests and `systemctl reload smart-erp.service`; confirm the old version starts against the current schema (`updates.md`).
7. Tunnel or certificate: check expiry dates and that a phone outside the office logs in (`cert-or-tunnel-failure.md`).
8. Disk headroom: `df -h` on both machines; anything over 70 percent gets a procurement line.
9. `just backup-now` on production, then `just anchor-now`; both green closes the day.

## File it

- [ ] Signed drill reports from steps 2 to 4 stored off-site and linked in the console.
- [ ] Measured times: drive restore, off-site restore, UPS runtime, spare swap estimate.
- [ ] Findings and owners recorded; anything red is an issue with a due date before the next quarter.
- [ ] Stakeholder signs the rehearsal record with the anchor hash of the day.
