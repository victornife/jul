from pathlib import Path

p = Path("internal/handler/proxy.go")
s = p.read_text()
old = "\t\tvar backendLabel string\n\t\tout, backendLabel, err = prepareProxyAttempt(out, b, t.tlsBackend)\n\t\tif err != nil {\n\t\t\treturn upstream.AttemptResult{Err: err, Terminal: true}\n\t\t}\n"
new = "\t\tprepared, backendLabel, prepErr := prepareProxyAttempt(out, b, t.tlsBackend)\n\t\tif prepErr != nil {\n\t\t\treturn upstream.AttemptResult{Err: prepErr, Terminal: true}\n\t\t}\n\t\tout = prepared\n"
if old not in s:
    raise SystemExit("retry callback patch target not found")
p.write_text(s.replace(old, new, 1))

p = Path("internal/config/validate_unix_http_test.go")
lines = p.read_text().splitlines()
if len(lines) < 27:
    raise SystemExit("generated validation test unexpectedly short")
lines[26] = "\tif len(errs) == 0 {"
p.write_text("\n".join(lines) + "\n")
