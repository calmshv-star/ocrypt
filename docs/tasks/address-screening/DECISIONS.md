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
6. **Listed receiving addresses are refused** at go-live and as deposit
   addresses (OCP04 → 400 `address_not_accepted`). The merchant is told the
   address is not accepted, not why: the platform neither accuses nor tips
   off.

## 2026-11-03 — independent review findings and fixes

1. **Critical (fixed).** One account under two tags (ETH and USDT) broke the
   primary key and aborted the whole OFAC load. Tags are now an array per
   (family, address, reference); live `S7` loads such a pair.
2. **High (fixed).** OFAC cuts remarks at 1000 characters; the importer now
   joins `SDN_COMMENTS.CSV` per entity (`--comments`), tested with a cut inside
   an address.
3. **Medium (fixed).** Alerts went to every merchant that ever had a route on
   the address. Now only routes for that asset whose window covers the
   transfer, else the deposit address owner; a tenant-shared address without a
   covering route alerts nobody.
4. **Medium (fixed).** The go-live refusal read as a bad signature; it now has
   its own code and message.
5. **Medium (fixed).** Deposit addresses are screened on insert and on
   replacement, not only at go-live.
6. **Low–medium (fixed).** Notifications are deduplicated per merchant, sender
   and UTC hour; every transfer is still an alert. Alerts survive reorgs (the
   transfer was observed); documented.
7. **Low (fixed).** An official list's addresses cannot be emptied or halved in
   one load.
8. **Low (fixed).** Tags are validated; Solana is recognised by format.
9. **Low (fixed).** Operator case references are masked for merchants.
10. **Low (fixed).** The notification access check now counts global bindings
    like the cabinet does (also for `#/sanctions-alerts`).
11. **Low (accepted).** No index on `payment_routes(chain_id,
    receiving_address)`: only a hit pays for the lookup, the dust check has the
    same pattern, and a non-concurrent index build would block route writes.
    Add it with `CREATE INDEX CONCURRENTLY` in a maintenance window if hits
    become frequent.
