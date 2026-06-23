import { useEffect, useRef } from "react";
import { EditorState, type Extension } from "@codemirror/state";
import {
  EditorView,
  keymap,
  lineNumbers,
  highlightActiveLine,
  highlightActiveLineGutter,
  drawSelection,
} from "@codemirror/view";
import { defaultKeymap, history, historyKeymap, indentWithTab } from "@codemirror/commands";
import {
  StreamLanguage,
  syntaxHighlighting,
  defaultHighlightStyle,
  bracketMatching,
  indentOnInput,
} from "@codemirror/language";
import { toml } from "@codemirror/legacy-modes/mode/toml";
import { cspNonce } from "@/api/client.ts";

// Editor chrome themed to the jul design tokens. The CSS custom properties are
// emitted to :root by Tailwind's @theme block, so referencing them keeps the
// editor in lock-step with the rest of the console.
const julTheme = EditorView.theme(
  {
    "&": {
      color: "var(--color-jul-text)",
      backgroundColor: "var(--color-jul-surface)",
      fontSize: "12px",
      height: "100%",
    },
    ".cm-scroller": { fontFamily: "var(--font-family-mono)", lineHeight: "1.6" },
    ".cm-content": { caretColor: "var(--color-jul-accent)" },
    "&.cm-focused": { outline: "none" },
    ".cm-cursor, .cm-dropCursor": { borderLeftColor: "var(--color-jul-accent)" },
    ".cm-gutters": {
      backgroundColor: "var(--color-jul-bg)",
      color: "var(--color-jul-muted)",
      border: "none",
    },
    ".cm-activeLine": { backgroundColor: "rgba(90, 200, 250, 0.06)" },
    ".cm-activeLineGutter": { backgroundColor: "rgba(90, 200, 250, 0.06)" },
    ".cm-selectionBackground, &.cm-focused .cm-selectionBackground": {
      backgroundColor: "rgba(90, 200, 250, 0.20)",
    },
    ".cm-matchingBracket": { outline: "1px solid var(--color-jul-accent)" },
  },
  { dark: true },
);

export interface CodeEditorProps {
  readonly value: string;
  readonly onChange: (next: string) => void;
  readonly readOnly?: boolean;
  readonly ariaLabel?: string;
}

/**
 * Lazy-loaded CodeMirror 6 TOML editor. Imported via React.lazy so neither
 * CodeMirror nor its language modes land in the initial-route bundle. Inline
 * theme styles are stamped with the per-response CSP nonce read from the SPA
 * shell, so the editor works under the strict `style-src 'self' 'nonce-…'`
 * policy without `unsafe-inline`.
 */
export function CodeEditor({
  value,
  onChange,
  readOnly = false,
  ariaLabel = "Configuration TOML editor",
}: CodeEditorProps) {
  const host = useRef<HTMLDivElement | null>(null);
  const view = useRef<EditorView | null>(null);
  const onChangeRef = useRef(onChange);
  onChangeRef.current = onChange;
  // Captured once: the editor is created a single time and kept in sync via the
  // value effect below, so these initial inputs are read through a ref to keep
  // the mount effect free of reactive dependencies.
  const init = useRef({ value, readOnly, ariaLabel });

  useEffect(() => {
    const parent = host.current;
    if (!parent) return;

    const nonce = cspNonce();
    const extensions: Extension[] = [
      lineNumbers(),
      highlightActiveLineGutter(),
      highlightActiveLine(),
      drawSelection(),
      history(),
      bracketMatching(),
      indentOnInput(),
      keymap.of([...defaultKeymap, ...historyKeymap, indentWithTab]),
      StreamLanguage.define(toml),
      syntaxHighlighting(defaultHighlightStyle, { fallback: true }),
      julTheme,
      EditorView.lineWrapping,
      EditorView.contentAttributes.of({ "aria-label": init.current.ariaLabel }),
      EditorView.editable.of(!init.current.readOnly),
      EditorState.readOnly.of(init.current.readOnly),
      EditorView.updateListener.of((update) => {
        if (update.docChanged) onChangeRef.current(update.state.doc.toString());
      }),
    ];
    if (nonce) extensions.push(EditorView.cspNonce.of(nonce));

    const editor = new EditorView({
      state: EditorState.create({ doc: init.current.value, extensions }),
      parent,
    });
    view.current = editor;
    return () => {
      editor.destroy();
      view.current = null;
    };
  }, []);

  // Propagate external value changes (wizard load, reset) into the document
  // without disturbing the cursor when the value already matches.
  useEffect(() => {
    const editor = view.current;
    if (!editor) return;
    const current = editor.state.doc.toString();
    if (value !== current) {
      editor.dispatch({ changes: { from: 0, to: current.length, insert: value } });
    }
  }, [value]);

  return <div ref={host} className="h-full w-full overflow-hidden" />;
}
