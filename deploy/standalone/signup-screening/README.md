# Signup sanctions screening (REG-3)

Every self-service signup is screened inside the registration transaction
(migration `000059_signup_screening`). A signup is **held** — its merchant is
created in status `review` with no cabinet rights — when:

| Reason | Meaning |
| --- | --- |
| `name_match` | The company name or the registrant's name matches a listed name or alias: same words in any order; the same letters with spaces removed ("Mel-li"); at least two words of one name all present in the other; or an entity listed under one word of four or more letters appearing as a word of the name. Case, accents, apostrophes, punctuation and legal forms (LLC, GmbH, ООО, АО, …) are ignored. |
| `domain_match` | The website host or the email domain is a listed domain or one of its subdomains. |
| `ip_region` | The client region from the edge proxy is a closed signup country or `T1` (Tor). |
| `ip_region_unknown` | The region header is configured but missing, repeated, malformed or `XX`. |
| `screening_list_stale` | A required list (`ofac_sdn`, `un_consolidated`) is missing, or an official list is older than 8 days, counted from its publication time when the file states one, else from loading. |

The registrant only sees "your application is being reviewed". A global
reviewer (`security_admin`, or `auditor` read-only) admits or rejects it on
the cabinet page **Platform → Signup reviews** with a reason; the decision is
final and audited (`admin.signup_review.approved` / `rejected`). Screening
reasons and reviewer notes go only to the global audit log, never to the
merchant's own audit view.

Matching is on normalized words, not fuzzy: a transliteration that spells a
name differently, or a letter from another script that looks the same, is
not caught. Treat the lists as a first filter,
not as complete sanctions compliance.

## Configuration

With `ADMIN_SELF_SERVICE_SIGNUP=true` the admin API needs exactly one of:

- `ADMIN_SIGNUP_REGION_HEADER` — the header the edge proxy sets to the
  client's country, for example `CF-IPCountry`. The proxy must overwrite any
  value the client sends in that header.
- `ADMIN_SIGNUP_UNSCREENED_IP=true` — an explicit opt-out for installs without
  a geo-aware proxy. IP screening is then off; name, domain and list
  freshness are still screened.

## Loading and refreshing the lists

Download the files yourself from the official sources, then pipe the
importer's SQL into `psql` as the **database owner** (the runtime roles can
neither read the lists nor replace them):

- OFAC: `SDN.CSV` and `ALT.CSV` (legacy flat files) from the Sanctions List
  Service, https://ofac.treasury.gov/sanctions-list-service
- UN Security Council consolidated list:
  https://scsanctions.un.org/resources/xml/en/consolidated.xml

```sh
python3 scripts/signup_screening_import.py ofac --sdn SDN.CSV --alt ALT.CSV | psql "$OWNER_DATABASE_URL"
python3 scripts/signup_screening_import.py un --xml consolidated.xml | psql "$OWNER_DATABASE_URL"
# Optional names, domains or crypto addresses your compliance team blocks
# (header kind,value,reference; an address row is "address,TRX T…,case-9"):
python3 scripts/signup_screening_import.py operator --csv operator.csv | psql "$OWNER_DATABASE_URL"
```

### Crypto addresses (payer screening)

The same runs load the list's crypto addresses ("Digital Currency Address -
ETH 0x…" in the OFAC remarks; operator rows of kind `address`) through
`replace_sanctioned_addresses`, in the same transaction as the names. Only
the families the platform watches are kept: EVM (one 0x account on every EVM
chain), TRON (base58check, verified, with its 20 bytes in hex) and Solana;
Bitcoin, Monero and other chains are skipped.

Every new transfer's sender is checked when the scanner records it. A hit is
an alert for each merchant whose payment address received it (cabinet page
"Sanctions alerts") and a notification to everyone who gets that merchant's
notifications. The payment is not changed: the platform holds no funds and
the merchant decides. A failing check logs `sanctions screening skipped for
transfer` and never blocks the scanner; alert on that log line. A listed
address cannot be submitted as a go-live receiving address.

Transfers are screened against the list loaded when they arrive; a refresh
does not re-screen earlier transfers.

Each run replaces one source in one transaction. A replacement that would
empty an official list, shrink it to less than half of what is loaded, or
replace it with an older publication is refused and the old list stays, so a
truncated or stale download cannot weaken screening. The UN file states its
generation time; for OFAC pass `--published` only with the time the file
itself states. The importer needs Python 3.9 or later. To start a source over on purpose:
`DELETE FROM signup_screening_sources WHERE source='ofac_sdn';`.

Refresh daily (for example from a systemd timer or cron). If a required list
is missing or an official list is older than 8 days, every new signup is held
until it is refreshed. The required lists are the rows of
`signup_screening_required_sources`.

The review queue shows the oldest 100 held signups. Signups held only because
the lists were stale are not re-screened after a refresh; decide them by
hand.

Check what is loaded:

```sql
SELECT source, entry_count, published_at, loaded_at, stale_after FROM signup_screening_sources ORDER BY source;
```

Existing merchants are not re-screened when a list changes. Rolling back
migration 000059 is refused while any signup still awaits review.
