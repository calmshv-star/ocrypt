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
