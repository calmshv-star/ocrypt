import importlib.util
import json
import re
from pathlib import Path

import pytest

SPEC = importlib.util.spec_from_file_location("signup_screening_import", Path(__file__).resolve().parents[2] / "scripts/signup_screening_import.py")
module = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(module)

# Synthetic rows in the OFAC legacy CSV layout (no header, -0- is null).
SDN = (
    '101,"ROGUE TRADING COMPANY LTD",-0- ,"SDGT",-0- ,-0- ,-0- ,-0- ,-0- ,-0- ,-0- ,"Website www.rogue-trading.example; Email Address info@gmail.com; alt. Website https://rogue-shop.example/en."\r\n'
    '102,"IVANOV, Ivan Petrovich","individual","UKRAINE-EO13660",-0- ,-0- ,-0- ,-0- ,-0- ,-0- ,-0- ,"DOB 01 Jan 1970."\r\n'
    '103,"SEA PHANTOM","vessel","IRAN",-0- ,"9XYZ","Crude Oil Tanker",-0- ,-0- ,"Panama",-0- ,"Website www.not-a-company.example"\r\n'
    '104,"QUOTE ""INSIDE"" HOLDING",-0- ,"SDGT",-0- ,-0- ,-0- ,-0- ,-0- ,-0- ,-0- ,-0- \r\n'
    '106,"FRONT MEDIA",-0- ,"SDGT",-0- ,-0- ,-0- ,-0- ,-0- ,-0- ,-0- ,"Website www.facebook.com/frontmedia; Website https://t.me/frontmedia; Website front-media.example/about."\r\n'
)
ALT = (
    '101,201,"aka","ROGUE COMMERCE",-0- \r\n'
    '103,202,"fka","OCEAN PHANTOM",-0- \r\n'
    '102,203,"aka","IVANOV, Vanya",-0- \r\n'
)
UN = """<?xml version="1.0" encoding="UTF-8"?>
<CONSOLIDATED_LIST xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" dateGenerated="2026-11-02T23:00:04.371Z">
  <INDIVIDUALS>
    <INDIVIDUAL>
      <DATAID>1000001</DATAID>
      <FIRST_NAME>PLACEHOLDER</FIRST_NAME>
      <SECOND_NAME>PERSON </SECOND_NAME>
      <THIRD_NAME/>
      <REFERENCE_NUMBER>EXi.001</REFERENCE_NUMBER>
      <NAME_ORIGINAL_SCRIPT>Плейсхолдер Персон</NAME_ORIGINAL_SCRIPT>
      <INDIVIDUAL_ALIAS><QUALITY>Good</QUALITY><ALIAS_NAME>PLACE HOLDER</ALIAS_NAME></INDIVIDUAL_ALIAS>
      <INDIVIDUAL_ALIAS><QUALITY>Low</QUALITY><ALIAS_NAME>PH</ALIAS_NAME></INDIVIDUAL_ALIAS>
      <INDIVIDUAL_ALIAS><QUALITY/><ALIAS_NAME/></INDIVIDUAL_ALIAS>
    </INDIVIDUAL>
  </INDIVIDUALS>
  <ENTITIES>
    <ENTITY>
      <DATAID>2000001</DATAID>
      <FIRST_NAME>PLACEHOLDER COMPANY LTD</FIRST_NAME>
      <REFERENCE_NUMBER>EXe.001</REFERENCE_NUMBER>
      <ENTITY_ALIAS><QUALITY>a.k.a.</QUALITY><ALIAS_NAME>PLACEHOLDER ALIAS CO</ALIAS_NAME></ENTITY_ALIAS>
    </ENTITY>
    <ENTITY>
      <DATAID>2000002</DATAID>
      <FIRST_NAME>NO REFERENCE TRADING</FIRST_NAME>
      <REFERENCE_NUMBER/>
    </ENTITY>
  </ENTITIES>
</CONSOLIDATED_LIST>
"""


def write(tmp_path, name, text, encoding="utf-8"):
    path = tmp_path / name
    path.write_bytes(text.encode(encoding))
    return path


def entries_of(sql):
    match = re.search(r"\$(ocrypt_[0-9a-f]{16})\$(.*)\$\1\$::jsonb", sql, re.S)
    assert match, sql[:300]
    return {(e["kind"], e["value"], e["reference"]) for e in json.loads(match.group(2))}


