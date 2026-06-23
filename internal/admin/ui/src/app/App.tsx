import { Routes, Route, Navigate } from "react-router-dom";
import { Layout } from "@/app/Layout.tsx";
import { OverviewPanel } from "@/features/overview/OverviewPanel.tsx";
import { RoutesPanel } from "@/features/routes/RoutesPanel.tsx";
import { AppsPanel } from "@/features/apps/AppsPanel.tsx";
import { TLSPanel } from "@/features/tls/TLSPanel.tsx";
import { SecurityPanel } from "@/features/security/SecurityPanel.tsx";
import { TrafficControlsPanel } from "@/features/traffic-controls/TrafficControlsPanel.tsx";
import { ObservabilityPanel } from "@/features/observability/ObservabilityPanel.tsx";
import { ConfigPanel } from "@/features/config/ConfigPanel.tsx";
import { HistoryPanel } from "@/features/history/HistoryPanel.tsx";
import { WizardPanel } from "@/features/wizard/WizardPanel.tsx";

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
        <Route path="/observability" element={<ObservabilityPanel />} />
        <Route path="/config" element={<ConfigPanel />} />
        <Route path="/ui" element={<Navigate to="/config" replace />} />
        <Route path="/history" element={<HistoryPanel />} />
        <Route path="/wizard" element={<WizardPanel />} />
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
