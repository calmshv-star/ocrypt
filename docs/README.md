# Merchant Platform documentation

This directory turns the full Russian architecture specification into shorter,
role-oriented guides. The long-form source remains
[`FULL_CRYPTO_MERCHANT_SPEC_RU.md`](../../FULL_CRYPTO_MERCHANT_SPEC_RU.md).

Each localized guide contains three views of the same product:

- **Product:** scope, user journeys, exception handling, and non-goals.
- **Developer:** API flow, exact-money rules, state machines, and webhooks.
- **Operations:** deployment, observability, reconciliation, incident response,
  and security controls.

## Languages

| Language | Product/operations | API integration | Event delivery | Team/settings | Provider operations |
| --- | --- | --- | --- | --- | --- |
| English | [guide](en/guide.md) | [API](en/api-integration.md) | [JetStream](en/event-delivery.md) | [team](en/merchant-team-settings.md) | [providers](en/provider-operations.md) |
| 简体中文 | [指南](zh-CN/guide.md) | [API](zh-CN/api-integration.md) | [JetStream](zh-CN/event-delivery.md) | [团队](zh-CN/merchant-team-settings.md) | [提供商](zh-CN/provider-operations.md) |
| Español | [guía](es/guide.md) | [API](es/api-integration.md) | [JetStream](es/event-delivery.md) | [equipo](es/merchant-team-settings.md) | [proveedores](es/provider-operations.md) |
| Français | [guide](fr/guide.md) | [API](fr/api-integration.md) | [JetStream](fr/event-delivery.md) | [équipe](fr/merchant-team-settings.md) | [fournisseurs](fr/provider-operations.md) |
| Deutsch | [Leitfaden](de/guide.md) | [API](de/api-integration.md) | [JetStream](de/event-delivery.md) | [Team](de/merchant-team-settings.md) | [Provider](de/provider-operations.md) |
| Русский | [руководство](ru/guide.md) | [API](ru/api-integration.md) | [JetStream](ru/event-delivery.md) | [команда](ru/merchant-team-settings.md) | [провайдеры](ru/provider-operations.md) |

Additional engineering material:

- [Integration examples](../examples/README.md)
- [Framework inbox/order/outbox skeletons](../examples/frameworks/README.md)
- [Official SDKs and shared signing vectors](../sdk/README.md)
- [Deployment and operations](../deploy/README.md)
- [Implementation and release status](IMPLEMENTATION_STATUS.md)
- [Independent test plan](TEST_PLAN.md)
- API contract: [`../contracts/openapi.yaml`](../contracts/openapi.yaml)
- Merchant management contract: [`../contracts/management-openapi.yaml`](../contracts/management-openapi.yaml)
- Merchant team/settings contract: [`../contracts/merchant-settings-openapi.yaml`](../contracts/merchant-settings-openapi.yaml)

The guides are translations, not independent specifications. In a conflict,
the versioned OpenAPI schema, canonical webhook fixtures, and accepted
architecture decisions take precedence over prose.
