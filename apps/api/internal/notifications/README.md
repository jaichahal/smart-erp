# notifications

Outbox consumers, WebSocket hub, FCM HTTP v1 and APNs senders, preferences, and alert rules (P1.8, R13.1 to R13.7).

Identity is read through `Directory`. Approvals are reached through `Approvals` and `MasterApproval`. Neither module is imported.

`cmd/api` and `cmd/worker` are owned by Track A. Call `Mount` and `RegisterWorkers` from those binaries when they are wired.
