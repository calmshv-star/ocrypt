# Betriebsablauf für Schattenmigration

Migration `000021` belässt PostgreSQL als Buchungsquelle. `migration-control` prüft offline und ausschließlich im Dry-Run. Das begrenzte, geheimnisfreie Inventar wird als exakte kanonische Bytes von zwei verschiedenen autorisierten Ed25519-Schlüsseln signiert und über die mandantenbezogene Admin-API eingereicht.

Übergänge laufen nur über Inventar, Validierung, getrennte Anfrage/Freigabe/Ausführung, Import, Shadow und Canary. Cutover bleibt bis zum ACK von Aktionsversion und Fence durch den getrennt authentifizierten Aktuator ausstehend. Canary-Abbruch und Rollback bewahren Fakten und Ownership-Fences; importierte Watch-only-Adressen werden nie freigegeben.

Der Verification Job startet mit `MIGRATION_EXECUTE=false`; Schreibzugriff verlangt eigene Rolle, Lease/Fence, gegenseitiges TLS 1.3 und quorum-signierte Fakten. Decommission verlangt DB-seitig null Backlog sowie Archiv-, Restore- und Schlüsselwiderrufsnachweis.

Live-Quell-DB, Chain, PostgreSQL-Cutover, Aktuator, Helm-Cluster und Provider-Quorum wurden lokal nicht ausgeführt; Nachweise gehören vor Freigabe ins Release-Manifest.