def test_ofac_names_aliases_and_websites(tmp_path):
    entries = module.ofac_entries(write(tmp_path, "SDN.CSV", SDN), write(tmp_path, "ALT.CSV", ALT))
    # Entities (SDN_Type null) are matched by any single listed word too;
    # individuals are not, so a common given name never holds everyone.
    assert entries == [
        {"kind": "name", "value": "ROGUE TRADING COMPANY LTD", "reference": "SDN-101", "whole_word": True},
        {"kind": "domain", "value": "www.rogue-trading.example", "reference": "SDN-101"},
        {"kind": "domain", "value": "rogue-shop.example", "reference": "SDN-101"},
        {"kind": "name", "value": "IVANOV, Ivan Petrovich", "reference": "SDN-102"},
        {"kind": "name", "value": 'QUOTE "INSIDE" HOLDING', "reference": "SDN-104", "whole_word": True},
        {"kind": "name", "value": "FRONT MEDIA", "reference": "SDN-106", "whole_word": True},
        {"kind": "domain", "value": "front-media.example", "reference": "SDN-106"},
        {"kind": "name", "value": "ROGUE COMMERCE", "reference": "SDN-101", "whole_word": True},
        {"kind": "name", "value": "IVANOV, Vanya", "reference": "SDN-102"},
    ]


def test_ofac_never_lists_free_mail_or_vessels(tmp_path):
    entries = module.ofac_entries(write(tmp_path, "SDN.CSV", SDN), write(tmp_path, "ALT.CSV", ALT))
    values = {e["value"] for e in entries}
    assert "gmail.com" not in values and "SEA PHANTOM" not in values and "OCEAN PHANTOM" not in values
    assert "www.not-a-company.example" not in values
    # A page on a shared platform must not block the whole platform.
    assert not {"facebook.com", "www.facebook.com", "t.me"} & values


def test_ofac_reads_windows_1252_and_rejects_wrong_layout(tmp_path):
    cp1252 = write(tmp_path, "SDN.CSV", '105,"CAFÉ NOIR SARL",-0- ,"SDGT",-0- ,-0- ,-0- ,-0- ,-0- ,-0- ,-0- ,-0- \r\n', "cp1252")
    assert module.ofac_entries(cp1252, None) == [{"kind": "name", "value": "CAFÉ NOIR SARL", "reference": "SDN-105", "whole_word": True}]
    with pytest.raises(ValueError):
        module.ofac_entries(write(tmp_path, "BAD.CSV", "ent_num,SDN_Name\r\n1,X\r\n"), None)


def test_un_individuals_entities_scripts_and_good_aliases(tmp_path):
    published, entries = module.un_entries(write(tmp_path, "un.xml", UN))
    assert published == "2026-11-02T23:00:04.371Z"
    assert {(e["kind"], e["value"], e["reference"], e.get("whole_word", False)) for e in entries} == {
        ("name", "PLACEHOLDER PERSON", "UN-EXi.001", False),
        ("name", "Плейсхолдер Персон", "UN-EXi.001", False),
        ("name", "PLACE HOLDER", "UN-EXi.001", False),
        ("name", "PLACEHOLDER COMPANY LTD", "UN-EXe.001", True),
        ("name", "PLACEHOLDER ALIAS CO", "UN-EXe.001", True),
        ("name", "NO REFERENCE TRADING", "UN-2000002", True),
    }


def test_un_date_parses_on_python_before_3_11(tmp_path, monkeypatch):
    # dateGenerated ends in Z, which datetime.fromisoformat rejects before 3.11.
    real = module.datetime

    class OldDatetime:
        @staticmethod
        def fromisoformat(value):
            if value.endswith("Z"):
                raise ValueError(f"Invalid isoformat string: {value!r}")
            return real.fromisoformat(value)
    monkeypatch.setattr(module, "datetime", OldDatetime)
    assert module.main(["un", "--xml", str(write(tmp_path, "un.xml", UN))]) == 0


def test_un_refuses_entity_declarations(tmp_path):
    hostile = '<?xml version="1.0"?><!DOCTYPE x [<!ENTITY a "aaaa">]><CONSOLIDATED_LIST dateGenerated="2026-11-02T00:00:00Z"/>'
    with pytest.raises(ValueError):
        module.un_entries(write(tmp_path, "evil.xml", hostile))


def base58(raw):
    number, out = int.from_bytes(raw, "big"), ""
    while number:
        number, rest = divmod(number, 58)
        out = module.BASE58[rest] + out
    return "1" * (len(raw) - len(raw.lstrip(b"\x00"))) + out


def base58check(payload):
    """Test helper: a valid TRON address for 20 synthetic bytes."""
    import hashlib
    return base58(payload + hashlib.sha256(hashlib.sha256(payload).digest()).digest()[:4])


