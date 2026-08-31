import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import "./styles.css";

function Dashboard() {
  return (
    <main className="shell" aria-labelledby="app-title">
      <p className="eyebrow">Deterministic orchestration</p>
      <h1 id="app-title">DARKSTAR</h1>
      <p className="status">Dashboard module ready.</p>
    </main>
  );
}

const root = document.getElementById("root");

if (!root) {
  throw new Error("Dashboard root element is missing");
}

createRoot(root).render(
  <StrictMode>
    <Dashboard />
  </StrictMode>,
);
