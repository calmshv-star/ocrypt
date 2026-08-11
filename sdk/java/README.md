# Java SDK

Requires Java 17+ and Jackson Databind.

```java
var client = new MerchantClient("https://api.example", keyId, secret, Duration.ofSeconds(10));
var events = client.listEvents(0, 100);
```

The client covers the stable merchant API, payment-link/checkout aliases, signed report verification with JCA Ed25519, and raw webhooks. Exact monetary values remain strings. See the [cross-language guide](../README.md).
