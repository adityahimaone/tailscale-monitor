# Tailscale Monitor (`tailmon`)

Lightweight Tailscale fleet monitor built with **Go standard library only** and a single embedded static `index.html` (vanilla JS + CSS, zero npm/bundlers/external dependencies).

It polls local `tailscale status --json` every 60 seconds, probes online Linux/macOS nodes in parallel (5s timeout per node) for CPU, RAM, disk, OS version, load average, and uptime, caches the latest snapshot, and serves both the dashboard (`/`) and JSON API (`GET /api/devices.json`).

---

## Build

Requires Go 1.24+ (no external packages).

### Local build (macOS or Linux)
```sh
go build -o tailmon .
```

### Cross-compile on Mac for Linux VPS (`amd64` / `arm64`)
```sh
GOOS=linux GOARCH=amd64 go build -o tailmon .
# or for ARM64 VPS:
# GOOS=linux GOARCH=arm64 go build -o tailmon .
```

---

## Configuration (Environment Variables)

| Variable | Default | Description |
|---|---|---|
| `TAILMON_ADDR` | `127.0.0.1:8791` | HTTP listen address |
| `TAILMON_PASSWORD` | _(empty)_ | Shared dashboard password. When non-empty, enables auth gate on all routes |
| `TAILMON_SSH_MAC` | `mac-tailscale` | SSH target/alias for Mac node |
| `TAILMON_SSH_TOKYO` | `ssh -p 25528 root@jp-tyo01.tryaltare.ai` | SSH target/command for Tokyo container node |
| `TAILMON_SSH_VPS` | _(empty)_ | Optional SSH target for VPS if collector runs off-VPS |
| `TAILMON_INTERVAL` | `60s` | Background poll interval |
| `TAILMON_SSH_TIMEOUT` | `5s` | Max timeout per node for spec probes |
| `TAILMON_TAILSCALE_BIN` | _(auto-detected)_ | Explicit path to `tailscale` CLI |

> **Never hardcode `TAILMON_PASSWORD` or commit `.env` files containing secrets.**

---

## Password Gate (`TAILMON_PASSWORD`)

When `TAILMON_PASSWORD` is non-empty, every route requires authentication using one of two consistent methods:

1. **Browser session cookie (`POST /login` -> `tailmon_auth`):**
   - Unauthenticated `GET /` returns `401` with a dark login form.
   - Submitting the password sets an `HttpOnly`, `SameSite=Lax` (`Secure` over HTTPS) cookie derived via HMAC-SHA256 of the password.
   - `POST /logout` clears the cookie.
2. **Bearer header (for `curl` / scripts):**
   - Pass `Authorization: Bearer <TAILMON_PASSWORD>` on `GET /`, `GET /api/devices.json`, or `POST /api/refresh`.
   - Unauthenticated `/api/*` requests return `401 Unauthorized` JSON (`{"error":"unauthorized"}`).

Example:
```sh
curl -H "Authorization: Bearer YOUR_PASSWORD" http://127.0.0.1:8791/api/devices.json
```

---

## Running on VPS (`systemd`)

1. Copy the compiled Linux binary to `/usr/local/bin/tailmon` (`chmod +x /usr/local/bin/tailmon`).
2. Create `/etc/tailmon.env` with restricted permissions (`chmod 600`):
   ```ini
   TAILMON_ADDR=127.0.0.1:8791
   TAILMON_PASSWORD=replace-with-strong-random-password
   TAILMON_SSH_MAC=mac-tailscale
   TAILMON_SSH_TOKYO=ssh -p 25528 root@jp-tyo01.tryaltare.ai
   ```
3. Create `/etc/systemd/system/tailmon.service`:
   ```ini
   [Unit]
   Description=Tailscale Monitor
   After=network-online.target tailscaled.service
   Wants=network-online.target

   [Service]
   Type=simple
   User=root
   EnvironmentFile=/etc/tailmon.env
   ExecStart=/usr/local/bin/tailmon
   Restart=always
   RestartSec=5

   [Install]
   WantedBy=multi-user.target
   ```
   *(If running as a non-root user, ensure that user can execute `tailscale status --json` and has access to `~/.ssh/config` + SSH keys for `mac-tailscale` and Tokyo.)*
4. Enable and start:
   ```sh
   sudo systemctl daemon-reload
   sudo systemctl enable --now tailmon
   sudo systemctl status tailmon
   ```

---

## Nginx Reverse Proxy (`devices.adityahimaone.space`)

Create `/etc/nginx/sites-available/devices.adityahimaone.space`:

```nginx
server {
    listen 80;
    listen [::]:80;
    server_name devices.adityahimaone.space;

    location / {
        proxy_pass http://127.0.0.1:8791;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

Enable the site and issue TLS via Certbot:

```sh
sudo ln -s /etc/nginx/sites-available/devices.adityahimaone.space /etc/nginx/sites-enabled/
sudo nginx -t && sudo systemctl reload nginx
sudo certbot --nginx -d devices.adityahimaone.space
```

---

## Remaining Deployment Checklist

Before considering production live, complete these steps on DNS & VPS:

1. Point DNS `A` (and `AAAA` if applicable) record for `devices.adityahimaone.space` to the VPS IP.
2. Cross-compile `tailmon` for Linux (`GOOS=linux GOARCH=amd64 go build -o tailmon .`) and upload it to the VPS.
3. Create `/etc/tailmon.env` (`chmod 600`) with a strong `TAILMON_PASSWORD`.
4. Verify SSH non-interactive (`BatchMode=yes`) connectivity from the VPS user running `tailmon` to both `mac-tailscale` and `ssh -p 25528 root@jp-tyo01.tryaltare.ai`.
5. Start `tailmon.service`, configure the nginx site above, and run `certbot --nginx -d devices.adityahimaone.space`.
6. Open `https://devices.adityahimaone.space`, unlock with `TAILMON_PASSWORD`, and confirm all 9 devices and specs render cleanly.
