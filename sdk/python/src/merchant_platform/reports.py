import base64
import hashlib
from typing import Mapping, Any

def reconciliation_signature_message(report_id: str, snapshot_ledger_sequence: str, digest: bytes) -> bytes:
    if len(digest) != 32: raise ValueError("SHA-256 digest must be 32 bytes")
    return b"merchant-reconciliation-jsonl-v1\0" + report_id.encode() + b"\0" + snapshot_ledger_sequence.encode() + b"\0" + digest

def verify_reconciliation_report(raw: bytes, report: Mapping[str, Any], public_keys: Mapping[str, bytes]) -> None:
    if report.get("status") != "ready": raise ValueError("report is not ready")
    digest = hashlib.sha256(raw).digest()
    if digest.hex() != report.get("object_sha256"): raise ValueError("reconciliation report digest mismatch")
    if "object_size_bytes" in report and int(report["object_size_bytes"]) != len(raw): raise ValueError("reconciliation report size mismatch")
    key_id = report.get("signing_key_id"); public_key = public_keys.get(key_id)
    if public_key is None: raise ValueError("unknown reconciliation signing key: " + str(key_id))
    signature = base64.urlsafe_b64decode(str(report["signature"]) + "=" * (-len(str(report["signature"])) % 4))
    try:
        from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey
    except ImportError as error:
        raise RuntimeError("install merchant-platform-sdk[reports] for Ed25519 verification") from error
    Ed25519PublicKey.from_public_bytes(public_key).verify(signature, reconciliation_signature_message(str(report["id"]), str(report["snapshot_ledger_sequence"]), digest))
