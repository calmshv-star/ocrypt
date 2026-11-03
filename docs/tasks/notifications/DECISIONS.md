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
