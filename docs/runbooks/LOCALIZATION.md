# Runbook localization coverage

| Topic | EN | zh-CN | ES | FR | DE | RU | Fallback |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Operator entry and production gate | native | native | native | native | native | native | none |
| Critical incidents and all component scenarios | native | native | native | native | native | native | none |
| PostgreSQL backup, restore, reconciliation and cutover | native | native | native | native | native | native | none |
| Release and rollback implementation detail | native | EN | EN | EN | EN | EN | English canonical procedure |
| Alert-specific service-unavailable summary | native | localized critical guide | localized critical guide | localized critical guide | localized critical guide | localized critical guide | English alert link plus native full procedure |

The two critical safety procedures are fully translated and are not summaries.
Release command syntax and alert annotations intentionally fall back to English so
there is one canonical, version-specific source. Every localized index discloses
that fallback. During an incident, the scribe records a single UTC timeline and may
use the native critical guide; command approvals still reference the canonical
release artifact, digest, and English release procedure.
