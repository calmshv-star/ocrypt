# Procédures d'incidents critiques

## Contrôles communs

1. Déclarez la sévérité, l'heure UTC, le responsable d'incident, l'opérateur et le
   scribe ; ajoutez la sécurité si nécessaire. Utilisez un canal unique et audité.
2. Arrêtez releases, migrations, rotations de clés, backfills et résolutions
   manuelles. Notez les derniers digests et hashes de configuration valides.
3. Consignez environnement, nombre de tenants, chaîne/actif, intervalle et transition.
   Ne publiez ni secret, ni corps de callback, ni adresse, ni donnée personnelle.
4. Isolez de façon réversible : retirez le trafic gateway, réduisez uniquement le
   worker atteint à zéro ou bloquez son egress. Ne supprimez pas de lignes, ne
   déplacez pas de curseur, ne forcez pas « paid » et n'utilisez pas de SQL ad hoc.
5. Préservez health/readiness, événements Deployment, logs expurgés, âge des files,
   heads fournisseurs, santé DB et historique des releases. Toute correction de
   solde/droit nécessite une double validation et une liste d'IDs append-only protégée.
6. Clôturez après résorption du backlog, contrôle des invariants, remédiation client,
   test des alertes, conservation des preuves et attribution des actions correctives.

## API ou worker indisponible

1. Comparez `/healthz` et `/readyz` : vivant mais non prêt pointe souvent vers DB ou
   credentials ; les deux en échec vers processus, image, scheduling, mémoire ou nœud.
2. Vérifiez replicas souhaités/prêts, redémarrages, PDB, pods Pending, présence des
   clés Secret sans afficher leur valeur, pool DB, TLS, DNS et NetworkPolicies récentes.
3. Si la release est en cause, revenez au digest enregistré du seul Deployment
   touché. Ne rétrogradez pas le schéma. Si l'intégrité DB est douteuse, stoppez les writers.
4. Avant réouverture graduelle, testez readiness, requête signée, création idempotente
   d'intent, progression settlement et un callback contrôlé.

## Retard du scanner ou désaccord des fournisseurs

Cette section ne s'applique qu'après validation production du scanner.

1. Réduisez scanner et settlement à zéro ; laissez les callbacks déjà commis. Ne
   déplacez pas le curseur et ne baissez pas finality pour rattraper le retard.
2. Enregistrez identité RPC, chain/genesis, finalized head, hash du dernier bloc
   commun, latence et erreur. Capture d'explorer ou reçu client ne suffisent pas.
3. Retirez un fournisseur uniquement si les autres sont réellement indépendants et
   gardent le quorum. Trouvez la dernière hauteur dont hash/parent concordent au quorum.
4. Rescannez avec overlap dans un stockage comparatif isolé. Rapprochez unicité du
   transfer, allowlist contrat/mint, decimals, destinataire, logs/appels internes/
   instructions et seuil de confirmations.
5. Reprenez avec un shard canary, puis settlement ; surveillez âge et doublons. Ne
   créditez jamais automatiquement unmatched ou candidat suggéré uniquement par IA.

## Réorganisation de chaîne

1. Suspendez scanner et settlement de la chaîne. Relevez anciens/nouveaux hashes,
   ancêtre commun, profondeur, quorum et event IDs touchés.
2. Déterminez si l'événement orphelin a franchi finality et modifié ledger/droits ;
   gardez les deux historiques. Ne supprimez pas observations et n'éditez pas ledger.
3. Utilisez le flux reorg/reversal prévu. Une correction financière est une nouvelle
   transaction compensatrice liée et équilibrée, sous double contrôle.
4. Rescannez avant l'ancêtre, rapprochez puis reprenez. Informez le merchant avec des
   IDs stables et l'état de remédiation, sans promettre l'irréversibilité.

## Échec des callbacks

1. Confirmez que settlement est correct : le callback notifie, il n'est pas source
   de vérité. Mesurez le plus ancien pending et les endpoints touchés.
2. Contrôlez readiness, leases, DNS/HTTPS egress, certificats, classes de réponse et
   déchiffrement de l'envelope key sans révéler URL credentialisée ni corps.
3. Après correction, laissez la file durable réessayer les IDs/signatures originaux.
   Ne créez pas de doublons et ne contournez pas SSRF. Limitez par endpoint, respectez
   backoff et validez la déduplication du destinataire.

## Hausse des paiements non identifiés

1. Stoppez matching automatique et manuel au-delà du seuil approuvé, tout en gardant
   l'ingestion. L'IA est consultative et n'autorise jamais un crédit.
2. Segmentez par chaîne, contrat/mint, destinataire, mécanisme, decimals, heure et
   version de route ; vérifiez pool d'adresses et expiration des assignments.
3. Validez la normalisation avec deux RPC indépendants et des fixtures contrats/
   exchanges. Une capture n'est pas une preuve.
4. Corrigez parser/route vers l'avant et rejouez en stockage comparatif. Toute
   résolution manuelle exige double contrôle, idempotence et unicité démontrées.

## Fuite d'un credential ou d'une clé

1. Considérez-le compromis sans demander de le renvoyer. Révoquez-le à l'autorité et
   isolez le workload. Préservez auth logs, audit Secret, flux, digest et historique.
2. Émettez un remplacement minimal, actualisez le Secret externe, redémarrez seulement
   le workload touché et confirmez le rejet de l'ancienne valeur.
3. Ne remplacez pas directement une envelope key : il faut déchiffrement versionné,
   re-chiffrement vérifié de chaque ligne et clé de rollback sous double contrôle.
   En cas de doute, prenez d'abord un snapshot.
4. Contrôlez intents, resolutions, callbacks et ledger non autorisés ; compensez sans
   effacer l'historique et notifiez selon le processus sécurité/juridique approuvé.

## Corruption DB ou split-brain

1. Coupez les écritures, arrêtez API/scanner/workers et fencez le primary suspect.
   N'autorisez jamais deux primaires écrivables ni des redémarrages répétés du stockage.
2. Préservez logs, WAL, timelines, snapshots, preuve chaîne et dernier UTC fiable.
3. Restaurez une nouvelle DB isolée, rapprochez puis basculez de manière contrôlée.
   N'écrasez jamais l'unique copie et n'improvisez pas de réparation de lignes.
