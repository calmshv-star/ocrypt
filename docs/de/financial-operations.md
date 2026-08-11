# Finanzoperationen

Dieses Subsystem bietet mandantenisolierte Treasury-Sweeps, verifizierte Rückerstattungen und deterministische Abstimmung. Beträge sind kanonische uint256-Strings in atomaren Einheiten; Gleitkommazahlen sind verboten.

## Sicherheitsmodell

Alle Schreibvorgänge laufen in PostgreSQL `SERIALIZABLE` mit erzwungenem Tenant-RLS. Idempotency Keys sind gesperrt und an einen SHA-256-Fingerprint gebunden. Aggregat, Reservierungen, ausgeglichene doppelte Buchung, hashverkettetes Audit und Outbox werden atomar committet. Quelle/Nonce, Tageslimit und erstattbarer Settlement-Betrag werden unter Lock reserviert. Ein beobachteter Absender ist kein Eigentumsnachweis; Rückzahlung an den verifizierten Ursprung ist Standard. Genehmigung erfordert Step-up und einen zweiten Operator.

## Ausführungsisolation

Ein finalisierter Transfer ist nur Evidenz, kein Eigentumsnachweis. Refund bleibt fail-closed, bis ein separat zugelassener Wallet-Signature/Custodian-Verifier `financial_verified_refund_destinations` schreibt; CEX-, Contract- oder Hot-Wallet-Absender werden nie automatisch vertraut.

Die Operator-API besitzt keine Build-, Sign- oder Broadcast-Route. `financial-worker` führt pro Fencing Token genau eine Stufe aus. Builder, Signer, Broadcaster, unabhängiger Finality-Verifier und Event Sink benötigen fünf verschiedene HTTPS-Origins und Credentials. Redirects und Umgebungs-Proxys sind deaktiviert. Jeder externe Effekt trägt einen stabilen Idempotency Key und Aggregate-Binding. Private Blockchain-Schlüssel werden nie gespeichert.

Finality stammt nicht vom Signer/Broadcaster, sondern vom separat zugelassenen Quorum-Verifier. Refund-Finality und Reorg erzeugen unveränderliche Ausgleichs-/Umkehrbuchungen. Die Outbox nutzt `SKIP LOCKED`, monotone Lease Tokens, Retry, Dead Letter nach 20 Versuchen und eine Ack mit identischer Event-ID.

## API und Betrieb

Siehe `contracts/financial-openapi.yaml`. Der IAM-Proxy signiert Tenant, Actor, sortierte Permissions, begrenztes Step-up, Timestamp, Nonce, Pfad/Query und Body-Digest. Nonces liegen in `financial_proxy_nonces`, unabhängig von Merchant-Clients. Lesen erfordert `financial:read`.

Die API benötigt Datenbank, TLS und `FINANCIAL_OPERATOR_ASSERTION_SECRET_FILE`. Der Worker benötigt explizite Tenant-UUIDs sowie getrennte `FINANCIAL_{BUILDER,SIGNER,BROADCASTER,FINALITY,EVENT_SINK}_{URL,TOKEN_FILE}`. `:9093` ist für Kubernetes-Pod-Probes erlaubt, darf aber keinen öffentlichen Service besitzen.

Audit-Einträge sind pro Tenant SHA-256-verkettet und werden nur über `append_financial_audit` geschrieben; der letzte Hash muss extern verankert werden. Vor Freigabe: Migration up/down/up, Least-Privilege-Rechte, Zwei-Tenant-RLS-Test, KMS/HSM/MPC-Key-Ceremony, Provider-Zulassung sowie Lost-Response-, stale-fence-, Reorg-, Dead-Letter-, Backup/Restore- und Audit-Chain-Tests durchführen.

## Finanzkabinett der Administration

Der Browser kommuniziert ausschließlich mit dem gleich-originigen Admin BFF. Das BFF leitet aus aktuellen Datenbankrollen eine geschlossene, tenantweite Berechtigungsliste ab; händlergebundene Rollen und vom Browser gelieferte Rechte werden abgelehnt. Treasury-Operatoren dürfen Sweeps und Rückerstattungen anfordern oder abbrechen und Abstimmungen anfordern. Ein unabhängiger Senior-Genehmiger darf genehmigen und die Abstimmung ausführen. Support- und Zahlungsoperatoren erhalten keine Finanzrechte.

Jede Mutation verlangt CSRF, exakten Origin, aktuelle Version, Begründung und `Idempotency-Key`. Entscheidungs-Replay wird atomar mit Aggregat, Audit und Outbox gespeichert; dieselbe Kennung mit anderer Methode, anderem Pfad oder Body führt zum Konflikt. Genehmigung verlangt aktuelle MFA und einen anderen Actor. Atomare Beträge bleiben in UI und API Strings.

Das BFF erreicht die private Finanz-API nur per TLS 1.3 mit gepinnter CA, explizitem Servernamen und verifiziertem Client-Zertifikat. Monitoring nutzt ein getrenntes Least-Privilege-Zertifikat; Health-Endpunkte werden nicht auf Klartext herabgestuft. Redirects und Umgebungs-Proxys sind deaktiviert. Interne Origin, Assertion-Secret und Custody-Daten gelangen nie in den Browser; Build-/Sign-/Broadcast-/Geldausführungsrouten werden nicht angeboten. Live-Custody bleibt deaktiviert und fail-closed; die Oberfläche behauptet keine Provider-Zulassung.