TRON = base58check(b"\x41" + bytes(range(1, 21)))
SOLANA = base58(bytes(range(7, 39)))


def test_addresses_of_watched_families_only(tmp_path):
    remarks = f"Digital Currency Address - ETH 0xAbC0000000000000000000000000000000000001; Digital Currency Address - USDT {TRON}; alt. Digital Currency Address - XBT 1BoatSLRHtKNngkdXEeobR76b53LETtpyT; Digital Currency Address - SOL {SOLANA}; Digital Currency Address - TRX T{'1' * 33}."
    sdn = write(tmp_path, "SDN.CSV", f'301,"CRYPTO MIXER",-0- ,"CYBER2",-0- ,-0- ,-0- ,-0- ,-0- ,-0- ,-0- ,"{remarks}"\r\n')
    found = module.ofac_addresses(sdn)
    assert found == [
        {"family": "evm", "address": "0xabc0000000000000000000000000000000000001", "currency": "ETH", "reference": "SDN-301"},
        {"family": "tron", "address": TRON, "hex": bytes(range(1, 21)).hex(), "currency": "USDT", "reference": "SDN-301"},
        {"family": "solana", "address": SOLANA, "currency": "SOL", "reference": "SDN-301"},
    ], "Bitcoin is not watched and a TRON address with a bad checksum is dropped"


def test_operator_csv(tmp_path):
    path = write(tmp_path, "operator.csv", f"kind,value,reference\r\ndomain,blocked.example,case-7\r\nname,Bad Actor Ltd,case-8\r\naddress,TRX {TRON},case-9\r\n")
    entries, addresses = module.operator_entries(path)
    assert entries == [
        {"kind": "domain", "value": "blocked.example", "reference": "case-7"},
        {"kind": "name", "value": "Bad Actor Ltd", "reference": "case-8"},
    ]
    assert addresses == [{"family": "tron", "address": TRON, "hex": bytes(range(1, 21)).hex(), "currency": "TRX", "reference": "case-9"}]
    for bad in ("kind,value\r\nname,X\r\n", "kind,value,reference\r\nip,10.0.0.1,x\r\n", "kind,value,reference\r\nname,,x\r\n", "kind,value,reference\r\naddress,XBT 1BoatSLRHtKNngkdXEeobR76b53LETtpyT,x\r\n"):
        with pytest.raises(ValueError):
            module.operator_entries(write(tmp_path, "bad.csv", bad))


def test_sql_is_one_transaction_and_quotes_any_text(tmp_path):
    hostile = [{"kind": "name", "value": "x$$'); DROP TABLE tenants; --", "reference": "r'1"}, {"kind": "name", "value": "$ocrypt_0000000000000000$", "reference": "r2"}]
    sql = module.render_sql("ofac_sdn", "OFAC SDN", "2026-11-02T23:00:04.371Z", "8 days", hostile)
    assert sql.startswith("\\set ON_ERROR_STOP on\nBEGIN;\n") and sql.rstrip().endswith("COMMIT;")
    assert sql.count("replace_signup_screening_source(") == 1 and sql.count("replace_sanctioned_addresses('ofac_sdn',") == 1
    assert sql.index("replace_signup_screening_source(") < sql.index("replace_sanctioned_addresses(") < sql.index("COMMIT;")
    assert entries_of(sql) == {(e["kind"], e["value"], e["reference"]) for e in hostile}
    assert "'2026-11-02T23:00:04.371Z'::timestamptz" in sql and "'8 days'::interval" in sql
    operator = module.render_sql("operator", "Operator list", None, None, hostile[:1])
    assert "NULL::timestamptz" in operator and "NULL::interval" in operator
    for bad in [("OFAC SDN", "d", None, "8 days"), ("ofac_sdn", "d", "yesterday", "8 days"), ("ofac_sdn", "d", None, "8 days'; --")]:
        with pytest.raises(ValueError):
            module.render_sql(*bad, hostile)


def test_cli_prints_sql_for_psql(tmp_path, capsys):
    assert module.main(["ofac", "--sdn", str(write(tmp_path, "SDN.CSV", SDN)), "--alt", str(write(tmp_path, "ALT.CSV", ALT))]) == 0
    sql = capsys.readouterr().out
    assert ("name", "ROGUE COMMERCE", "SDN-101") in entries_of(sql) and "'ofac_sdn'" in sql
    assert module.main(["un", "--xml", str(write(tmp_path, "un.xml", UN))]) == 0
    assert "'un_consolidated'" in capsys.readouterr().out
