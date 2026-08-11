import pytest
from merchant_platform import MerchantApiError
from merchant_platform.integration import RetryPolicy, instrument, sandbox_endpoint, with_retry

def test_retry_requires_idempotency_and_honors_retry_after():
    with pytest.raises(ValueError,match="idempotency key"):with_retry(lambda:None,safe=False)
    attempts=[];sleeps=[]
    def action():
        attempts.append(1)
        if len(attempts)<3:raise MerchantApiError(429,"rate_limited","later",retryable=True,retry_after_seconds=0.75)
        return "ok"
    assert with_retry(action,safe=False,idempotency_key="order-42:write",policy=RetryPolicy(),sleep=sleeps.append,random_value=lambda:0.5)=="ok"
    assert sleeps==[0.75,0.75]

def test_telemetry_is_low_cardinality_and_sandbox_is_explicit():
    events=[];assert instrument("payment_intent.get","GET",events.append,lambda:42)==42
    assert sandbox_endpoint("https://sandbox.example").environment=="sandbox"
    assert all(not ({"url","body","headers","secret"}&set(event)) for event in events)
    with pytest.raises(ValueError,match="low-cardinality"): instrument("https://secret.example/order?id=1","GET",None,lambda:1)
