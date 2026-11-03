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
read) opens **Platform → Go-live requests**:

1. For each listed address, create the merchant's watch-only wallet and admit
   it as a `wallet_pool` platform change for the merchant's tenant. This is
   the usual four-eyes platform flow (one operator drafts and requests, a
   second approves and activates); the queue shows each address as
   *admitted* once it is the current deposit address of an admitted wallet.
2. When every address is admitted, approve with a reason of at least eight
   characters. One transaction then activates the preset's policy (the owner
   requested it, the reviewer approves and activates it), switches the
   merchant to live, and writes `admin.go_live.approved` to the global audit
   log.
3. Or reject with a reason; the merchant stays in test mode and the preset
   change is rejected. The owner sees only that the request was not
   approved.

A decision is final, needs an MFA session, and can never be made by a member
of the merchant. Rolling back migration 000061 is refused while a request is
pending.
