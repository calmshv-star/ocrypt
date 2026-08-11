export const financialMutationStorageKey = "merchant.admin.financial.pending.v1";
export const financialMutationMaximum = 24;

type PendingMutation = { f: string; k: string; t: number };

const keyPattern = /^financial-[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;

function valid(value: unknown): value is PendingMutation {
  if (!value || typeof value !== "object") return false;
  const entry = value as Partial<PendingMutation>;
  return typeof entry.f === "string" && entry.f.length > 0 && entry.f.length <= 4096
    && typeof entry.k === "string" && keyPattern.test(entry.k)
    && typeof entry.t === "number" && Number.isSafeInteger(entry.t) && entry.t >= 0;
}

function read(storage: Storage): PendingMutation[] {
  const raw = storage.getItem(financialMutationStorageKey);
  if (!raw) return [];
  if (raw.length > 131072) {
    storage.removeItem(financialMutationStorageKey);
    return [];
  }
  try {
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed) || parsed.length > financialMutationMaximum || !parsed.every(valid)) throw new Error("invalid pending mutations");
    return parsed;
  } catch {
    storage.removeItem(financialMutationStorageKey);
    return [];
  }
}

function write(storage: Storage, entries: PendingMutation[]) {
  if (entries.length === 0) storage.removeItem(financialMutationStorageKey);
  else storage.setItem(financialMutationStorageKey, JSON.stringify(entries.slice(-financialMutationMaximum)));
}

function browserStorage(): Storage | undefined {
  try {
    return typeof window === "undefined" ? undefined : window.sessionStorage;
  } catch {
    return undefined;
  }
}

export function pendingFinancialMutationKey(fingerprint: string, create: () => string = () => `financial-${crypto.randomUUID()}`, storage = browserStorage()): string {
  if (fingerprint.length === 0 || fingerprint.length > 4096) throw new Error("Invalid financial mutation fingerprint");
  if (!storage) return create();
  const entries = read(storage);
  const existing = entries.find((entry) => entry.f === fingerprint);
  if (existing) return existing.k;
  const key = create();
  if (!keyPattern.test(key)) throw new Error("Invalid financial idempotency key");
  entries.push({ f: fingerprint, k: key, t: Date.now() });
  write(storage, entries);
  return key;
}

export function completeFinancialMutation(fingerprint: string, storage = browserStorage()) {
  if (!storage) return;
  write(storage, read(storage).filter((entry) => entry.f !== fingerprint));
}
