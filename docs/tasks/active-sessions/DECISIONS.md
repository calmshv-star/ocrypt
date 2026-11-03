# active-sessions decisions

Goal: Let every cabinet user see their own active sessions with a coarse device label, end any other session or all of them, and see how the current session was confirmed

## 2026-11-03 — agent decisions (owner: "do the whole specification, I am not here")

1. **No raw User-Agent, no IP.** The label is a fixed browser name and a
   fixed system name; the database admits only that shape. That is enough to
   recognise a device and stores nothing that identifies a person further.
2. **The current session ends by signing out**, not from the list, so a
   user cannot cut the request they are making and the logout path (cookie
   clearing) stays the only way to end it.
3. **No step-up for ending sessions.** Ending sessions only reduces access,
   so it needs the ordinary session and CSRF token, like logout.
4. **Factors stay at the identity provider.** The page names the methods from
   `amr` and says where to manage them; the cabinet never offers a fake
   "enable 2FA" switch.
5. **Another user's session is "not found"**, not "forbidden", so the
   endpoint does not confirm that an id exists.

## 2026-11-03 — independent review findings and fixes

- **M1 (fixed).** The revocations matched every unrevoked row, so expired
  history and invitation enrolments were rewritten and counted. Both now use
  the list's own filter (`purpose='admin'`, unrevoked, idle and absolute
  expiry in the future); an expired id is "not found". Live `S5` adds an
  expired session.
- **M2 (documented).** Ending a cabinet session does not end the IdP
  session. The page now says so and points to signing out and changing
  factors at the provider; SPEC lists IdP freshness enforcement out of scope.
- **L1 (fixed).** Revocation and its audit entry commit in one transaction
  (the count goes into the entry for "revoke others"); live `S5` checks one
  entry per revocation.
- **L2 (fixed).** The contract lists 401 and 403 (origin/CSRF) responses.
- **L3 (fixed).** Each end button names its device for screen readers.
- **L4 (fixed).** "Ends by" shows the absolute expiry, a true upper bound.
- **Info (fixed).** A 404 (already ended elsewhere) reads as ended; iOS
  Chrome/Firefox/Edge and ChromeOS are labelled; the revoke-others audit is
  asserted exactly. A User-Agent is client-controlled, so the label is a
  hint, never evidence.
