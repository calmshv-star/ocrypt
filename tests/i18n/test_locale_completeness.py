from __future__ import annotations

import json
import re
from pathlib import Path

import pytest

PLATFORM = Path(__file__).resolve().parents[2]
I18N = PLATFORM / "packages" / "i18n" / "src"
EXPECTED = ("en", "zh-CN", "es", "fr", "de", "ru")
CATALOGS = {
    "zh-CN": ("zh.ts", "zhCN"),
    "es": ("es.ts", "es"),
    "fr": ("fr.ts", "fr"),
    "de": ("de.ts", "de"),
    "ru": ("ru.ts", "ru"),
}


def parse_properties(block: str) -> dict[str, str]:
    properties: dict[str, str] = {}
    pattern = re.compile(r'^\s*"([^"]+)":\s*("(?:[^"\\]|\\.)*")\s*,?\s*$', re.MULTILINE)
    for key, encoded_value in pattern.findall(block):
        properties[key] = json.loads(encoded_value)
    return properties


def extract_object(source: str, declaration: str, ending: str) -> dict[str, str]:
    match = re.search(
        rf"(?:export )?const\s+{re.escape(declaration)}\s*=\s*\{{(.*?)\n\}}\s*{ending}",
        source,
        re.DOTALL,
    )
    assert match, f"locale object {declaration} is missing"
    return parse_properties(match.group(1))


def test_all_six_documentation_locales_are_substantive() -> None:
    markers = {
        "en": ("Product guide", "Developer guide", "Operations guide"),
        "zh-CN": ("产品指南", "开发者指南", "运维指南"),
        "es": ("Guía de producto", "Guía para desarrolladores", "Guía de operaciones"),
        "fr": ("Guide produit", "Guide développeur", "Guide d'exploitation"),
        "de": ("Produktleitfaden", "Entwicklerleitfaden", "Betriebsleitfaden"),
        "ru": ("Для владельца продукта", "Для разработчика", "Для эксплуатации"),
    }
    for locale in EXPECTED:
        path = PLATFORM / "docs" / locale / "guide.md"
        assert path.is_file(), f"missing {locale} guide"
        text = path.read_text(encoding="utf-8")
        assert len(text) >= 4_000, f"{locale} guide is too short to be substantive"
        assert all(marker in text for marker in markers[locale]), f"{locale} guide has untranslated sections"
        assert not re.search(r"\b(TODO|TBD|PLACEHOLDER)\b", text, re.IGNORECASE)


@pytest.mark.parametrize("locale", CATALOGS)
def test_frontend_catalogs_have_identical_non_placeholder_key_sets(locale: str) -> None:
    english_source = (I18N / "messages.ts").read_text(encoding="utf-8")
    english = extract_object(english_source, "en", r"as\s+const;")
    assert len(english) >= 100, "English catalog was not parsed or is unexpectedly small"

    filename, variable = CATALOGS[locale]
    module = I18N / filename
    assert module.is_file(), f"{locale} catalog must live in {filename}"
    translated = extract_object(
        module.read_text(encoding="utf-8"), variable, r"satisfies\s+Messages;"
    )
    missing = set(english) - set(translated)
    extra = set(translated) - set(english)
    assert not missing, f"{locale} is missing {len(missing)} keys: {sorted(missing)[:8]}"
    assert not extra, f"{locale} has unknown keys: {sorted(extra)[:8]}"
    assert all(value.strip() and value != key for key, value in translated.items())
    unchanged = sum(translated[key] == value for key, value in english.items())
    assert unchanged / len(english) < 0.20, f"{locale} appears to fall back to English ({unchanged} identical values)"


@pytest.mark.parametrize("locale", CATALOGS)
def test_runtime_exports_each_real_catalog_instead_of_english_aliases(locale: str) -> None:
    source = (I18N / "locales.ts").read_text(encoding="utf-8")
    declared = re.search(r'export type Locale\s*=\s*([^;]+);', source)
    assert declared
    assert set(re.findall(r'"([^"]+)"', declared.group(1))) == set(EXPECTED)
    filename, variable = CATALOGS[locale]
    module_name = filename.removesuffix(".ts")
    assert f'import {{ {variable} }} from "./{module_name}";' in source
    quoted = f'"{locale}"' if "-" in locale else locale
    explicit = re.search(rf'^\s*{re.escape(quoted)}:\s*{variable},?\s*$', source, re.MULTILINE)
    shorthand = locale == variable and re.search(rf'^\s*{variable},?\s*$', source, re.MULTILINE)
    assert explicit or shorthand, (
        f"{locale} runtime catalog is not wired to its translation"
    )
