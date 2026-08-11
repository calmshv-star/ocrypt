# Python SDK

Requires Python 3.9+ and uses the standard library for HTTP and HMAC. Install the optional `reports` extra for Ed25519 report verification.

```python
from merchant_platform import MerchantClient
client = MerchantClient("https://api.example", key_id, secret)
page = client.list_events(after_sequence=0, limit=100)
```

The client covers the stable merchant API, payment-link/checkout aliases, signed reports, and raw webhooks. Exact monetary values remain strings. See the [cross-language guide](../README.md).
