# Going live (REG-4)

A self-service merchant starts in test mode (the sandbox). To take real
payments the owner opens **Go live** in the cabinet:

1. adds a receiving address per network and signs the shown message with
   that wallet (EVM `personal_sign`, Solana `signMessage`, TRON
   `signMessageV2`; TON is not supported yet). The cabinet never asks for a
   seed phrase or a key;
2. chooses the acceptance rules: **Fast**, **Standard** or **Reliable**
   (exact payments only);
3. sends the request. Fast and Standard also file a matching-policy change
   that the reviewer approves.

## Operator steps

A platform reviewer with a global `security_admin` binding (an `auditor` can
read) opens **Platform → Go-live requests**. Each request shows the tenant,
the merchant, the chosen rules and, per network, the canonical address, the
wallet id once it exists, and whether the wallet and new routes are admitted.

1. **Create wallet records** (button): creates the merchant's watch-only
   wallet and deposit address records from the proven addresses. They are
   inert until admitted.
2. Admit them with the usual four-eyes platform changes on **Platform
   configuration** (one operator drafts and requests, a second approves and
   activates), for the merchant's tenant:
   - `wallet_pool`, key = the wallet id shown, payload
     `{"status": "active", "chain_ref": "<chain id>"}` — one per network;
   - `merchant_environment`, key = the merchant id, payload
     `{"status": "active"}`;
   - `feature_flag`, key `new_routes`, payload
     `{"key": "new_routes", "enabled": true, "rollout_bps": 10000}`.

   The global `chain`, `asset_contract` and `finality_policy` heads of each
   network must already be active (standalone bootstrap sets them).
3. When every address shows *admitted* and *new routes admitted*, approve
   with a reason of at least eight characters. One transaction files the
   preset's matching policy already activated — the owner requested it, the
   reviewer approves and activates it (which also needs global
   `matching_policy:approve` and `matching_policy:activate`) — with a neutral
   reason, switches the merchant to live, and writes
   `admin.go_live.approved` with your reason to the global audit log.
4. Or reject with a reason; nothing is filed and the merchant stays in test
   mode. The owner sees only that the request was not approved.

A decision is final, needs an MFA session, and can never be made by a member
of the merchant. After going live the merchant's test API keys and sandbox
stop working; it needs live API keys.

Rolling back migration 000061 is refused while a request is pending; it keeps
merchants that are already live and their activated policies, and removes
only the request records.
