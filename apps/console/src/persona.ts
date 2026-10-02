import type { Profile } from "./session";

const TABS: Record<string, string[]> = {
  Stakeholder: ["Brief", "Approvals", "Activity", "Reports", "Profile"],
  "Sales Agent": ["My Day", "Stock", "Orders", "Activity", "Profile"],
  "Collection Agent": ["Receivables", "Receipts", "Customers", "Activity", "Profile"],
  Driver: ["Trip", "Activity", "Profile"],
  Accountant: ["Queue", "Capture", "Approvals", "Activity", "Profile"],
  "Credit Controller": ["Queue", "Approvals", "Activity", "Profile"],
  "Stock Counter": ["Stock", "Activity", "Profile"],
  "Production Supervisor": ["Stock", "Activity", "Profile"],
  Auditor: ["Activity", "Reports", "Profile"],
  "System Manager": ["Admin", "Activity", "Profile"],
};

const STAKEHOLDER_TITLES = new Set(["CFO", "Partner", "CTO"]);

export type PersonaHome = { title: string; tabs: string[] };

export function personaHome(profile: Pick<Profile, "roles" | "personas">): PersonaHome {
  const labels = [...profile.personas, ...profile.roles];
  for (const label of labels) {
    const tabs = TABS[label];
    if (tabs) {
      return { title: label, tabs };
    }
  }
  for (const label of labels) {
    if (STAKEHOLDER_TITLES.has(label)) {
      return { title: "Stakeholder", tabs: TABS.Stakeholder };
    }
  }
  const title = profile.personas[0] || profile.roles[0] || "Unknown";
  return { title, tabs: ["Profile"] };
}

export function canGateVendor(profile: Pick<Profile, "roles" | "personas">): boolean {
  return [...profile.roles, ...profile.personas].some((label) => label === "CFO" || label === "Partner");
}
