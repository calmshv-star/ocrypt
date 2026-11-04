import { AdminAPIError } from "./api/client";
import type { MerchantProjectSettings } from "./api/types";

export type AcceptanceMethod = "link" | "api" | "plugin" | "invoice" | "telegram";
export const ACCEPTANCE_METHODS: AcceptanceMethod[] = ["link", "api", "plugin", "invoice", "telegram"];
// Methods that send payment events to the merchant's server.
export const METHODS_WITH_WEBHOOK = new Set<AcceptanceMethod>(["api", "plugin"]);
// Methods that are not built yet; the wizard points to a payment link.
export const METHODS_COMING = new Set<AcceptanceMethod>(["plugin", "invoice", "telegram"]);
// An overpayment settles as payment.overpaid without a payment.settled first,
// so both mean "paid".
export const WIZARD_WEBHOOK_EVENTS = ["payment.settled", "payment.overpaid", "payment.partially_paid", "payment.needs_review"];
export const WIZARD_KEY_SCOPES = ["payments:read", "payments:write", "events:read"];
export const WIZARD_PROFILE_REASON = "Profile set in the setup wizard";
export type SnippetLanguage = "node" | "python" | "php" | "go";
export const SNIPPET_LANGUAGES: SnippetLanguage[] = ["node", "python", "php", "go"];
export type WizardAction = () => Promise<unknown>;
export type Outcome = "saved" | "failed" | "stepUp" | "verifyFailed" | null;
export const URL_PLACEHOLDER = "https://";
export function httpStatusLabel(status?: number): string | undefined {
  return status ? `HTTP ${status}` : undefined;
}

// The server limits the shop name to 120 bytes of UTF-8, not 120 characters.
export const NAME_MAX_BYTES = 120;
export const nameFits = (value: string) => new TextEncoder().encode(value.trim()).length <= NAME_MAX_BYTES;

// A merchant has no project settings until they are first saved; the first
// save sends version 1 and creates them.
export function newProfile(locale: string): MerchantProjectSettings {
  const supported: MerchantProjectSettings["locale"][] = ["en", "ru", "zh-CN", "es", "fr", "de"];
  let timezone = "UTC";
  try {
    timezone = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    // Older engines: UTC is always accepted.
  }
  return {
    display_name: "", locale: supported.find((value) => value === locale) ?? "en", timezone,
    notifications: { payment_succeeded: true, payment_failed: true, weekly_summary: false },
    allowed_embed_origins: [], updated_at: "", version: 1
  };
}
export const isMissing = (error: unknown) => error instanceof AdminAPIError && error.status === 404;

// Time zones the browser knows, so the server's check cannot fail on a typo.
export function timeZones(current: string): string[] {
  let zones: string[] = [];
  try {
    zones = (Intl as unknown as { supportedValuesOf?: (key: string) => string[] }).supportedValuesOf?.("timeZone") ?? [];
  } catch {
    zones = [];
  }
  return Array.from(new Set(["UTC", current, ...zones])).filter(Boolean);
}

// The management API reports a lapsed MFA confirmation as 403
// permission_denied, the same code as a missing right, so both lead to a fresh
// sign-in with a note about the role. Only 503 from the verify call means the
// merchant's server failed the challenge.
export function managementOutcome(error: unknown, verifying = false): Outcome {
  if (!(error instanceof AdminAPIError)) return "failed";
  if (error.status === 403) return "stepUp";
  if (verifying && error.status === 503 && error.code === "dependency_unavailable") return "verifyFailed";
  return "failed";
}

// Arrow keys, Home and End move between the example tabs.
export function nextLanguage(current: SnippetLanguage, key: string): SnippetLanguage | null {
  const last = SNIPPET_LANGUAGES.length - 1;
  const at = SNIPPET_LANGUAGES.indexOf(current);
  const index = key === "ArrowRight" ? (at + 1) % (last + 1) : key === "ArrowLeft" ? (at + last) % (last + 1) : key === "Home" ? 0 : key === "End" ? last : -1;
  return index < 0 ? null : SNIPPET_LANGUAGES[index] ?? null;
}

// The method a merchant chose is a browser preference: it only decides
// which instructions the wizard shows.
const methodKey = (merchantId: string) => `ocrypt.setup.method.${merchantId}`;
export function savedMethod(merchantId: string): AcceptanceMethod | null {
  try {
    const value = window.localStorage.getItem(methodKey(merchantId));
    return ACCEPTANCE_METHODS.includes(value as AcceptanceMethod) ? value as AcceptanceMethod : null;
  } catch {
    return null;
  }
}
export function saveMethod(merchantId: string, method: AcceptanceMethod) {
  try {
    window.localStorage.setItem(methodKey(merchantId), method);
  } catch {
    // Private browsing: the choice lasts for this page only.
  }
}

