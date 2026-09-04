import { AppShell } from "../components/AppShell";
import { DashboardStateProvider } from "../state/DashboardStateProvider";
import { RouterProvider } from "./router";

export function App() {
  return (
    <DashboardStateProvider>
      <RouterProvider>
        <AppShell />
      </RouterProvider>
    </DashboardStateProvider>
  );
}
