import { crc32 as nodeCrc32 } from "node:zlib";
import { describe, expect, it } from "vitest";
import { columnName, toXLSX } from "./xlsx";

// Reads the stored (uncompressed) entries of a ZIP through its central
// directory and checks every CRC with Node's own implementation.
function unzip(bytes: Uint8Array): Map<string, string> {
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  let end = bytes.length - 22;
  while (end >= 0 && view.getUint32(end, true) !== 0x06054b50) end -= 1;
  expect(end).toBeGreaterThanOrEqual(0);
  const count = view.getUint16(end + 10, true);
  let offset = view.getUint32(end + 16, true);
  const files = new Map<string, string>();
  for (let index = 0; index < count; index += 1) {
    expect(view.getUint32(offset, true)).toBe(0x02014b50);
    expect(view.getUint16(offset + 10, true)).toBe(0);
    const crc = view.getUint32(offset + 16, true);
    const size = view.getUint32(offset + 20, true);
    const nameLength = view.getUint16(offset + 28, true);
    const local = view.getUint32(offset + 42, true);
    const name = new TextDecoder().decode(bytes.subarray(offset + 46, offset + 46 + nameLength));
    const dataStart = local + 30 + view.getUint16(local + 26, true) + view.getUint16(local + 28, true);
    const data = bytes.subarray(dataStart, dataStart + size);
    expect(nodeCrc32(data) >>> 0).toBe(crc);
    files.set(name, new TextDecoder().decode(data));
    offset += 46 + nameLength + view.getUint16(offset + 30, true) + view.getUint16(offset + 32, true);
  }
  return files;
}

describe("xlsx", () => {
  it("names columns past Z", () => {
    expect([0, 25, 26, 51, 52, 701, 702].map(columnName)).toEqual(["A", "Z", "AA", "AZ", "BA", "ZZ", "AAA"]);
  });

  it("writes a workbook a spreadsheet program can open", () => {
    const files = unzip(toXLSX("Daily totals", [["Day", "Amount", "Note"], ["2026-10-02", { number: "57.00" }, "a < b & \"c\""], ["2026-10-03", { number: "123456789012345678901234573.59" }, "=1+1"]]));
    expect([...files.keys()].sort()).toEqual(["[Content_Types].xml", "_rels/.rels", "xl/_rels/workbook.xml.rels", "xl/workbook.xml", "xl/worksheets/sheet1.xml"]);
    const sheet = files.get("xl/worksheets/sheet1.xml")!;
    expect(sheet).toContain('<c r="B2"><v>57.00</v></c>');
    // Beyond 15 significant digits a number would be rounded: keep it exact as text.
    expect(sheet).toContain('<c r="B3" t="inlineStr"><is><t>123456789012345678901234573.59</t></is></c>');
    expect(sheet).toContain("a &lt; b &amp; &quot;c&quot;");
    // Text is never a formula, whatever it starts with.
    expect(sheet).toContain('<c r="C3" t="inlineStr"><is><t>=1+1</t></is></c>');
    expect(files.get("xl/workbook.xml")).toContain('<sheet name="Daily totals" sheetId="1" r:id="rId1"/>');
  });

  it("keeps sheet names and text within the format's rules", () => {
    const files = unzip(toXLSX("a/b:c*d?e[f]g\\h and a very long tail", [["bell\u0007tab\tok"]]));
    expect(files.get("xl/workbook.xml")).toContain('name="a b c d e f g h and a very long"');
    expect(files.get("xl/worksheets/sheet1.xml")).toContain("<t xml:space=\"preserve\">belltab\tok</t>");
  });
});
