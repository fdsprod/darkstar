import { AppShell } from "../components/AppShell";
import { RouterProvider } from "./router";

export function App() {
  return (
    <RouterProvider>
      <AppShell />
    </RouterProvider>
  );
}
