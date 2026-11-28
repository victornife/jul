/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ testRoute: vi.fn() }));

vi.mock("@/api/client.ts", async () => {
  const actual = await vi.importActual<typeof import("@/api/client.ts")>("@/api/client.ts");
  return { ...actual, testRoute: mocks.testRoute };
});

import { RouteTester } from "@/features/routes/RouteTester.tsx";

function renderTester() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <RouteTester onClose={() => undefined} />
    </QueryClientProvider>,
  );
}

describe("Route Test quota classification", () => {
  beforeEach(() => {
    mocks.testRoute.mockReset().mockResolvedValue({
      matched: true,
      auth: false,
      cache: false,
      compression: false,
      rate_limit: false,
      secure: false,
      explanation: "Matched route.",
      quota_classification: {
        policy: "api",
        operation: "export",
        class: "office",
        winning_rule: 'longest matching CIDR pool "internal" (192.0.2.0/24)',
        precedence: "verified principal mapping, then longest matching attributed CIDR, then public fallback",
        note: "Simulation only; this does not authenticate the supplied identity.",
      },
    });
  });

  it("sends hypothetical address inputs and displays the effective class and rule", async () => {
    renderTester();
    fireEvent.click(screen.getByText("Simulate quota classification inputs"));
    fireEvent.change(screen.getByLabelText("Attributed client address (optional)"), {
      target: { value: "192.0.2.10" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Test route" }));

    expect(await screen.findByText("Application quota classification")).toBeTruthy();
    expect(screen.getByText("office")).toBeTruthy();
    expect(screen.getByText('longest matching CIDR pool "internal" (192.0.2.0/24)')).toBeTruthy();
    expect(mocks.testRoute).toHaveBeenCalledWith(
      expect.objectContaining({ client_address: "192.0.2.10" }),
    );
    expect(screen.getByText(/does not authenticate/i)).toBeTruthy();
  });
});
