# Plateforme universelle de paiement crypto

Ce guide résume les volets produit, développement et exploitation d’une
plateforme de paiement en cryptomonnaies autonome et polyvalente.

## Guide produit

### Mission

Le marchand crée une intention de paiement. La plateforme fige un devis, émet
une route réseau, observe la blockchain, conserve chaque transfert comme un fait
immuable, l'apparie, inscrit le règlement au ledger puis livre un événement
signé.

Le marchand reste responsable de sa commande, de son client, de son stock, de
son abonnement ou de son solde. La plateforme atteste le paiement, mais ne livre
jamais le produit métier du marchand.

### Périmètre complet

- API serveur à serveur, checkout hébergé ou intégré, liens de paiement.
- Actifs natifs et tokens sur EVM, TRON, Solana, TON et adaptateurs Move validés.
- Paiements exacts, partiels, insuffisants, excédentaires, tardifs, frais déduits,
  mauvais actif et transferts internes de smart contracts.
- Curseurs persistants, plusieurs RPC, politiques de finalité et gestion des
  réorganisations.
- Ledger comptable, outbox de callbacks, historique d'événements et
  rapprochement.
- Portail marchand, file opérateur, administration plateforme et sandbox
  déterministe.
- Refund/sweep facultatifs via un signataire de trésorerie isolé. Une installation
  watch-only annonce clairement l'absence de fonctions de conservation.

### Invariants

- Un événement canonique on-chain n'est crédité qu'une seule fois.
- Aucun `float` binaire dans la logique financière.
- L'heure du bloc, et non l'heure de découverte, détermine le retard.
- L'IA classe des candidats déterministes mais ne règle aucun paiement.
- Une décision manuelle conserve auteur, motif, version, preuve et approbations.
- Un reorg produit un processus compensatoire au lieu d'effacer l'historique.
- Les clés privées ne résident ni dans l'API, ni dans le checkout, les scanners
  ou l'administration.

### Parcours de paiement

1. Le backend marchand crée un intent avec référence opaque et montant minor.
2. Une route fige réseau, actif, montant raw, adresse/memo, devis et expiration.
3. Le checkout affiche réseau et contract/mint, montant net exact, QR, compteur,
   copie sûre et avertissement de mauvais réseau.
4. `observed` et `confirming` informent l'interface sans livrer le produit.
5. La finalité puis le commit du ledger créent `payment.settled`.
6. L'inbox transactionnel du marchand applique cet événement une seule fois.

Les paiements ambigus, tardifs ou dans le mauvais actif passent en revue au lieu
de disparaître. Annuler un intent n'arrête pas l'observation on-chain.

## Guide développeur

### Modèle et états

Les objets essentiels sont `payment_intent`, `route`, `transfer_event`,
`match/contribution`, `settlement`, `domain_event`, `delivery` et
`unmatched_case`.

```text
created → awaiting_route_selection → pending → observed → confirmed → settled
                                      │          └→ partially_paid
                                      ├→ expired → needs_review
                                      └→ cancelled
needs_review → settled | reversed
settled → overpaid
confirmed/settled/overpaid → reorg_review → settled | reversed
```

`settled` ne retourne jamais à `pending`. Le transfert suit
`observed → confirmed → finalized`, avec des chemins explicites `reorged` et
`invalidated`.

Un dossier non apparié suit `new → candidates_ready → bound →
verification_requested → verified → resolved`, avec les branches explicites
`approval_required`, `verification_retry`, `conflict` et `reorged`. Accepter un
montant insuffisant ou un autre actif exige un second opérateur. La vérification
relit la preuve stockée et la chaîne ; aucun opérateur ne saisit le montant
crédité.

### Flux API minimal

```http
POST /v1/payment-intents
Idempotency-Key: commande-2026-00042

{
  "merchant_order_id": "commande-2026-00042",
  "amount_minor": "49900",
  "currency": "EUR",
  "currency_scale": 2,
  "customer_reference": "client-opaque-17",
  "expires_in": 900,
  "allowed_routes": [{"provider": "on_chain", "chain_id": "tron:mainnet", "asset_id": "usdt-tron"}]
}
```

Les montants JSON sont des chaînes assorties d'un scale/decimals explicite.
Chaque mutation exige une clé d'idempotence : même clé et même corps renvoient la
ressource d'origine ; des données immuables différentes produisent
`idempotency_conflict`.

La surface comprend intents, routes, annulation, payment proofs comme simple
indice, historique d'événements, transfers, rapprochement, assets et simulation
sandbox.

### Signature des requêtes

```text
HMAC-SHA256(secret,
  METHOD + "\n" + CANONICAL_PATH_AND_QUERY + "\n" +
  TIMESTAMP + "\n" + NONCE + "\n" + SHA256_HEX(RAW_BODY))
```

Envoyez key ID, timestamp, nonce aléatoire, `Content-Digest` et signature. Les
identifiants live et sandbox sont séparés ; Ed25519 et mTLS sont disponibles pour
les intégrations à assurance renforcée.

### Consommer les webhooks

Seul `payment.settled` déclenche normalement la livraison métier :

1. limiter puis lire le corps brut ;
2. vérifier key ID, timestamp, nonce/delivery ID, digest et signature avant de
   faire confiance au JSON ;
3. contrôler marchand, environnement, commande, montant, devise et type ;
4. insérer `(event_id, body_digest)` dans une inbox unique, en transaction ;
5. acquitter un doublon identique sans répéter l'effet ;
6. répondre 409 et alerter si le même ID porte un autre digest ;
7. mettre à jour la commande et écrire un fulfillment outbox dans la même
   transaction ;
8. commit puis renvoyer `acknowledged_event_id`.

Une nouvelle livraison conserve event ID et canonical body, mais renouvelle
delivery ID, timestamp, nonce et signature. L'ordre HTTP n'est pas garanti. Les
exemples exécutables sont dans [`../../examples`](../../examples/README.md).

## Guide d'exploitation

### Déploiement et fiabilité

- API et admin BFF stateless derrière ingress/WAF.
- PostgreSQL comme unique source de vérité financière.
- Outbox transactionnel et workers à lease ; NATS/Redis ne remplacent pas le
  ledger.
- Indexeurs par réseau avec curseur durable et fallback/quorum RPC.
- Workers distincts pour delivery, rates, expiration et rapprochement.
- Signataire de trésorerie isolé soumis aux règles d'approbation.

### Observabilité

Corrélez intent, route, transfer, match, settlement, event et delivery. Surveillez
le retard des scanners, les désaccords RPC, les latences jusqu'au settlement,
l'âge de la file unmatched, les retries/dead letters, les conflits
d'idempotence, les échecs de signature/replay, les écarts du ledger, l'âge des
devis, les reorgs et les décisions manuelles.

### Incidents, sauvegardes et mise en production

Suspendez si possible uniquement l'actif défaillant. Les runbooks couvrent panne
RPC, scanner en retard, reorg, callback indisponible, croissance unmatched,
anomalie de cours, clé compromise, écart comptable, signer et restauration DB.
On ne répare jamais l'historique financier par un `UPDATE` manuel.

Il faut un WAL continu chiffré, un PITR testé, des restaurations régulières, un
audit partitionné et expurgé, ainsi qu'un archivage WORM selon la politique. La
production exige les tests de contrat, concurrence, doublon, montant exact,
reorg, sécurité, i18n, accessibilité, charge, restauration et rapprochement :
[`../TEST_PLAN.md`](../TEST_PLAN.md).
