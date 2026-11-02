"""The merchant cabinet must be installable as a PWA (product spec: cabinet on a phone)."""

import json
import struct
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
ADMIN = ROOT / "apps" / "admin"


def png_size(path: Path) -> tuple[int, int]:
    header = path.read_bytes()[:24]
    assert header[:8] == b"\x89PNG\r\n\x1a\n", f"{path.name} is not a PNG"
    return struct.unpack(">II", header[16:24])


def test_manifest_declares_an_installable_standalone_app() -> None:
    manifest = json.loads((ADMIN / "public" / "manifest.webmanifest").read_text())
    assert manifest["display"] == "standalone"
    assert manifest["start_url"].startswith("/") and manifest["scope"] == "/"
    sizes = {}
    for icon in manifest["icons"]:
        path = ADMIN / "public" / icon["src"].lstrip("/")
        width, height = png_size(path)
        assert icon["sizes"] == f"{width}x{height}"
        sizes.setdefault(icon["purpose"], set()).add(width)
    assert {192, 512} <= sizes["any"]
    assert 512 in sizes["maskable"]


def test_index_links_the_manifest_and_touch_icon() -> None:
    html = (ADMIN / "index.html").read_text()
    assert '<link rel="manifest" href="/manifest.webmanifest" />' in html
    assert '<link rel="apple-touch-icon" href="/apple-touch-icon.png" />' in html
    assert png_size(ADMIN / "public" / "apple-touch-icon.png") == (180, 180)
