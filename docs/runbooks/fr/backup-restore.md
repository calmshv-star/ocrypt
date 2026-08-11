# Sauvegarde et restauration PostgreSQL

## Objectif et responsabilités

Protéger intents, observations chaîne, idempotence, matching, ledger, outbox et
curseurs. Le responsable DB opère les sauvegardes ; le responsable d'incident
autorise la restauration ; risque/finance signe le rapprochement ; sécurité contrôle
les credentials. Objectifs : RPO maximal de 5 minutes et RTO de 4 heures, sauf SLA plus strict.

Sont obligatoires : WAL/PITR continu chiffré, export logique quotidien, copie chiffrée
immuable dans un autre compte/domaine de panne, surveillance rétention/fraîcheur/gaps/
checksums, identités runtime/migration/backup/restore distinctes, exercice isolé
trimestriel et exercice régional annuel. « Backup enabled » n'est pas une preuve :
conservez temps, continuité WAL, checksums, récupération des clés, durée, résultat et double visa.

## Décision avant restauration

1. Déclarez l'incident, stoppez releases/migrations et distinguez panne, corruption,
   compromission ou lenteur. En cas de risque d'intégrité, fencez les écritures et
   notez le dernier instant UTC fiable avec justification.
2. Préservez logs, timelines, WAL, snapshots, preuve chaîne et digests. Choisissez
   PITR pour suppression/erreur et restauration logique pour validation indépendante.
3. Ne restaurez jamais sur la DB actuelle. Créez une cible neuve sans accès réseau
   des applications. Un second relecteur approuve cible, point, backup, clés et perte attendue.

## Restaurer et valider

1. Restaurez avec l'outil fournisseur dans une nouvelle instance/compte : réseau
   privé, TLS, chiffrement, audit et credential temporaire. Bloquez applications/callbacks.
2. Pour une archive logique, utilisez des outils PostgreSQL compatibles, arrêtez-vous
   à la première erreur, conservez version/sortie et n'importez pas owner/ACL ouvrant
   des droits. Appliquez uniquement des migrations forward compatibles, jamais down.
3. Vérifiez intégrité, constraints/FK, extensions/collations, RLS enabled/forced et grants minimaux.
4. Rapprochez au minimum :
   - volumes/états intents et routes par tenant et période ;
   - unicité transfer identity et active payment matches ;
   - somme nulle des entries de chaque ledger transaction ;
   - idempotency keys et response hashes ;
   - unmatched/manual resolutions et approbateurs ;
   - identité, ordre, attempts et état terminal callback/outbox ;
   - continuité scanner cursor/gaps face à une finalized chain indépendante ;
   - assignments/reservations sans leases actifs conflictuels.
5. Comparez l'intervalle RPO perdu aux preuves immuables et préparez la liste
   replay/compensation approuvée. Aucun crédit en masse sur montant, heure, capture ou IA.

## Bascule

1. Créez de nouveaux credentials runtime minimaux, actualisez les Secrets externes
   et testez depuis des pods canary isolés. Ne réutilisez pas le credential de restore.
2. Démarrez API ingress fermé, puis settlement, callback et scanner séparément.
   Scanner reste désactivé sans release gate. Testez API signée/idempotente, file et callback.
3. Ouvrez graduellement en surveillant readiness, erreurs DB, âge, doublons, quorum et
   callbacks. Gardez l'ancienne DB fenced/read-only pendant la fenêtre de rollback.
4. Exécutez uniquement replay/compensations revus, avec IDs stables et double contrôle.
   Informez le merchant de l'intervalle exact et de la remédiation.
5. Clôturez après mesure RPO/RTO, signature du rapprochement, validation du backup du
   nouveau primary, test des alertes, révocation des anciens credentials et conservation des preuves.

L'exercice échoue si les clés manquent, WAL a un gap, la cible n'est pas isolée, la
release fixée ne démarre pas, le ledger n'est pas équilibré, les contraintes unique/
idempotence diffèrent, la continuité scanner n'est pas prouvée, les objectifs sont
manqués sans risque accepté ou deux approbateurs indépendants sont absents.
