import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, fireEvent, cleanup } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { CommandPalette, type CommandItem } from "@/app/CommandPalette.tsx";

const navigateMock = vi.fn();
vi.mock("react-router-dom", async () => {
  const actual = await vi.importActual<typeof import("react-router-dom")>("react-router-dom");
  return { ...actual, useNavigate: () => navigateMock };
});

const COMMANDS: readonly CommandItem[] = [
  { to: "/", label: "Overview", glyph: "▣", group: "Operate" },
  { to: "/routes", label: "Routes", glyph: "⇄", group: "Configure" },
  { to: "/security", label: "Security", glyph: "🛡", group: "Configure" },
  { to: "/config", label: "Config", glyph: "⚙", group: "Change safely" },
];

function renderPalette() {
  return render(
    <MemoryRouter>
      <CommandPalette commands={COMMANDS} />
    </MemoryRouter>,
  );
}

function openPalette() {
  fireEvent.keyDown(window, { key: "k", ctrlKey: true });
}

afterEach(() => {
  cleanup();
  navigateMock.mockReset();
});

describe("CommandPalette", () => {
  it("is hidden until the Ctrl/Cmd+K shortcut opens it", () => {
    renderPalette();
    expect(screen.queryByRole("dialog")).toBeNull();
    openPalette();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });

  it("filters destinations by a case-insensitive label/group match", () => {
    renderPalette();
    openPalette();
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "sec" } });
    expect(screen.getByText("Security")).toBeInTheDocument();
    expect(screen.queryByText("Overview")).toBeNull();
  });

  it("shows an empty state when nothing matches", () => {
    renderPalette();
    openPalette();
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "zzz" } });
    expect(screen.getByText("No matching destinations.")).toBeInTheDocument();
  });

  it("navigates to a result on click and closes", () => {
    renderPalette();
    openPalette();
    fireEvent.click(screen.getByText("Routes"));
    expect(navigateMock).toHaveBeenCalledWith("/routes");
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("navigates to the active result on Enter", () => {
    renderPalette();
    openPalette();
    const input = screen.getByRole("textbox");
    // First result is Overview; ArrowDown moves to Routes, Enter selects it.
    fireEvent.keyDown(input, { key: "ArrowDown" });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(navigateMock).toHaveBeenCalledWith("/routes");
  });

  it("closes on Escape", () => {
    renderPalette();
    openPalette();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    fireEvent.keyDown(window, { key: "Escape" });
    expect(screen.queryByRole("dialog")).toBeNull();
  });
});
