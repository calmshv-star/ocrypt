# PHP SDK

Requires PHP 8.1+. The `ext-sodium` extension is required only for Ed25519 reconciliation-report verification.

```php
$client = new MerchantPlatform\Client('https://api.example', $keyId, $secret);
$events = $client->listEvents(0, 100);
```

The client covers the stable merchant API, payment-link/checkout aliases, signed reports, and raw webhooks. Exact monetary values remain decimal strings. See the [cross-language guide](../README.md).
