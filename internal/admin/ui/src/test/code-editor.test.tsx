/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { CodeEditor } from "@/features/config/CodeEditor.tsx";

afterEach(cleanup);

describe("CodeEditor", () => {
  it("does not report programmatic value synchronization as a user edit", () => {
    const onChange = vi.fn();
    const { rerender } = render(<CodeEditor value="a = 1" onChange={onChange} />);
    rerender(<CodeEditor value="a = 2" onChange={onChange} />);
    expect(onChange).not.toHaveBeenCalled();
  });

  it("updates editability when readOnly changes", () => {
    const onChange = vi.fn();
    const { rerender } = render(
      <CodeEditor value="a = 1" onChange={onChange} readOnly ariaLabel="editor" />,
    );
    const editor = screen.getByLabelText("editor");
    expect(editor).toHaveAttribute("contenteditable", "false");
    rerender(<CodeEditor value="a = 1" onChange={onChange} readOnly={false} ariaLabel="editor" />);
    expect(editor).toHaveAttribute("contenteditable", "true");
    fireEvent.input(editor, { target: { textContent: "a = 2" } });
  });
});
