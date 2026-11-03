#!/usr/bin/env python3
"""Turn an official sanctions list into one SQL transaction for psql.

Reads files the operator downloaded from the official source and prints SQL
that replaces one screening source atomically through
replace_signup_screening_source (migration 000059). Apply it with the
database owner role, for example:

  python3 scripts/signup_screening_import.py ofac --sdn SDN.CSV --alt ALT.CSV \
    | psql "$OWNER_DATABASE_URL"

The script never connects to the network or the database. Names are sent as
published; the database computes every match key, so the importer and the
registration check cannot disagree.
"""
import argparse
import csv
import io
import json
import re
import secrets
import sys
import xml.etree.ElementTree as ElementTree
from datetime import datetime

SOURCE = re.compile(r"^[a-z][a-z0-9_]{1,31}$")
STALE_AFTER = re.compile(r"^[1-9][0-9]{0,2} (days|hours)$")
WEBSITE = re.compile(r"\bWebsite\s+(?:https?://)?([A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+)(/[^\s;,]*[^\s;,.])?", re.IGNORECASE)
# Platforms that host many unrelated pages: a listed page there must not
# block the platform, so such a Website remark with a path is skipped.
SHARED_HOSTS = {
    "facebook.com", "fb.com", "instagram.com", "t.me", "telegram.me", "twitter.com", "x.com", "linkedin.com",
    "youtube.com", "youtu.be", "vk.com", "ok.ru", "tiktok.com", "github.com", "medium.com", "wordpress.com",
    "blogspot.com", "sites.google.com", "google.com", "linktr.ee", "wa.me", "whatsapp.com", "pinterest.com",
}
OFFICIAL_STALE_AFTER = "8 days"
OFAC_NULL = "-0-"


def read_text(path):
    """OFAC publishes ASCII files that occasionally carry Windows-1252 letters."""
    data = open(path, "rb").read()
    try:
        return data.decode("utf-8-sig")
    except UnicodeDecodeError:
        return data.decode("cp1252")


def ofac_rows(path, columns):
    rows = []
    for row in csv.reader(io.StringIO(read_text(path), newline="")):
        if not row or row == [""]:
            continue
        if len(row) != columns:
            raise ValueError(f"{path}: expected {columns} columns per OFAC row, found {len(row)}")
        rows.append([OFAC_NULL if value.strip() == OFAC_NULL else value.strip() for value in row])
    return rows


def entry(kind, value, reference, whole_word=False):
    """whole_word marks an entity name: any single listed word of it is a hit."""
    value = {"kind": kind, "value": value, "reference": reference}
    if whole_word:
        value["whole_word"] = True
    return value


def websites(remarks):
    for host, path in WEBSITE.findall(remarks):
        bare = host.lower().removeprefix("www.")
        if path and path != "/" and bare in SHARED_HOSTS:
            continue
        yield host


def ofac_entries(sdn_path, alt_path):
    """SDN.CSV: ent_num, SDN_Name, SDN_Type, ..., Remarks (12 columns, no header).
    ALT.CSV: ent_num, alt_num, alt_type, alt_name, alt_remarks. Vessels and
    aircraft are not businesses that can sign up, so they and their aliases
    are skipped. Email addresses are not used: they are often free mail.
    Entities (SDN_Type null) are flagged whole_word; individuals are not."""
    entries, listed, entities = [], set(), set()
    for ent_num, name, sdn_type, *_, remarks in ofac_rows(sdn_path, 12):
        if sdn_type.lower() in ("vessel", "aircraft"):
            continue
        reference = f"SDN-{ent_num}"
        listed.add(ent_num)
        if sdn_type == OFAC_NULL:
            entities.add(ent_num)
        if name != OFAC_NULL:
            entries.append(entry("name", name, reference, ent_num in entities))
        if remarks != OFAC_NULL:
            entries.extend(entry("domain", host, reference) for host in websites(remarks))
    if alt_path:
        for ent_num, _alt_num, _alt_type, alt_name, _alt_remarks in ofac_rows(alt_path, 5):
            if ent_num in listed and alt_name != OFAC_NULL:
                entries.append(entry("name", alt_name, f"SDN-{ent_num}", ent_num in entities))
    return entries


def text_of(element, tag):
    child = element.find(tag)
    return (child.text or "").strip() if child is not None else ""


