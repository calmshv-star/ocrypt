# TypeScript SDK

Requires Node.js 20+ or a standards-based runtime with `fetch`, Web Crypto, `TextEncoder`, and `btoa`. Build with `pnpm test` in this directory.

```ts
import { MerchantClient, CheckoutClient, verifyWebhook } from "@merchant-platform/sdk";
const merchant = new MerchantClient({ baseUrl: "https://api.example", keyId: process.env.KEY_ID!, secret: process.env.SECRET! });
const events = await merchant.listEvents(0, 100);
const publicCheckout = new CheckoutClient("https://checkout.example");
```

The client covers the stable merchant API, payment-link/checkout aliases, signed report download/verification, and raw webhook verification. Keep exact money as strings. See the [cross-language guide](../README.md).
