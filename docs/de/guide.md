# Universelle Krypto-Zahlungsplattform

Dieser Leitfaden beschreibt Produkt, Entwicklung und Betrieb einer
eigenständigen, universell einsetzbaren Krypto-Zahlungsplattform.

## Produktleitfaden

### Aufgabe

Ein Händler erzeugt einen Payment Intent. Die Plattform fixiert ein Angebot,
stellt eine Netzwerkroute bereit, beobachtet die Blockchain, speichert jeden
Transfer als unveränderliche Tatsache, ordnet ihn zu, verbucht das Settlement im
Ledger und liefert ein signiertes Ereignis aus.

Der Händler bleibt für Bestellung, Kunde, Bestand, Abonnement oder Guthaben
verantwortlich. Die Plattform bestätigt die Zahlung, liefert aber niemals das
Geschäftsprodukt des Händlers aus.

### Vollständiger Umfang

- Server-API, gehosteter und eingebetteter Checkout sowie Payment Links.
- Native Assets und Token auf EVM, TRON, Solana, TON und freigegebenen
  Move-Adaptern.
- Exakte, Teil-, Unter-, Über- und Spätzahlungen, gebührenbereinigte Zahlungen,
  falsche Assets und interne Smart-Contract-Transfers.
- Dauerhafte Cursor, mehrere RPC-Quellen, Finality-Regeln und Reorg-Verarbeitung.
- Buchungsledger, Callback-Outbox, Ereignishistorie und Abstimmung.
- Händlerportal, Operator-Warteschlange, Plattformverwaltung und deterministische
  Sandbox.
- Optionale Refund-/Sweep-Abläufe über einen isolierten Treasury-Signer. Eine
  Watch-only-Installation weist Custody-Funktionen ausdrücklich als inaktiv aus.

### Unverhandelbare Regeln

- Ein kanonisches Chain-Event darf nur einmal gutgeschrieben werden.
- Finanzbeträge verwenden niemals binäre `float`-Werte.
- Die Blockzeit, nicht die Scannerzeit, entscheidet über eine Spätzahlung.
- KI sortiert nur deterministische Kandidaten und führt kein Settlement aus.
- Manuelle Entscheidungen speichern Akteur, Grund, Version, Evidenz und Freigaben.
- Ein Reorg erzeugt einen kompensierenden Ablauf; Historie wird nicht gelöscht.
- Private Schlüssel liegen weder in API, Checkout, Scannern noch Admin-Diensten.

### Zahlungsablauf

1. Das Händler-Backend erzeugt einen Intent mit opaker Referenz und Minor-Betrag.
2. Eine Route fixiert Netzwerk, Asset, Raw-Betrag, Adresse/Memo, Quote und Ablauf.
3. Der Checkout zeigt Netzwerk und Contract/Mint, exakten Nettobetrag, QR,
   Countdown, Kopierfunktionen und Warnungen vor einem falschen Netzwerk.
4. `observed` und `confirming` informieren die Oberfläche, liefern aber nichts aus.
5. Finality und Ledger-Commit erzeugen `payment.settled`.
6. Die transaktionale Inbox des Händlers verarbeitet dieses Ereignis genau einmal.

Mehrdeutige, verspätete oder Wrong-Asset-Zahlungen gehen zur Prüfung und gehen
nicht verloren. Cancel beendet die reguläre Annahme, nicht die Chain-Beobachtung.

## Entwicklerleitfaden

### Modell und Zustände

Kernobjekte sind `payment_intent`, `route`, `transfer_event`,
`match/contribution`, `settlement`, `domain_event`, `delivery` und
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

Von `settled` führt kein Weg zurück zu `pending`. Transfers durchlaufen
`observed → confirmed → finalized`; `reorged` und `invalidated` sind eigene
Pfade.

Ein nicht zugeordneter Fall durchläuft `new → candidates_ready → bound →
verification_requested → verified → resolved`, ergänzt um
`approval_required`, `verification_retry`, `conflict` und `reorged`. Die
Freigabe einer Unterzahlung oder eines anderen Assets erfordert eine zweite
Person. Die Verifikation lädt gespeicherte Evidenz und Chain-Daten neu; ein
Operator gibt keinen Gutschriftsbetrag ein.

### Minimaler API-Ablauf

