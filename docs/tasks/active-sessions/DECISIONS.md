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
