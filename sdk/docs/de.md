# Merchant Platform SDK – Schnellstart

[English](../README.md) · [简体中文](zh-CN.md) · [Español](es.md) · [Français](fr.md) · [Deutsch](de.md) · [Русский](ru.md)

Die Clients für TypeScript, Python, Go, PHP, Java und .NET verwenden dieselben HMAC-, Idempotenz- und Webhook-Regeln. Geldbeträge und atomare Chain-Einheiten bleiben immer dezimale Ganzzahl-Strings und dürfen nie in Gleitkommazahlen umgewandelt werden.

```ts
const client = new MerchantClient({ baseUrl, keyId, secret, timeoutMs: 10_000 });
const intent = await client.createPaymentIntent({
  merchant_order_id: "order-2026-42", amount_minor: "49900",
  currency: "USD", currency_scale: 2,
  allowed_routes: [{ provider: "on_chain", chain_id: "tron:mainnet", asset_id: "usdt-tron" }]
}, "order-2026-42-create");
```

Jede Mutation benötigt einen eindeutigen Idempotenzschlüssel. Ein erneuter Versuch mit demselben Schlüssel muss exakt denselben Body verwenden. Die SDKs verlangen außerhalb lokaler Entwicklung HTTPS, setzen feste Zeitlimits und schreiben Geheimnisse weder in Logs noch in Fehler.

Verifiziere die unveränderten HTTP-Rohbytes eines Webhooks, bevor JSON geparst wird. Normalerweise darf nur `payment.settled` die Lieferung auslösen. Der `WebhookInbox`-Adapter muss Ereignis, Auftragsänderung und Fulfillment-Job in einer Datenbanktransaktion speichern; dieselbe Ereignis-ID mit anderem Digest ist ein Konflikt.

Vor dem Settlement enthalten `payment.observed`, `payment.confirming` und `payment.reorged` ein begrenztes `observation`-Objekt mit kanonischem Transfer, aktuellen/erforderlichen Bestätigungen, Finalität und Evidenz-Digest. `payment.resolution.updated` enthält ein geheimnisfreies `resolution`-Objekt. Diese Ereignisse sind informativ und erlauben keine Lieferung.

Die stabile Abdeckung umfasst expire/metadata, Payment Proofs, Events über `after_sequence`, Event-/Transfer-/Quote-Details, Balances, Reconciliation Reports sowie Payment Links und Checkout. Für Reports ist der separate Scope `reconciliation:read` erforderlich; vor JSONL-Verarbeitung müssen Größe, SHA-256, eingefrorene Key-ID und Ed25519-Signatur geprüft werden. Admin-, Operator- und Treasury-Methoden gehören absichtlich nicht in den Merchant-Client. Details stehen im [englischen Index](../README.md) und im [deutschen Integrationsleitfaden](../../docs/de/api-integration.md).
