from __future__ import annotations

import os

import pytest

pytestmark = pytest.mark.contract


def test_release_ci_requires_an_authenticated_api_target() -> None:
    if os.environ.get("REQUIRE_CONTRACT_TARGET") != "1":
        pytest.skip("set REQUIRE_CONTRACT_TARGET=1 in release CI")
    missing = [
        name
        for name in ("MERCHANT_BASE_URL", "MERCHANT_KEY_ID", "MERCHANT_SECRET")
        if not os.environ.get(name)
    ]
    assert not missing, f"release contract target is incomplete: {', '.join(missing)}"


def test_release_ci_requires_deterministic_chain_simulation() -> None:
    if os.environ.get("REQUIRE_SANDBOX_CONTRACT") != "1":
        pytest.skip("set REQUIRE_SANDBOX_CONTRACT=1 when sandbox states are a release gate")
    assert os.environ.get("RUN_SANDBOX_CONTRACT") == "1", (
        "REQUIRE_SANDBOX_CONTRACT also requires RUN_SANDBOX_CONTRACT=1"
    )
    missing = [
        name
        for name in ("SANDBOX_BASE_URL", "SANDBOX_KEY_ID", "SANDBOX_SECRET")
        if not os.environ.get(name)
    ]
    assert not missing, f"sandbox contract target is incomplete: {', '.join(missing)}"
    if os.environ.get("REQUIRE_CONTRACT_TARGET") == "1":
        merchant = os.environ.get("MERCHANT_BASE_URL", "").rstrip("/")
        sandbox = os.environ.get("SANDBOX_BASE_URL", "").rstrip("/")
        assert merchant and merchant != sandbox, (
            "production/canary and deterministic sandbox contracts require distinct base URLs"
        )
