# Leitfaden zur API-Integration

## Vertrauensgrenze und Zugangsdaten

Verwenden Sie pro Dienst und Umgebung einen eigenen HMAC-Client. Ein Payment-Backend benötigt typischerweise `payments:write`, `payments:read` und `events:read`; Exporte sollten einen separaten Schlüssel mit `reconciliation:read` nutzen. Fügen Sie `payment-links:read`, `payment-links:write` oder `checkout:write` nur bei Bedarf hinzu. Schlüssel und Secret gehören nie in Browser, Mobile-App, Bot-UI, URL, Log oder Support-Ticket.

Merchant-Anfragen gehen an den API-Origin, Payment-Link-/Checkout-Aliases an den Management-/Gateway-Origin. Öffentliche `pl_…`- und `cs_…`-Werte sind hochentropische Bearer-Capabilities mit Zeit-, Aktions- oder Nutzungslimit.

## Rechnungswährungen und Kurse

Die Rechnungswährung ist nicht auf RUB festgelegt. `currency` besteht aus genau drei ASCII-Großbuchstaben und sollte einen ISO-4217-Code enthalten; `currency_scale` gibt die Nachkommastellen ausdrücklich an. `RUB`, `USD`, `EUR`, `KZT`, `INR` und `CNY` verwenden normalerweise Scale `2`, sodass `amount_minor: "3813"` den Betrag `38,13` in der gewählten Währung bedeutet. Die API leitet den Scale nicht aus dem Code ab.

Ein akzeptierter Währungscode allein stellt noch keine Krypto-Quote bereit. Vor dem Erstellen einer On-Chain- oder Hosted-Route benötigt Production einen frischen zugelassenen Kurs für das exakte Paar `asset_id`/Währung. Fehlende, veraltete, nicht quorate, zukünftige oder zu stark abweichende Kurse schlagen fail-closed fehl; für jede Verkaufswährung müssen unabhängige normalisierte Kursquellen konfiguriert und zugelassen werden.

## Zahlungsablauf

1. Erstellen Sie einen Intent mit eindeutiger `merchant_order_id`, exaktem String `amount_minor`, Währungsscale, Ablaufzeit und erlaubten Routen.
2. Nach Auswahl von Netzwerk/Asset erstellen Sie die Route und speichern Atomic Amount, Adresse/Memo, Quote-Ablauf und `grace_ends_at`.
3. Zeigen Sie nur API-Routendaten. Wallet-Beleg oder Payment Proof sind Suchhinweise, kein Settlement-Nachweis.
4. Verifizieren und verarbeiten Sie Webhooks dauerhaft. Erfüllen Sie normalerweise nur bei `payment.settled` und idempotent.
5. Schließen Sie Lücken über `GET /v1/events?after_sequence=N`; Zustellung kann doppelt oder ungeordnet sein.

Cancel/expire löschen keine On-Chain-Historie. Bis zum Ende des Grace-Fensters kann eine späte Zahlung zu Review oder Settlement führen; Adressen dürfen nicht sofort wiederverwendet werden. Metadata ersetzt nur erlaubte nichtfinanzielle Felder und verlangt `expected_version`; nach `409` neu lesen und entscheiden.

## Signatur, Wiederholung und Webhooks

Signieren Sie exakt serialisierte Bytes und den kanonischen Path/Query; jeder Nonce ist einmalig. Transportfehler, `429` und zulässige `5xx` werden mit exponentiellem Backoff und Jitter bei gleichem Body/Idempotency Key wiederholt. Validierungs- oder Versionskonflikte nicht automatisch wiederholen.

Prüfen Sie rohe Webhook-Bytes vor JSON: Digest, Zeit, Event ID, Key ID und HMAC über `<event-id>.<timestamp>.<raw-body>`. Während Rotation beide Schlüssel behalten. Beanspruchen Sie `(event_id, body_digest)`, ändern Bestellung, schreiben Fulfillment-Outbox und committen in einer Transaktion; erst dann acknowledgement senden. Gleiche ID mit anderem Digest ist ein Vorfall.

## Payment Links, Checkout und Abstimmung

Ein Payment Link bindet derzeit genau eine Route. `public_url` erscheint nur beim Erstellen oder exakten Replay; list/get verraten das Secret nicht. Redeem verbraucht atomar eine Nutzung, erstellt intent/quote/address/route und gibt `cs_…` aus. Return URLs sind fest. Embedded Checkout an exakten HTTPS Origin binden und Explorer-URLs aus eigener Allowlist ableiten.

Reports umfassen höchstens 366 Tage und keine Zukunft. Bei `ready` vor JSONL Größe, SHA-256, eingefrorene `signing_key_id` und Ed25519-Signatur prüfen. Alte Public Keys über die gesamte Retention behalten. Der Header fixiert globale Ledger Sequence/Cutoff, der Footer enthält exakte String-Totals.

## Sandbox und Produktion

Testen Sie exact, partial, over, late, wrong asset, duplicate delivery, settle-then-reorg, Event-Lücken, Key-Rotation und Timeout-Recovery. Sandbox-Erfolg ist keine Produktionsfreigabe: reale unabhängige Provider, Finality/Reorg- und Restore-Übungen, gepinnte Images, Rotation und Lasttests bleiben Pflicht.

Team/settings ist eine getrennte Cabinet-API und nicht Teil des HMAC-SDK. Der Backend-Vertrag existiert, BFF/Browser-Aktivierung bleibt pre-release; siehe [Team-Einstellungen](merchant-team-settings.md) nach Übergabe.

Transaktionsadapter für FastAPI/Django, Laravel/Symfony, Express/NestJS, Spring Boot, ASP.NET, Telegram und generischen Handel stehen im [Framework-Skeleton-Index](../../examples/frameworks/README.md). Sie sind Vorlagen und installieren keine Framework-Abhängigkeiten.
