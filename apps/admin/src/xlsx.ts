// A minimal Office Open XML workbook writer: one sheet, inline strings and
// plain numbers, packed in an uncompressed ZIP. No dependency, and text is
// never written as a formula, so a cell cannot execute anything.

export type XLSXCell = string | { number: string };

const encoder = new TextEncoder();

let crcTable: Uint32Array | undefined;
function crc32(data: Uint8Array): number {
  if (!crcTable) {
    crcTable = new Uint32Array(256);
    for (let index = 0; index < 256; index += 1) {
      let value = index;
      for (let bit = 0; bit < 8; bit += 1) value = value & 1 ? 0xedb88320 ^ (value >>> 1) : value >>> 1;
      crcTable[index] = value >>> 0;
    }
  }
  let crc = 0xffffffff;
  for (const byte of data) crc = crcTable[(crc ^ byte) & 0xff]! ^ (crc >>> 8);
  return (crc ^ 0xffffffff) >>> 0;
}

// Stored entries only; the DOS date is fixed so the same rows give the same bytes.
function zip(entries: Array<[string, string]>): Uint8Array<ArrayBuffer> {
  const locals: Uint8Array[] = [];
  const centrals: Uint8Array[] = [];
  let offset = 0;
  for (const [name, text] of entries) {
    const nameBytes = encoder.encode(name);
    const data = encoder.encode(text);
    const crc = crc32(data);
    const local = new Uint8Array(30 + nameBytes.length + data.length);
    const localView = new DataView(local.buffer);
    localView.setUint32(0, 0x04034b50, true);
    localView.setUint16(4, 20, true);
    localView.setUint16(6, 0x0800, true);
    localView.setUint16(8, 0, true);
    localView.setUint16(10, 0, true);
    localView.setUint16(12, 0x21, true);
    localView.setUint32(14, crc, true);
    localView.setUint32(18, data.length, true);
    localView.setUint32(22, data.length, true);
    localView.setUint16(26, nameBytes.length, true);
    local.set(nameBytes, 30);
    local.set(data, 30 + nameBytes.length);
    const central = new Uint8Array(46 + nameBytes.length);
    const centralView = new DataView(central.buffer);
    centralView.setUint32(0, 0x02014b50, true);
    centralView.setUint16(4, 20, true);
    centralView.setUint16(6, 20, true);
    centralView.setUint16(8, 0x0800, true);
    centralView.setUint16(10, 0, true);
    centralView.setUint16(12, 0, true);
    centralView.setUint16(14, 0x21, true);
    centralView.setUint32(16, crc, true);
    centralView.setUint32(20, data.length, true);
    centralView.setUint32(24, data.length, true);
    centralView.setUint16(28, nameBytes.length, true);
    centralView.setUint32(42, offset, true);
    central.set(nameBytes, 46);
    locals.push(local);
    centrals.push(central);
    offset += local.length;
  }
  const centralSize = centrals.reduce((sum, part) => sum + part.length, 0);
  const end = new Uint8Array(22);
  const endView = new DataView(end.buffer);
  endView.setUint32(0, 0x06054b50, true);
  endView.setUint16(8, entries.length, true);
  endView.setUint16(10, entries.length, true);
  endView.setUint32(12, centralSize, true);
  endView.setUint32(16, offset, true);
  const out = new Uint8Array(offset + centralSize + 22);
  let position = 0;
  for (const part of [...locals, ...centrals, end]) {
    out.set(part, position);
    position += part.length;
  }
  return out;
}

export function columnName(index: number): string {
  let name = "";
  for (let value = index + 1; value > 0; value = Math.floor((value - 1) / 26)) name = String.fromCharCode(65 + ((value - 1) % 26)) + name;
  return name;
}

// XML 1.0 forbids most control characters; tab, line feed and carriage return
// stay (a carriage return as a character reference, which parsers keep). An
// existing _xHHHH_ sequence is escaped so Excel does not decode it.
function xmlText(value: string): string {
  return value.replace(/_x([0-9A-Fa-f]{4})_/g, "_x005F_x$1_").replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f\ufffe\uffff]/g, "").replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;").replaceAll('"', "&quot;").replaceAll("\r", "&#13;");
}

