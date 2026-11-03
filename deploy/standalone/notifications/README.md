# Notifications: email and Telegram

Cabinet users choose which events reach them (`#/notifications`):

| Event | Source | Who may receive it |
| --- | --- | --- |
| `paid` | `payment.settled` | users with `payments:read` on the merchant |
| `underpaid` | `payment.partially_paid` | same |
| `review` | `payment.needs_review` | same |
| `webhook_failed` | a delivery reaching `dead_letter` (one notice per endpoint and UTC hour) | users with `webhooks:read` |
| `sign_in` | a fresh cabinet login (not step-up) | the user themself |
| `sanctioned` | a transfer to the merchant's address from a sender on a sanctions list | everyone who gets the merchant's notifications, whatever events they chose |

A self-service owner who saved nothing gets every merchant event by email.
Everybody else subscribes on the page. Access is checked when the job is
queued and again when it is sent; a user who lost access gets nothing more.

The fan-out runs in the transaction that records the event, inside a guarded
block: if queuing fails, PostgreSQL logs a warning and the payment write
commits normally. That notification is then lost, so alert on the server log
line `notification fan-out skipped` (with the event id and SQLSTATE).

## Worker

`notification-worker` (database role `merchant_notification_worker`,
function-only, no table privileges). Compose profile `notifications`, Helm
workload `notification-worker` (disabled by default). Health on `:9103`.

| Variable | Meaning |
| --- | --- |
| `DATABASE_URL` | login role that inherits `merchant_notification_worker` |
| `NOTIFICATION_WORKER_ID` | UUID; Helm uses the pod UID |
| `NOTIFICATION_CABINET_URL` | `https://` origin of the cabinet, used in links |
| `NOTIFICATION_EMAIL_RELAY_URL` / `_BEARER_FILE` / `_CA_FILE` | email relay; leave the URL empty to turn email off |
| `NOTIFICATION_TELEGRAM_BOT_TOKEN_FILE` | file with the bot token; empty turns Telegram off |
| `NOTIFICATION_TELEGRAM_API_URL` | Bot API origin, default `https://api.telegram.org` |
| `NOTIFICATION_TELEGRAM_POLL` | `false` on a replica that should only deliver |
| `NOTIFICATION_POLL_INTERVAL`, `_LEASE`, `_BASE_BACKOFF`, `_MAX_BACKOFF`, `_MAX_ATTEMPTS`, `_BATCH_SIZE` | delivery loop |

At least one channel must be configured or the worker refuses to start.
Failed sends retry with exponential backoff. A refusal (Telegram 400/403,
relay 400/422) or an unconfigured channel ends the job at once. Finished jobs
are purged after 30 days.

### Email relay contract

The same contract as the invitation relay: HTTPS with TLS 1.3, a pinned CA,
`Authorization: Bearer <file>`, `Idempotency-Key: <job id>`. The body:

```json
{"type":"ocrypt.notification","event":"paid","recipient_email":"owner@shop.example","locale":"ru","subject":"…","text":"…"}
```

Answer `2xx` with an `X-Delivery-ID` header once the message is durably
queued. The text is plain; the relay sends it as `text/plain`.

## Telegram bot

1. Create a bot with @BotFather and save the token in a file readable only
   by the worker (Compose secret `notification_telegram_token`, or a Kubernetes
   Secret mounted at the path in `NOTIFICATION_TELEGRAM_BOT_TOKEN_FILE`).
2. Set `ADMIN_TELEGRAM_BOT_USERNAME` on the admin BFF to the bot's username
   (for example `ocrypt_alerts_bot`; Compose passes it through, in Helm set
   `workloads.admin-api.env.ADMIN_TELEGRAM_BOT_USERNAME`). Without it the cabinet does not offer
   Telegram.
3. Do not set a webhook on the bot: the worker uses `getUpdates`. Only one
   replica polls at a time; the others wait on a database advisory lock.
4. Admit egress to `api.telegram.org:443` (Helm `httpsEgressPeers`).

Access: merchant events need a binding with `payments:read` (and
`webhooks:read` for webhook failures) for the merchant, its tenant or the
whole platform, or the self-service owner membership. Global operators get a
merchant's notifications only after saving preferences for it.
Changing sign-in alerts and unlinking Telegram are audited.

Linking: the cabinet issues a one-time `https://t.me/<bot>?start=<code>`
link (256-bit code, 10 minutes, only its SHA-256 is stored). The user presses
Start in a private chat; the worker links that chat and replies in the user's
language. Group chats are ignored. Linking and unlinking are audited.

Messages are plain text: merchant name, order id, amount and a cabinet
link. They never contain payer addresses, customer emails or secrets.
For a webhook failure only the endpoint host is named.

## Checks

```sql
SELECT status, channel, event, count(*) FROM notification_jobs GROUP BY 1,2,3;
SELECT last_error_code, count(*) FROM notification_jobs WHERE status='dead' GROUP BY 1;
```
