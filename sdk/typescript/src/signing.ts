const encoder = new TextEncoder();

function bytesToBase64(bytes: Uint8Array): string {
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}
function toHex(bytes: Uint8Array): string { return [...bytes].map((byte) => byte.toString(16).padStart(2, "0")).join(""); }
function base64Url(bytes: Uint8Array): string { return bytesToBase64(bytes).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/, ""); }
function ownedBuffer(bytes: Uint8Array): ArrayBuffer { const copy = new Uint8Array(bytes.byteLength); copy.set(bytes); return copy.buffer; }
export function canonicalQuery(query: Record<string, string | number | readonly (string | number)[] | undefined>): string {
  const encode = (value: string) => encodeURIComponent(value).replace(/%20/g, "+").replace(/[!'()*]/g, (char) => `%${char.charCodeAt(0).toString(16).toUpperCase()}`);
  const pairs: string[] = [];
  for (const key of Object.keys(query).sort()) {
    const value = query[key];
    if (value === undefined) continue;
    for (const item of Array.isArray(value) ? value : [value]) pairs.push(`${encode(key)}=${encode(String(item))}`);
  }
  return pairs.join("&");
}
export interface SignInput { keyId: string; secret: string; method: string; pathAndQuery: string; body: Uint8Array; timestamp: number; nonce: string }
export interface SignedHeaders { "Merchant-Key-Id": string; "Merchant-Timestamp": string; "Merchant-Nonce": string; "Content-Digest": string; "Merchant-Signature": string }
export async function signRequest(input: SignInput): Promise<SignedHeaders> {
  const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", ownedBuffer(input.body)));
  const canonical = [input.method.toUpperCase(), input.pathAndQuery, String(input.timestamp), input.nonce, toHex(digest)].join("\n");
  const key = await crypto.subtle.importKey("raw", encoder.encode(input.secret), { name: "HMAC", hash: "SHA-256" }, false, ["sign"]);
  const signature = new Uint8Array(await crypto.subtle.sign("HMAC", key, encoder.encode(canonical)));
  return {
    "Merchant-Key-Id": input.keyId,
    "Merchant-Timestamp": String(input.timestamp),
    "Merchant-Nonce": input.nonce,
    "Content-Digest": `sha-256=:${bytesToBase64(digest)}:`,
    "Merchant-Signature": base64Url(signature)
  };
}
export function randomNonce(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  return toHex(bytes);
}
export async function sha256Digest(value: Uint8Array): Promise<string> {
  return `sha-256=:${bytesToBase64(new Uint8Array(await crypto.subtle.digest("SHA-256", ownedBuffer(value))))}:`;
}
export async function hmacBase64Url(secret: string, value: Uint8Array): Promise<string> {
  const key = await crypto.subtle.importKey("raw", encoder.encode(secret), { name: "HMAC", hash: "SHA-256" }, false, ["sign"]);
  return base64Url(new Uint8Array(await crypto.subtle.sign("HMAC", key, ownedBuffer(value))));
}
export function timingSafeEqual(left: string, right: string): boolean {
  const a = encoder.encode(left); const b = encoder.encode(right); let mismatch = a.length ^ b.length;
  const length = Math.max(a.length, b.length);
  for (let index = 0; index < length; index += 1) mismatch |= (a[index % a.length] ?? 0) ^ (b[index % b.length] ?? 0);
  return mismatch === 0;
}
