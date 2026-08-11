# Providerbetrieb

Der Providerbetrieb ist eine geheimnisfreie Steuerungs- und Gesundheitsebene. Das
Cabinet zeigt nur stabile Provider-ID, Operation, Policy-/Circuit-Version,
geschlossenen Zustand, Lag, Zeitpunkte und Freigabeidentitäten — niemals Endpunkte,
Zugangsdaten, Signer-Material, Wallet-Adressen oder rohe Fehler.

Der Produktionsscanner nutzt nur aktive On-Chain-Provider mit aktueller,
unveränderlich freigegebener `rpc_provider`-Policy, geschlossenem Circuit und
frischem Quorum aus unabhängigen Failure Domains. Pausiert, offen, veraltet oder
abweichend bedeutet Ausschluss; unter Quorum stoppt der Scan. Timeout, Retry,
Backoff, Rate-Limit, Priorität und Gesundheitsgrenzen stammen aus der Policy.

Pause und Freigabe sind getrennte Vier-Augen-Aktionen mit aktuellem MFA, exakten
Versionen, Grund und Idempotency-Key. Änderung, Audit und Outbox committen atomar.
Der private Worker auf Port 9100 verwendet fenced Leases und operationsspezifische,
nur lesende Probes; Readiness verlangt einen nichtleeren Erfolg und ein zulässiges
Peer-Quorum. Hosted-Provider starten pausiert: signierte Callbacks bleiben
append-only Evidenz ohne Ledger/Settlement, ausgehende Aufrufe bleiben bis zu einer
separat genehmigten Policy und erfolgreicher Probe gesperrt.

Der Antrag bindet die exakt sechs Vorgangsrichtlinien und eine begrenzte, nur
schreibbare Bootstrap-Statusreferenz in einen unveränderlichen Digest. Listen und
Entscheidungen geben die Referenz nie zurück. Nach unabhängiger MFA-Freigabe sind
erfolgreiche Statusprüfungen aus mindestens zwei Fehlerdomänen und anschließend
eine getrennte Vier-Augen-Freigabe zum Aufheben der Pause erforderlich.
