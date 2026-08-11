# Rétention et archives immuables

Le worker de rétention est un plan de données d'archivage à fermeture sûre. Il ne supprime aucun enregistrement métier. Il écrit des archives déterministes signées Ed25519 dans un bucket S3 versionné avec Object Lock `COMPLIANCE`, vérifie la version exacte, la taille, le SHA-256 et la date de rétention, puis inscrit seulement alors les preuves immuables dans PostgreSQL.

Les classes fermées sont `callback_event_body`, `published_outbox_payload` et `event_history_payload`. Les corps de callback et l'historique restent en mode archive seule, car la relivraison et les lectures marchandes utilisent encore leurs octets actifs. Seul un payload d'outbox publié peut devenir un objet `retention-tombstone/v1`, à condition qu'une copie strictement identique existe dans `event_history`. Identifiants, portée, types, séquences, horodatages, empreinte originale et référence objet sont conservés.

L'élagage exige une politique locataire versionnée et effective, l'âge requis, une preuve WORM vérifiée, aucune conservation légale active, aucune dépendance de livraison ou de rejeu, un lease/fence valide et deux contrôles séparés par le délai de grâce. La transaction SERIALIZABLE finale répète toutes les vérifications sous le même verrou consultatif que les changements de legal hold.

Le worker exige `APP_ENV=production`, `RETENTION_OBJECT_STORE=s3`, un `RETENTION_WORKER_ID` unique, une URL de base dédiée et des secrets S3/Ed25519 montés en fichiers. `/healthz` et `/readyz` restent privés. La readiness échoue si le schéma ou les droits manquent, si Object Lock/versioning n'est pas admis ou si un lease est périmé.

La migration 000022 ajoute le plan de contrôle par locataire sans DML direct. Une demande lie version et fence attendus, requiert l'approbation d'une autre personne avec MFA récent et ne s'active pas avant l'heure planifiée par l'horloge de la base. Callback et historique restent archive-only ; l'outbox conserve le second cycle de grâce. Une rétention réduite est refusée face à un hold actif ou à un Object Lock déjà admis.

Un legal hold devient immédiatement actif après `retention:hold_create` et MFA récent. Un seul scope tenant, merchant ou record est vérifié sans fuite d'existence inter-locataire. La levée exige toujours une demande et un approbateur différent du demandeur et du créateur. L'expiration est une transition worker explicite et auditée après `expires_at`. Le navigateur ne reçoit que référence de dossier bornée, état, empreintes, compteurs et identité tombstone, jamais corps, payload, clé/version objet ni credential.

Les droits exacts sont `retention:read`, `retention:policy_request`, `retention:policy_approve`, `retention:hold_create` et `retention:hold_release`. `/readyz` échoue sans 000022 ou ses capacités. API et worker n'ont aucun `UPDATE`/`DELETE` direct sur sources ou preuves.

Après une réponse PUT perdue, seul un HEAD immuable exactement conforme est accepté. Toute différence bloque l'acquittement et l'élagage. Un rollback ne peut pas recréer un payload déjà tombstoné ; 000015 doit rester en place jusqu'à une restauration séparément validée depuis l'archive.
