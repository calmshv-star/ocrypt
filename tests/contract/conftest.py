from __future__ import annotations

import pytest

from tests.contract.client import MerchantTestClient


@pytest.fixture
def api(merchant_base_url: str, merchant_credentials: tuple[str, str]) -> MerchantTestClient:
    key_id, secret = merchant_credentials
    return MerchantTestClient(merchant_base_url, key_id, secret)


@pytest.fixture
def sandbox_api(sandbox_base_url: str, sandbox_credentials: tuple[str, str]) -> MerchantTestClient:
    key_id, secret = sandbox_credentials
    return MerchantTestClient(sandbox_base_url, key_id, secret)
