# Équipe marchand et paramètres du projet

Le module est isolé par tenant et marchand. Il ne gère que les accès humains et les préférences non financières. Taux, actifs, réseaux, finality, matching, règlement et trésorerie restent dans les plans de contrôle dédiés.

Le navigateur n'appelle jamais le service privé et ne fournit ni acteur, tenant, marchand, permission ni approbateur. Le BFF valide session OIDC, e-mail vérifié, issuer/subject, appartenance et MFA, puis signe une assertion `MerchantSettingsAdmin` à usage unique d'une minute, liée à la méthode, au path/query canonique et au SHA-256 du corps. BFF et API communiquent en mTLS; l'API relit les permissions dans PostgreSQL.

## Rôles et double contrôle

`owner` dispose de tout; `security_admin` gère la sécurité; `admin` gère l'équipe ordinaire et les paramètres; `developer` lit l'équipe et modifie les paramètres; `support` et `viewer` lisent. Les rôles sont fermés et aucun client ne fournit des permissions. Ajouter/retirer `owner` ou `security_admin`, ou désactiver/supprimer leur détenteur, nécessite une demande durable et un autre acteur MFA actif dans une autre session. Auto-approbation et changement de son propre rôle sont refusés. Hash, version, identités, droits, sessions, MFA et expiration sont revérifiés dans une transaction sérialisable. La base interdit la perte du dernier owner actif, même en concurrence.

## Invitations

Seuls `admin`, `developer`, `support`, `viewer` sont invitables directement. L'acceptation est unique, avant expiration, par une identité OIDC dont l'e-mail vérifié normalisé correspond. Le membre est lié à admin user, issuer, subject et e-mail; l'auteur du grant reste l'invitant.

Un POST même origine propre aux invitations décode le jeton canonique de 43 caractères, calcule son SHA-256 et résout l’invitation avant OIDC. Le jeton brut n’est jamais stocké ni journalisé et n’entre ni dans state, return path, cookie ou URL envoyée au BFF. Le navigateur retire immédiatement le fragment de livraison et conserve le jeton uniquement dans `sessionStorage`. State, nonce, PKCE, MFA, issuer/subject exacts, `email_verified` et e-mail invité correspondant sont obligatoires. Une nouvelle identité devient `invited`, auditée et sans grant plateforme ; sa session ne peut qu’accepter cette invitation. Adhésion, consommation, activation et promotion de session sont atomiques dans une transaction PostgreSQL. Les inscriptions expirées restent inertes et leurs sessions sont révoquées. Seule la même session peut rejouer la même clé idempotente après une réponse perdue.

`copy_once` s'active atomiquement et révèle le jeton de 43 caractères uniquement dans la première réponse; aucun replay ne le révèle. `email` crée un job durable, admis seulement avec heartbeat récent et key ring complet. Le worker utilise invitation ID comme clé d'idempotence fournisseur, loue et réessaie de façon bornée, puis active après ACK durable. Expiration et dead letter sont audités.

PostgreSQL ne stocke que SHA-256 et un key ID non secret. Le jeton vaut `HMAC-SHA256(delivery_key, "merchant-invite-v1\0" || tenant_id || merchant_id || invitation_id)`. Cette clé est dédiée. Rotation: ajouter la nouvelle, la rendre current, attendre la fin des jobs de l'ancien key ID, puis le retirer; un retrait prématuré bloque le démarrage. Une compromission touche les invitations connues de cette clé jusqu'à expiry/revoke, pas les clés API ni les secrets financiers.

Désactivation/suppression émet un signal durable; un worker minimal révoque les sessions admin et acquitte atomiquement. Les paramètres versionnés couvrent nom, locale `en/zh-CN/es/fr/de/ru`, fuseau IANA, e-mail support optionnel, notifications et 100 origines HTTPS exactes maximum. HTTP, wildcard, credentials, path, query et fragment sont interdits. JSON inconnu/dupliqué, secrets et politiques financières sont rejetés. Chaque mise à jour exige version et motif, crée un snapshot immuable et une audit chain SHA-256.

API privée TLS 1.3 `:8447`, health `:9095`; révocation `:9096`; livraison `:9097`. L'e-mail est fail-closed. Ne jamais journaliser assertions, jetons, bearer, key ring, jetons OIDC ou réponse fournisseur complète. Contrat: `contracts/merchant-settings-openapi.yaml`; chaque mutation exige `Idempotency-Key`.
