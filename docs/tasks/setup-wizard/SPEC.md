# setup-wizard specification

## Requested outcome

The first-run wizard of the TZ: profile, acceptance method, webhook and test payment steps, leading to live mode

## Interview and scope

Source: TZ "Мастер первого запуска" (profile; wallet; acceptance method with
step-by-step instructions and automatic API keys; acceptance rules; webhook
with a test delivery, the server's answer on screen and a signature-check
example; a test payment showing the chain; then "go live"). Wallet and
acceptance rules are done in the go-live request. The owner asked for no
questions; agent decisions are in DECISIONS.md.

Behaviour (cabinet page `#/setup`, opened from Getting started):

1. Profile: shop name, support email, language and time zone through the
   existing project-settings API (the other settings are kept).
2. Acceptance method: payment link, API, shop plugin, invoice, Telegram bot.
   The choice is a browser preference and selects the instructions. API and
   plugin create an API key in one click (payments read/write, events read),
   the secret shown once. Plugin, invoice and Telegram bot are marked "soon"
   and point to a payment link meanwhile.
3. Webhook (API and plugin only): add an endpoint with the payment events,
   show its signing secret once, send the verification request and explain a
   failure; signature-check examples with the official SDKs (Node.js, Python,
   PHP, Go).
4. Test payment: the chain link/payment → confirmed payment → acknowledged
   webhook, with the latest delivery's HTTP status and response on screen.
5. Live mode: a button to the go-live request (REG-4 conditions).

## Acceptance matrix

| Case | Evidence |
| --- | --- |
| Profile saved with unchanged fields kept | `SetupWizard.test.tsx` |
| API method creates a key and a webhook; secrets shown | `SetupWizard.test.tsx` |
| Failed verification explained; last delivery answer shown; SDK tabs | `SetupWizard.test.tsx` |
| Payment link skips the webhook; live-mode button | `SetupWizard.test.tsx` |
| Copy in six languages, no raw UI strings | i18n tests |

## Out of scope

- Logo and invoice currency in the profile (no backend fields yet).
- A one-button sandbox payment from the cabinet (the sandbox runs on the
  merchant API with API credentials).
- The exact reason of a failed verification request (the management API
  returns only "unavailable").
