from dataclasses import dataclass
import random, re, time
from typing import Any, Callable, Iterator, Mapping, Optional, TypeVar
from .client import MerchantApiError, MerchantClient

T = TypeVar("T")
@dataclass(frozen=True)
class EndpointConfig:
    environment: str
    base_url: str
def live_endpoint(base_url: str) -> EndpointConfig: return EndpointConfig("live", base_url)
def sandbox_endpoint(base_url: str) -> EndpointConfig: return EndpointConfig("sandbox", base_url)

TelemetryHook = Callable[[Mapping[str, Any]], None]
def instrument(operation: str, method: str, hook: Optional[TelemetryHook], action: Callable[[], T]) -> T:
    if re.fullmatch(r"[a-z][a-z0-9_.-]{0,63}", operation) is None or re.fullmatch(r"[A-Z]{3,7}", method) is None: raise ValueError("telemetry operation or method is not low-cardinality")
    started = time.monotonic(); hook and hook({"phase":"start","operation":operation,"method":method})
    try:
        value = action(); hook and hook({"phase":"end","operation":operation,"method":method,"status":200,"duration_ms":round((time.monotonic()-started)*1000)}); return value
    except Exception as error:
        hook and hook({"phase":"end","operation":operation,"method":method,"status":getattr(error,"status",0),"duration_ms":round((time.monotonic()-started)*1000),"retryable":bool(getattr(error,"retryable",False))}); raise

@dataclass(frozen=True)
class RetryPolicy:
    max_attempts: int = 4
    base_delay: float = 0.2
    max_delay: float = 5.0
    jitter_ratio: float = 0.2

def with_retry(action: Callable[[], T], *, safe: bool, idempotency_key: Optional[str] = None, policy: RetryPolicy = RetryPolicy(), sleep: Callable[[float], None] = time.sleep, random_value: Callable[[], float] = random.random) -> T:
    if not safe and not idempotency_key: raise ValueError("unsafe retries require an idempotency key")
    if not 1 <= policy.max_attempts <= 10: raise ValueError("max_attempts must be 1..10")
    for attempt in range(1, policy.max_attempts + 1):
        try: return action()
        except MerchantApiError as error:
            if not error.retryable or attempt == policy.max_attempts: raise
            delay = min(policy.max_delay, policy.base_delay * 2 ** (attempt - 1)); delay *= 1 + (random_value() * 2 - 1) * policy.jitter_ratio
            sleep(max(0.0, min(error.retry_after_seconds if error.retry_after_seconds is not None else delay, policy.max_delay)))
    raise AssertionError("unreachable")

def iterate_payment_intents(client: MerchantClient, status: Optional[str] = None, page_size: int = 100) -> Iterator[Any]:
    after = None
    while True:
        page = client.list_payment_intents(status=status, after=after, limit=page_size)
        yield from page.data.items
        after = page.data.next_cursor or None
        if after is None: return

def iterate_events(client: MerchantClient, after_sequence: int = 0, page_size: int = 100) -> Iterator[Any]:
    cursor = after_sequence
    while True:
        page = client.list_events(cursor, page_size)
        yield from page.data.items
        next_cursor = int(page.data.next_sequence)
        if next_cursor == cursor or not page.data.items: return
        cursor = next_cursor
