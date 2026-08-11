# Betriebshandbücher

Diese Verfahren gelten für Produktion und Staging. Bei einem Vorfall zuerst die
allgemeinen Kontrollen im Leitfaden für kritische Vorfälle ausführen und danach das
passende Szenario bearbeiten. Produktionszugriff für Datenbank-Restores setzt eine
erfolgreiche Wiederherstellungsübung voraus.

- [Kritische Vorfälle](critical-incidents.md): Ausfall, Scanner-Abweichung,
  Reorganisation, Callback-Störung, nicht zugeordnete Zahlungen und Schlüsselverlust.
- [Backup und Restore](backup-restore.md): Sicherungen, isolierte Wiederherstellung,
  Abgleich und Umschaltung.
- Das kanonische Release-/Rollback-Verfahren steht im englischen Abschnitt.

Der Scanner ist deaktiviert, weil Binary und Provider-Adapter das Release-Gate noch
nicht bestanden haben. Deployment oder erfolgreicher Healthcheck sind keine
Freigabe für automatische Verbuchung. Produktion verlangt signierte Image-Digests,
externe Secrets, getrennte DB-Rollen, wirksame NetworkPolicies, PostgreSQL-TLS,
geprüftes PITR und Restore-Übung, Rufbereitschaft und getestete Alarme. Healthchecks
allein ersetzen die noch fehlenden Finanz- und Queue-Metriken nicht.
