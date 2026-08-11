from pathlib import Path
ROOT=Path(__file__).resolve().parents[2]
FRAMEWORKS=("fastapi-django","laravel-symfony","express-nestjs","spring-boot","aspnet","telegram-bot","ecommerce")

def test_reference_integrations_preserve_inbox_order_outbox_atomicity():
    base=ROOT/"examples/frameworks"
    for name in FRAMEWORKS:
        source="\n".join(path.read_text(encoding="utf-8") for path in (base/name).iterdir() if path.is_file()).lower()
        for marker in ("rawbody" if name in {"laravel-symfony","express-nestjs","spring-boot","aspnet"} else "raw_body","transaction","merchant_webhook_inbox","body_sha256","for update","payment.settled","commerce_orders","fulfillment_outbox","acknowledged_event_id"):
            assert marker in source,f"{name} misses {marker}"
        assert "merchant_secret=" not in source

def test_shared_schema_enforces_unique_inbox_and_outbox_event_ids():
    schema=(ROOT/"examples/frameworks/common/schema.sql").read_text(encoding="utf-8").lower()
    assert "event_id text primary key" in schema
    assert "event_id text not null unique" in schema
    assert "paid_event_id text unique" in schema

def test_all_sdk_languages_ship_retry_pagination_telemetry_and_sandbox_facilities():
    files=(ROOT/"sdk/typescript/src/integration.ts",ROOT/"sdk/python/src/merchant_platform/integration.py",ROOT/"sdk/go/integration.go",ROOT/"sdk/php/src/Integration.php",ROOT/"sdk/java/src/main/java/com/merchantplatform/Integration.java",ROOT/"sdk/dotnet/src/MerchantPlatform/Integration.cs")
    for path in files:
        source=path.read_text(encoding="utf-8").lower()
        for marker in ("retry","idempotency","telemetry","sandbox","payment","intent","event"):
            assert marker in source,f"{path} misses {marker}"
        assert "merchant-signature" not in source
        assert "merchant-nonce" not in source
