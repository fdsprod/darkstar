export interface DashboardBootstrap {
  apiVersion: "v1";
  authorization: `Bearer ${string}`;
}

declare global {
  interface Window {
    __DARKSTAR_BOOTSTRAP__?: Readonly<DashboardBootstrap>;
  }
}

function consumeBootstrap(): DashboardBootstrap | undefined {
  const value = window.__DARKSTAR_BOOTSTRAP__;
  delete window.__DARKSTAR_BOOTSTRAP__;
  if (value === undefined) return undefined;
  if (value.apiVersion !== "v1" || !value.authorization.startsWith("Bearer ")) {
    throw new Error("The dashboard bootstrap contract is invalid.");
  }
  return { apiVersion: value.apiVersion, authorization: value.authorization };
}

// Consume once during module evaluation. Never copy this value to URL or storage.
const bootstrap = consumeBootstrap();

export function getDashboardAuthorization() {
  return bootstrap?.authorization;
}
