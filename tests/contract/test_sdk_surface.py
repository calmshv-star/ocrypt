from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]

CLIENTS = {
    "typescript": ROOT / "sdk/typescript/src/client.ts",
    "python": ROOT / "sdk/python/src/merchant_platform/client.py",
    "go": ROOT / "sdk/go/client.go",
    "php": ROOT / "sdk/php/src/Client.php",
    "java": ROOT / "sdk/java/src/main/java/com/merchantplatform/MerchantClient.java",
    "dotnet": ROOT / "sdk/dotnet/src/MerchantPlatform/MerchantClient.cs",
}

STABLE_PATHS = (
    "/expire",
    "/metadata",
    "/v1/payment-proofs",
    "/v1/events",
    "/v1/transfers",
    "/v1/quotes",
    "/v1/balances",
    "/v1/reconciliation",
    "/v1/reconciliation-reports",
    "/v1/payment-links",
    "/v1/checkout-sessions",
)


def test_all_language_clients_cover_the_stable_merchant_surface():
    for language, path in CLIENTS.items():
        source = path.read_text(encoding="utf-8")
        missing = [marker for marker in STABLE_PATHS if marker not in source]
        assert not missing, f"{language} SDK misses {missing}"
        assert "operator/unmatched" not in source
        assert "/v1/management/" not in source


def test_documentation_has_no_stale_provisional_event_claim():
    documentation = "\n".join(
        path.read_text(encoding="utf-8")
        for path in (ROOT / "sdk").rglob("*.md")
    ).lower()
    assert "listevents is provisional" not in documentation
    assert "listEvents` as provisional".lower() not in documentation


def test_six_localized_api_guides_exist_and_preserve_exact_money():
    for locale in ("en", "zh-CN", "es", "fr", "de", "ru"):
        guide = (ROOT / "docs" / locale / "api-integration.md").read_text(encoding="utf-8")
        assert "amount_minor" in guide
        assert "reconciliation:read" in guide
        assert "Ed25519" in guide
