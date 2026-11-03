# active-sessions specification

## Requested outcome

Let every cabinet user see their own active sessions with a coarse device label, end any other session or all of them, and see how the current session was confirmed

## Interview and scope

Source: TZ "Безопасность" (active sessions, sign out everywhere, 2FA and
passkeys). The owner asked for no questions; agent decisions are in
DECISIONS.md.

Facts found in the code that shape the design:

- Sessions are server-side rows in `admin_sessions` (hashes of the session and
  CSRF secrets, issuer, subject, ACR/AMR, idle and absolute expiry). Sign-out
  and logout already set `revoked_at`/`revoke_reason`.
- Second factors and passkeys are owned by the OIDC identity provider (Dex or
  the operator's IdP). The cabinet sees only `acr` and `amr`; it never holds a
  factor, so it cannot enrol or remove one.

Behaviour:

1. `GET /admin/v1/session/sessions` returns the user's active sessions (not
   revoked, idle and absolute expiry in the future), newest activity first,
   at most 50, with `current` marking the calling session. `expires_at` is
   the absolute expiry, the latest moment the session can last.
2. `POST /admin/v1/session/sessions/{id}/revoke` ends one of the user's other
   sessions. The current session, a malformed id, another user's session and
   an already ended one are refused (400 / 404). Audited as
   `admin.session.revoked`.
3. `POST /admin/v1/session/sessions/revoke-others` ends every other active
   session of the user and returns the count. Audited as
   `admin.session.revoked_others` when the count is not zero.
4. At sign-in the BFF stores a device label `Browser/System` reduced from the
   User-Agent (a fixed set of names); the raw User-Agent is never stored. The
   column has a CHECK that admits only that shape, and the label is written
   once.
5. The cabinet page `#/security` (every signed-in user, any scope) shows the
   methods from `amr` (passkey, authenticator code, SMS, password,
   multi-factor, other), says that factors are managed at the identity
   provider, and lists the sessions with end buttons.

## Acceptance matrix

| Case | Evidence |
| --- | --- |
| User sees only own active sessions; current one marked | `TestUsersSeeAndEndOnlyTheirOwnSessions`, live `S5_user_sessions` |
| Ending another user's or an ended session is not found | live `S5_user_sessions` |
| The current session cannot be ended by id | `TestUsersSeeAndEndOnlyTheirOwnSessions` |
| Ending the others keeps the current session and others' sessions | live `S5_user_sessions` |
| Label keeps only browser and system; DB refuses any other shape; first label stays | `TestDeviceLabelKeepsOnlyBrowserAndSystem`, live `S5_user_sessions` |
| Audit entries for both revocations | `TestUsersSeeAndEndOnlyTheirOwnSessions` |
| Contract matches routes | `contract_drift_test.go` |
| Cabinet list, current badge, end one, end others, failure, no others | `apps/admin/src/Security.test.tsx` |

## Out of scope

- Ending the identity-provider session. A browser that is still signed in at
  the IdP can start a new cabinet session without credentials; the page says
  so and tells the user to sign out and change factors at the provider.
  Forcing `max_age`/`auth_time` freshness on every login is a separate
  decision for the operator's IdP (Dex does not enforce it).

- Enrolling or removing passkeys and second factors (identity provider).
- Notifications about a new sign-in (notifications task).
- Geo-IP or IP address display: the cabinet does not keep client IPs per
  session.
