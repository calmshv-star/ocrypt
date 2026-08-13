# Showy integration profile

Showy uses the same production protocol as every other merchant:

1. a scoped HMAC credential sends commands to create/read an order;
2. a verified HTTPS endpoint receives every payment state change;
3. the receiver verifies the untouched body, digest, signature, merchant,
   environment and event type;
4. inbox insert, invoice transition and subscription activation commit in one
   database transaction;
5. the exact event ID is acknowledged; retries are safe and never grant a
   second subscription;
6. API polling is not a normal status channel. Event/history reads are only a
   manual gap-recovery mechanism.

## Runtime files

- `server/api/ocrypt_webhook.py` is the Showy receiver.
- `server/api/migrations/0134_ocrypt_webhook_inbox.py` creates its durable inbox.
- `server/api/test_ocrypt_webhook.py` covers challenge, signature and rejection.
- `install_runtime.py receiver` fail-closed stages the endpoint, durable inbox,
  asynchronous provisioning and read-only keyring while keeping recovery
  polling active. `install_runtime.py finalize` removes polling only after an
  acknowledged delivery is proven. Both refuse unreviewed Showy source drift.

The official Python verifier from `sdk/python/src/merchant_platform` must be
copied into the Showy image. Configure only file references and public identity:

```text
OCRYPT_WEBHOOK_SECRETS_FILE=/run/secrets/ocrypt-webhook-secrets.json
OCRYPT_WEBHOOK_MERCHANT_ID=<public merchant UUID>
```

The root-owned keyring format is:

```json
{"keys":{"whk_current":"one-time webhook secret"}}
```

Never place the keyring in Git, `.env`, logs or the database. Keep an old key
only during an explicit overlap window, then remove it after all deliveries
signed with that key are acknowledged.

## Cutover order

1. Run `python3 integrations/showy/install_runtime.py receiver /path/to/showy`,
   review the exact diff, migrate and deploy the receiver.
2. Create and challenge-verify the endpoint, start the Ocrypt callback worker,
   and prove an acknowledged delivery plus duplicate safety.
3. Run `python3 integrations/showy/install_runtime.py finalize /path/to/showy`
   and deploy the polling removal. Existing open orders remain valid throughout.
