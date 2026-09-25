# Certificate or tunnel failure

Symptoms: phones show "connection is not private", the console will not load, pushes still arrive (they are outbound), Caddy logs `obtaining certificate` errors, or the Cloudflare/Tailscale tunnel shows disconnected. The product itself is fine; only the way in is broken. Operators still reach everything over Tailscale.

## Which path are we on?

- Public hostname with inbound 80/443: Caddy does ACME itself (`Caddyfile.prod`). Failures are DNS, port forwarding, or rate limits.
- No inbound port: Cloudflare Tunnel or Tailscale Funnel terminates TLS; Caddy runs with `tls internal` behind it. Failures are the tunnel daemon or its credentials.

## Caddy / ACME

1. `docker compose --profile prod logs --since 1h caddy | grep -i -E "error|obtain|renew"`.
2. `dig +short $ERP_PUBLIC_HOSTNAME` must return the office public IP; check the router still forwards 80 and 443 to the NUC (ISP changes and router resets are the usual cause).
3. `curl -sv https://$ERP_PUBLIC_HOSTNAME/healthz 2>&1 | grep -E "expire|issuer"` shows the certificate in use. Let's Encrypt renews at 30 days remaining; a certificate under 7 days means renewal has been failing for weeks: check the status page alert threshold is on.
4. Rate-limited (`too many certificates`): wait, or switch issuer temporarily with `acme_ca https://acme.zerossl.com/v2/DV90` in the global block and `systemctl reload smart-erp.service`.
5. Emergency access while fixing: the api port is bound to loopback in prod, so `tailscale serve https / http://127.0.0.1:8080` on the NUC gives operators a trusted URL on the tailnet in one command.

## Tunnel

1. `systemctl status cloudflared` or `tailscale funnel status`; restart once. Check the token or credentials file has not expired (Cloudflare tunnel tokens do not expire but the tunnel can be deleted in the dashboard).
2. If the tunnel provider is down, phones cannot reach the API from outside; on the LAN the console still works through Caddy directly. Tell the Stakeholders the expected recovery and that approvals from outside will queue.
3. Do not open inbound ports on the router as a shortcut. If the tunnel is dead for more than a day, switch to the public-hostname path properly (DNS, forwarding, `Caddyfile.prod`) and review it with the Stakeholders.

## After

1. Confirm a phone outside the office can log in and receive a push.
2. Record the outage window and root cause in the console; a certificate expiry is a process failure and goes on the quarterly rehearsal checklist.
