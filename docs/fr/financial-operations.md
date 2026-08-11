# Opérations financières

Ce sous-système fournit des sweeps de trésorerie isolés par locataire, des remboursements vérifiés et une réconciliation déterministe. Les montants sont des chaînes uint256 canoniques en unités atomiques; aucun flottant n'est accepté.

## Modèle de sécurité

Chaque écriture utilise PostgreSQL `SERIALIZABLE` avec RLS locataire forcé. La clé d'idempotence est verrouillée et liée à l'empreinte SHA-256. Agrégat, réservations, écriture comptable équilibrée, audit chaîné et outbox sont validés atomiquement. Source/nonce, limite journalière et montant remboursable sont réservés sous verrou. L'expéditeur observé n'est jamais une preuve de propriété; l'origine vérifiée est la valeur sûre par défaut. L'approbation exige un step-up et un second opérateur.

## Isolation d'exécution

Un transfert finalisé est une preuve de paiement, pas de propriété. Refund reste fail-closed jusqu'à ce qu'un vérificateur wallet-signature/custodian admis séparément écrive `financial_verified_refund_destinations`; aucun expéditeur CEX, contrat ou hot-wallet n'est approuvé automatiquement.

L'API opérateur ne possède aucune route build/sign/broadcast. `financial-worker` avance une étape par fencing token. Builder, signer, broadcaster, vérificateur de finalité indépendant et event sink utilisent cinq origines HTTPS et cinq credentials distincts. Redirections et proxy d'environnement sont désactivés. Chaque effet externe porte une clé d'idempotence stable et une liaison à l'agrégat. Aucune clé privée blockchain n'est stockée.

La finalité provient du vérificateur quorum admis séparément, jamais du signer/broadcaster. Finalité et reorg d'un remboursement créent des écritures immuables d'équilibrage/inversion. L'outbox utilise `SKIP LOCKED`, tokens de lease monotones, retry, dead-letter après 20 essais et accusé portant le même event ID.

## API et exploitation

Voir `contracts/financial-openapi.yaml`. Le proxy IAM signe tenant, actor, permissions triées, step-up borné, timestamp, nonce, chemin/requête et digest du corps. `financial_proxy_nonces` est indépendant des clients merchant. La lecture exige `financial:read`.

L'API exige base de données, TLS et `FINANCIAL_OPERATOR_ASSERTION_SECRET_FILE`. Le worker exige les UUID tenant explicites et les couples distincts `FINANCIAL_{BUILDER,SIGNER,BROADCASTER,FINALITY,EVENT_SINK}_{URL,TOKEN_FILE}`. `:9093` convient aux probes Kubernetes Pod mais ne doit pas être exposé publiquement.

L'audit est chaîné SHA-256 par tenant via `append_financial_audit`; ancrer régulièrement le dernier hash hors PostgreSQL. Avant activation: migration up/down/up, droits minimaux, test RLS à deux tenants, cérémonie KMS/HSM/MPC, admission des providers, puis tests réponse perdue, stale fence, reorg, dead-letter, sauvegarde/restauration et vérification de chaîne.

## Cabinet financier d’administration

Le navigateur communique uniquement avec l’Admin BFF de même origine. Le BFF dérive des rôles courants en base une liste fermée de droits à l’échelle du locataire; il refuse les rôles limités au marchand et tout droit fourni par le navigateur. L’opérateur de trésorerie peut demander ou annuler sweeps et remboursements, puis demander un rapprochement. Un approbateur senior distinct peut approuver et exécuter le rapprochement. Support et opérateurs de paiement ne reçoivent aucun droit financier.

Chaque mutation exige CSRF, Origin exact, version courante, motif et `Idempotency-Key`. Le replay d’une décision est enregistré atomiquement avec l’agrégat, l’audit et l’outbox; réutiliser la clé avec une autre méthode, route ou corps produit un conflit. L’approbation exige une MFA récente et un autre acteur. Les montants atomiques restent des chaînes dans l’UI et l’API.

Le BFF joint l’API financière privée en TLS 1.3 avec CA épinglée, nom serveur explicite et certificat client vérifié. La supervision possède un certificat client distinct et minimal; les endpoints de santé ne passent pas en clair. Redirects et proxies d’environnement sont interdits. Le navigateur ne reçoit ni origine interne, ni secret d’assertion, ni donnée de custody; aucune route build/sign/broadcast/exécution monétaire n’est exposée. La custody réelle reste désactivée et fail-closed, sans prétention d’admission d’un provider.
