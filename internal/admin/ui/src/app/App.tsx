import { Routes, Route } from "react-router-dom";
import { Layout } from "@/app/Layout.tsx";
import { OverviewPanel } from "@/features/overview/OverviewPanel.tsx";
import { RoutesPanel } from "@/features/routes/RoutesPanel.tsx";
import { AppsPanel } from "@/features/apps/AppsPanel.tsx";
import { TLSPanel } from "@/features/tls/TLSPanel.tsx";
import { SecurityPanel } from "@/features/security/SecurityPanel.tsx";
import { TrafficControlsPanel } from "@/features/traffic-controls/TrafficControlsPanel.tsx";

export function App() {
  return (
    <Routes>
      <Route element={<Layout />}>
        <Route path="/" element={<OverviewPanel />} />
        <Route path="/routes" element={<RoutesPanel />} />
        <Route path="/apps" element={<AppsPanel />} />
        <Route path="/tls" element={<TLSPanel />} />
        <Route path="/security" element={<SecurityPanel />} />
        <Route path="/traffic" element={<TrafficControlsPanel />} />
        <Route
          path="*"
          element={
            <div className="p-8 text-center text-jul-muted">
              Page not found.
            </div>
          }
        />
      </Route>
    </Routes>
  );
}
