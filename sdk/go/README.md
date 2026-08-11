# Go SDK

Requires Go 1.22+ and only the standard library.

```go
client, err := merchantplatform.NewClient("https://api.example", keyID, secret, 10*time.Second)
events, err := client.ListEvents(ctx, 0, 100)
```

The client covers the stable merchant API, capability checkout, streaming signed report downloads, report verification, and raw webhooks. Close every report response body. Exact monetary values use `AtomicAmount` strings. See the [cross-language guide](../README.md).
