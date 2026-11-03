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
