# account-lifecycle decisions

Goal: REG-5: access recovery, email change and account deletion with data export for cabinet users

## 2026-11-03 — agent decisions (owner: "do the whole specification, I am not here")

1. **The provider owns credentials.** Password and second-factor recovery,
   lockout after failed attempts and the email itself live at the identity
   provider; the cabinet links to its pages instead of re-implementing them.
2. **Follow the verified email, do not lock out.** The identity is the
   provider's issuer and subject; a verified email changed there is taken
   over at login, audited, and the old address told. Refusing the login
   (the previous behaviour) locked users out of their own merchants.
3. **Delete what is personal, keep what the law needs.** Deletion
   pseudonymises the user, memberships and sessions and deletes notification
   data; the audit log and payment records stay (retention obligations).
4. **No orphaned merchants by surprise.** A merchant the user alone owns is
   closed only with explicit consent; it keeps a pseudonymised owner record
   because the last-owner guard (rightly) forbids an ownerless merchant.
5. **Platform operators are offboarded by another operator**, so the
   platform never loses its last security administrator by self-service.
6. **Re-registration is possible.** The provider identity is freed, so the
   same person can sign up again as a new user.

## 2026-11-03 — independent review findings and fixes

1. **Medium-high (fixed).** Every request carries a fresh step-up window, so
   deletion was possible from any old open session. It now checks the
   session's stored step-up time (login or step-up) and answers
   `step_up_required` otherwise; the email is no longer shown as a hint.
2. **Medium (fixed).** A synced email skipped the signup checks; disposable
   and listed domains are now refused and audited. `ADMIN_FOLLOW_IDP_EMAIL`
   restores the strict behaviour for providers that may reuse subjects; the
   notice also goes to a linked Telegram chat.
3. **Medium (fixed).** Deletion removed the pending notice to the old
   address; notices to fixed addresses now survive, and the account's address
   is told about the deletion.
4. **Low-medium (fixed).** The notice dedupe key includes the time, and
   notices are capped at three a day.
5. **Low-medium (fixed).** Sent and dead jobs, session user-agent hashes and
   invitation enrolments are cleared too; invitation records are immutable
   and kept (documented).
6. **Low (fixed).** A co-owner counts only if their account is active.
7. **Low (fixed).** The user row is locked first (same order as the email
   sync) and serialization failures are retried.
8. **Low (fixed).** Consent names the merchant ids; a changed set is refused.
9. **Low (fixed).** The invitation and signup flows follow the email first.
10. **Low (fixed).** A signup under review cannot be deleted away.
11. **Low (fixed).** The new settings are in Compose, Helm and the env
    example.
12. **Low (fixed).** The preview has an error and retry, deletion ends on a
    confirmation page, and links are parsed as URLs.
13. **Info (fixed by 2 and 4).** Notice volume is capped per user and day.
