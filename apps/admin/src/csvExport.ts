import type { MessageKey } from "@merchant/i18n";
import type { AdminClient } from "./api/client";
import type { AdminScope, IntentRow } from "./api/types";
import { describeIntentStatus } from "./paymentStatus";
import type { XLSXCell } from "./xlsx";

// Spreadsheet programs execute cells that start with these characters.
const FORMULA_PREFIX = /^[=+\-@\t\r]/;

export function csvCell(value: string): string {
  const safe = FORMULA_PREFIX.test(value) ? `'${value}` : value;
  return /[",\r\n]/.test(safe) ? `"${safe.replaceAll('"', '""')}"` : safe;
}

export function toCSV(rows: string[][]): string {
  return rows.map((row) => row.map(csvCell).join(",")).join("\r\n") + "\r\n";
}

export function exactMinor(value: string, scale: number): string {
  const digits = value.replace(/^0+(?=\d)/, "");
  if (scale <= 0) return digits;
  const padded = digits.padStart(scale + 1, "0");
  return `${padded.slice(0, -scale)}.${padded.slice(-scale)}`;
}

export const EXPORT_PAGE_SIZE = 200;
export const EXPORT_MAX_ROWS = 10_000;

// Pages through the payments the session can see, newest first, and stops at
// EXPORT_MAX_ROWS so a browser tab never holds an unbounded export.
export async function collectIntents(client: AdminClient, scope: AdminScope, maxRows = EXPORT_MAX_ROWS): Promise<{ rows: IntentRow[]; truncated: boolean }> {
  const rows: IntentRow[] = [];
  let cursor = "";
  for (;;) {
    const page = await client.intents(scope, cursor, EXPORT_PAGE_SIZE);
    rows.push(...(page.items ?? []));
    if (rows.length >= maxRows) return { rows: rows.slice(0, maxRows), truncated: rows.length > maxRows || Boolean(page.next_cursor) };
    if (!page.next_cursor) return { rows, truncated: false };
    cursor = page.next_cursor;
  }
}

// One table for both exports; amounts are exact decimal strings marked as
// numbers so a spreadsheet can sum them (XLSX keeps long ones as text).
export function intentsTable(rows: IntentRow[], t: (key: MessageKey) => string): XLSXCell[][] {
  const header = ["admin.reference", "admin.identifier", "common.amount", "common.currency", "common.status", "reports.statusCode", "reports.received", "common.asset", "admin.createdAt", "admin.expiresAt"].map((key) => t(key as MessageKey));
  return [header, ...rows.map((row): XLSXCell[] => [
    row.merchant_order_id,
    row.id,
    { number: exactMinor(row.amount_minor, row.currency_scale) },
    row.currency,
    t(describeIntentStatus(row).label),
    row.status,
    row.received_amount_atomic !== undefined && row.received_asset_decimals !== undefined ? { number: exactMinor(row.received_amount_atomic, row.received_asset_decimals) } : "",
    row.received_asset_symbol ?? "",
    row.created_at,
    row.expires_at
  ])];
}

export function tableCSV(rows: XLSXCell[][]): string {
  return toCSV(rows.map((row) => row.map((value) => typeof value === "string" ? value : value.number)));
}

export function intentsCSV(rows: IntentRow[], t: (key: MessageKey) => string): string {
  return tableCSV(intentsTable(rows, t));
}

export function downloadCSV(name: string, csv: string) {
  // The byte-order mark makes spreadsheet programs read the file as UTF-8.
  const url = URL.createObjectURL(new Blob(["﻿", csv], { type: "text/csv;charset=utf-8" }));
  const link = document.createElement("a");
  link.href = url;
  link.download = name;
  link.click();
  window.setTimeout(() => URL.revokeObjectURL(url), 0);
}
