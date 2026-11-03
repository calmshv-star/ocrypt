# address-screening decisions

Goal: Screen payer addresses against the official sanctions lists, warn the merchant about a payment from a listed address, and refuse listed addresses as go-live receiving addresses

## 2026-11-03 — agent decisions (owner: "do the whole specification, I am not here")

1. **Warn, do not hold.** The TZ asks for a warning, and the platform holds
   no funds: the merchant decides, with guidance to consult compliance before
   fulfilling or returning funds. Changing settlement would also bypass the
   four-eyes matching policy.
2. **Screen at record time, in the database.** A trigger on `transfer_events`
   covers every scanner and chain, needs no new worker, and is guarded so it
   cannot stop the scanner.
3. **Mandatory notification.** A sanctions hit goes to everyone who already
   gets the merchant's notifications, whatever events they chose; it is not a
   preference.
4. **Families by address format.** OFAC tags (ETH, USDT, USDC, TRX, …) are
   unreliable for the chain; an EVM account is the same person on every EVM
   chain, TRON and Solana are recognised by format and checksum.
5. **Same source, same transaction.** Addresses belong to the list they came
   from and are replaced with its names, so freshness and rollback are shared.
6. **Go-live refuses listed addresses** with OCP02 (400), like any invalid
   address.
