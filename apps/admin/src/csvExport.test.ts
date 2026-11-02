import { describe, expect, it, vi } from "vitest";
import { en, type MessageKey } from "@merchant/i18n";
import type { AdminClient } from "./api/client";
import type { IntentRow } from "./api/types";
import { collectIntents, csvCell, exactMinor, intentsCSV, toCSV } from "./csvExport";

const t = (key: MessageKey) => en[key];
const scope = { tenantId: "t", merchantId: "m" };
const intent = (id: string, overrides: Partial<IntentRow> = {}): IntentRow => ({ id, merchant_id: "m", merchant_order_id: `ORDER-${id}`, amount_minor: "128000", currency: "USD", currency_scale: 2, status: "settled", created_at: "2026-11-03T10:00:00Z", expires_at: "2026-11-03T10:30:00Z", ...overrides });

describe("payments CSV export", () => {
  it("quotes RFC 4180 cells and neutralizes spreadsheet formulas", () => {
    expect(csvCell("plain")).toBe("plain");
    expect(csvCell('say "hi", ok')).toBe('"say ""hi"", ok"');
    expect(csvCell("line\nbreak")).toBe('"line\nbreak"');
    for (const formula of ["=HYPERLINK(\"x\")", "+1", "-1+2", "@SUM(A1)", "\tcmd"]) expect(csvCell(formula).replace(/^"/, "").startsWith("'")).toBe(true);
    expect(toCSV([["a", "b"], ["c", "d"]])).toBe("a,b\r\nc,d\r\n");
  });

  it("keeps money exact as strings", () => {
    expect(exactMinor("128000", 2)).toBe("1280.00");
    expect(exactMinor("5", 2)).toBe("0.05");
    expect(exactMinor("123456789012345678901234567890", 18)).toBe("123456789012.345678901234567890");
    const csv = intentsCSV([intent("1", { received_amount_atomic: "1280000000", received_asset_symbol: "USDT", received_asset_decimals: 6, merchant_order_id: "=cmd()" })], t);
    const [header, row] = csv.trim().split("\r\n");
    expect(header).toBe("Reference,Identifier,Amount,Currency,Status,Status code,Received,Asset,Created,Expires".split(",").map((label, index) => [en["admin.reference"], en["admin.identifier"], en["common.amount"], en["common.currency"], en["common.status"], en["reports.statusCode"], en["reports.received"], en["common.asset"], en["admin.createdAt"], en["admin.expiresAt"]][index] ?? label).map(csvCell).join(","));
    expect(row).toBe("'=cmd(),1,1280.00,USD,Paid,settled,1280.000000,USDT,2026-11-03T10:00:00Z,2026-11-03T10:30:00Z");
  });

  it("pages until the server stops and caps very large exports", async () => {
    const pages = [{ items: [intent("1"), intent("2")], next_cursor: "c1" }, { items: [intent("3")], next_cursor: "" }];
    const intents = vi.fn().mockImplementation(async (_scope, cursor: string) => cursor === "" ? pages[0] : pages[1]);
    const client = { intents } as unknown as AdminClient;
    expect(await collectIntents(client, scope)).toEqual({ rows: [intent("1"), intent("2"), intent("3")], truncated: false });
    expect(intents).toHaveBeenNthCalledWith(1, scope, "", 200);
    expect(intents).toHaveBeenNthCalledWith(2, scope, "c1", 200);
    const capped = await collectIntents(client, scope, 2);
    expect(capped.rows.map((row) => row.id)).toEqual(["1", "2"]);
    expect(capped.truncated).toBe(true);
  });
});
