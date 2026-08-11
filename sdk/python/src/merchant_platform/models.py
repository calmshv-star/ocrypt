from dataclasses import asdict, dataclass, field
from typing import Any, Dict, Generic, List, Mapping, Optional, TypeVar

AtomicAmount = str
T = TypeVar("T")

def assert_atomic_amount(value: str, positive: bool = False) -> None:
    if not isinstance(value, str) or not value.isdigit() or len(value) > 78 or (len(value) > 1 and value[0] == "0") or (positive and value == "0"):
        raise ValueError("amount must be a canonical integer string")

@dataclass(frozen=True)
class RouteSelector:
    provider: str
    asset_id: str
    chain_id: Optional[str] = None
    provider_id: Optional[str] = None
    @classmethod
    def on_chain(cls, chain_id: str, asset_id: str) -> "RouteSelector": return cls("on_chain", asset_id, chain_id=chain_id)
    @classmethod
    def hosted_gateway(cls, provider_id: str, asset_id: str) -> "RouteSelector": return cls("hosted_gateway", asset_id, provider_id=provider_id)

@dataclass(frozen=True)
class CreatePaymentIntentRequest:
    merchant_order_id: str
    amount_minor: AtomicAmount
    currency: str
    currency_scale: int
    description: Optional[str] = None
    customer_reference: Optional[str] = None
    expires_in: Optional[int] = None
    expires_at: Optional[str] = None
    allowed_routes: Optional[List[RouteSelector]] = None
    metadata: Optional[Dict[str, Any]] = None
    def to_dict(self) -> Dict[str, Any]:
        assert_atomic_amount(self.amount_minor, True)
        return _without_none(asdict(self))

@dataclass(frozen=True)
class CreatePaymentRouteRequest:
    provider: str
    on_chain: Optional[Dict[str, str]] = None
    hosted_gateway: Optional[Dict[str, str]] = None
    expires_in: Optional[int] = None
    def to_dict(self) -> Dict[str, Any]: return _without_none(asdict(self))
    @classmethod
    def on_chain_route(cls, chain_id: str, asset_id: str, expires_in: Optional[int] = None) -> "CreatePaymentRouteRequest": return cls("on_chain", on_chain={"chain_id": chain_id, "asset_id": asset_id}, expires_in=expires_in)
    @classmethod
    def hosted_gateway_route(cls, provider_id: str, asset_id: str, expires_in: Optional[int] = None) -> "CreatePaymentRouteRequest": return cls("hosted_gateway", hosted_gateway={"provider_id": provider_id, "asset_id": asset_id}, expires_in=expires_in)

@dataclass(frozen=True)
class CancelPaymentIntentRequest:
    reason: str
    expected_version: Optional[int] = None
    def to_dict(self) -> Dict[str, Any]: return _without_none(asdict(self))

@dataclass(frozen=True)
class ExpirePaymentIntentRequest:
    reason: str
    expected_version: int
    def to_dict(self) -> Dict[str, Any]: return asdict(self)

@dataclass(frozen=True)
class UpdatePaymentIntentMetadataRequest:
    expected_version: int
    metadata: Mapping[str, Any]
    def to_dict(self) -> Dict[str, Any]: return asdict(self)

@dataclass(frozen=True)
class CreateReconciliationReportRequest:
    period_start: str
    period_end: str
    format: str = "jsonl_v1"
    def to_dict(self) -> Dict[str, Any]: return asdict(self)

@dataclass(frozen=True)
class SubmitPaymentProofRequest:
    payment_intent_id: str
    chain_id: str
    transaction_id: str
    def to_dict(self) -> Dict[str, Any]: return asdict(self)

