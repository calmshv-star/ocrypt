# Close overpayments with repeated customer invoices

The ordinary >80 score rule must not treat every equal-score invoice as a
different customer. Conversely, a common receiving address or exchange sender
is not proof of customer ownership.

An additional narrow selection path accepts the strictly closest invoice when:

- the competing top-ranked candidates are small overpayments (at most 5%),
  made inside their original payment windows;
- all of those candidates have the same nonempty merchant-authenticated
  `customer_reference`, tenant and merchant;
- their original fiat amount, currency/scale, description and metadata match;
- the closest amount is unique. Equal amount distances still require review.

Merchants should include product/purchase context in description or metadata
when identically priced products must be distinguished. The engine compares
only information provided through the authenticated merchant API; it does not
infer product equivalence or identity from an exchange withdrawal address.

The scanner records the selection reason `same_customer_closest_overpayment`
and queues only the selected policy-bound route. The matching worker re-reads
current candidates and customer identities before allowing this specific
exception to its overlapping-route guard. A changed customer, price, context,
or competing closer route fails closed. Finality, policy limits, atomic ledger
posting, single-event allocation and callback delivery remain unchanged.

Excess is accounted according to the route's existing policy. With
`credit_expected_hold_excess`, 5.95 received against 5.94 expected credits 5.94
and records 0.01 as excess; it does not grant a second order.

## Verification and deployment

- Run `go test ./internal/application ./internal/adapters/postgres` in `backend`.
- The `Customer payment matching regression` GitHub workflow also runs the
  real PostgreSQL selection and settlement-worker guard on isolated synthetic
  records. Local DB runs require `OCRYPT_TEST_DATABASE_URL` and a database name
  beginning with `ocrypt_test_`; the test refuses all other databases.
- Deploy the same revision to scanner ingestion, settlement, matching and
  direct-proof workers. No database migration or downstream merchant code
  change is needed. Preserve configuration, provider pacing and scanner cursors.
- Retain old containers/images for rollback and check process readiness after
  each replacement. Do not bulk-replay historical review cases or send test
  payments/callbacks to real customers as a deployment check.
- Existing review cases require a separately authorized, single-payment
  resolution through the normal settlement and webhook path.
