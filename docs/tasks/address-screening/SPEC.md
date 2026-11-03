# address-screening specification

## Requested outcome

Screen payer addresses against the official sanctions lists, warn the merchant about a payment from a listed address, and refuse listed addresses as go-live receiving addresses

## Interview and scope

Source: TZ "Санкции" ("проверка мерчанта и его адресов по открытым спискам
(OFAC SDN, списки ЕС и Великобритании) … предупреждение мерчанту о входящем
платеже с санкционного адреса") and the "санкционный адрес" notification
event. The owner asked for no questions; agent decisions are in DECISIONS.md.

Facts found in the code:

- REG-3 already loads OFAC SDN, the UN list and an operator list through
  `scripts/signup_screening_import.py` into `signup_screening_sources`; OFAC
  publishes crypto addresses in the SDN remarks.
- Scanners record every transfer in `transfer_events` (sender, recipient,
  chain); a merchant's payment addresses are `payment_routes.receiving_address`.
- TRON senders may be reported in base58 or hex; EVM senders in any case.

Behaviour:

1. The importer extracts EVM, TRON (checksum-verified, plus hex form) and
   Solana addresses and replaces them with the source's names in one
   transaction (`replace_sanctioned_addresses`, owner only).
2. An AFTER INSERT trigger on `transfer_events` matches the sender on the
   chain's family (EVM by account in any case or hex; TRON in base58 or hex;
   Solana exactly). A hit records one alert per merchant whose route address
   received it (routes for that asset covering the transfer, else the deposit
   address owner) and queues a `sanctioned` notification to everyone who gets
   that merchant's notifications. The check runs in a guarded block and never
   blocks the scanner.
3. The payment is not changed (non-custodial; the merchant decides). The
   cabinet page "Sanctions alerts" lists alerts with guidance.
4. A listed address cannot be a go-live receiving address or a deposit
   address (OCP04, "address not accepted").

## Acceptance matrix

| Case | Evidence |
| --- | --- |
| Address extraction for watched families only; bad TRON checksums dropped | `tests/unit/test_signup_screening_import.py` |
| Names and addresses replaced in one transaction | importer SQL test |
| Listed sender (any case) alerts the receiving merchant | live `S7_address_screening` |
| Clean sender raises nothing; failing screen never blocks the write | live `S7` |
| TRON base58/hex matching; no cross-family match | live `S7` |
| Notification to every recipient regardless of chosen events; nobody without access | live `S7` |
| Cabinet list needs payments access | live `S7`, `TestSanctionsAlertsNeedPaymentAccessOnTheMerchant` |
| Listed address cannot go live | live `S7` |
| Reload removes addresses | live `S7` |
| Message in six languages | `TestRenderNamesTheEventInTheUsersLanguage` |
| Cabinet page | `apps/admin/src/Sanctions.test.tsx` |

## Out of scope

- EU and UK lists (no importer yet; the operator CSV can carry their addresses).
- Re-screening past transfers after a list refresh.
- Holding or rejecting a payment automatically.
- Bitcoin, Monero and other chains the platform does not watch; TON addresses.
