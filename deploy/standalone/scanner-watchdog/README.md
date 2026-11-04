# Scanner watchdog (standalone host)

`scripts/scanner_watchdog.py` alerts the operator when a scanner stays unready,
unreachable or too far behind for five minutes, repeats hourly while it stays
bad, and reports recovery. It also alerts while internal transfer records wait
for an operator. It reads only each scanner's `/readyz`,
`merchant_scanner_head_lag_blocks` and `merchant_scanner_unattributed_open`; it
never touches the database, providers,
cursors or containers. It also writes a secret-free, world-readable
`status.json` for a status page. Each channel (Telegram, webhook) keeps its own
outbox, so a failing channel is retried without silencing or spamming the
other.

## Configuration

Copy `config.example.json` and set:

- `targets`: one entry per scanner container, addressed by its name on the
  `ocrypt-data` network (container IPs change when a container is recreated).
  Optional `max_lag_blocks` also alerts while a scanner is ready but behind;
  with a limit set, an unreadable lag counts as a problem.
- `telegram`: a bot token file and a numeric chat ID (or `@channel`), and/or
  `webhook_url_file`: a file holding one `https://` URL that accepts
  `{"text": ...}`. Redirects are treated as failures.
- `alert_after_seconds` (default 300) and `repeat_after_seconds` (default 3600).

The watchdog refuses to start (exit 2) when the configuration or a secret file
is malformed or the state directory is not writable, without echoing secrets.

## Run

The watchdog runs as UID 65532, so prepare the state directory and secret
ownership first:

```sh
install -d -o 65532 -g 65532 -m 0750 /opt/ocrypt/scanner-watchdog/state
install -o 65532 -g 65532 -m 0400 /path/to/telegram-bot-token /opt/ocrypt/secrets/watchdog/telegram-bot-token
install -o root -g root -m 0444 scripts/scanner_watchdog.py /opt/ocrypt/scanner-watchdog/scanner_watchdog.py
install -o root -g root -m 0444 config.json /opt/ocrypt/scanner-watchdog/config.json
```

Then run it as its own container on the scanner network, so scanner names
resolve and nothing is exposed:

```sh
docker run -d --name ocrypt-scanner-watchdog --restart unless-stopped --init \
  --network ocrypt-data --read-only --cap-drop ALL \
  --security-opt no-new-privileges:true --user 65532:65532 \
  --memory 64m --cpus 0.1 --pids-limit 16 \
  --log-opt max-size=10m --log-opt max-file=3 \
  -v /opt/ocrypt/scanner-watchdog/scanner_watchdog.py:/app/scanner_watchdog.py:ro \
  -v /opt/ocrypt/scanner-watchdog/config.json:/etc/ocrypt/watchdog.json:ro \
  -v /opt/ocrypt/secrets/watchdog/telegram-bot-token:/run/secrets/telegram-bot-token:ro \
  -v /opt/ocrypt/scanner-watchdog/state:/var/lib/ocrypt-scanner-watchdog \
  python:3.12.16-alpine3.24 python -B /app/scanner_watchdog.py \
  --config /etc/ocrypt/watchdog.json --interval 60
```

Every tick logs one line (`N target(s), M bad, K message(s) pending`), so a
silent log means the watchdog itself has stopped. To test delivery, point one
extra target at a stopped container with `alert_after_seconds: 0`, wait for
the message, then remove it (a "no longer monitored" note follows).
