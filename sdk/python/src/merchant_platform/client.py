import json
import secrets
import socket
import time
import re
from dataclasses import asdict
from typing import Any, Dict, Mapping, Optional
from urllib.error import HTTPError, URLError
from urllib.parse import quote, urlsplit
from urllib.request import Request, urlopen
from .models import Asset, CancelPaymentIntentRequest, CheckoutRoute, CheckoutSession, CreatePaymentIntentRequest, CreatePaymentRouteRequest, CreateReconciliationReportRequest, CursorPage, Envelope, EventPage, ExpirePaymentIntentRequest, PaymentIntent, PaymentProof, PaymentRoute, SubmitPaymentProofRequest, UpdatePaymentIntentMetadataRequest
from .signing import canonical_query, sign_request

class MerchantApiError(RuntimeError):
    def __init__(self, status: int, code: str, message: str, request_id: Optional[str] = None, details: Optional[Mapping[str, Any]] = None, retryable: bool = False, retry_after_seconds: Optional[float] = None):
        super().__init__(message); self.status = status; self.code = code; self.request_id = request_id; self.details = details; self.retryable = retryable; self.retry_after_seconds = retry_after_seconds

class MerchantClient:
    def __init__(self, base_url: str, key_id: str, secret: str, timeout: float = 10.0, report_timeout: float = 900.0, max_report_bytes: int = 268_435_456):
        _validate_base_url(base_url)
        if max_report_bytes < 1_048_576: raise ValueError("max_report_bytes must be at least 1 MiB")
        self._base_url, self._key_id, self._secret, self._timeout, self._report_timeout, self._max_report_bytes = base_url.rstrip("/"), key_id, secret, timeout, report_timeout, max_report_bytes
    def create_payment_intent(self, value: CreatePaymentIntentRequest, idempotency_key: str, request_id: Optional[str] = None) -> Envelope[PaymentIntent]: return self._intent_envelope(self._request("POST", "/v1/payment-intents", value.to_dict(), idempotency_key=idempotency_key, request_id=request_id))
    def list_payment_intents(self, status: Optional[str] = None, after: Optional[str] = None, limit: int = 50) -> Envelope[CursorPage[PaymentIntent]]:
        envelope = self._request("GET", "/v1/payment-intents", query={"status": status, "after": after, "limit": limit}); data = envelope["data"]
        return Envelope(CursorPage([PaymentIntent.from_dict(item) for item in data["items"]], data.get("next_cursor", "")), envelope["request_id"], envelope["api_version"])
    def get_payment_intent(self, intent_id: str, request_id: Optional[str] = None) -> Envelope[PaymentIntent]: return self._intent_envelope(self._request("GET", "/v1/payment-intents/" + quote(intent_id, safe=""), request_id=request_id))
    def create_payment_route(self, intent_id: str, value: CreatePaymentRouteRequest, idempotency_key: str) -> Envelope[PaymentRoute]: return self._route_envelope(self._request("POST", "/v1/payment-intents/{}/routes".format(quote(intent_id, safe="")), value.to_dict(), idempotency_key=idempotency_key))
    def list_payment_routes(self, intent_id: str) -> Envelope[list]:
        envelope = self._request("GET", "/v1/payment-intents/{}/routes".format(quote(intent_id, safe=""))); return Envelope([PaymentRoute.from_dict(item) for item in envelope["data"]["items"]], envelope["request_id"], envelope["api_version"])
    def cancel_payment_intent(self, intent_id: str, value: CancelPaymentIntentRequest, idempotency_key: str) -> Envelope[PaymentIntent]: return self._intent_envelope(self._request("POST", "/v1/payment-intents/{}/cancel".format(quote(intent_id, safe="")), value.to_dict(), idempotency_key=idempotency_key))
    def expire_payment_intent(self, intent_id: str, value: ExpirePaymentIntentRequest, idempotency_key: str) -> Envelope[PaymentIntent]: return self._intent_envelope(self._request("POST", "/v1/payment-intents/{}/expire".format(quote(intent_id, safe="")), value.to_dict(), idempotency_key=idempotency_key))
    def update_payment_intent_metadata(self, intent_id: str, value: UpdatePaymentIntentMetadataRequest, idempotency_key: str) -> Envelope[PaymentIntent]: return self._intent_envelope(self._request("POST", "/v1/payment-intents/{}/metadata".format(quote(intent_id, safe="")), value.to_dict(), idempotency_key=idempotency_key))
    def list_assets(self) -> Envelope[list]:
        envelope = self._request("GET", "/v1/assets"); return Envelope([Asset.from_dict(item) for item in envelope["data"]["items"]], envelope["request_id"], envelope["api_version"])
    def submit_payment_proof(self, value: SubmitPaymentProofRequest, idempotency_key: str) -> Envelope[PaymentProof]: return self._proof_envelope(self._request("POST", "/v1/payment-proofs", asdict(value), idempotency_key=idempotency_key))
    def get_payment_proof(self, proof_id: str) -> Envelope[PaymentProof]: return self._proof_envelope(self._request("GET", "/v1/payment-proofs/" + quote(proof_id, safe="")))
    def list_events(self, after_sequence: int = 0, limit: int = 100) -> Envelope[EventPage[Mapping[str, Any]]]:
        envelope = self._request("GET", "/v1/events", query={"after_sequence": after_sequence, "limit": limit}); data = envelope["data"]
        return Envelope(EventPage(data["items"], data["next_cursor"], data["next_sequence"]), envelope["request_id"], envelope["api_version"])
    def get_event(self, event_id: str) -> Envelope[Mapping[str, Any]]: return self._mapping_envelope(self._request("GET", "/v1/events/" + quote(event_id, safe="")))
    def list_transfers(self, after: Optional[str] = None, limit: int = 50) -> Envelope[CursorPage[Mapping[str, Any]]]: return self._mapping_page(self._request("GET", "/v1/transfers", query={"after": after, "limit": limit}))
    def get_transfer_events(self, network: str, transaction_id: str) -> Envelope[list]:
        raw = self._request("GET", "/v1/transfers/{}/{}".format(quote(network, safe=""), quote(transaction_id, safe=""))); return Envelope(raw["data"]["items"], raw["request_id"], raw["api_version"])
    def list_quotes(self, after: Optional[str] = None, limit: int = 50) -> Envelope[CursorPage[Mapping[str, Any]]]: return self._mapping_page(self._request("GET", "/v1/quotes", query={"after": after, "limit": limit}))
    def get_quote(self, quote_id: str) -> Envelope[Mapping[str, Any]]: return self._mapping_envelope(self._request("GET", "/v1/quotes/" + quote(quote_id, safe="")))
    def list_balances(self) -> Envelope[list]:
        raw = self._request("GET", "/v1/balances"); return Envelope(raw["data"]["items"], raw["request_id"], raw["api_version"])
    def get_reconciliation(self) -> Envelope[Mapping[str, Any]]: return self._mapping_envelope(self._request("GET", "/v1/reconciliation"))
    def create_reconciliation_report(self, value: CreateReconciliationReportRequest, idempotency_key: str) -> Envelope[Mapping[str, Any]]: return self._mapping_envelope(self._request("POST", "/v1/reconciliation-reports", value.to_dict(), idempotency_key=idempotency_key))
    def get_reconciliation_report(self, report_id: str) -> Envelope[Mapping[str, Any]]: return self._mapping_envelope(self._request("GET", "/v1/reconciliation-reports/" + quote(report_id, safe="")))
    def download_reconciliation_report(self, report_id: str) -> tuple[bytes, Mapping[str, str]]: return self._download("/v1/reconciliation-reports/{}/download".format(quote(report_id, safe="")))
    def create_payment_link(self, value: Mapping[str, Any], idempotency_key: str) -> Mapping[str, Any]: return self._request("POST", "/v1/payment-links", value, idempotency_key=idempotency_key)
    def list_payment_links(self, cursor: Optional[str] = None, limit: int = 50) -> Mapping[str, Any]: return self._request("GET", "/v1/payment-links", query={"cursor": cursor, "limit": limit})
    def get_payment_link(self, link_id: str) -> Mapping[str, Any]: return self._request("GET", "/v1/payment-links/" + quote(link_id, safe=""))
    def disable_payment_link(self, link_id: str, version: int, idempotency_key: str) -> Mapping[str, Any]: return self._request("POST", "/v1/payment-links/{}/disable".format(quote(link_id, safe="")), {"version": version}, idempotency_key=idempotency_key)
    def create_checkout_session(self, value: Mapping[str, Any], idempotency_key: str) -> Mapping[str, Any]: return self._request("POST", "/v1/checkout-sessions", value, idempotency_key=idempotency_key)
    def _request(self, method: str, path: str, payload: Optional[Mapping[str, Any]] = None, query: Optional[Mapping[str, Any]] = None, idempotency_key: Optional[str] = None, request_id: Optional[str] = None) -> Mapping[str, Any]:
        if idempotency_key is not None and not 8 <= len(idempotency_key) <= 255: raise ValueError("idempotency key must be 8..255 characters")
        query_string = canonical_query(query or {}); path_and_query = path + (("?" + query_string) if query_string else "")
        body = b"" if payload is None else json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
        signed = sign_request(self._key_id, self._secret, method, path_and_query, body, int(time.time()), secrets.token_hex(16))
        headers = {"Accept": "application/json", **signed.as_dict()}
        if payload is not None: headers["Content-Type"] = "application/json"
        if idempotency_key: headers["Idempotency-Key"] = idempotency_key
        if request_id: headers["Request-Id"] = request_id
        request = Request(self._base_url + path_and_query, data=None if payload is None else body, headers=headers, method=method)
        response_retry_after = None
        try:
            with urlopen(request, timeout=self._timeout) as response: raw, status, response_request_id = response.read(), response.status, response.headers.get("Request-Id")
        except HTTPError as error: raw, status, response_request_id, response_retry_after = error.read(), error.code, error.headers.get("Request-Id"), _retry_after(error.headers.get("Retry-After"))
        except (URLError, socket.timeout, TimeoutError): raise MerchantApiError(0, "transport_error", "request failed", retryable=True)
        try: value = json.loads(raw.decode("utf-8")) if raw else {}
        except (UnicodeDecodeError, json.JSONDecodeError): raise MerchantApiError(status, "invalid_response", "server returned invalid JSON", response_request_id)
        if status < 200 or status >= 300:
            error = value.get("error", {}) if isinstance(value, dict) else {}
            raise MerchantApiError(status, error.get("code", "http_error"), error.get("message", "API request failed"), value.get("request_id", response_request_id), error.get("details"), status == 429 or status >= 500, response_retry_after)
        return value
    def _download(self, path: str) -> tuple[bytes, Mapping[str, str]]:
        signed = sign_request(self._key_id, self._secret, "GET", path, b"", int(time.time()), secrets.token_hex(16))
        request = Request(self._base_url + path, headers={"Accept": "application/x-ndjson", **signed.as_dict()})
        try:
            with urlopen(request, timeout=self._report_timeout) as response:
                raw = response.read(self._max_report_bytes + 1); headers = {"sha256": response.headers.get("X-Reconciliation-SHA256", ""), "signature": response.headers.get("X-Reconciliation-Signature", ""), "signing_key_id": response.headers.get("X-Reconciliation-Signing-Key-Id", "")}
        except HTTPError as error: raise MerchantApiError(error.code, "report_unavailable", "reconciliation report unavailable", retryable=error.code == 429 or error.code >= 500)
        if len(raw) > self._max_report_bytes: raise MerchantApiError(200, "report_too_large", "reconciliation report exceeds configured client limit")
        if re.fullmatch(r"[0-9a-f]{64}", headers["sha256"]) is None or not headers["signature"] or not headers["signing_key_id"]: raise MerchantApiError(200, "invalid_response", "missing reconciliation integrity headers")
        return raw, headers
    @staticmethod
    def _intent_envelope(value): return Envelope(PaymentIntent.from_dict(value["data"]), value["request_id"], value["api_version"])
    @staticmethod
    def _route_envelope(value): return Envelope(PaymentRoute.from_dict(value["data"]), value["request_id"], value["api_version"])
    @staticmethod
    def _proof_envelope(value): return Envelope(PaymentProof.from_dict(value["data"]), value["request_id"], value["api_version"])
    @staticmethod
    def _mapping_envelope(value): return Envelope(value["data"], value["request_id"], value["api_version"])
    @staticmethod
    def _mapping_page(value): return Envelope(CursorPage(value["data"]["items"], value["data"].get("next_cursor", "")), value["request_id"], value["api_version"])

