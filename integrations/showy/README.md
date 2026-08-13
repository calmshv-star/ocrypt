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
- `install_runtime.py` fail-closed installs the endpoint, removes minute
  polling, adds asynchronous post-payment provisioning and mounts the keyring
  read-only. It refuses unreviewed Showy source drift.

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

Deploy the receiver and inbox first, create and challenge-verify the endpoint,
start the Ocrypt callback worker, prove an acknowledged delivery, and only then
apply the polling-removal portion. Existing open orders remain valid throughout.
