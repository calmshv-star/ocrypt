from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import pytest

FIXTURES = Path(__file__).resolve().parents[1] / "fixtures"


def load_json(name: str) -> dict[str, Any]:
    return json.loads((FIXTURES / name).read_text(encoding="utf-8"))


def assert_exact_money_strings(value: Any, path: str = "$") -> None:
    money_fields = {
        "amount_minor",
        "expected_raw",
        "received_raw",
        "credited_raw",
        "expected_amount_atomic",
        "received_amount_atomic",
    }
    if isinstance(value, dict):
        for key, child in value.items():
            child_path = f"{path}.{key}"
            if key in money_fields:
                assert isinstance(child, str), f"{child_path} must be a JSON string"
                assert child.isascii() and child.isdecimal(), f"{child_path} must be an integer string"
            assert_exact_money_strings(child, child_path)
    elif isinstance(value, list):
        for index, child in enumerate(value):
            assert_exact_money_strings(child, f"{path}[{index}]")


def test_golden_webhook_uses_exact_money_strings() -> None:
    event = load_json("payment_settled.json")
    assert_exact_money_strings(event)
    assert int(event["settlement"]["received_raw"]) == 6_380_000


@pytest.mark.parametrize("bad_value", [6_380_000, 6_380_000.0, True, None, "6.38", "-1", "1e6"])
def test_numeric_or_non_atomic_money_is_rejected(bad_value: Any) -> None:
    event = load_json("payment_settled.json")
    event["settlement"]["received_raw"] = bad_value
    with pytest.raises(AssertionError):
        assert_exact_money_strings(event)


def test_state_contract_is_closed_and_has_no_unknown_targets() -> None:
    contract = load_json("state_contract.json")
    for machine in ("intent", "unmatched"):
        states = set(contract[machine])
        targets = {target for allowed in contract[machine].values() for target in allowed}
        assert targets <= states, f"{machine} has transitions to undefined states: {targets - states}"


def test_settlement_never_transitions_silently_back_to_pending() -> None:
    intent = load_json("state_contract.json")["intent"]
    assert "pending" not in intent["settled"]
    assert "reorg_review" in intent["settled"]
    assert set(intent["reorg_review"]) == {"settled", "reversed"}


def test_unmatched_resolution_requires_candidates_binding_and_verification() -> None:
    unmatched = load_json("state_contract.json")["unmatched"]
    assert "resolved" not in unmatched["new"]
    assert "resolved" not in unmatched["candidates_ready"]
    assert "resolved" not in unmatched["bound"]
    assert "resolved" not in unmatched["verification_requested"]
    assert unmatched["verified"] == ["resolved", "conflict", "reorged"]


def test_reorg_paths_are_explicit_for_settled_and_resolved_money() -> None:
    contract = load_json("state_contract.json")
    assert "reorg_review" in contract["intent"]["settled"]
    assert "reorged" in contract["unmatched"]["resolved"]
    assert contract["unmatched"]["reorged"] == []


def manual_resolution_approval_is_valid(
    *,
    requested_by: str,
    approved_by: str,
    accept_shortfall: bool = False,
    accept_late_payment: bool = False,
    accept_cross_asset: bool = False,
) -> bool:
    del accept_late_payment  # late-only review is auditable but not a default four-eyes trigger
    if not (accept_shortfall or accept_cross_asset):
        return True
    return bool(approved_by) and approved_by != requested_by


@pytest.mark.parametrize("risk_flag", ["accept_shortfall", "accept_cross_asset"])
def test_shortfall_and_cross_asset_manual_resolution_require_four_eyes(risk_flag: str) -> None:
    request = {risk_flag: True}
    assert not manual_resolution_approval_is_valid(
        requested_by="operator-a", approved_by="", **request
    )
    assert not manual_resolution_approval_is_valid(
        requested_by="operator-a", approved_by="operator-a", **request
    )
    assert manual_resolution_approval_is_valid(
        requested_by="operator-a", approved_by="operator-b", **request
    )


def test_late_only_resolution_does_not_invent_a_second_approval_requirement() -> None:
    assert manual_resolution_approval_is_valid(
        requested_by="operator-a",
        approved_by="",
        accept_late_payment=True,
    )
