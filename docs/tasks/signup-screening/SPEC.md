# signup-screening specification

## Requested outcome

Screen every self-service signup against official sanctions lists and the client IP region, and hold any match for an operator decision instead of admitting it

## Interview and scope

Source: product specification stage 2, REG-3: "sanctions screening of name,
domain and IP at registration; a match goes to manual operator review, never
automatic admission". The owner asked to finish the specification without
further questions, so the choices below are agent decisions recorded in
DECISIONS.md.

Flow. The signup form is screened by country and business type as before
(000058). When the browser posts the form, the admin BFF also reads the
client's region from one header set by the trusted edge proxy
(`ADMIN_SIGNUP_REGION_HEADER`, for example `CF-IPCountry`) and stores it on
the signup request. After OIDC with a verified email and MFA,
`register_self_service_merchant` screens the request once more inside the
registration transaction:

- `name_match`: the company name matches a listed name or alias;
- `domain_match`: the website host or the email domain equals a listed
  domain or is a subdomain of one;
- `ip_region`: the client region is a closed signup country or a screened
  pseudo-region (`T1` Tor);
- `ip_region_unknown`: the header is configured but missing, repeated, not a
  single region code, or `XX` (unknown to the edge);
- `screening_list_stale`: a required official list (`ofac_sdn`,
  `un_consolidated`) is not loaded, or an official list is older than its
  refresh window (8 days by default), counted from its publication time when
  the file states one.

No reason: the merchant is created `active` in test mode as today. Any
reason: the tenant, owner identity and membership are created, but the
merchant is created in status `review`, so no cabinet permission is
projected (the owner projection requires an active merchant). The screening
record keeps the reasons and the matched list references. The registrant
sees only "your application is being reviewed", never the reasons.

