import type { NotificationEvent } from "./api/types";

export type AcceptanceMethod = "link" | "api" | "plugin" | "invoice" | "telegram";
export const ACCEPTANCE_METHODS: AcceptanceMethod[] = ["link", "api", "plugin", "invoice", "telegram"];
// Methods that send payment events to the merchant's server.
export const METHODS_WITH_WEBHOOK = new Set<AcceptanceMethod>(["api", "plugin"]);
// Methods that are not built yet; the wizard points to a payment link.
export const METHODS_COMING = new Set<AcceptanceMethod>(["plugin", "invoice", "telegram"]);
export const WIZARD_WEBHOOK_EVENTS = ["payment.settled", "payment.partially_paid", "payment.needs_review"];
export const WIZARD_KEY_SCOPES = ["payments:read", "payments:write", "events:read"];
export const WIZARD_PROFILE_REASON = "Profile set in the setup wizard";
export type SnippetLanguage = "node" | "python" | "php" | "go";
export const SNIPPET_LANGUAGES: SnippetLanguage[] = ["node", "python", "php", "go"];
export type WizardEvent = NotificationEvent;
export type WizardAction = () => Promise<unknown>;
export const URL_PLACEHOLDER = "https://";
export function httpStatusLabel(status?: number): string | undefined {
  return status ? `HTTP ${status}` : undefined;
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

// Verifying a delivery with the official SDKs: read the raw body, check the
// signature header and the content digest, then answer with the
// acknowledgement. Secrets are resolved by key id so rotation keeps working.
export const SIGNATURE_SNIPPETS: Record<SnippetLanguage, string> = {
  node: `import { acknowledgement, verifyWebhook } from "@merchant-platform/sdk";

// Express: app.post("/ocrypt/webhook", express.raw({ type: "application/json" }), handler)
async function handler(req, res) {
  const verified = await verifyWebhook({
    rawBody: new Uint8Array(req.body),
    signatureHeader: req.get("Merchant-Webhook-Signature"),
    contentDigest: req.get("Content-Digest"),
    resolveSecret: (keyId) => keyId === process.env.OCRYPT_WEBHOOK_KEY_ID ? process.env.OCRYPT_WEBHOOK_SECRET : undefined,
  });
  if (verified.event.event_type === "payment.settled") {
    // Fulfil the order once per event id, in your own transaction.
  }
  res.json(acknowledgement(verified.eventId));
}`,
  python: `from merchant_platform.webhooks import acknowledgement, verify_webhook

# Flask: @app.post("/ocrypt/webhook")
def webhook():
    verified = verify_webhook(
        request.get_data(),
        request.headers["Merchant-Webhook-Signature"],
        request.headers["Content-Digest"],
        lambda key_id: os.environ["OCRYPT_WEBHOOK_SECRET"] if key_id == os.environ["OCRYPT_WEBHOOK_KEY_ID"] else None,
    )
    if verified.event.event_type == "payment.settled":
        pass  # Fulfil the order once per event id, in your own transaction.
    return acknowledgement(verified.event_id)`,
  php: `<?php
use MerchantPlatform\\Webhooks;

$verified = Webhooks::verify(
    file_get_contents('php://input'),
    $_SERVER['HTTP_MERCHANT_WEBHOOK_SIGNATURE'] ?? '',
    $_SERVER['HTTP_CONTENT_DIGEST'] ?? '',
    fn (string $keyId) => $keyId === getenv('OCRYPT_WEBHOOK_KEY_ID') ? getenv('OCRYPT_WEBHOOK_SECRET') : null,
);
// Fulfil the order once per event id, in your own transaction.
header('Content-Type: application/json');
echo json_encode(Webhooks::acknowledgement($verified->eventId));`,
  go: `func webhook(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	verified, err := merchantplatform.VerifyWebhook(raw, r.Header.Get("Merchant-Webhook-Signature"), r.Header.Get("Content-Digest"),
		func(keyID string) ([]byte, bool) { return []byte(os.Getenv("OCRYPT_WEBHOOK_SECRET")), keyID == os.Getenv("OCRYPT_WEBHOOK_KEY_ID") },
		time.Now(), 5*time.Minute)
	if err != nil {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	// Fulfil the order once per event id, in your own transaction.
	_ = json.NewEncoder(w).Encode(merchantplatform.Acknowledgement(verified.EventID))
}`,
};

// What the endpoint must answer when the wizard verifies it.
export const CHALLENGE_EXAMPLE = `POST {"type":"merchant.webhook.challenge","challenge":"whc_…"}
→ 200 {"challenge":"whc_…"}`;
