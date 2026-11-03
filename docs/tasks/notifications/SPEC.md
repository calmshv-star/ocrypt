# notifications specification

## Requested outcome

Email and Telegram notifications for cabinet users: chosen payment and webhook events per merchant, and an alert for every new sign-in

## Interview and scope

Source: TZ "Уведомления" (email, Telegram bot, webhooks; events paid,
underpaid, review, webhook error, sanctioned address) and "Безопасность"
(sign-in notifications). The owner asked for no questions; agent decisions
are in DECISIONS.md.

Facts found in the code:

- Merchant-facing payment events are rows in `callback_events`
  (`payment.settled`, `payment.partially_paid`, `payment.needs_review`).
  Webhook deliveries end as `callback_deliveries.status='dead_letter'`.
- Cabinet access for a merchant comes from tenant role bindings or, for a
  self-service owner, from the active owner membership (`merchant_owner`).
- Outbound email already goes through an operator HTTPS relay (invitations).
- There is no payer-address screening, so "sanctioned address" has no source
  event. It is a separate TZ item.

Behaviour:

1. Events: `paid`, `underpaid`, `review`, `webhook_failed` per merchant;
   `sign_in` per account. Channels: email (the IdP email) and Telegram (one
   linked private chat per user).
2. Fan-out happens in the event's transaction (triggers). A failure there is
   logged and never aborts the payment or delivery write.
3. Recipients: users with saved preferences who hold `payments:read`
   (`webhooks:read` for webhook failures) on the merchant now; a self-service
   owner without saved preferences gets every merchant event by email.
   Access is checked again at send time.
4. Webhook failures: at most one notice per endpoint per UTC hour; only the
   endpoint host is named.
5. Sign-in alert: queued after a fresh login (not step-up, not signup) with
   the device label; email and Telegram on by default.
6. Telegram linking by a one-time 256-bit code in a `t.me` deep link, valid
   10 minutes, stored as a hash; the worker links the private chat that sends
   `/start <code>`. Linking and unlinking are audited.
7. Worker: function-only role, leases with retries and backoff, permanent
   refusals end the job, finished jobs purged after 30 days, a single
   Telegram poller via an advisory lock.
8. Messages in the user's saved language (or the browser's for a sign-in
   alert before anything is saved), in six languages, plain text, test
   payments marked.

## Acceptance matrix

| Case | Evidence |
| --- | --- |
| Fan-out to the owner by default and to subscribers by choice; nobody without access | live `S6_notifications` |
| Webhook failure needs `webhooks:read`; once per endpoint and hour; host only | live `S6` |
| Broken fan-out never aborts the payment write | live `S6` (CHECK probe) |
| Preferences validated and access-checked in the database | live `S6`, `TestNotificationSettingsRespectPaymentAndWebhookAccess` |
| Telegram code links once, audited; unlink removes the chat | live `S6`, `TestTelegramLinkStoresOnlyTheCodeHash`, `TestLinkerLinksOnlyPrivateChatsWithAValidCode` |
| Sign-in alert only for the user's own fresh login | live `S6`, `TestSignInAlertOnlyForAFreshLogin` |
| Recipient re-checked at claim; lost access ends jobs | live `S6` |
| Retry, backoff, rate limit and permanent failures | `TestWorkerRetriesTransientAndEndsPermanentFailures` |
| Token never leaks; errors classified | `TestTelegramClientNeverLeaksTheTokenAndClassifiesErrors` |
| Relay contract | `TestEmailRelayUsesTheJobAsIdempotencyKey` |
| Messages in six languages, control characters removed, exact amounts | `TestRenderNamesTheEventInTheUsersLanguage`, `TestDecimalKeepsTheExactAmount` |
| One Telegram poller | live `S6` (advisory lock) |
| Cabinet page | `apps/admin/src/Notifications.test.tsx` |
| Role is function-only; BFF cannot claim jobs | CI role assertions, `runtime-grants.sql` |

## Out of scope

- "Sanctioned address" event (needs payer-address screening, separate TZ item).
- Daily summary and invoice creation in the bot (stage 3 Telegram bot).
- Choosing a different email address than the sign-in one.
