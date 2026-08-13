"""Production webhook receiver for the Showy merchant integration.

The raw request is verified before JSON is trusted. Inbox insertion, invoice
state transition and entitlement activation share one database transaction.
"""

from __future__ import annotations

import json
import logging
import os
from decimal import Decimal, InvalidOperation

from django.db import connection, transaction
from django.http import JsonResponse
from django.utils import timezone
from django.views.decorators.csrf import csrf_exempt
from django.views.decorators.http import require_POST

from api.marketing.offers import OfferError
from api.marketing.payments import PaymentActivationError, activate_checkout
from api.models import ShowyCryptoInvoice
from merchant_platform.webhooks import (
    WebhookVerificationError,
    acknowledgement,
    challenge_response,
    verify_webhook,
)

logger = logging.getLogger(__name__)

MAX_BODY_BYTES = 256 * 1024
SUPPORTED_EVENT_TYPES = frozenset({
    "payment.intent.created",
    "payment.route.created",
    "payment.observed",
    "payment.confirming",
    "payment.partially_paid",
    "payment.needs_review",
    "payment.settled",
    "payment.overpaid",
    "payment.expired",
    "payment.cancelled",
    "payment.reorged",
    "payment.resolution.updated",
})
STATUS_BY_EVENT = {
    "payment.observed": ShowyCryptoInvoice.Status.OBSERVED,
    "payment.confirming": ShowyCryptoInvoice.Status.CONFIRMED,
    "payment.partially_paid": ShowyCryptoInvoice.Status.PARTIALLY_PAID,
    "payment.needs_review": ShowyCryptoInvoice.Status.NEEDS_REVIEW,
    "payment.expired": ShowyCryptoInvoice.Status.EXPIRED,
    "payment.cancelled": ShowyCryptoInvoice.Status.CANCELLED,
}


class ShowyWebhookError(RuntimeError):
    pass


def _json(data, status=200):
    return JsonResponse(data, status=status, json_dumps_params={"separators": (",", ":")})


def _required_env(name: str) -> str:
    value = (os.getenv(name) or "").strip()
    if not value:
        raise ShowyWebhookError(f"{name} is not configured")
    return value


def _keyring() -> dict[str, str]:
    path = _required_env("OCRYPT_WEBHOOK_SECRETS_FILE")
    with open(path, "r", encoding="utf-8") as source:
        value = json.load(source)
    keys = value.get("keys") if isinstance(value, dict) else None
    if not isinstance(keys, dict) or not 1 <= len(keys) <= 8:
        raise ShowyWebhookError("webhook keyring is invalid")
    result = {}
    for key_id, secret in keys.items():
        if not isinstance(key_id, str) or not isinstance(secret, str) or not key_id or not secret:
            raise ShowyWebhookError("webhook keyring is invalid")
        result[key_id] = secret
    return result


def _is_challenge(raw_body: bytes) -> bool:
    try:
        value = json.loads(raw_body.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError):
        return False
    return isinstance(value, dict) and value.get("type") == "merchant.webhook.challenge"


def _fiat_amount(amount_minor) -> Decimal:
    text = str(amount_minor or "")
    if not text.isdigit():
        raise ShowyWebhookError("payment amount is invalid")
    try:
        return (Decimal(text) / Decimal(100)).quantize(Decimal("0.01"))
    except InvalidOperation as exc:
        raise ShowyWebhookError("payment amount is invalid") from exc


def _target_status(event) -> str | None:
    if event.event_type in {"payment.settled", "payment.overpaid"}:
        # Showy grants the purchased product for any verified overpayment. The
        # excess remains visible in Ocrypt accounting and is never credited as
        # a second subscription.
        return ShowyCryptoInvoice.Status.SETTLED
    if event.event_type == "payment.reorged":
        return ShowyCryptoInvoice.Status.NEEDS_REVIEW
    if event.event_type == "payment.resolution.updated":
        status = str(event.payment_intent.get("status") or "")
        if status in {"settled", "overpaid"}:
            return ShowyCryptoInvoice.Status.SETTLED
        return {
            "partially_paid": ShowyCryptoInvoice.Status.PARTIALLY_PAID,
            "needs_review": ShowyCryptoInvoice.Status.NEEDS_REVIEW,
            "expired": ShowyCryptoInvoice.Status.EXPIRED,
            "cancelled": ShowyCryptoInvoice.Status.CANCELLED,
        }.get(status)
    return STATUS_BY_EVENT.get(event.event_type)


