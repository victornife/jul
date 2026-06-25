// Client-side helper for the "externalize a literal secret" workflow (Wave A).
// It does not patch config fields in place (that arrives with the structured
// patch API in a later wave); instead it builds the correctly-formatted secret
// reference an operator pastes in place of a literal value, so a token, key, or
// password is never committed to the config file.
//
// Supported reference schemes (SEC-1):
//   ${env:NAME}   — value comes from an environment variable
//   ${file:/path} — value is the trimmed contents of a file

export type SecretSource = "env" | "file";

// suggestEnvName derives a conventional SCREAMING_SNAKE_CASE environment-variable
// name from a free-text field label (e.g. "admin token" → "JUL_ADMIN_TOKEN").
// The JUL_ prefix namespaces the variable so it does not collide with unrelated
// process environment.
export function suggestEnvName(label: string): string {
  const cleaned = label
    .trim()
    .toUpperCase()
    .replace(/[^A-Z0-9]+/g, "_")
    .replace(/^_+|_+$/g, "");
  if (cleaned.length === 0) return "JUL_SECRET";
  return cleaned.startsWith("JUL_") ? cleaned : `JUL_${cleaned}`;
}

// secretReference builds the reference string for the chosen source. For env it
// expects a variable NAME; for file it expects an absolute path. The value is
// intentionally not part of the reference — it is supplied out-of-band (the
// environment or the file), which is the whole point of externalizing it.
export function secretReference(source: SecretSource, target: string): string {
  const t = target.trim();
  return source === "env" ? `\${env:${t}}` : `\${file:${t}}`;
}

// secretRefWarnings reports problems with the chosen target before the operator
// copies the reference.
export function secretRefWarnings(source: SecretSource, target: string): string[] {
  const warn: string[] = [];
  const t = target.trim();
  if (t.length === 0) {
    warn.push(source === "env" ? "Enter an environment-variable name." : "Enter a file path.");
    return warn;
  }
  if (source === "env") {
    if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(t)) {
      warn.push(
        "Environment-variable names should be letters, digits, and underscores, not starting with a digit.",
      );
    }
  } else {
    if (!t.startsWith("/")) {
      warn.push(
        "Use an absolute file path so the reference resolves regardless of working directory.",
      );
    }
  }
  return warn;
}

// looksLikeSecretLiteral is a heuristic for the helper's intro copy: does the
// pasted value look like a real secret (long/high-entropy) rather than a
// placeholder? It never blocks anything; it only tunes the guidance shown.
export function looksLikeSecretLiteral(value: string): boolean {
  const v = value.trim();
  if (v.length < 8) return false;
  const hasUpper = /[A-Z]/.test(v);
  const hasLower = /[a-z]/.test(v);
  const hasDigit = /[0-9]/.test(v);
  const classes = [hasUpper, hasLower, hasDigit].filter(Boolean).length;
  return v.length >= 16 || classes >= 2;
}