class CheckoutClient:
    def __init__(self, base_url: str, timeout: float = 10.0):
        _validate_base_url(base_url)
        self._base_url, self._timeout = base_url.rstrip("/"), timeout
    def get_session(self, opaque_token: str) -> CheckoutSession:
        if re.fullmatch(r"cs_[A-Za-z0-9_-]{43}", opaque_token) is None: raise ValueError("invalid checkout token")
        try:
            with urlopen(Request(self._base_url + "/v1/checkout-sessions/" + quote(opaque_token, safe=""), headers={"Accept": "application/json"}), timeout=self._timeout) as response: value = json.load(response)
        except HTTPError as error: raise MerchantApiError(error.code, "checkout_unavailable", "checkout session unavailable", retryable=error.code == 429 or error.code >= 500)
        except (URLError, socket.timeout, TimeoutError): raise MerchantApiError(0, "transport_error", "checkout request failed", retryable=True)
        statuses = {"pending", "detected", "confirming", "settled", "expired", "preparing_payment_route", "payment_route_failed"}
        if value.get("status") not in statuses or not isinstance(value.get("routes"), list) or not isinstance(value.get("selected_route_id"), str): raise MerchantApiError(200, "invalid_response", "invalid checkout response")
        waiting = value["status"] in {"preparing_payment_route", "payment_route_failed"}
        if (waiting and (value["routes"] or value["selected_route_id"] != "")) or (not waiting and not value["routes"]): raise MerchantApiError(200, "invalid_response", "invalid checkout response")
        routes = [CheckoutRoute(**{key: route.get(key) for key in CheckoutRoute.__dataclass_fields__}) for route in value["routes"]]
        if value["selected_route_id"] and not any(route.id == value["selected_route_id"] for route in routes): raise MerchantApiError(200, "invalid_response", "invalid selected checkout route")
        return CheckoutSession(value["intent_id"], value["order_id"], value["status"], value["expires_at"], value["selected_route_id"], routes)
    def get_payment_link(self, token: str) -> Mapping[str, Any]: return self._public("GET", "/v1/public/payment-links/" + quote(_require_token(token, "pl"), safe=""))
    def redeem_payment_link(self, token: str, idempotency_key: str, value: Optional[Mapping[str, Any]] = None, origin: Optional[str] = None) -> Mapping[str, Any]: return self._public("POST", "/v1/public/payment-links/{}/redeem".format(quote(_require_token(token, "pl"), safe="")), value or {}, idempotency_key, origin)
    def select_route(self, token: str, route_id: str, idempotency_key: str, origin: Optional[str] = None) -> Mapping[str, Any]: return self._public("POST", "/v1/checkout-sessions/{}/select-route".format(quote(_require_token(token, "cs"), safe="")), {"route_id": route_id}, idempotency_key, origin)
    def _public(self, method: str, path: str, value: Optional[Mapping[str, Any]] = None, idempotency_key: Optional[str] = None, origin: Optional[str] = None) -> Mapping[str, Any]:
        body = None if value is None else json.dumps(value, ensure_ascii=False, separators=(",", ":")).encode("utf-8"); headers = {"Accept": "application/json"}
        if body is not None: headers["Content-Type"] = "application/json"
        if idempotency_key: headers["Idempotency-Key"] = idempotency_key
        if origin: headers["Origin"] = origin
        try:
            with urlopen(Request(self._base_url + path, data=body, headers=headers, method=method), timeout=self._timeout) as response: return json.load(response)
        except HTTPError as error: raise MerchantApiError(error.code, "checkout_unavailable", "public checkout request failed", retryable=error.code == 429 or error.code >= 500)

def _validate_base_url(value: str) -> None:
    parsed = urlsplit(value)
    loopback_http = parsed.scheme == "http" and parsed.hostname in {"localhost", "127.0.0.1"}
    if (parsed.scheme != "https" and not loopback_http) or not parsed.hostname or parsed.username or parsed.password or parsed.query or parsed.fragment or parsed.path not in {"", "/"}:
        raise ValueError("base_url must be an HTTPS origin")

def _require_token(value: str, prefix: str) -> str:
    if re.fullmatch(prefix + r"_[A-Za-z0-9_-]{43}", value) is None: raise ValueError("invalid capability token")
    return value

def _retry_after(value: Optional[str]) -> Optional[float]:
    if value is None or not value.isdigit(): return None
    return min(float(value), 300.0)
