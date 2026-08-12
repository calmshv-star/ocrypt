# Betrieb der Rate Runtime

Der Rate Worker arbeitet fail-closed. Er liest ausschließlich aktive, unveränderliche `rate_policy`- und `rate_source`-Snapshots über `ActiveSnapshotReader`, niemals draft/approved/scheduled. Vor dem Commit werden Snapshot-ID und Fence-Token unter Sperre erneut geprüft; eine konkurrierende Aktivierung rollt die gesamte Sammlung zurück.

Die Policy benötigt `base_asset`, `quote_asset`, mindestens zwei verschiedene `sources`, `quorum`, `max_age_seconds` und `max_spread_bps`; optional `future_tolerance_seconds` (Standard 5, explizit 0 bedeutet Nulltoleranz) und `poll_interval_seconds` (15). Eine Source definiert `provider_ref`, einen normalisierten HTTPS-Endpoint, dasselbe Paar, Grenzen und optional `credential_ref`. Credentials, Query und Fragment in der URL sind verboten.

Antworten erfüllen `contracts/rate-provider-v1.schema.json`. Der Preis besteht aus positiven uint256 numerator/denominator, nie JSON number/float. Bei gerader Quellenzahl wird konservativ der untere beobachtete Median gewählt, ohne Mittelwert oder Rundung. Alle Prüfungen müssen gültig sein; andernfalls entsteht kein Tick.

Redirects, Proxies, private/Loopback/Link-local/reservierte IPs und gemischte DNS-Antworten werden blockiert; DNS wird je Verbindung fixiert. TLS, Timeout, Content-Type und Größe sind begrenzt. Secrets werden nur aus dem read-only `RATE_SECRET_DIR` gelesen und erscheinen nie in Snapshots, DB, Logs oder Health.

Der Standalone-Bootstrap aktiviert sofort `RUB`, `USD`, `EUR`, `KZT`, `INR` und `CNY` für `eth-ethereum`, `sol-solana`, `ton-ton`, `trx-tron` und `usdt-tron`—insgesamt 30 Policy-Targets. Das normalisierte Gateway bündelt und cached die Aufrufe. CoinGecko und CoinPaprika liefern die zwei Krypto-Beobachtungen; KZT nutzt zusätzlich den täglichen offiziellen USD/KZT-Kurs der Nationalbank Kasachstans, da beide Anbieter KZT nicht direkt quotieren. `rate_gateway_origin` muss beim Standalone-Bootstrap der öffentliche HTTPS-API-Origin sein.

Pflicht-Env: `DATABASE_URL`, UUID `RATE_WORKER_ID`, striktes globales `RATE_TARGETS_JSON`. Tenant-Targets werden abgelehnt; `base_asset` muss eine aktive `assets.id`, `quote_asset` ein dreistelliger Fiat-Code sein. Optional: `RATE_SECRET_DIR`, `RATE_POLL_INTERVAL=5s`, `RATE_LEASE_DURATION=30s`, `RATE_MAX_ATTEMPTS=8`, `RATE_MAX_READY_AGE=2m`, `RATE_HEALTH_ADDRESS=:9092`. NOLOGIN-Rolle `rate_runtime_worker` vor Migration 000007 anlegen (oder Grants danach anwenden), vom Workload-Login erben lassen und enabled Identity anlegen.

`/healthz` prüft PostgreSQL; `/readyz` verlangt für jedes Target einen frischen, nicht abgelaufenen Tick und keine Dead-Letter. Der Commit speichert immutable Provenance und projiziert denselben Tick in die von `PersistedPlanner` gelesene `asset_rate_ticks`; Unique Pair und Policy Binding verhindern Mehrdeutigkeit. Stale/divergent wird nie auswählbar.
