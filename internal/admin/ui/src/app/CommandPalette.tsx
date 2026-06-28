import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useFocusTrap } from "@/lib/useFocusTrap.ts";
import { COMMAND_PALETTE_EVENT } from "@/app/commandPaletteBus.ts";

export interface CommandItem {
  readonly to: string;
  readonly label: string;
  readonly glyph: string;
  readonly group: string;
}

export interface CommandPaletteProps {
  readonly commands: readonly CommandItem[];
}

/**
 * A keyboard-first command palette (complements the task-grouped nav, P1-7).
 * Opens with Ctrl/Cmd+K from anywhere, filters every primary destination by a
 * case-insensitive substring match over the label and group, and navigates on
 * Enter or click. It is purely a navigation accelerator — it owns no routes of
 * its own and adds no backend surface.
 */
export function CommandPalette({ commands }: CommandPaletteProps) {
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const dialogRef = useRef<HTMLDivElement>(null);
  useFocusTrap(dialogRef, open);

  // Global open shortcut: Ctrl+K (Windows/Linux) or Cmd+K (macOS). Escape closes.
  // A custom event (COMMAND_PALETTE_EVENT) opens it from header affordances too.
  useEffect(() => {
    function onKey(e: KeyboardEvent): void {
      if ((e.ctrlKey || e.metaKey) && (e.key === "k" || e.key === "K")) {
        e.preventDefault();
        setOpen((o) => !o);
      } else if (e.key === "Escape") {
        setOpen(false);
      }
    }
    function onOpenEvent(): void {
      setOpen(true);
    }
    window.addEventListener("keydown", onKey);
    window.addEventListener(COMMAND_PALETTE_EVENT, onOpenEvent);
    return () => {
      window.removeEventListener("keydown", onKey);
      window.removeEventListener(COMMAND_PALETTE_EVENT, onOpenEvent);
    };
  }, []);

  // Reset query/selection and focus the input whenever the palette opens.
  useEffect(() => {
    if (open) {
      setQuery("");
      setActive(0);
      // Focus after the element is mounted.
      requestAnimationFrame(() => inputRef.current?.focus());
    }
  }, [open]);

  const results = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (q === "") return commands;
    return commands.filter(
      (c) => c.label.toLowerCase().includes(q) || c.group.toLowerCase().includes(q),
    );
  }, [commands, query]);

  // Keep the active index within the (possibly shrunk) result set.
  useEffect(() => {
    setActive((a) => (a >= results.length ? 0 : a));
  }, [results.length]);

  if (!open) return null;

  function go(to: string): void {
    setOpen(false);
    void navigate(to);
  }

  function onInputKey(e: React.KeyboardEvent<HTMLInputElement>): void {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setActive((a) => (results.length === 0 ? 0 : (a + 1) % results.length));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setActive((a) => (results.length === 0 ? 0 : (a - 1 + results.length) % results.length));
    } else if (e.key === "Enter") {
      e.preventDefault();
      const sel = results[active];
      if (sel) go(sel.to);
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center pt-[15vh]">
      <button
        type="button"
        aria-label="Close command palette"
        className="absolute inset-0 cursor-default bg-black/40"
        onClick={() => {
          setOpen(false);
        }}
      />
      <div
        ref={dialogRef}
        tabIndex={-1}
        role="dialog"
        aria-modal="true"
        aria-label="Command palette"
        className="relative z-10 w-full max-w-lg overflow-hidden rounded-lg border border-jul-border bg-jul-surface shadow-2xl outline-none"
      >
        <input
          ref={inputRef}
          type="text"
          value={query}
          placeholder="Jump to… (type to filter, ↑↓ to move, Enter to go)"
          onChange={(e) => {
            setQuery(e.target.value);
          }}
          onKeyDown={onInputKey}
          className="w-full border-b border-jul-border bg-jul-surface px-4 py-3 text-sm text-jul-text placeholder:text-jul-muted focus:outline-none"
        />
        <ul className="max-h-80 overflow-auto py-1">
          {results.length === 0 ? (
            <li className="px-4 py-3 text-sm text-jul-muted">No matching destinations.</li>
          ) : (
            results.map((c, i) => (
              <li key={c.to}>
                <button
                  type="button"
                  onMouseEnter={() => {
                    setActive(i);
                  }}
                  onClick={() => {
                    go(c.to);
                  }}
                  className={`flex w-full items-center gap-3 px-4 py-2 text-left text-sm ${
                    i === active ? "bg-jul-accent text-jul-bg" : "text-jul-text hover:bg-jul-bg"
                  }`}
                >
                  <span aria-hidden className="text-base">
                    {c.glyph}
                  </span>
                  <span className="flex-1">{c.label}</span>
                  <span className={`text-xs ${i === active ? "text-jul-bg/80" : "text-jul-muted"}`}>
                    {c.group}
                  </span>
                </button>
              </li>
            ))
          )}
        </ul>
      </div>
    </div>
  );
}
