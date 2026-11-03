# self-service-signup decisions

Goal: Let a merchant register, verify email and MFA, and land in their own test-mode cabinet without an invitation

For each material decision record date, accepted choice, reason, evidence,
and effect on scope or acceptance criteria. Separate user requirements from
agent assumptions. Amend the specification when accepted behavior changes.

## 2026-11-03 — independent review of the integrated diff

An independent reviewer read `ea10f0e..2609507` (backend, migration, BFF, UI).
Accepted findings and what changed:

1. **High — `merchant_owner` outlived the owner membership.** The role was a
   stored `admin_role_bindings` row, so a founder removed by a co-owner, or a
   disabled merchant or tenant, kept wallet, API-key and webhook rights.
   Decision: `merchant_owner` is never stored. A CHECK constraint refuses such
   a binding, `FindIdentity` ignores the role in stored bindings, and the new
   definer function `list_current_admin_self_service_owner_grants()` projects
   it from every active `owner` membership of an active self-service merchant
   (one with a `merchant_signups` row) in an active tenant. Co-owners added
   through the team flow get the same rights; merchants created by invitation
   or bootstrap are unaffected. Evidence: live `S1` disables the merchant,
   adds a co-owner and revokes the founder.
2. **Medium — receiving addresses could change without fresh MFA.** Every
   wallet write (replace, import challenge, import) now requires a valid
   step-up, for operators too. Evidence: `TestMerchantOwnerManagesOnlyItsOwnWallets`.
3. **Medium — password-only mode could satisfy the signup MFA check.** The
   combination is refused by `loadAdminConfig` and `NewService`.
4. **Medium — a 5-minute window to create an IdP account, verify email and
   enroll MFA.** Signup has its own TTL (`ADMIN_SIGNUP_TTL`, default 30m,
   5m..1h); the login-attempt expiry constraint allows one hour for the
   `signup` purpose only.
5. **Low — anonymous rows were never purged and options hit the database on
   every call.** `create_admin_signup_request` deletes up to 100 expired,
   unconsumed requests (and their attempts, by cascade) per call; options are
   cached for one minute. The shared per-address login limiter is unchanged;
   production ingress must still enforce a cluster-wide limit.
6. **Low — outages read as a rejected form.** Only `ErrInvalid` maps to 422
   `signup_rejected`; anything else is 503 `signup_unavailable`.
7. **Low — returning identities were not audited.** A signup by an existing
   identity with an active membership writes
   `admin.self_service.returning_identity`. Invited or memberless identities
   still cannot self-register: they finish their invitation or ask an owner.
8. **Low — control, format and bidi characters in company names.** Rejected
   in Go (Unicode Cc/Cf) and in the screening function.
9. **UI.** "Switch to live mode" reads "Ready", not "Done", because there is
   no live switch yet. Onboarding and its overview banner are for
   `wallets:read` holders only. The country list is the full ISO 3166-1 list
   plus XK, and the server's blocked countries come with the options so the
   form explains the refusal before submitting (ТЗ REG-2). The paid-intent
   check still reads the first page of 50 intents; that is enough for a new
   test-mode merchant.
10. **Nit — unused `SELECT` on `signup_blocked_countries`.** It is now used by
    the options endpoint.

Not changed: a failed OIDC callback still answers with a JSON problem for
every purpose (login, step-up, invitation, signup). A friendly landing page is
general cabinet work and is tracked for the cabinet stage.

## 2026-11-03 — disposable mailboxes (REG-5)

`signup_blocked_email_domains` lists widely known disposable mailbox
providers; a listed domain also blocks its subdomains. The check runs in
`register_self_service_merchant` after OIDC, so such an identity gets no
account and the signup request stays unconsumed. The seed is a hand-written
list of well-known providers, not a copied third-party dataset; operators add
domains with plain SQL. The registrant currently sees the generic callback
failure (see the callback limitation above).
