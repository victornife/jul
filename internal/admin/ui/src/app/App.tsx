/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { Component, lazy, Suspense, type ReactNode } from "react";
import { Routes, Route, Navigate } from "react-router-dom";
import { Layout } from "@/app/Layout.tsx";
import { OverviewPanel } from "@/features/overview/OverviewPanel.tsx";
import { RoutesPanel } from "@/features/routes/RoutesPanel.tsx";
import { AppsPanel } from "@/features/apps/AppsPanel.tsx";
import { TLSPanel } from "@/features/tls/TLSPanel.tsx";
import { SecurityPanel } from "@/features/security/SecurityPanel.tsx";
import { PluginsPanel } from "@/features/plugins/PluginsPanel.tsx";
import { StreamsPanel } from "@/features/streams/StreamsPanel.tsx";
import { SearchPanel } from "@/features/search/SearchPanel.tsx";
import { OperationsPanel } from "@/features/operations/OperationsPanel.tsx";
import { AuditPanel } from "@/features/security/AuditPanel.tsx";
import { ConfigPanel } from "@/features/config/ConfigPanel.tsx";
import { Button, Loading } from "@/components/ui.tsx";

class RouteErrorBoundary extends Component<{ readonly children: ReactNode }, { failed: boolean }> {
  state = { failed: false };

  static getDerivedStateFromError() {
    return { failed: true };
  }

  componentDidCatch(error: Error) {
    console.error("Console page failed to load:", error);
  }

  render() {
    if (!this.state.failed) return this.props.children;
    return (
      <div role="alert" className="space-y-3 rounded-md border border-jul-danger/40 p-4">
        <p>This page could not be loaded. Reload the Console to try again.</p>
        <Button variant="secondary" onClick={() => { window.location.reload(); }}>
          Reload Console
        </Button>
      </div>
    );
  }
}

const HistoryPanel = lazy(() =>
  import("@/features/history/HistoryPanel.tsx").then((m) => ({
    default: m.HistoryPanel,
  })),
);
const TrafficControlsPanel = lazy(() =>
  import("@/features/traffic-controls/TrafficControlsPanel.tsx").then((m) => ({
    default: m.TrafficControlsPanel,
  })),
);
const TranscodeDesignerPanel = lazy(() =>
  import("@/features/transcode/TranscodeDesignerPanel.tsx").then((m) => ({
    default: m.TranscodeDesignerPanel,
  })),
);
const WizardPanel = lazy(() =>
  import("@/features/wizard/WizardPanel.tsx").then((m) => ({
    default: m.WizardPanel,
  })),
);

export function App() {
  return (
    <Routes>
      <Route element={<Layout />}>
        <Route path="/" element={<OverviewPanel />} />
        <Route path="/routes" element={<RoutesPanel />} />
        <Route path="/apps" element={<AppsPanel />} />
        <Route path="/tls" element={<TLSPanel />} />
        <Route path="/security" element={<SecurityPanel />} />
        <Route
          path="/traffic"
          element={
            <RouteErrorBoundary key="traffic">
              <Suspense fallback={<Loading label="Loading traffic controls…" />}>
                <TrafficControlsPanel />
              </Suspense>
            </RouteErrorBoundary>
          }
        />
        <Route path="/plugins" element={<PluginsPanel />} />
        <Route path="/streams" element={<StreamsPanel />} />
        <Route
          path="/transcode"
          element={
            <Suspense fallback={null}>
              <TranscodeDesignerPanel />
            </Suspense>
          }
        />
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
        <Route
          path="/history"
          element={
            <RouteErrorBoundary key="history">
              <Suspense fallback={<Loading label="Loading history…" />}>
                <HistoryPanel />
              </Suspense>
            </RouteErrorBoundary>
          }
        />
        <Route
          path="/wizard"
          element={
            <Suspense fallback={<Loading label="Loading setup wizard…" />}>
              <WizardPanel />
            </Suspense>
          }
        />
        <Route
          path="*"
          element={<div className="p-8 text-center text-jul-muted">Page not found.</div>}
        />
      </Route>
    </Routes>
  );
}
