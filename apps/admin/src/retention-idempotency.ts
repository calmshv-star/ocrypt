const storageKey = "merchant.admin.retention.pending.v1";
type Entry = { fingerprint: string; key: string };

function storage(): Storage | undefined {
  try { return typeof window === "undefined" ? undefined : window.sessionStorage; } catch { return undefined; }
}

function entries(store: Storage): Entry[] {
  const raw = store.getItem(storageKey);
  if (!raw || raw.length > 32768) return [];
  try {
    const value: unknown = JSON.parse(raw);
    return Array.isArray(value) ? value.filter((item): item is Entry => Boolean(item) && typeof item.fingerprint === "string" && typeof item.key === "string").slice(-16) : [];
  } catch { return []; }
}

export function retentionMutationKey(fingerprint: string): string {
  if (!fingerprint || fingerprint.length > 4096) throw new Error("invalid retention fingerprint");
  const store = storage();
  if (!store) return `retention-${crypto.randomUUID()}`;
  const current = entries(store);
  const found = current.find((item) => item.fingerprint === fingerprint);
  if (found) return found.key;
  const key = `retention-${crypto.randomUUID()}`;
  store.setItem(storageKey, JSON.stringify([...current, { fingerprint, key }].slice(-16)));
  return key;
}

export function completeRetentionMutation(fingerprint: string) {
  const store = storage();
  if (!store) return;
  const next = entries(store).filter((item) => item.fingerprint !== fingerprint);
  if (next.length) store.setItem(storageKey, JSON.stringify(next));
  else store.removeItem(storageKey);
}
