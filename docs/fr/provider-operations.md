# Opérations des fournisseurs

Ce plan de contrôle et de santé ne publie aucun secret. Le cabinet affiche seulement
l'identité stable, l'opération, les versions de policy/circuit, l'état fermé, le
retard, les dates et les identités d'approbation — jamais endpoints, identifiants,
matériel de signature, adresses ou erreurs brutes.

Le scanner de production n'admet que les fournisseurs on-chain actifs dont la
policy immuable `rpc_provider` est actuelle, le circuit fermé et le quorum récent
réparti sur des domaines de panne indépendants. Paused, open, stale ou divergent
sont exclus ; sous le quorum le scan s'arrête. Timeouts, retries, backoff, rate
limits, priorité et limites de santé viennent de la policy approuvée.

Pause et reprise sont deux actions quatre-yeux avec MFA récent, versions, motif et
clé d'idempotence exacts ; état, audit et outbox sont atomiques. Le worker privé sur
9100 emploie des leases fenced et des probes read-only par opération ; readiness
exige un cycle réussi non vide et un groupe de pairs admissible. Hosted démarre en
pause : les callbacks signés restent une preuve append-only sans ledger/settlement,
et tout appel sortant reste refusé jusqu'à policy approuvée et probe réussi.

La demande lie les six politiques exactes et une référence de statut initial
bornée, en écriture seule, à une empreinte immuable. Cette référence n'est jamais
renvoyée dans les listes ou décisions. Après approbation MFA indépendante, des
sondes de statut réussies dans au moins deux domaines de panne puis une reprise
séparée à quatre yeux restent obligatoires.
