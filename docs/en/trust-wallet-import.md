# Trust Wallet receiving-address import

The admin financial settings page can import public receiving addresses from Trust Wallet. It never asks for or accepts a seed phrase, private key, signer reference, spending approval, or transaction.

## Supported connection paths

- Trust Wallet browser extension: EVM accounts are discovered with EIP-6963 and connected through the EIP-1193 provider.
- Trust Wallet mobile: EVM accounts connect through WalletConnect v2 using a QR code or the Trust Wallet deep link.
- Trust Wallet in-app browser: EVM and Solana public addresses can be imported from the injected Trust providers.
- TON, TRON, and Aptos remain manual public-address entries until Trust Wallet exposes a documented mobile connection contract for them.

Create a free project in the [Reown Dashboard](https://dashboard.reown.com/) and set the build-time variable below for the production admin bundle:

```text
VITE_WALLETCONNECT_PROJECT_ID=<32-character-project-id>
```

Without that value, browser-extension and Trust in-app imports continue to work, while the mobile QR path reports that it is not configured.

## Verification and activation

Ocrypt creates a five-minute, domain-bound challenge and seals the public address, selected network wallet IDs, current wallet versions, expiry, and single-use UUID in an opaque encrypted token. Trust Wallet signs the exact message. The admin API opens its own token, rejects changed browser fields, and verifies EVM `personal_sign` signatures with secp256k1 or Solana signatures with ed25519 before changing any address.

All selected addresses change in one serializable database transaction. Existing payment routes retain their previous address, scanner readiness is checked for every network, quarantined addresses stay quarantined, and the append-only audit records that no private key was stored.
