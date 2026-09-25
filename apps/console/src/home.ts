export type HomeKind = "cfo" | "finance-manager" | "accountant" | "sales" | "default";

export type DragSheet = "approve" | "reject" | "flag";

export function homeKind(roles: string[], personas: string[]): HomeKind {
  const labels = [...roles, ...personas].map((value) => value.trim().toLowerCase()).filter(Boolean);
  const has = (needle: string) => labels.some((value) => value.includes(needle));
  if (has("cfo") || has("partner")) return "cfo";
  if (has("finance manager") || has("finance_manager")) return "finance-manager";
  if (has("accountant")) return "accountant";
  if (has("sales") || has("collection")) return "sales";
  return "default";
}

// A drag opens a sheet. It never decides the request.
export function sheetForDrag(dx: number, dy: number): DragSheet | null {
  if (dy > 72 && Math.abs(dy) > Math.abs(dx)) return "flag";
  if (dx > 72) return "approve";
  if (dx < -72) return "reject";
  return null;
}
