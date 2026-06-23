import { Routes, Route } from "react-router-dom";
import { Layout } from "@/app/Layout.tsx";
import { OverviewPanel } from "@/features/overview/OverviewPanel.tsx";

export function App() {
  return (
    <Routes>
      <Route element={<Layout />}>
        <Route path="/" element={<OverviewPanel />} />
        <Route
          path="*"
          element={
            <div className="p-8 text-center text-jul-muted">
              Page not found — Console v2 scaffold.
            </div>
          }
        />
      </Route>
    </Routes>
  );
}
