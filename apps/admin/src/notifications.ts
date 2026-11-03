import type { NotificationEvent } from "./api/types";

export type NotificationSection = "events" | "account" | "telegram";
export type SaveAction = () => Promise<unknown>;

// toggled returns a copy of the set with the event added or removed.
export function toggled(set: ReadonlySet<NotificationEvent>, event: NotificationEvent): Set<NotificationEvent> {
  const next = new Set(set);
  if (next.has(event)) next.delete(event); else next.add(event);
  return next;
}
