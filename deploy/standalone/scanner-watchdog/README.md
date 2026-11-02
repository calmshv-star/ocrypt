# Scanner watchdog (standalone host)

`scripts/scanner_watchdog.py` alerts the operator when a scanner stays unready,
unreachable or too far behind for five minutes, repeats hourly while it stays
bad, and reports recovery. It reads only each scanner's `/readyz` and
`merchant_scanner_head_lag_blocks`; it never touches the database, providers,
cursors or containers. It also writes a secret-free `status.json` for a status
page.

## Configuration

Copy `config.example.json` and set:

- `targets`: one entry per scanner container, addressed by its name on the
  `ocrypt-data` network (container IPs change when a container is recreated).
  Optional `max_lag_blocks` also alerts while a scanner is ready but behind.
- `telegram`: a bot token file (mode `0600`) and the chat ID to notify, and/or
  `webhook_url_file`: a file holding an HTTPS URL that accepts `{"text": ...}`.
- `alert_after_seconds` (default 300) and `repeat_after_seconds` (default 3600).

## Run

The watchdog runs as its own container on the scanner network, so scanner names
resolve and nothing is exposed:

```sh
docker run -d --name ocrypt-scanner-watchdog --restart unless-stopped \
  --network ocrypt-data --read-only --cap-drop ALL \
  --security-opt no-new-privileges:true --user 65532:65532 \
  --memory 64m --cpus 0.1 --pids-limit 16 \
  -v /opt/ocrypt/scanner-watchdog/scanner_watchdog.py:/app/scanner_watchdog.py:ro \
  -v /opt/ocrypt/scanner-watchdog/config.json:/etc/ocrypt/watchdog.json:ro \
  -v /opt/ocrypt/secrets/watchdog/telegram-bot-token:/run/secrets/telegram-bot-token:ro \
  -v ocrypt-scanner-watchdog-state:/var/lib/ocrypt-scanner-watchdog \
  python:3.12-alpine python /app/scanner_watchdog.py \
  --config /etc/ocrypt/watchdog.json --interval 60
```

The named volume must be writable by UID 65532. Check delivery once with a
deliberately wrong target name pointing at a stopped container, then remove it.