// A webhook handler with the official SDKs. The cabinet's "Send test request"
// posts an unsigned challenge to the same address, so the handler answers it
// first; every other request must carry a valid signature and content digest.
// Secrets are resolved by key id so rotation keeps working.
export const SIGNATURE_SNIPPETS: Record<SnippetLanguage, string> = {
  node: `import { acknowledgement, verifyWebhook } from "@merchant-platform/sdk";

const PAID = new Set(["payment.settled", "payment.overpaid"]);

// Express: app.post("/ocrypt/webhook", express.raw({ type: "application/json" }), handler)
async function handler(req, res) {
  try {
    const body = JSON.parse(req.body.toString("utf8"));
    if (body.type === "merchant.webhook.challenge") return res.json({ challenge: body.challenge });
    const verified = await verifyWebhook({
      rawBody: new Uint8Array(req.body),
      signatureHeader: req.get("Merchant-Webhook-Signature") ?? "",
      contentDigest: req.get("Content-Digest") ?? "",
      resolveSecret: (keyId) => keyId === process.env.OCRYPT_WEBHOOK_KEY_ID ? process.env.OCRYPT_WEBHOOK_SECRET : undefined,
    });
    if (PAID.has(verified.event.event_type)) {
      // Fulfil the order once per event id, in your own transaction.
    }
    res.json(acknowledgement(verified.eventId));
  } catch {
    res.status(401).end();
  }
}`,
  python: `from merchant_platform.webhooks import WebhookVerificationError, acknowledgement, challenge_response, verify_webhook

PAID = {"payment.settled", "payment.overpaid"}

# Flask: @app.post("/ocrypt/webhook")
def webhook():
    raw = request.get_data()
    if b"merchant.webhook.challenge" in raw:
        try:
            return challenge_response(raw)
        except WebhookVerificationError:
            pass  # Not a challenge: verify it as a delivery below.
    try:
        verified = verify_webhook(
            raw,
            request.headers.get("Merchant-Webhook-Signature", ""),
            request.headers.get("Content-Digest", ""),
            lambda key_id: os.environ.get("OCRYPT_WEBHOOK_SECRET") if key_id == os.environ.get("OCRYPT_WEBHOOK_KEY_ID") else None,
        )
    except WebhookVerificationError:
        return "", 401
    if verified.event.event_type in PAID:
        pass  # Fulfil the order once per event id, in your own transaction.
    return acknowledgement(verified.event_id)`,
  php: `<?php
use MerchantPlatform\\Webhooks;

$raw = file_get_contents('php://input');
header('Content-Type: application/json');
$body = json_decode($raw, true);
if (is_array($body) && ($body['type'] ?? null) === 'merchant.webhook.challenge') {
    echo json_encode(['challenge' => $body['challenge'] ?? '']);
    exit;
}
try {
    $verified = Webhooks::verify(
        $raw,
        $_SERVER['HTTP_MERCHANT_WEBHOOK_SIGNATURE'] ?? '',
        $_SERVER['HTTP_CONTENT_DIGEST'] ?? '',
        fn (string $keyId) => $keyId === getenv('OCRYPT_WEBHOOK_KEY_ID') ? getenv('OCRYPT_WEBHOOK_SECRET') : null,
    );
} catch (Throwable $e) {
    http_response_code(401);
    exit;
}
if (in_array($verified->event->data['event_type'] ?? '', ['payment.settled', 'payment.overpaid'], true)) {
    // Fulfil the order once per event id, in your own transaction.
}
echo json_encode(Webhooks::acknowledgement($verified->eventId));`,
  go: `func webhook(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var challenge struct{ Type, Challenge string }
	if json.Unmarshal(raw, &challenge) == nil && challenge.Type == "merchant.webhook.challenge" {
		_ = json.NewEncoder(w).Encode(map[string]string{"challenge": challenge.Challenge})
		return
	}
	verified, err := merchantplatform.VerifyWebhook(raw, r.Header.Get("Merchant-Webhook-Signature"), r.Header.Get("Content-Digest"),
		func(keyID string) ([]byte, bool) {
			secret := os.Getenv("OCRYPT_WEBHOOK_SECRET")
			return []byte(secret), secret != "" && keyID == os.Getenv("OCRYPT_WEBHOOK_KEY_ID")
		},
		time.Now(), 5*time.Minute)
	if err != nil {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	switch verified.Event.EventType {
	case "payment.settled", "payment.overpaid":
		// Fulfil the order once per event id, in your own transaction.
	}
	_ = json.NewEncoder(w).Encode(merchantplatform.Acknowledgement(verified.EventID))
}`,
};

// What the endpoint must answer when the wizard verifies it.
export const CHALLENGE_EXAMPLE = `POST {"type":"merchant.webhook.challenge","challenge":"whc_…"}
→ 200 {"challenge":"whc_…"}`;
