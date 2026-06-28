/**
 * Render test for the TimelinePanel severity tooltips (Phase 2): each event dot
 * carries an accessible label and a native title so operators can tell what the
 * coloured dot means without decoding the palette.
 */
import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, waitFor, cleanup } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";

vi.mock("@/api/client.ts", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client.ts")>();
  return {
    ...actual,
    fetchTimeline: vi.fn().mockResolvedValue([
      {
        time: "2024-01-01T00:00:00Z",
        category: "tls",
        type: "cert.renew",
        severity: "warning",
        message: "cert renewing",
      },
    ]),
  };
});

import { TimelinePanel } from "@/features/observability/TimelinePanel.tsx";

function Wrapper({ children }: { readonly children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
}

afterEach(() => {
  cleanup();
});

describe("TimelinePanel tooltips", () => {
  it("labels each event dot with its severity and category", async () => {
    render(
      <Wrapper>
        <TimelinePanel />
      </Wrapper>,
    );
    await waitFor(() => {
      expect(screen.getByText("cert renewing")).toBeInTheDocument();
    });
    const dot = screen.getByRole("img", { name: /warning tls event/i });
    expect(dot).toHaveAttribute("title", expect.stringContaining("Warning severity"));
  });
});
