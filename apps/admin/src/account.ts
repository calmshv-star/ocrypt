// downloadJSON saves a value as a pretty-printed JSON file.
export function downloadJSON(name: string, value: unknown) {
  const url = URL.createObjectURL(new Blob([JSON.stringify(value, null, 2)], { type: "application/json;charset=utf-8" }));
  const link = document.createElement("a");
  link.href = url;
  link.download = name;
  link.click();
  window.setTimeout(() => URL.revokeObjectURL(url), 0);
}

// sameEmail compares addresses the way the server does.
export function sameEmail(left: string, right: string): boolean {
  return left.trim().toLowerCase() === right.trim().toLowerCase() && left.trim() !== "";
}

export type DeletionOutcome = "stepUp" | "ownerConflict" | "failed" | null;
