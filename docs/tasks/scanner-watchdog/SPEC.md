# scanner-watchdog specification

## Requested outcome

Alert the operator within minutes when a scanner stops being ready or falls behind

## Interview and scope

Source: product specification stage 1, NFR-2/NFR-3. In October 2026 the
production BSC scanner stalled from 2026-10-20 and the TON scanner from
2026-10-21; both were found by hand after roughly a day. The standalone host
runs plain containers without Prometheus or Alertmanager.

Scope: `scripts/scanner_watchdog.py`, a dependency-free Python 3.10+ script run
by a systemd timer (or `--interval` loop). For each configured scanner it reads
only `/readyz` and the `merchant_scanner_head_lag_blocks` gauge from
`/metrics`. A scanner is bad when it is unreachable, not ready, or behind more
than its `max_lag_blocks`. After `alert_after_seconds` (default 300) of
continuous bad state it sends one alert (Telegram bot and/or a JSON webhook,
secrets read from files), repeats every `repeat_after_seconds` (default 3600)
while bad, and sends a recovery message. A failed notification is retried on
the next run. It writes a secret-free `status.json` for a status page.

Non-goals: database access, restarting or changing scanners, provider
changes, quorum or interval changes, deploying to production.

## Acceptance matrix

| ID | Behavior/input/fault | Expected result | Test or manual check | Evidence |
| --- | --- | --- | --- | --- |
| W1 | Metrics text | Lag parsed from `merchant_scanner_head_lag_blocks` only | `test_parse_lag_reads_only_the_scanner_lag_gauge` || tests pass incl. non-finite values |
| W2 | Ready / 503 / lagging / connection error | Classified ready, not ready, behind, unreachable | `test_probe_classifies_*`, `test_real_http_probe_against_a_stalled_scanner` || tests pass incl. http.client errors and a real HTTP stalled scanner |
| W3 | Continuous bad state | No alert before 300 s, one alert at 300 s, repeat after 3600 s, recovery message, state reset | `test_alert_after_threshold_*`, `test_short_blip_*` || tests pass incl. run_once repeat/recovery, clock skew, unobserved gaps, removed targets |
| W4 | Telegram and webhook delivery | Exact Bot API URL and payload; webhook gets `{"text": ...}`; failed sends retried next run | `test_run_once_*`, `test_failed_notification_*`, `test_webhook_*` || tests pass: exact payload, per-channel outboxes, one failing channel neither spams nor silences the other, redirect not counted |
| W5 | Secrets | Token never in stdout, stderr, state or status files | `test_run_once_alerts_through_telegram_and_writes_status` || tests pass on success and failure paths with real InvalidURL messages |
| W6 | Invalid config | No targets, non-HTTP URL, no notifier, duplicate names rejected | `test_invalid_configuration_is_rejected` || tests pass; each invalid case now has a notifier so it tests its own rule |

For financial changes include relevant exact-money, negative, duplicate/replay,
concurrency, tenant, finality, and external-delivery cases.

## Runtime prerequisites

Identify required local services and external evidence separately. Record which
checks are offline/static and which require a live test environment.