def un_entries(path):
    """UN Security Council consolidated list XML. Individuals: name parts
    joined, original-script name and Good-quality aliases; entities: name
    and every alias. Returns (dateGenerated, entries)."""
    data = open(path, "rb").read()
    if b"<!DOCTYPE" in data or b"<!ENTITY" in data:
        raise ValueError(f"{path}: document type and entity declarations are refused")
    root = ElementTree.fromstring(data)
    if root.tag != "CONSOLIDATED_LIST":
        raise ValueError(f"{path}: not a UN consolidated list")
    entries = []
    for record in root.iter("INDIVIDUAL"):
        parts = (text_of(record, tag) for tag in ("FIRST_NAME", "SECOND_NAME", "THIRD_NAME", "FOURTH_NAME"))
        entries.extend(un_record(record, "INDIVIDUAL_ALIAS", True, False, [" ".join(p for p in parts if p), text_of(record, "NAME_ORIGINAL_SCRIPT")]))
    for record in root.iter("ENTITY"):
        entries.extend(un_record(record, "ENTITY_ALIAS", False, True, [text_of(record, "FIRST_NAME"), text_of(record, "NAME_ORIGINAL_SCRIPT")]))
    return root.get("dateGenerated"), entries


def un_record(record, alias_tag, good_only, whole_word, names):
    reference = "UN-" + (text_of(record, "REFERENCE_NUMBER") or text_of(record, "DATAID"))
    for alias in record.iter(alias_tag):
        if not good_only or text_of(alias, "QUALITY").lower() == "good":
            names.append(text_of(alias, "ALIAS_NAME"))
    return [entry("name", name, reference, whole_word) for name in dict.fromkeys(names) if name]


def operator_entries(path):
    """An operator-curated CSV with the header kind,value,reference."""
    reader = csv.DictReader(io.StringIO(read_text(path), newline=""))
    if reader.fieldnames != ["kind", "value", "reference"]:
        raise ValueError(f"{path}: header must be kind,value,reference")
    entries = []
    for row in reader:
        kind, value, reference = (row["kind"] or "").strip(), (row["value"] or "").strip(), (row["reference"] or "").strip()
        if kind not in ("name", "domain") or not value or not 1 <= len(reference) <= 128:
            raise ValueError(f"{path}: line {reader.line_num} needs kind name or domain, a value and a reference")
        entries.append(entry(kind, value, reference))
    return entries


def sql_literal(value):
    return "'" + value.replace("'", "''") + "'"


def render_sql(source, description, published, stale_after, entries):
    if not SOURCE.match(source):
        raise ValueError("source must be a lowercase identifier such as ofac_sdn")
    if published is not None:
        # fromisoformat accepts a trailing Z only from Python 3.11.
        datetime.fromisoformat(published[:-1] + "+00:00" if published.endswith("Z") else published)
    if stale_after is not None and not STALE_AFTER.match(stale_after):
        raise ValueError("stale-after must look like '8 days'")
    if not 1 <= len(description) <= 200 or not description.isprintable():
        raise ValueError("description must be 1..200 printable characters")
    payload = json.dumps(entries, ensure_ascii=False, separators=(",", ":"))
    tag = f"ocrypt_{secrets.token_hex(8)}"
    while f"${tag}$" in payload:
        tag = f"ocrypt_{secrets.token_hex(8)}"
    return (
        "\\set ON_ERROR_STOP on\n"
        "BEGIN;\n"
        f"SELECT replace_signup_screening_source({sql_literal(source)},{sql_literal(description)},"
        f"{sql_literal(published) if published else 'NULL'}::timestamptz,"
        f"{sql_literal(stale_after) if stale_after else 'NULL'}::interval,"
        f"${tag}${payload}${tag}$::jsonb) AS loaded_entries;\n"
        "COMMIT;\n"
    )


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    commands = parser.add_subparsers(dest="command", required=True)
    ofac = commands.add_parser("ofac", help="OFAC SDN.CSV and ALT.CSV (legacy flat files)")
    ofac.add_argument("--sdn", required=True)
    ofac.add_argument("--alt")
    ofac.add_argument("--published", help="publication time, ISO 8601")
    un = commands.add_parser("un", help="UN Security Council consolidated list XML")
    un.add_argument("--xml", required=True)
    operator = commands.add_parser("operator", help="operator CSV: kind,value,reference")
    operator.add_argument("--csv", required=True)
    args = parser.parse_args(argv)
    try:
        if args.command == "ofac":
            sql = render_sql("ofac_sdn", "OFAC Specially Designated Nationals (SDN.CSV, ALT.CSV)", args.published, OFFICIAL_STALE_AFTER, ofac_entries(args.sdn, args.alt))
        elif args.command == "un":
            published, entries = un_entries(args.xml)
            sql = render_sql("un_consolidated", "UN Security Council Consolidated List", published, OFFICIAL_STALE_AFTER, entries)
        else:
            sql = render_sql("operator", "Operator-curated screening list", None, None, operator_entries(args.csv))
    except (OSError, ValueError, ElementTree.ParseError) as error:
        print(f"signup_screening_import: {error}", file=sys.stderr)
        return 2
    sys.stdout.write(sql)
    return 0


if __name__ == "__main__":
    sys.exit(main())