Matching is on normalized words, not fuzzy: casefold, dots and apostrophes
(including ‘ ʼ ` ´) dropped, NFKD with every combining mark removed, other
punctuation to spaces, legal-form words (LLC, Ltd, GmbH, OOO, ООО, АО and
similar) and "the/of/and" removed. A name matches when its distinct sorted
words are equal, when its words joined without spaces are equal (catching
"Mel-li"), when at least two words of one name all appear in the other, or
when an entity listed under one word of four or more letters appears as a
word of the signup name. Homoglyphs from other scripts are not folded. So "ООО «Рога и Копыта»" equals "Копыта и Рога", and "Société
Générale S.A." equals "societe generale". Keys shorter than 3 characters are
not stored or matched. The key is computed only in PostgreSQL
(`signup_screening_name_key`, `COLLATE pg_unicode_fast`), so importer and
registration cannot disagree, whatever the database locale.

Lists. `scripts/signup_screening_import.py` reads files the operator
downloaded from the official source and prints one SQL transaction for
`psql` (database owner role) that replaces one source atomically through
`replace_signup_screening_source`:

- `ofac_sdn`: OFAC SDN.CSV and ALT.CSV (no header, `-0-` is null); names of
  individuals and entities and their aliases, vessels and aircraft skipped;
  `Website` domains from Remarks;
- `un_consolidated`: UN Security Council consolidated XML; individual name
  parts joined, entity names, original-script names and aliases;
- `operator`: a CSV of `kind,value,reference` the operator curates (never
  stale).

A replacement that would empty an official list or shrink it below half of
the loaded entries is refused, so a truncated download cannot silently turn
screening off. The runtime role can read neither the lists nor call the
replace function.

Review. A platform operator with a global binding holding
`signup_reviews:read` sees the held signups (company, country, business type,
website, email, region, reasons, matched references) at `#/signup-reviews`.
With `signup_reviews:decide` and a fresh MFA step-up, the operator approves
(merchant `review` -> `active`) or rejects (merchant -> `disabled`) with a
reason of 3..1000 characters. A decision is final, made once, never by the
registrant, and audited. Roles: `security_admin` gets both permissions,
`auditor` gets read.

Configuration. With `ADMIN_SELF_SERVICE_SIGNUP=true` the BFF refuses to start
unless `ADMIN_SIGNUP_REGION_HEADER` names a header or
`ADMIN_SIGNUP_UNSCREENED_IP=true` explicitly opts out (installs without a
geo-aware proxy; the opt-out is then visible in configuration).

Non-goals: fuzzy or phonetic matching (a later increment can add pg_trgm
similarity as an extra review reason), EU and UK lists (the importer has a
source slot for each, parsers come later), screening of existing merchants
when a list changes, automatic list download, KYC documents.

## Acceptance matrix

| ID | Behavior/input/fault | Expected result | Test or manual check | Evidence |
| --- | --- | --- | --- | --- |
| G1 | Name key: case, accents, ё/е, punctuation, legal forms, word order, short keys | Equal keys for equivalent names; NULL for keys under 3 characters; same result under database collation `C` | live `S2_signup_screening` | |
| G2 | Replace a source: valid entries; empty official list; shrink below half; bad kind; unknown source name | Atomic replace with count and load time; refusals leave the old list intact | live `S2_signup_screening` | |
| G3 | Signup with no match and fresh lists | Merchant active, screening `clear`, owner grants projected | live `S2_signup_screening` | |
| G4 | Name alias match, website subdomain match, email domain match, Tor or closed-country region, missing region, stale or missing list | Merchant `review`, screening `review` with every reason and matched references, no owner grants, audit `admin.self_service.screening_hold` | live `S2_signup_screening` | |
| G5 | Review list and decision without a global binding, with a tenant binding, without step-up, by the registrant, twice, with a short reason | Refused; one decision only | live `S2_signup_screening`, `TestSignupReviewRoutes` | |
| G6 | Approve; reject | Approve: merchant active and owner grants appear; reject: merchant disabled, nothing projected; audit with reason | live `S2_signup_screening` | |
| G7 | BFF region header: one valid code, repeated, invalid, absent; header not configured | Region stored or `ip_region_unknown`; not screened only when opted out | `TestSignupReadsTheRegionFromTheTrustedHeader` | |
| G8 | Config: signup on without header or opt-out | Refused at startup | `TestSignupNeedsARegionHeaderOrExplicitOptOut` | |
| G9 | Held registration callback and later sign-in | Redirect to the review screen; `/session/me` reports `signup_review: pending` or `rejected`, never the reasons | `TestHeldSignupLandsOnTheReviewScreen`, `Signup.test.tsx` | |
| G10 | Importer: OFAC SDN/ALT rows with nulls, quotes, vessels, Website remarks; UN XML individuals, entities, aliases, empty placeholders; operator CSV; SQL quoting | Exact entry set; SQL safe for any input text | `tests/unit/test_signup_screening_import.py` | |
| G11 | Review page | Lists reasons in plain language; decision needs a reason; step-up prompt on `step_up_required`; hidden without permission | `SignupReviews.test.tsx` | |
| G12 | Review findings: reasons and reviewer notes in a later-admitted owner's audit; one-word entity plus a word; split or apostrophe variants; missing UN list; old publication re-imported; quoted email local part; operator list shrink; Python 3.9 importer; non-ASCII reason length; 42501 mapping; rollback with held signups | Hold and decision audit only in the global log; held; held; held; refused; screened by the last `@`; allowed; loads; counted in characters; stays a server error; rollback refused | live `S2`, `TestSignupReviewRoutes`, `TestSignupReviewRepositoryErrorsStayGeneric`, importer tests, manual down-migration run | |

## Runtime prerequisites

Live cases need a disposable PostgreSQL 18 database
(`OCRYPT_RUN_PAYMENT_FAULT_TESTS=1`, `OCRYPT_PAYMENT_FAULT_DATABASE_URL`).
Everything else is offline. Loading the real lists on a server needs the
owner database role and files downloaded from OFAC and the UN; that is an
operator step documented in `deploy/standalone/signup-screening/README.md`.
