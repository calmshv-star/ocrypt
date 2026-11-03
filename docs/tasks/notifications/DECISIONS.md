# notifications decisions

Goal: Email and Telegram notifications for cabinet users: chosen payment and webhook events per merchant, and an alert for every new sign-in

## 2026-11-03 — agent decisions (owner: "do the whole specification, I am not here")

1. **Fan-out by trigger, guarded.** Queuing in the event's own transaction
   needs no new consumer and cannot miss an event; the guarded block (logged
   warning) keeps a notification problem from ever blocking a settlement.
2. **Defaults only for the self-service owner.** A small merchant should hear
   about payments without configuring anything; operators with tenant-wide
   access would otherwise be flooded, so they opt in.
3. **Two access checks.** At queue time and at send time, with the same
   definer function as the cabinet grants (`payments:read`/`webhooks:read`
   bindings or the owner projection).
4. **Telegram via `getUpdates`, not a webhook.** No inbound public endpoint
   is needed; one replica polls under an advisory lock.
5. **Plain text only.** No parse mode, control characters removed, merchant-
   controlled values bounded; nothing sensitive (payer addresses, customer
   data, endpoint paths or credentials) leaves the database.
6. **Email through the operator relay**, the same contract as invitations,
   so no SMTP credentials live in the platform.
7. **Locale**: saved settings win; before anything is saved a sign-in alert
   uses the browser's language and merchant events use English.
8. **"Sanctioned address" is deferred** to payer-address screening, which
   does not exist yet; the TZ lists it as its own item.

## Needs the owner

- A Telegram bot (token) and its username for `ADMIN_TELEGRAM_BOT_USERNAME`.
- The email relay endpoint, bearer and CA for notifications.

## 2026-11-03 — independent review findings and fixes

- **H1 (fixed).** A send could outlive its lease (Telegram client 45 s, lease
  30 s), so its failure was never recorded and the job was re-leased without
  limit until a CHECK broke every claim. Sends now run under half the lease,
  Telegram sends have their own 10 s client, the lease must be at least twice
  that, the claim ends a job after 20 attempts (`attempts_exhausted`), and
  readiness moves with every job. Live `S6` covers the cap.
- **M1 (fixed).** A returning identity signing in through the signup flow
  now gets the sign-in alert like any login.
- **M2 (fixed).** The cabinet drops events the user can no longer receive
  (webhook access or the bot gone) when saving, so the form stays savable.
- **L1 (fixed).** Compose and Helm pass `ADMIN_TELEGRAM_BOT_USERNAME`.
- **L2 (fixed).** An expired Telegram link resets to "Connect Telegram".
- **L3 (fixed).** The offset advances per handled update; a database error
  leaves the update for the next poll; a replay from the chat that already
  used the code answers "connected"; a bare `/start` explains where the link
  comes from.
- **L4 (decided).** Merchant notifications need a tenant-scoped binding or the
  owner membership; global platform operators do not get them (the cabinet
  hides the section since the database says no access). A webhooks-only role
  cannot subscribe to webhook failures without payment access. Documented in
  the runbook.
- **L5 (fixed).** Changing sign-in alerts is audited in the same transaction.
- **L6 (documented).** A swallowed fan-out error loses that notification; the
  runbook says to alert on the `notification fan-out skipped` log line.
- **L7 (fixed).** Saving any setting puts all of the user's preferences in
  the same language.
- **L8 (fixed).** The contract lists 403 on GET; the runbook no longer claims
  the network is shown; CI checks the worker's offset grant and that it has
  none of the cabinet functions. `runtime-contracts.json` keeps its layout.
