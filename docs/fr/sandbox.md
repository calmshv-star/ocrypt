# Sandbox marchand déterministe

Le sandbox est un produit de test séparé, pas un commutateur du moteur live. `/v1/sandbox/*` n'est enregistré qu'avec `APP_ENV=sandbox|test`, `SANDBOX_RUNTIME=postgres`, une base dédiée et un identifiant `mk_test_`. La production et le développement ordinaire renvoient `404` ; PostgreSQL refuse aussi les marchands live.

`GET /v1/sandbox/workspace` renvoie l'horloge, la version, les métadonnées d'identifiant expurgées, les adresses de test déterministes et le jeton HMAC de reset lié à la version. `POST /v1/sandbox/scenarios` crée un payment intent et une route propres au sandbox ; leurs UUID ne sont pas lisibles via `/v1/payment-intents`. Les montants sont des chaînes entières exactes.

Scénarios : `exact_payment`, `partial_payment`, `underpayment`, `overpayment`, `late_payment`, `wrong_asset`, `duplicate_callback`, `out_of_order_callback`, `timeout`, `dead_letter`, `reorg` et `reorg_recovery`. `POST /v1/sandbox/scenarios/{id}/actions` simule observation, confirmations, finalité, callback, reorg et reprise par étapes. Aucun settlement n'est possible sans les confirmations requises et une finalité explicite. Après un reorg, l'observation est réincluse puis reconfirmée. `POST .../{id}/run` exécute le modèle atomiquement et de façon idempotente.

`GET /v1/sandbox/callbacks` expose le JSON canonique, son SHA-256, les tentatives bornées et le statut avec pagination par curseur. Les secrets et textes de réponse/erreur ne sont ni stockés ni affichés. Reset exige une clé d'idempotence, la version courante et le jeton HMAC, et ne supprime que les lignes `sandbox_*` du marchand.

Réussir le sandbox ne remplace pas les validations de fournisseurs réels, finalité/reorg, restauration, rotation, artefacts épinglés ou charge. L'IA n'observe, ne confirme et ne règle aucun paiement.