@dataclass(frozen=True)
class PaymentRoute:
    id: str; intent_id: str; asset_id: str; provider: str
    expected_amount_atomic: AtomicAmount; asset_decimals: int; display_amount: str
    required_finality: int; status: str; version: int
    starts_at: str; expires_at: str; grace_ends_at: str; chain_id: Optional[str] = None
    address: Optional[str] = None; memo: Optional[str] = None; provider_id: Optional[str] = None
    provider_order_id: Optional[str] = None; provider_reference: Optional[str] = None; payment_url: Optional[str] = None
    @classmethod
    def from_dict(cls, value: Mapping[str, Any]) -> "PaymentRoute": return cls(**{key: value.get(key) for key in cls.__dataclass_fields__})

@dataclass(frozen=True)
class PaymentIntent:
    id: str; merchant_id: str; merchant_order_id: str; amount_minor: AtomicAmount
    currency: str; currency_scale: int; status: str; allowed_routes: List[RouteSelector]
    version: int; created_at: str; updated_at: str; expires_at: str; routes: List[PaymentRoute]
    customer_reference: Optional[str] = None; description: Optional[str] = None
    status_reason: Optional[str] = None; metadata: Optional[Dict[str, Any]] = None
    settled_at: Optional[str] = None; cancelled_at: Optional[str] = None; checkout_token: Optional[str] = None
    @classmethod
    def from_dict(cls, value: Mapping[str, Any]) -> "PaymentIntent":
        data = {key: value.get(key) for key in cls.__dataclass_fields__}
        data["allowed_routes"] = [RouteSelector(**item) for item in value.get("allowed_routes", [])]
        data["routes"] = [PaymentRoute.from_dict(item) for item in value.get("routes", [])]
        return cls(**data)

@dataclass(frozen=True)
class PaymentProof:
    id: str; merchant_id: str; payment_intent_id: str; chain_id: str; transaction_id: str
    status: str; transfer_event_ids: List[str]; created_at: str; updated_at: str; version: int
    @classmethod
    def from_dict(cls, value: Mapping[str, Any]) -> "PaymentProof": return cls(**{key: value.get(key) for key in cls.__dataclass_fields__})

@dataclass(frozen=True)
class Asset:
    id: str; chain_id: str; symbol: str; name: str; kind: str; decimals: int
    status: str; minimum_deposit_atomic: AtomicAmount; contract: Optional[str] = None
    @classmethod
    def from_dict(cls, value: Mapping[str, Any]) -> "Asset": return cls(**{key: value.get(key) for key in cls.__dataclass_fields__})

@dataclass(frozen=True)
class Envelope(Generic[T]):
    data: T
    request_id: str
    api_version: str

@dataclass(frozen=True)
class CursorPage(Generic[T]):
    items: List[T]
    next_cursor: str = ""

@dataclass(frozen=True)
class EventPage(Generic[T]):
    items: List[T]
    next_cursor: str
    next_sequence: str

@dataclass(frozen=True)
class WebhookEvent:
    event_id: str; event_type: str; schema_version: str; sequence: int; occurred_at: str
    merchant_id: str; livemode: bool; payment_intent: Mapping[str, Any]
    settlement: Optional[Mapping[str, Any]] = None
    observation: Optional[Mapping[str, Any]] = None
    resolution: Optional[Mapping[str, Any]] = None
    @classmethod
    def from_dict(cls, value: Mapping[str, Any]) -> "WebhookEvent": return cls(**{key: value.get(key) for key in cls.__dataclass_fields__})

@dataclass(frozen=True)
class CheckoutRoute:
    id: str; provider: str; asset: str; amount: str
    provider_id: Optional[str] = None; network: Optional[str] = None; address: Optional[str] = None
    payment_url: Optional[str] = None; transaction_hash: Optional[str] = None; explorer_url: Optional[str] = None

@dataclass(frozen=True)
class CheckoutSession:
    intent_id: str; order_id: str; status: str; expires_at: str; selected_route_id: str
    routes: List[CheckoutRoute] = field(default_factory=list)

def _without_none(value: Dict[str, Any]) -> Dict[str, Any]: return {key: item for key, item in value.items() if item is not None}
