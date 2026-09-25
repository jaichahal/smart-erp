import { useState } from "react";
import { CollectionReceipt, SalesOrder } from "./Documents";
import { personaHome } from "./persona";
import { PurchaseList } from "./PurchaseList";
import type { Profile } from "./session";
import { VendorDashboard } from "./VendorDashboard";

type Screen = "home" | "vendor" | "sales" | "collection" | "purchases";

export function Home({ profile }: { profile: Profile }) {
  const home = personaHome(profile);
  const [screen, setScreen] = useState<Screen>("home");
  return (
    <div>
      <p data-testid="signed-in-name">{profile.name}</p>
      <p data-testid="persona-home">{home.title}</p>
      <nav>
        {home.tabs.map((tab) => (
          <button key={tab} type="button" onClick={() => setScreen("home")}>
            {tab}
          </button>
        ))}
        <button type="button" onClick={() => setScreen("vendor")}>
          Vendor dashboard
        </button>
        <button type="button" onClick={() => setScreen("sales")}>
          Sales order
        </button>
        <button type="button" onClick={() => setScreen("collection")}>
          Collection receipt
        </button>
        <button type="button" onClick={() => setScreen("purchases")}>
          Purchase list
        </button>
      </nav>
      {screen === "home" ? <p>{home.title} home</p> : null}
      {screen === "vendor" ? <VendorDashboard profile={profile} /> : null}
      {screen === "sales" ? <SalesOrder profile={profile} /> : null}
      {screen === "collection" ? <CollectionReceipt profile={profile} /> : null}
      {screen === "purchases" ? <PurchaseList profile={profile} /> : null}
    </div>
  );
}
