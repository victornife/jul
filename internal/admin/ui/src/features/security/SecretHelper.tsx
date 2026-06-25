import { useState } from "react";
import { Drawer } from "@/components/Drawer.tsx";
import {
  looksLikeSecretLiteral,
  secretRefWarnings,
  secretReference,
  suggestEnvName,
  type SecretSource,
} from "@/lib/secretsRef.ts";

function TextField({
  label,
  hint,
  value,
  placeholder,
  onChange,
}: {
  readonly label: string;
  readonly hint?: string;
  readonly value: string;
  readonly placeholder?: string;
  readonly onChange: (v: string) => void;
}) {
  return (
    <label className="block space-y-1">
      <span className="text-sm font-medium text-jul-text">{label}</span>
      <input
        type="text"
        value={value}
        placeholder={placeholder}
        onChange={(e) => {
          onChange(e.target.value);
        }}
        className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 font-mono text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
      />
      {hint && <span className="text-xs text-jul-muted">{hint}</span>}
    </label>
  );
}

export interface SecretHelperProps {
  readonly onClose: () => void;
}

/**
 * Secret-reference helper (Wave A). Externalizing a literal secret is the safe
 * pattern, but in-place field patching is a later wave; until then this helper
 * builds the correctly-formatted ${env:…}/${file:…} reference an operator pastes
 * in place of a literal token/key/password, so the secret never lands in the
 * config file. It is non-mutating: it only produces a snippet to copy.
 */
export function SecretHelper({ onClose }: SecretHelperProps) {
  const [label, setLabel] = useState("");
  const [literal, setLiteral] = useState("");
  const [source, setSource] = useState<SecretSource>("env");
  const [target, setTarget] = useState("");
  const [copied, setCopied] = useState(false);

  const suggestedEnv = suggestEnvName(label || "secret");
  const effectiveTarget = target.trim() || (source === "env" ? suggestedEnv : "");
  const reference = secretReference(source, effectiveTarget);
  const warnings = secretRefWarnings(source, effectiveTarget);
  const showSecretHint = literal.trim().length > 0 && looksLikeSecretLiteral(literal);

  async function copyReference(): Promise<void> {
    try {
      await navigator.clipboard.writeText(reference);
      setCopied(true);
      setTimeout(() => {
        setCopied(false);
      }, 1500);
    } catch {
      setCopied(false);
    }
  }

  return (
    <Drawer
      title="Externalize a secret"
      subtitle="Build a secret reference to paste in place of a literal value."
      onClose={onClose}
      footer={
        <button
          type="button"
          onClick={() => {
            void copyReference();
          }}
          disabled={warnings.length > 0}
          className="ml-auto rounded-md bg-jul-accent px-4 py-1.5 text-sm font-medium text-jul-bg hover:brightness-110 disabled:opacity-40"
        >
          {copied ? "Copied!" : "Copy reference"}
        </button>
      }
    >
      <div className="space-y-5">
        <p className="rounded-md border border-jul-border bg-jul-surface p-3 text-xs text-jul-muted">
          A secret reference keeps tokens, keys, and passwords out of the config file. Jul resolves{" "}
          <span className="font-mono">${"{env:NAME}"}</span> from an environment variable and{" "}
          <span className="font-mono">${"{file:/path}"}</span> from a file at load time, and masks
          the resolved value in logs. Replace the literal value in your config with the reference
          below, then provide the value via the environment or a file.
        </p>

        <TextField
          label="What is this secret? (optional)"
          hint="Used only to suggest an environment-variable name."
          value={label}
          placeholder="admin token"
          onChange={(v) => {
            setLabel(v);
          }}
        />

        <TextField
          label="Current literal value (optional, never stored)"
          hint="Paste it only to confirm it looks like a real secret; it is not sent anywhere."
          value={literal}
          placeholder="paste the value you want to externalize"
          onChange={(v) => {
            setLiteral(v);
          }}
        />
        {showSecretHint && (
          <p className="rounded-md border border-jul-warning/40 bg-jul-warning/10 p-2 text-xs text-jul-warning">
            This looks like a real secret. Externalize it and remove the literal from the config.
          </p>
        )}

        <label className="block space-y-1">
          <span className="text-sm font-medium text-jul-text">Source</span>
          <select
            value={source}
            onChange={(e) => {
              setSource(e.target.value as SecretSource);
            }}
            className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
          >
            <option value="env">Environment variable (${"{env:NAME}"})</option>
            <option value="file">File contents (${"{file:/path}"})</option>
          </select>
        </label>

        {source === "env" ? (
          <TextField
            label="Environment-variable name"
            hint={`Suggested: ${suggestedEnv}. Set this variable in the server's environment.`}
            value={target}
            placeholder={suggestedEnv}
            onChange={(v) => {
              setTarget(v);
            }}
          />
        ) : (
          <TextField
            label="File path"
            hint="Absolute path to a file containing the secret (e.g. a mounted Docker/K8s secret)."
            value={target}
            placeholder="/run/secrets/admin-token"
            onChange={(v) => {
              setTarget(v);
            }}
          />
        )}

        {warnings.length > 0 && (
          <div className="space-y-1 rounded-md border border-jul-warning/40 bg-jul-warning/10 p-2">
            {warnings.map((wn, i) => (
              <p key={`sw-${String(i)}`} className="text-xs text-jul-warning">
                {wn}
              </p>
            ))}
          </div>
        )}

        <div className="space-y-1">
          <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
            Reference to paste
          </span>
          <pre className="overflow-auto rounded-md border border-jul-border bg-jul-surface p-3 font-mono text-sm leading-relaxed text-jul-text">
            {reference}
          </pre>
          {source === "env" ? (
            <p className="text-xs text-jul-muted">
              Then export it before starting Jul, e.g.{" "}
              <span className="font-mono">{`export ${effectiveTarget || suggestedEnv}=…`}</span>
            </p>
          ) : (
            <p className="text-xs text-jul-muted">
              Then write the secret to that file with restrictive permissions (e.g.{" "}
              <span className="font-mono">chmod 600</span>).
            </p>
          )}
        </div>
      </div>
    </Drawer>
  );
}
