# Framework integration skeletons

These source-only adapters show where to connect the official SDK, raw-body middleware, and the application's transaction manager. No framework dependency is installed by this repository and no sample contains a real credential.

| Stack | Skeleton |
| --- | --- |
| FastAPI / Django | [fastapi-django/webhook.py](fastapi-django/webhook.py) |
| Laravel / Symfony | [laravel-symfony/WebhookController.php](laravel-symfony/WebhookController.php) |
| Express / NestJS | [express-nestjs/webhook.ts](express-nestjs/webhook.ts) |
| Spring Boot | [spring-boot/WebhookController.java](spring-boot/WebhookController.java) |
| ASP.NET | [aspnet/WebhookEndpoint.cs](aspnet/WebhookEndpoint.cs) |
| Telegram bot backend | [telegram-bot/backend.py](telegram-bot/backend.py) |
| Generic e-commerce | [ecommerce/order_flow.py](ecommerce/order_flow.py) |

Apply [common/schema.sql](common/schema.sql) through the application's migration system. Each endpoint must receive the untouched, size-limited body, resolve the webhook secret by key ID, and use the official SDK verifier. The transaction then claims the unique event ID/body digest, validates amount/currency/order against local truth, updates the order, and inserts one fulfillment outbox row. It commits before acknowledging. Duplicate same-body delivery is acknowledged; same ID/different digest returns a conflict and triggers a security alert.

The endpoint never calls inventory, email, Telegram, license, subscription, or shipping services directly. A separate leased outbox worker performs those effects with its outbox ID as their idempotency key. `payment.reorged` enters a local risk/recovery workflow; it does not silently delete the original audit record.

Webhook `event_id` is stored as bounded opaque text, not a database UUID. This
matches the public webhook schema and shipped `evt_…` golden fixtures while
remaining compatible with deployments that generate UUID-shaped IDs.
