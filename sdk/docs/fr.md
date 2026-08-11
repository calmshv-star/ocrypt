# Démarrage rapide des SDK Merchant Platform

[English](../README.md) · [简体中文](zh-CN.md) · [Español](es.md) · [Français](fr.md) · [Deutsch](de.md) · [Русский](ru.md)

Les clients TypeScript, Python, Go, PHP, Java et .NET appliquent les mêmes règles HMAC, d’idempotence et de webhook. Les montants et unités atomiques restent toujours des chaînes d’entiers décimaux, jamais des nombres flottants.

```ts
const client = new MerchantClient({ baseUrl, keyId, secret, timeoutMs: 10_000 });
const intent = await client.createPaymentIntent({
  merchant_order_id: "order-2026-42", amount_minor: "49900",
  currency: "USD", currency_scale: 2,
  allowed_routes: [{ provider: "on_chain", chain_id: "tron:mainnet", asset_id: "usdt-tron" }]
}, "order-2026-42-create");
```

Chaque mutation exige une clé d’idempotence unique ; une nouvelle tentative avec la même clé doit conserver exactement le même corps. Les SDK imposent HTTPS hors développement local, bornent les délais et n’inscrivent jamais les secrets dans les journaux ou les erreurs.

Vérifiez les octets HTTP bruts du webhook avant de décoder le JSON. Seul `payment.settled` autorise normalement la livraison. L’adaptateur `WebhookInbox` doit enregistrer l’événement, mettre à jour la commande et mettre la livraison en file dans une même transaction ; un même identifiant avec une empreinte différente est un conflit.

Avant le settlement, `payment.observed`, `payment.confirming` et `payment.reorged` contiennent un objet `observation` borné avec le transfert canonique, les confirmations actuelles/requises, la finalité et l’empreinte de preuve. `payment.resolution.updated` contient un objet `resolution` sans secret. Ces événements sont informatifs et n’autorisent pas la livraison.

La couverture stable comprend expire/metadata, payment proofs, événements via `after_sequence`, détails d’événement/transfert/devis, balances, rapports de rapprochement et payment links/checkout. Les rapports exigent le scope distinct `reconciliation:read`; taille, SHA-256, key ID figé et signature Ed25519 sont vérifiés avant JSONL. Admin, operator et treasury sont volontairement exclus du client merchant. Consultez l’[index anglais](../README.md) et le [guide français](../../docs/fr/api-integration.md).
