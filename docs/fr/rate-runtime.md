# Exploitation du runtime de taux

Le rate worker est fail-closed : il ne consomme que les snapshots actifs et immuables `rate_policy`/`rate_source` via `ActiveSnapshotReader`, jamais draft/approved/scheduled. Le commit revérifie snapshot ID et fence token sous verrou ; une activation concurrente annule toute la collecte.

La policy contient `base_asset`, `quote_asset`, au moins deux `sources` distinctes, `quorum`, `max_age_seconds`, `max_spread_bps`, et éventuellement `future_tolerance_seconds` (5 par défaut, 0 explicite signifie zéro tolérance) et `poll_interval_seconds` (15). Une source contient `provider_ref`, un endpoint HTTPS normalisé, la même paire, les limites et éventuellement `credential_ref`. L’URL ne peut contenir credentials, query ni fragment.

La réponse suit `contracts/rate-provider-v1.schema.json` : le prix est un couple uint256 positif numerator/denominator, jamais un JSON number/float. Avec un nombre pair de sources, la médiane observée inférieure est choisie sans moyenne ni arrondi. Tous les contrôles doivent être valides ; sinon aucun tick n’est créé.

Redirects, proxies, IP privées/loopback/link-local/réservées et DNS mixte sont bloqués ; le DNS est épinglé par connexion. TLS, timeout, content type et taille sont bornés. Les secrets sont lus uniquement sous `RATE_SECRET_DIR` en lecture seule et n’apparaissent jamais dans snapshots, BD, logs ou health.

Le bootstrap standalone active immédiatement `RUB`, `USD`, `EUR`, `KZT`, `INR` et `CNY` pour `eth-ethereum`, `sol-solana`, `ton-ton`, `trx-tron` et `usdt-tron`, soit 30 policy targets. Le gateway normalisé groupe et met en cache les appels. CoinGecko et CoinPaprika restent les deux observations crypto ; KZT utilise le taux officiel quotidien USD/KZT de la Banque nationale du Kazakhstan car ces fournisseurs ne cotent pas KZT directement. `rate_gateway_origin` doit être l’origine API HTTPS publique lors du bootstrap standalone.

Env requises : `DATABASE_URL`, UUID `RATE_WORKER_ID`, `RATE_TARGETS_JSON` global strict. Les targets tenant sont rejetées ; `base_asset` doit être un `assets.id` actif et `quote_asset` un fiat de trois caractères. Options : `RATE_SECRET_DIR`, `RATE_POLL_INTERVAL=5s`, `RATE_LEASE_DURATION=30s`, `RATE_MAX_ATTEMPTS=8`, `RATE_MAX_READY_AGE=2m`, `RATE_HEALTH_ADDRESS=:9092`. Créez le rôle NOLOGIN `rate_runtime_worker` avant 000007 (ou appliquez les grants après), faites-le hériter par le login et créez l’identity enabled.

`/healthz` vérifie PostgreSQL ; `/readyz` exige un tick récent non expiré pour chaque cible et aucun dead-letter. Le commit écrit la provenance immuable et projette le même tick dans `asset_rate_ticks` lu par `PersistedPlanner`; unique pair et policy binding évitent toute ambiguïté. Stale/divergent n’est jamais sélectionnable.
