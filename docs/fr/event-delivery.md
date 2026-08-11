# Exploitation de la livraison JetStream

PostgreSQL reste la source de vérité des événements. JetStream est seulement
une aide optionnelle de livraison au moins une fois ; `GET /v1/events` récupère
toujours depuis PostgreSQL et ne bascule jamais silencieusement vers le broker.

N'activez le worker outbox qu'après le provisionnement administratif de
`MERCHANT_EVENTS_V1` pour le sujet fixe `merchant.events.v1` : au moins trois
réplicas, limites finies d'âge/octets/messages/enveloppe de 1 Mio, suppression
des plus anciens, fenêtre de déduplication supérieure au délai maximal de
reprise, Delete/Purge interdits. Aucun identifiant tenant dans les sujets.

Le worker n'accepte que `tls://` avec TLS 1.3, CA et nom serveur épinglés,
certificat client et exactement un fichier externe credentials ou token. Le
port 4222 et la santé restent privés. Readiness vérifie PostgreSQL, NATS et la
politique exacte. En panne, laissez les lignes en attente sans changer de
transport. Seul un ack du bon stream avec séquence non nulle autorise le
marquage publié ; les reprises gardent `Nats-Msg-Id=event_id`.

Le consumer pull de référence confirme après le commit atomique inbox/effet ;
un doublon est un succès. La mise en production exige des preuves réelles TLS,
dérive de politique, ack perdu, backpressure, panne DB après ack et redelivery.
Les tests locaux ne prouvent pas un cluster actif.
