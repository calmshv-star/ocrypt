# Guide d’intégration API

## Frontière de confiance et identifiants

Utilisez un client HMAC distinct par service et environnement. Un backend de paiement requiert généralement `payments:write`, `payments:read` et `events:read`; les exports doivent utiliser un identifiant séparé avec `reconciliation:read`. N’ajoutez `payment-links:read`, `payment-links:write` ou `checkout:write` qu’en cas de besoin. N’exposez jamais le secret au navigateur, mobile, bot, URL, journal ou support.

Les requêtes merchant utilisent l’origine API; les aliases payment-link/checkout utilisent l’origine management/gateway. Les jetons publics `pl_…` et `cs_…` sont des capabilities bearer à forte entropie, limitées dans le temps, les actions ou le nombre d’usages.

## Devises de facturation et taux

La devise de facturation n’est pas limitée à RUB. `currency` contient exactement trois lettres ASCII majuscules et doit utiliser un code ISO 4217 ; `currency_scale` déclare explicitement le nombre de décimales. `RUB`, `USD`, `EUR`, `KZT`, `INR` et `CNY` utilisent normalement l’échelle `2` : `amount_minor: "3813"` signifie donc `38,13` dans la devise choisie. L’API ne déduit pas l’échelle du code.

Accepter un code devise ne rend pas automatiquement une cotation crypto disponible. Avant de créer une route on-chain ou hosted, la production exige un taux récent et admis pour la paire exacte `asset_id`/devise. Un taux absent, périmé, sans quorum, daté du futur ou trop divergent échoue en mode fermé ; configurez et admettez des sources normalisées indépendantes pour chaque devise vendue.

## Cycle du paiement

1. Créez un intent avec `merchant_order_id` unique, `amount_minor` exact sous forme de chaîne, scale, expiration et routes autorisées.
2. Après le choix réseau/actif, créez le route et conservez montant atomique, adresse/memo, expiration du devis et `grace_ends_at`.
3. N’affichez que les données API. Un reçu ou payment proof aide à rechercher mais ne prouve pas le règlement.
4. Vérifiez et traitez durablement le webhook. Livrez normalement uniquement sur `payment.settled`, de façon idempotente.
5. Réparez les trous avec `GET /v1/events?after_sequence=N`; les livraisons peuvent être dupliquées ou désordonnées.

Cancel/expire n’effacent pas l’historique on-chain. Jusqu’à la fin de grace, un transfert tardif peut mener à review ou settlement; ne recyclez pas l’adresse immédiatement. Metadata ne remplace que les champs non financiers autorisés et exige `expected_version`; après `409`, relisez et décidez à nouveau.

## Signature, reprises et webhooks

Signez les octets exacts et le path/query canonique; un nonce n’est utilisé qu’une fois. Reprenez erreurs réseau, `429` et `5xx` admis avec backoff exponentiel et jitter en gardant body/idempotency key. Ne reprenez pas automatiquement validation ou conflit de version.

Vérifiez le webhook brut avant JSON: digest, temps, event ID, key ID et HMAC de `<event-id>.<timestamp>.<raw-body>`. Pendant une rotation, gardez les deux clés. Dans une transaction, revendiquez `(event_id, body_digest)`, modifiez la commande, écrivez le fulfillment outbox et commit; répondez ensuite seulement avec l’acknowledgement. Un même ID avec un digest différent est un incident.

## Payment links, checkout et rapprochement

Un payment link contient actuellement une seule route. `public_url` n’est révélé qu’à la création ou au replay exact; list/get ne révèlent pas le secret. Redeem consomme un usage et crée atomiquement intent/quote/address/route puis émet `cs_…`. Les return URLs sont fixes. L’embedded checkout doit être lié à un HTTPS Origin exact; construisez les explorer URLs depuis votre propre allowlist.

Un rapport couvre au plus 366 jours et jamais le futur. À l’état `ready`, vérifiez taille, SHA-256, `signing_key_id` figé et signature Ed25519 avant JSONL. Conservez les anciennes clés publiques pendant toute la rétention. Le header fige ledger sequence/cutoff global et le footer contient des totaux exacts en chaînes.

## Sandbox et production

Testez exact, partial, over, late, wrong asset, duplicate delivery, settle-then-reorg, trous d’événements, rotation des clés et reprise après timeout. Le sandbox ne vaut pas admission production: fournisseurs réels indépendants, exercices finality/reorg et restore, images épinglées, rotation et charge restent obligatoires.

Team/settings est une API de cabinet séparée, hors SDK HMAC. Le contrat backend existe mais BFF/navigateur reste pre-release; consultez [paramètres de l’équipe](merchant-team-settings.md) après livraison.

Les adaptateurs transactionnels FastAPI/Django, Laravel/Symfony, Express/NestJS, Spring Boot, ASP.NET, Telegram et commerce générique sont dans l’[index des skeletons](../../examples/frameworks/README.md). Ce sont des modèles d’adaptation, pas des dépendances installées.
