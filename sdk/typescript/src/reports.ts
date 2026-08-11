import type { ReconciliationReport } from "./models.js";

function decodeBase64Url(value: string): Uint8Array {
  const padded = value.replaceAll("-", "+").replaceAll("_", "/") + "=".repeat((4 - value.length % 4) % 4);
  return Uint8Array.from(atob(padded), (character) => character.charCodeAt(0));
}
function hex(bytes: Uint8Array): string { return [...bytes].map((value) => value.toString(16).padStart(2, "0")).join(""); }
function ownedBuffer(bytes: Uint8Array): ArrayBuffer { const copy = new Uint8Array(bytes.byteLength); copy.set(bytes); return copy.buffer; }

/** Verify downloaded bytes and the detached report signature against the key ID frozen on the report. */
export async function verifyReconciliationReport(bytes: Uint8Array, report: ReconciliationReport, publicKeys: Readonly<Record<string, Uint8Array>>): Promise<void> {
  if (report.status !== "ready" || !report.object_sha256 || !report.signature || !report.signing_key_id) throw new Error("report is not ready or lacks integrity metadata");
  const publicKey = publicKeys[report.signing_key_id];
  if (!publicKey) throw new Error(`unknown reconciliation signing key: ${report.signing_key_id}`);
  const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", ownedBuffer(bytes)));
  if (hex(digest) !== report.object_sha256) throw new Error("reconciliation report digest mismatch");
  if (report.object_size_bytes !== undefined && BigInt(report.object_size_bytes) !== BigInt(bytes.byteLength)) throw new Error("reconciliation report size mismatch");
  const prefix = new TextEncoder().encode(`merchant-reconciliation-jsonl-v1\0${report.id}\0${report.snapshot_ledger_sequence}\0`);
  const message = new Uint8Array(prefix.length + digest.length); message.set(prefix); message.set(digest, prefix.length);
  const key = await crypto.subtle.importKey("raw", ownedBuffer(publicKey), { name: "Ed25519" }, false, ["verify"]);
  if (!await crypto.subtle.verify("Ed25519", key, ownedBuffer(decodeBase64Url(report.signature)), ownedBuffer(message))) throw new Error("reconciliation report signature mismatch");
}
