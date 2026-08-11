import hashlib
import pytest
from merchant_platform.reports import reconciliation_signature_message, verify_reconciliation_report

def test_report_signature_message_is_domain_separated_and_binary_safe():
    digest = hashlib.sha256(b"header\nfooter\n").digest()
    assert reconciliation_signature_message("report-id", "42", digest) == b"merchant-reconciliation-jsonl-v1\0report-id\0" + b"42\0" + digest

def test_report_verification_rejects_unknown_frozen_key_before_crypto_dependency():
    raw = b'\n'
    report = {"status":"ready","id":"r","snapshot_ledger_sequence":"0","object_sha256":hashlib.sha256(raw).hexdigest(),"object_size_bytes":"1","signature":"AA","signing_key_id":"retired"}
    with pytest.raises(ValueError, match="unknown reconciliation signing key"):
        verify_reconciliation_report(raw, report, {})
