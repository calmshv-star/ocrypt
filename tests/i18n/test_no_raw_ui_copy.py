from __future__ import annotations

import re
from pathlib import Path

import pytest

PLATFORM = Path(__file__).resolve().parents[2]
SOURCE_ROOTS = (PLATFORM / "apps", PLATFORM / "packages" / "ui")

# Tokens that are intentionally language-neutral when they appear alone.
NEUTRAL_TEXT = {
    "API",
    "RPC",
    "QR",
    "AI",
    "WAL",
    "WORM",
    "SLA",
    "CASE",
    "ID",
    "USD",
    "USDT",
    "USDC",
    "BTC",
    "ETH",
    "TRX",
    "TRON",
    "TON",
    "Solana",
    "Ethereum",
    "Explorer",
    "K",
    "⋯",
}

TECHNICAL_NAMES = {
    "bitcoin",
    "ethereum",
    "solana",
    "tron",
    "ton",
    "evm",
    "move",
    "usdt",
    "usdc",
    "btc",
    "eth",
    "trx",
    "sol",
    "hmac-sha256",
    "sha-256",
    "ed25519",
    "mtls",
    "json",
    "http",
    "https",
    "rpc",
    "api",
    "qr",
    "wal",
    "worm",
    "sla",
}


def user_facing_literals(path: Path) -> list[tuple[int, str]]:
    findings: list[tuple[int, str]] = []
    for line_number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        # Literal JSX children: <Button>Retry delivery</Button>. Expressions and
        # translation calls contain braces and are intentionally excluded.
        for match in re.finditer(r">\s*([^<>{}]+?)\s*<", line):
            value = " ".join(match.group(1).split())
            if _must_translate(value):
                findings.append((line_number, value))
        # Accessibility and input copy is just as user-facing as visible text.
        for match in re.finditer(
            r'\b(?:aria-label|placeholder|title|alt|label)\s*=\s*["\']([^"\'{][^"\']*)["\']',
            line,
        ):
            value = " ".join(match.group(1).split())
            if _must_translate(value):
                findings.append((line_number, value))
        for match in re.finditer(
            r"\b(?:aria-label|placeholder|title|alt|label)\s*=\s*\{`([^`]*)`\}", line
        ):
            value = re.sub(r"\$\{[^}]+\}", "", match.group(1))
            value = " ".join(value.split())
            if _must_translate(value):
                findings.append((line_number, value))
        # Component-library fallback labels are shipped copy too. A default such
        # as `empty = "No records"` used to bypass the JSX-only scan and would
        # silently reintroduce English whenever a caller omitted the prop.
        for match in re.finditer(
            r"\b(?:empty|emptyMessage|rowsLabel|previousLabel|nextLabel|"
            r"label|title|description|placeholder|caption|helperText|message)"
            r"\s*(?::|=)\s*[\"']([^\"']+)[\"']",
            line,
        ):
            value = " ".join(match.group(1).split())
            if _must_translate(value):
                findings.append((line_number, value))
    return findings


def _must_translate(value: str) -> bool:
    if not value or value in NEUTRAL_TEXT:
        return False
    if _is_technical_atom(value):
        return False
    if value.startswith(("http://", "https://", "#/", "/v1/")):
        return False
    if re.fullmatch(r"[\d\s.,:%+\-/]+", value):
        return False
    # Exact sample values, timestamps and abbreviated counters carry no grammar.
    if re.fullmatch(
        r"[≈~]?[+-]?\s*\d[\d.,]*(?:[kKmMgG]|\s*(?:BTC|ETH|SOL|TON|TRX|USDT|USDC|USD|EUR|RUB|CNY))?",
        value,
    ):
        return False
    if re.fullmatch(r"(?:p\d{1,3}|\d{1,2}:\d{2}(?::\d{2})?\s*UTC)", value, re.IGNORECASE):
        return False
    # Canonical IDs, hashes, addresses and event indices are copied verbatim.
    if re.fullmatch(r"(?:pi|evt|tx|set|st|dlv|mk|whsec|kv|trace)_[A-Za-z0-9…-]+", value):
        return False
    if re.fullmatch(r"(?:sha256:|0x)[A-Fa-f0-9…]+", value):
        return False
    if re.fullmatch(r"(?:trace|log|outer|inner|lt):[A-Za-z0-9,._-]+", value):
        return False
    if re.fullmatch(r"\d+(?:\.\d+)?\s*(?:ns|µs|ms|s)", value):
        return False
    if re.fullmatch(r"\d{3}\s*·\s*\d+(?:\.\d+)?\s*ms", value):
        return False
    if "·" in value and all(_is_technical_atom(part.strip()) for part in value.split("·")):
        return False
    if re.fullmatch(r"[A-Za-z0-9]{3,12}…[A-Za-z0-9]{3,12}", value):
        return False
    if not re.search(r"\s", value) and len(value) >= 16 and re.search(r"\d", value):
        return False
    # Code-ish values such as event IDs and CSS arrows are not prose.
    if re.fullmatch(r"[a-z0-9_.:-]+", value) and ("_" in value or "." in value or ":" in value):
        return False
    return bool(re.search(r"[A-Za-zÀ-žА-Яа-я]", value))


def _is_technical_atom(value: str) -> bool:
    if value in NEUTRAL_TEXT or value.casefold() in TECHNICAL_NAMES:
        return True
    return bool(
        re.fullmatch(r"(?:pi|evt|tx|set|st|dlv|mk|whsec|kv|trace)_[A-Za-z0-9…-]+", value)
        or re.fullmatch(r"(?:sha256:|0x)[A-Fa-f0-9…]+", value)
        or re.fullmatch(r"[A-Za-z0-9]{3,12}…[A-Za-z0-9]{3,12}", value)
    )


@pytest.mark.parametrize(
    "value",
    [
        "pi_01JQ8GT4M0HX",
        "trace:0,2,1",
        "sha256:7ce2…91ad",
        "TWb4…19Vp",
        "≈ 1,280.00 USD",
        "201 · 84 ms",
        "HMAC-SHA256 · kv_4",
        "Tron · USDT",
    ],
)
def test_protocol_sample_values_are_language_neutral(value: str) -> None:
    assert not _must_translate(value)


@pytest.mark.parametrize(
    "value",
    ["1 block", "4 slots", "Refresh candidates", "Candidate score %", "Search results"],
)
def test_human_copy_and_units_still_require_translation(value: str) -> None:
    assert _must_translate(value)


def test_component_default_copy_is_detected(tmp_path: Path) -> None:
    fixture = tmp_path / "component.tsx"
    fixture.write_text(
        'function Table({ empty = "No records", nextLabel = "Next" }) { return null; }',
        encoding="utf-8",
    )
    assert user_facing_literals(fixture) == [(1, "No records"), (1, "Next")]


def test_react_ui_has_no_untranslated_literal_copy() -> None:
    findings: list[str] = []
    for root in SOURCE_ROOTS:
        if not root.exists():
            continue
        for path in sorted(root.rglob("*.tsx")):
            # Test fixtures intentionally contain source-language copy to assert
            # rendering behavior; the release gate applies to shipped UI only.
            if path.name.endswith((".test.tsx", ".spec.tsx")):
                continue
            for line, value in user_facing_literals(path):
                findings.append(f"{path.relative_to(PLATFORM)}:{line}: {value}")
    assert not findings, (
        "User-facing JSX literals bypass the six locale catalogs. Move them to "
        "@merchant/i18n or explicitly document a language-neutral exemption:\n"
        + "\n".join(findings[:80])
        + (f"\n... and {len(findings) - 80} more" if len(findings) > 80 else "")
    )
