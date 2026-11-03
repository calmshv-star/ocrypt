# signup-screening decisions

Goal: Screen every self-service signup against official sanctions lists and the client IP region, and hold any match for an operator decision instead of admitting it

For each material decision record date, accepted choice, reason, evidence,
and effect on scope or acceptance criteria. Separate user requirements from
agent assumptions. Amend the specification when accepted behavior changes.

## 2026-11-03 — agent decisions (owner: "finish the specification without distracting me")

User requirement (TZ REG-3): screen name, domain and IP at registration; a
match goes to manual operator review, never automatic admission.

1. **Hold, do not refuse.** A match creates the account with the merchant in
   the existing status `review`. The owner projection already requires an
   active merchant, so no new permission path was needed and a held account
   holds no right. Refusing would also tell a sanctioned registrant which
   check fired.
2. **Lists are official files, loaded by the owner role.** OFAC SDN/ALT and
   the UN consolidated XML, parsed by a stdlib script that prints one SQL
   transaction for `psql`. No network access in the script, no new service
   image, no third-party list (OpenSanctions is not free for commercial use).
   EU and UK lists have a source slot but no parser yet.
3. **Fail closed on stale lists.** No official list, or one older than its
   8-day window, holds every new signup (`screening_list_stale`). An empty
   replacement or one under half the loaded size is refused. Evidence: live
   `S2` stale and missing-list cases.
4. **Keys only in PostgreSQL**, with `COLLATE pg_unicode_fast`, so the
   importer and the registration check cannot normalize differently and the
   database locale does not matter (live `S2` checks the same key under
   collation `C`). Dots and apostrophes are dropped before splitting, so
   `S.A.` is one word.
5. **Exact words plus a two-word subset rule, not fuzzy matching.** Same word
   set, or at least two words of one name all present in the other: a listed
   person without the patronymic, or a listed company with extra words. One
   shared word never matches. Fuzzy or phonetic matching is left out: it
   would hold many honest signups and needs tuning against real traffic.
6. **Email domains are screened, OFAC email addresses are not imported.**
   SDN remarks list free-mail addresses; importing their domains would hold
   every Gmail user. Only `Website` remarks become domain entries.
7. **IP = region from the trusted edge header.** Same model as the checkout
   gate (CHK-8). `T1` (Tor) and closed countries hold; missing, repeated or
   malformed values and `XX` hold as `ip_region_unknown`. An install without a
   geo proxy must opt out explicitly (`ADMIN_SIGNUP_UNSCREENED_IP=true`).
8. **The registrant's OIDC display name is screened too**, so an individual
   cannot pass by naming the company differently.
9. **Reviewer = global binding.** `security_admin` decides, `auditor` reads.
   A tenant-scoped grant is refused by the BFF and again by the database. The
   registrant (or any member of the held merchant) cannot decide. A decision
   is final and audited with its reason.
10. **Step-up.** In this cabinet every authenticated session is treated as
    freshly MFA-verified (`operationalAuthResult`), so the decision checks the
    session's ACR/AMR context; the BFF test uses a non-MFA session instead of
    an expired step-up.
11. **The cabinet scope model is unchanged.** Cabinet scopes come only from
    tenant bindings, so a reviewer also needs a tenant binding to open the
    cabinet; the queue itself sends no scope header and refuses one.

## 2026-11-03 — independent review of 3ac9f03

No critical or high finding. Fixed, each with a test written first:

- **M1** Hold and decision audit events carried the held merchant's tenant
  and merchant, so an owner admitted later could read the screening reasons,
  matched references and the reviewer's note through `audit:read`. Both now
  go to the global audit log only (tenant and merchant NULL); the tenant keeps
  the reason-free `admin.self_service.registered`. Live `S2` checks the rows.
- **M2** A one-word listed name was evaded by adding any word, and a hyphen,
  a ‘/ʼ/`/´ apostrophe or a combining mark outside U+0300–U+036F split words.
  Entities are now flagged `whole_word` by the importer (OFAC null SDN_Type,
  UN ENTITY) and a one-word entity of four or more letters hits wherever it
  appears; individuals are not flagged, so one given name never holds
  everyone. A squashed key (words joined without spaces) catches splits;
  every apostrophe variant and every combining-mark block is dropped.
  Homoglyphs from other scripts remain out of scope (documented).
- **M3** A missing UN list did not count as stale and freshness counted from
  load time. `signup_screening_required_sources` (OFAC SDN, UN) must all be
  loaded; freshness uses the publication time when the file states one; an
  older publication cannot replace a newer one.
- **M4** The UN date `…Z` failed on Python before 3.11. The importer now
  converts the `Z`; checked with the macOS Python 3.9.6.
- **Lows:** the email domain is taken after the last `@` (also in the
  disposable-mailbox check); Website remarks pointing to a page on a shared
  platform (Facebook, Telegram, X, …) are skipped; the down migration refuses
  to run while signups await review; the reason limit counts characters, not
  bytes; screening functions raise `OCP01`/`OCP02` instead of `42501`/`22023`
  so a missing GRANT stays a server error; the shrink guard applies only to
  official lists; the admin readiness check now requires the new functions, so
  a new admin API never serves against an old schema; invisible characters in
  the migration are escaped again; the OpenAPI tag is declared; SPEC's `XX`
  wording is corrected.
- **Accepted as limits:** the queue shows the oldest 100 held signups, and
  signups held only for stale lists are not re-screened automatically; both
  are documented for operators. The two-word subset rule will hold some
  honest names ("International Trading"); that is the intended fail-safe
  direction.