// A spreadsheet stores numbers as doubles: beyond 15 significant digits the
// value would change, so such an amount stays exact as text.
function exactNumber(value: string): boolean {
  const match = /^-?([0-9]+)(?:\.([0-9]+))?$/.exec(value);
  if (!match) return false;
  const significant = (match[1]!.replace(/^0+/, "") + (match[2] ?? "").replace(/0+$/, "")).replace(/^0+/, "");
  return significant.length <= 15;
}

function decimalsOf(value: string): number {
  return value.split(".")[1]?.length ?? 0;
}

function cell(reference: string, value: XLSXCell, styleFor: (decimals: number) => number): string {
  if (typeof value !== "string" && exactNumber(value.number)) return `<c r="${reference}" s="${styleFor(decimalsOf(value.number))}"><v>${value.number}</v></c>`;
  const text = typeof value === "string" ? value : value.number;
  const preserve = /^\s|\s$|[\t\n\r]/.test(text) ? ' xml:space="preserve"' : "";
  return `<c r="${reference}" t="inlineStr"><is><t${preserve}>${xmlText(text)}</t></is></c>`;
}

// Sheet names: no []:*?/\\, no leading or trailing apostrophe, at most 31
// characters, and never Excel's reserved "History".
function sheetTitle(value: string): string {
  const name = value.replace(/[[\]:*?/\\]/g, " ").replace(/\s+/g, " ").replace(/^'+|'+$/g, "").trim().slice(0, 31).trim() || "Sheet1";
  return name.toLowerCase() === "history" ? "History 1" : name;
}

// Number cells keep their decimals visible: one cell style per count of
// decimal places, built-in "0" for integers and custom "0.00…" formats.
function styles(decimals: number[]): string {
  const custom = decimals.filter((count) => count > 0);
  const numFmts = custom.length ? `<numFmts count="${custom.length}">${custom.map((count, index) => `<numFmt numFmtId="${164 + index}" formatCode="0.${"0".repeat(count)}"/>`).join("")}</numFmts>` : "";
  const xfs = decimals.map((count) => `<xf numFmtId="${count === 0 ? 1 : 164 + custom.indexOf(count)}" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/>`).join("");
  return `<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">${numFmts}<fonts count="1"><font><sz val="11"/><name val="Calibri"/></font></fonts><fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills><borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders><cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs><cellXfs count="${decimals.length + 1}"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/>${xfs}</cellXfs></styleSheet>`;
}

export function toXLSX(sheetName: string, rows: XLSXCell[][]): Uint8Array<ArrayBuffer> {
  const name = xmlText(sheetTitle(sheetName));
  const decimals: number[] = [];
  const styleFor = (count: number) => {
    let index = decimals.indexOf(count);
    if (index < 0) index = decimals.push(count) - 1;
    return index + 1;
  };
  const sheetRows = rows.map((row, rowIndex) => `<row r="${rowIndex + 1}">${row.map((value, columnIndex) => cell(`${columnName(columnIndex)}${rowIndex + 1}`, value, styleFor)).join("")}</row>`).join("");
  const header = '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n';
  const main = "http://schemas.openxmlformats.org/spreadsheetml/2006/main";
  const relationships = "http://schemas.openxmlformats.org/officeDocument/2006/relationships";
  return zip([
    ["[Content_Types].xml", `${header}<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/><Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/></Types>`],
    ["_rels/.rels", `${header}<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="${relationships}/officeDocument" Target="xl/workbook.xml"/></Relationships>`],
    ["xl/workbook.xml", `${header}<workbook xmlns="${main}" xmlns:r="${relationships}"><sheets><sheet name="${name}" sheetId="1" r:id="rId1"/></sheets></workbook>`],
    ["xl/_rels/workbook.xml.rels", `${header}<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="${relationships}/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Type="${relationships}/styles" Target="styles.xml"/></Relationships>`],
    ["xl/worksheets/sheet1.xml", `${header}<worksheet xmlns="${main}"><sheetData>${sheetRows}</sheetData></worksheet>`],
    ["xl/styles.xml", `${header}${styles(decimals)}`]
  ]);
}

export function downloadXLSX(name: string, bytes: Uint8Array<ArrayBuffer>) {
  const url = URL.createObjectURL(new Blob([bytes], { type: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" }));
  const link = document.createElement("a");
  link.href = url;
  link.download = name;
  link.click();
  window.setTimeout(() => URL.revokeObjectURL(url), 0);
}
