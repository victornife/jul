import { Routes, Route, Navigate } from "react-router-dom";
import { Layout } from "@/app/Layout.tsx";
import { OverviewPanel } from "@/features/overview/OverviewPanel.tsx";
import { RoutesPanel } from "@/features/routes/RoutesPanel.tsx";
import { AppsPanel } from "@/features/apps/AppsPanel.tsx";
import { TLSPanel } from "@/features/tls/TLSPanel.tsx";
import { SecurityPanel } from "@/features/security/SecurityPanel.tsx";
import { TrafficControlsPanel } from "@/features/traffic-controls/TrafficControlsPanel.tsx";
import { PluginsPanel } from "@/features/plugins/PluginsPanel.tsx";
import { StreamsPanel } from "@/features/streams/StreamsPanel.tsx";
import { SearchPanel } from "@/features/search/SearchPanel.tsx";
import { OperationsPanel } from "@/features/operations/OperationsPanel.tsx";
import { AuditPanel } from "@/features/security/AuditPanel.tsx";
import { ConfigPanel } from "@/features/config/ConfigPanel.tsx";
import { HistoryPanel } from "@/features/history/HistoryPanel.tsx";
import { WizardPanel } from "@/features/wizard/WizardPanel.tsx";
import { TranscodeDesignerPanel } from "@/features/transcode/TranscodeDesignerPanel.tsx";

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
        <Route path="/plugins" element={<PluginsPanel />} />
        <Route path="/streams" element={<StreamsPanel />} />
        <Route path="/transcode" element={<TranscodeDesignerPanel />} />
        <Route path="/search" element={<SearchPanel />} />
        <Route path="/operations" element={<OperationsPanel tab="diagnostics" />} />
        <Route path="/operations/events" element={<OperationsPanel tab="events" />} />
        <Route path="/operations/logs" element={<OperationsPanel tab="logs" />} />
        <Route path="/operations/timeline" element={<OperationsPanel tab="timeline" />} />
        {/* C-4: Events and Timeline are now tabs of Operations; keep the old
            paths working by redirecting into the consolidated workspace. */}
        <Route path="/observability" element={<Navigate to="/operations/events" replace />} />
        <Route path="/timeline" element={<Navigate to="/operations/timeline" replace />} />
        <Route path="/audit" element={<AuditPanel />} />
        <Route path="/config" element={<ConfigPanel />} />
        <Route path="/ui" element={<Navigate to="/config" replace />} />
        <Route path="/history" element={<HistoryPanel />} />
        <Route path="/wizard" element={<WizardPanel />} />
        <Route
          path="*"
          element={<div className="p-8 text-center text-jul-muted">Page not found.</div>}
        />
      </Route>
    </Routes>
  );
}
