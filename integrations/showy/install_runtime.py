#!/usr/bin/env python3
"""Idempotently install the reviewed Showy webhook wiring.

The installer refuses source drift instead of guessing. Run it from the Ocrypt
checkout with the Showy project root as its only argument.
"""

from __future__ import annotations

import pathlib
import shutil
import sys


POLL_TASK = '''@app.task(bind=True)
def poll_showy_crypto_orders(self, batch_size: int = 100):
    from .models import ShowyCryptoInvoice
    from .showy_crypto import (
        ACTIVE_POLL_STATUSES,
        ShowyCryptoError,
        apply_status_result,
        get_order_sync,
        is_configured,
    )

    if not is_configured():
        return {"status": "disabled"}
    now = timezone.now()
    check_before = now - timedelta(seconds=45)
    invoices = list(
        ShowyCryptoInvoice.objects.filter(
            status__in=ACTIVE_POLL_STATUSES,
            created_at__gte=now - timedelta(days=7),
        )
        .filter(Q(last_checked_at__isnull=True) | Q(last_checked_at__lte=check_before))
        .order_by("last_checked_at", "created_at")[: max(int(batch_size), 1)]
        .values("order_id", "payment_id")
    )
    result = {
        "status": "ok",
        "selected": len(invoices),
        "checked": 0,
        "activated": 0,
        "changed": 0,
        "failed": 0,
    }
    for invoice in invoices:
        try:
            payload = get_order_sync(str(invoice["payment_id"]))
            applied = apply_status_result(
                order_id=invoice["order_id"],
                payload=payload,
                source="background_poll",
            )
            result["checked"] += 1
            result["changed"] += int(bool(applied.get("status_changed")))
            if applied.get("activated"):
                result["activated"] += 1
                try:
                    _ensure_paid_crypto_vpn(applied["checkout_id"])
                except Exception:
                    logger.exception(
                        "Failed to provision VPN after crypto activation checkout=%s",
                        applied["checkout_id"],
                    )
        except ShowyCryptoError as exc:
            result["failed"] += 1
            logger.warning(
                "Showy crypto poll failed order_id=%s payment_id=%s error=%s",
                invoice["order_id"],
                invoice["payment_id"],
                exc,
            )
        except Exception:
            result["failed"] += 1
            logger.exception(
                "Unexpected Showy crypto poll failure order_id=%s payment_id=%s",
                invoice["order_id"],
                invoice["payment_id"],
            )
    logger.info("poll_showy_crypto_orders result=%s", result)
    return result
'''

PROVISION_TASK = '''@app.task(bind=True, autoretry_for=(Exception,), retry_backoff=True, retry_kwargs={"max_retries": 8})
def provision_showy_crypto_checkout(self, checkout_id: str):
    """Run non-database fulfillment after an acknowledged payment webhook."""
    _ensure_paid_crypto_vpn(checkout_id)
    return {"status": "ok", "checkout_id": str(checkout_id)}
'''


def replace_once(path: pathlib.Path, old: str, new: str) -> None:
    text = path.read_text()
    if new and new in text:
        return
    if not new and old not in text:
        return
    if text.count(old) != 1:
        raise SystemExit(f"refusing source drift in {path}: expected one reviewed block")
    path.write_text(text.replace(old, new, 1))


def main() -> None:
    if len(sys.argv) != 2:
        raise SystemExit("usage: install_runtime.py /path/to/showy")
    integration = pathlib.Path(__file__).resolve().parent
    sdk = integration.parents[1] / "sdk" / "python" / "src" / "merchant_platform"
    root = pathlib.Path(sys.argv[1]).resolve()
    api = root / "server" / "api"
    if not (api / "urls.py").is_file() or not sdk.is_dir():
        raise SystemExit("Showy root or Ocrypt SDK source is missing")

    (api / "migrations").mkdir(parents=True, exist_ok=True)
    shutil.copy2(integration / "server/api/ocrypt_webhook.py", api / "ocrypt_webhook.py")
    shutil.copy2(
        integration / "server/api/test_ocrypt_webhook.py",
        api / "test_ocrypt_webhook.py",
    )
    shutil.copy2(
        integration / "server/api/migrations/0134_ocrypt_webhook_inbox.py",
        api / "migrations/0134_ocrypt_webhook_inbox.py",
    )
    target_sdk = root / "server" / "merchant_platform"
    if target_sdk.exists():
        shutil.rmtree(target_sdk)
    shutil.copytree(sdk, target_sdk)

    replace_once(
        api / "urls.py",
        "from .unmatched_views import unmatched_payment_webhook\n",
        "from .unmatched_views import unmatched_payment_webhook\nfrom .ocrypt_webhook import ocrypt_webhook\n",
    )
    replace_once(
        api / "urls.py",
        '    path("healthz/", healthz, name="healthz"),\n',
        '    path("healthz/", healthz, name="healthz"),\n'
        '    path("ocrypt/webhook/", ocrypt_webhook, name="ocrypt_webhook"),\n',
    )
    replace_once(
        root / "server/conf/settings.py",
        '''    "poll-showy-crypto-orders": {
        "task": "api.tasks.poll_showy_crypto_orders",
        "schedule": crontab(minute="*", hour="*"),
    },
''',
        "",
    )
    replace_once(api / "tasks.py", POLL_TASK, PROVISION_TASK)
    replace_once(
        root / "docker-compose.yml",
        "    volumes: *lampac-media-volume\n    shm_size: \"512mb\"\n",
        "    volumes:\n"
        "      - ${LAMPAC_MEDIA_SOURCE:-media_volume}:/app/server/conf/media\n"
        "      - ${OCRYPT_WEBHOOK_SECRETS_PATH:-./secrets/ocrypt-webhook-secrets.json}:/run/secrets/ocrypt-webhook-secrets.json:ro\n"
        "    shm_size: \"512mb\"\n",
    )


if __name__ == "__main__":
    main()