def _apply_event(event, payload: dict) -> dict:
    payment = event.payment_intent
    payment_id = str(payment.get("id") or "")
    order_id = str(payment.get("merchant_order_id") or "")
    if not payment_id or not order_id:
        raise ShowyWebhookError("payment identity is incomplete")

    invoice = (
        ShowyCryptoInvoice.objects.select_for_update()
        .select_related("checkout")
        .filter(payment_id=payment_id, order_id=order_id)
        .first()
    )
    if invoice is None:
        if event.event_type in {"payment.intent.created", "payment.route.created"}:
            return {"outcome": "informational", "payment_id": payment_id}
        raise ShowyWebhookError("Showy crypto invoice is not committed yet")
    if str(payment.get("currency") or "").upper() != invoice.currency.upper():
        raise ShowyWebhookError("payment currency does not match Showy invoice")
    if _fiat_amount(payment.get("amount_minor")) != invoice.fiat_amount:
        raise ShowyWebhookError("payment amount does not match Showy invoice")

    target = _target_status(event)
    previous = invoice.status
    now = timezone.now()
    ignored_regression = previous == ShowyCryptoInvoice.Status.SETTLED and target != previous
    if ignored_regression:
        target = previous

    activated = False
    if target == ShowyCryptoInvoice.Status.SETTLED:
        try:
            checkout, activated = activate_checkout(
                invoice.checkout,
                paid_amount=invoice.fiat_amount,
                currency=invoice.currency,
                provider_payment_id=str(invoice.payment_id),
                metadata={
                    "crypto_provider": "ocrypt_webhook",
                    "showy_crypto_source": "signed_webhook",
                    "showy_crypto_event_id": event.event_id,
                    "showy_crypto_event_type": event.event_type,
                },
            )
        except (PaymentActivationError, OfferError) as exc:
            raise ShowyWebhookError(str(exc)) from exc
        invoice.checkout = checkout
        if invoice.settled_at is None:
            invoice.settled_at = now
        if invoice.credited_at is None:
            invoice.credited_at = checkout.activated_at or now

    if target is not None:
        invoice.status = target
    reason = {
        "payment.overpaid": "verified overpayment accepted",
        "payment.reorged": "reorg requires operator review",
    }.get(event.event_type, "")
    if ignored_regression:
        reason = "status regression ignored after fulfillment"
    invoice.status_reason = reason
    invoice.last_checked_at = now
    invoice.status_payload = payload
    history = list(invoice.status_history or [])
    history.append({
        "at": now.isoformat(),
        "source": "signed_webhook",
        "event_id": event.event_id,
        "event_type": event.event_type,
        "sequence": event.sequence,
        "status": invoice.status,
        "ignored": "status_regression_after_settlement" if ignored_regression else "",
    })
    invoice.status_history = history[-100:]
    invoice.save(update_fields=[
        "status",
        "status_reason",
        "last_checked_at",
        "status_payload",
        "status_history",
        "settled_at",
        "credited_at",
        "updated_at",
    ])

    if activated:
        checkout_id = str(invoice.checkout.checkout_id)

        def enqueue_provisioning():
            from api.tasks import provision_showy_crypto_checkout

            provision_showy_crypto_checkout.delay(checkout_id)

        transaction.on_commit(enqueue_provisioning)
    return {
        "outcome": "activated" if activated else "processed",
        "payment_id": payment_id,
        "status": invoice.status,
    }


def _persist_and_apply(verified, raw_body: bytes) -> None:
    payload = json.loads(raw_body.decode("utf-8"))
    event = verified.event
    with transaction.atomic():
        with connection.cursor() as cursor:
            cursor.execute(
                """
                INSERT INTO api_ocrypt_webhook_inbox
                  (event_id,body_sha256,event_type,event_sequence,payment_id,order_id,payload,processing_status,received_at)
                VALUES (%s,%s,%s,%s,NULLIF(%s,'')::uuid,%s,%s::jsonb,'received',clock_timestamp())
                ON CONFLICT (event_id) DO NOTHING
                RETURNING event_id
                """,
                [
                    verified.event_id,
                    verified.body_sha256,
                    event.event_type,
                    event.sequence,
                    str(event.payment_intent.get("id") or ""),
                    str(event.payment_intent.get("merchant_order_id") or ""),
                    raw_body.decode("utf-8"),
                ],
            )
            inserted = cursor.fetchone() is not None
            if not inserted:
                cursor.execute(
                    "SELECT body_sha256 FROM api_ocrypt_webhook_inbox WHERE event_id=%s FOR UPDATE",
                    [verified.event_id],
                )
                existing = cursor.fetchone()
                if existing is None or existing[0] != verified.body_sha256:
                    raise ShowyWebhookError("event ID was reused with a different body")
                return
        result = _apply_event(event, payload)
        with connection.cursor() as cursor:
            cursor.execute(
                """
                UPDATE api_ocrypt_webhook_inbox
                SET processing_status='processed', result=%s::jsonb, processed_at=clock_timestamp()
                WHERE event_id=%s
                """,
                [json.dumps(result, separators=(",", ":")), verified.event_id],
            )


@csrf_exempt
@require_POST
def ocrypt_webhook(request):
    raw_body = bytes(request.body)
    if not raw_body or len(raw_body) > MAX_BODY_BYTES:
        return _json({"error": "invalid body"}, 400)
    if _is_challenge(raw_body):
        try:
            return _json(challenge_response(raw_body))
        except WebhookVerificationError:
            return _json({"error": "invalid challenge"}, 400)
    try:
        keyring = _keyring()
        verified = verify_webhook(
            raw_body,
            request.META.get("HTTP_MERCHANT_WEBHOOK_SIGNATURE", ""),
            request.META.get("HTTP_CONTENT_DIGEST", ""),
            keyring.get,
            expected_merchant_id=_required_env("OCRYPT_WEBHOOK_MERCHANT_ID"),
            expected_livemode=True,
            allowed_event_types=SUPPORTED_EVENT_TYPES,
        )
        _persist_and_apply(verified, raw_body)
    except WebhookVerificationError:
        return _json({"error": "invalid webhook"}, 401)
    except ShowyWebhookError as exc:
        logger.warning("Ocrypt webhook was not applied: %s", exc)
        if "reused with a different body" in str(exc):
            return _json({"error": "event conflict"}, 409)
        return _json({"error": "temporarily unavailable"}, 503)
    except Exception:
        logger.exception("Unexpected Ocrypt webhook failure")
        return _json({"error": "temporarily unavailable"}, 503)
    return _json(acknowledgement(verified.event_id))
