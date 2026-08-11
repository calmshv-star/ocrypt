# .NET SDK

Targets .NET 8.

```csharp
var client = new MerchantClient("https://api.example", keyId, secret);
var events = await client.ListEventsAsync(0, 100);
```

The client covers the stable merchant API, payment-link/checkout aliases, signed report download/digest validation, and raw webhooks. Connect `Reports.SignatureMessage` to the deployment's audited Ed25519 verifier. Exact monetary values remain strings. See the [cross-language guide](../README.md).
