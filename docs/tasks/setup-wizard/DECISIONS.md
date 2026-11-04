# setup-wizard decisions

Goal: The first-run wizard of the TZ: profile, acceptance method, webhook and test payment steps, leading to live mode

## 2026-11-03 — agent decisions (owner: "do the whole specification, I am not here")

1. **Front end only, on the existing APIs.** Every step calls the same
   endpoints as its full page, so permissions, step-up and audit stay as
   they are; no new server surface.
2. **The method choice is a browser preference.** It only selects the
   instructions; nothing on the server depends on it.
3. **Honest "soon" labels.** Plugins, invoices and the Telegram bot are
   stage 3; the wizard says so and offers a payment link instead of a dead
   end.
4. **Profile reason is fixed** ("Profile set in the setup wizard") so the
   audit log still records why the settings changed.
5. **SDK examples, not raw HMAC code**, so merchants use the reviewed
   verification (digest, timestamp tolerance, key rotation).

## 2026-11-03 — after the independent review

6. **Self-service owners pass the management actor check (000066).** The
   database refused every payment link, API client and management audit row
   written by a `merchant_owner`, because that role is projected from an
   owner membership and never stored as a binding. The check now accepts the
   same projection as `list_current_admin_self_service_owner_grants`, for
   the owner's own permissions only; a disabled merchant or a revoked owner
   fails it again. The live S1 test proves both directions.
7. **A cabinet-created key carries its creator's API scopes.** The admin
   proxy used to sign every assertion with one scope, so the management
   service refused any API scope in a new key. For `api_clients:write` the
   proxy now adds the API scopes behind the cabinet permissions the member
   holds in that merchant. `payments:write` follows `checkout:write` (both
   create payments); `events:read` follows `webhooks:read`. Other calls keep
   their single fixed scope; the assertion stays bound to one request.
8. **Overpaid is paid.** An overpayment settles as `payment.overpaid` without
   `payment.settled`, so the webhook subscribes to it and every example
   fulfils on either.
9. **Examples answer the verification challenge first** and return 401 on a
   bad signature, so a copied handler passes "Send test request". The Go
   example and the Go SDK refuse an empty secret, like the other SDKs.
10. **Errors are named by their cause.** Any 403 from these calls offers a
    fresh MFA sign-in with a note about the role (the management API reports
    a lapsed step-up as `permission_denied`). Only 503 from the verify call
    blames the merchant's server.
11. **Honest copy.** The profile is the team's view (checkout shows the name
    given at registration); sandbox payments stay in the sandbox and are not
    shown in the chain; "Request live mode" because it goes to review.
12. **New merchants have no settings row.** The profile step starts a new one
    (version 1) with the browser's locale and time zone; time zones come from
    the browser's list and the name is checked in bytes, as the server does.

