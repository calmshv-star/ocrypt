from __future__ import annotations

import os
import sys
from pathlib import Path

import pytest

PLATFORM_ROOT = Path(__file__).resolve().parents[1]
WORKSPACE_ROOT = PLATFORM_ROOT.parent

for path in (PLATFORM_ROOT, WORKSPACE_ROOT):
    if str(path) not in sys.path:
        sys.path.insert(0, str(path))


@pytest.fixture(scope="session")
def merchant_base_url() -> str:
    value = os.environ.get("MERCHANT_BASE_URL")
    if not value:
        pytest.skip("MERCHANT_BASE_URL is not set; black-box API contract is not enabled")
    return value.rstrip("/")


@pytest.fixture(scope="session")
def merchant_credentials() -> tuple[str, str]:
    key_id = os.environ.get("MERCHANT_KEY_ID")
    secret = os.environ.get("MERCHANT_SECRET")
    if not key_id or not secret:
        pytest.skip("MERCHANT_KEY_ID/MERCHANT_SECRET are not set")
    return key_id, secret


@pytest.fixture(scope="session")
def sandbox_base_url() -> str:
    value = os.environ.get("SANDBOX_BASE_URL")
    if not value:
        pytest.skip("SANDBOX_BASE_URL is not set; sandbox contract is not enabled")
    return value.rstrip("/")


@pytest.fixture(scope="session")
def sandbox_credentials() -> tuple[str, str]:
    key_id = os.environ.get("SANDBOX_KEY_ID")
    secret = os.environ.get("SANDBOX_SECRET")
    if not key_id or not secret:
        pytest.skip("SANDBOX_KEY_ID/SANDBOX_SECRET are not set")
    return key_id, secret