```http
POST /v1/payment-intents
Idempotency-Key: bestellung-2026-00042

{
  "merchant_order_id": "bestellung-2026-00042",
  "amount_minor": "49900",
  "currency": "EUR",
  "currency_scale": 2,
  "customer_reference": "kunde-opak-17",
  "expires_in": 900,
  "allowed_routes": [{"provider": "on_chain", "chain_id": "tron:mainnet", "asset_id": "usdt-tron"}]
}
```

JSON-Beträge sind Strings mit explizitem Scale/Decimals. Jede Mutation benötigt
einen Idempotenzschlüssel. Gleicher Schlüssel und Body liefern die ursprüngliche
Ressource; geänderte unveränderliche Daten ergeben `idempotency_conflict`.

Die API umfasst Intents, Routes, Cancel, Payment Proofs als reinen Suchhinweis,
Event History, Transfers, Reconciliation, Assets und Sandbox-Simulationen.

### Request-Signatur

```text
HMAC-SHA256(secret,
  METHOD + "\n" + CANONICAL_PATH_AND_QUERY + "\n" +
  TIMESTAMP + "\n" + NONCE + "\n" + SHA256_HEX(RAW_BODY))
```

Übertragen werden Key ID, Timestamp, zufällige Nonce, `Content-Digest` und
Signatur. Live- und Sandbox-Credentials sind getrennt; Ed25519 und mTLS stehen
für Integrationen mit höherem Schutzbedarf bereit.

### Webhooks sicher konsumieren

Nur `payment.settled` löst normalerweise die Produktausgabe aus:

1. Größe begrenzen und Raw Body lesen;
2. Key ID, Timestamp, Nonce/Delivery ID, Digest und Signatur prüfen, bevor JSON
   vertraut wird;
3. Händler, Umgebung, Bestellung, Betrag, Währung und Ereignistyp abgleichen;
4. `(event_id, body_digest)` in einer DB-Transaktion eindeutig in die Inbox
   einfügen;
5. ein identisches Duplikat ohne zweiten Effekt bestätigen;
6. bei gleichem ID und anderem Digest 409 liefern und alarmieren;
7. Bestellung ändern und Fulfillment-Outbox in derselben Transaktion schreiben;
8. committen und `acknowledged_event_id` zurückgeben.

Retries behalten Event ID und Canonical Body, erneuern aber Delivery ID,
Timestamp, Nonce und Signatur. HTTP-Reihenfolge ist nicht garantiert.
Ausführbare Beispiele: [`../../examples`](../../examples/README.md).

## Betriebsleitfaden

### Deployment und Zuverlässigkeit

- Zustandslose API und Admin-BFF hinter Ingress/WAF.
- PostgreSQL als einzige finanzielle Wahrheit.
- Transaktionale Outbox und geleaste Worker; NATS/Redis ersetzen das Ledger nicht.
- Netzwerk-Indexer mit dauerhaftem Cursor und RPC-Fallback/Quorum.
- Separate Worker für Delivery, Rates, Ablauf und Abstimmung.
- Isolierter Treasury-Signer mit Freigaberichtlinien.

### Observability

Intent, Route, Transfer, Match, Settlement, Event und Delivery werden korreliert.
Überwacht werden Scanner-Lag, RPC-Abweichungen, Settlement-Latenz, Alter der
Unmatched-Queue, Callback-Retries/Dead Letters, Idempotenzkonflikte,
Signatur-/Replay-Fehler, Ledger-Differenzen, Quote-Alter, Reorgs und manuelle
Freigaben.

### Vorfälle, Sicherung und Freigabe

Wenn möglich wird nur das betroffene Asset pausiert. Runbooks behandeln
RPC-Ausfall, Scanner-Lag, Reorg, Callback-Ausfall, wachsende Unmatched-Queue,
Kursanomalie, kompromittierte Schlüssel, Ledger-Abweichung, Signer und
Datenbankwiederherstellung. Finanzhistorie wird nie per manuellem `UPDATE`
repariert.

Erforderlich sind verschlüsseltes kontinuierliches WAL, getestetes PITR,
regelmäßige Restores sowie partitioniertes, redigiertes Audit mit WORM-Archiv
nach Richtlinie. Production verlangt Contract-, Concurrency-, Duplicate-,
Exact-Money-, Reorg-, Security-, i18n-, Accessibility-, Last-, Restore- und
Reconciliation-Tests: [`../TEST_PLAN.md`](../TEST_PLAN.md).
