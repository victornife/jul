from pathlib import Path

p = Path("internal/handler/proxy.go")
s = p.read_text()
old = "\t\tvar backendLabel string\n\t\tout, backendLabel, err = prepareProxyAttempt(out, b, t.tlsBackend)\n\t\tif err != nil {\n\t\t\treturn upstream.AttemptResult{Err: err, Terminal: true}\n\t\t}\n"
new = "\t\tprepared, backendLabel, prepErr := prepareProxyAttempt(out, b, t.tlsBackend)\n\t\tif prepErr != nil {\n\t\t\treturn upstream.AttemptResult{Err: prepErr, Terminal: true}\n\t\t}\n\t\tout = prepared\n"
if old not in s:
    raise SystemExit("retry callback patch target not found")
s = s.replace(old, new, 1)
old = "\t\tRewrite: func(pr *httputil.ProxyRequest) {\n\t\t\tpr.SetURL(target)\n"
new = "\t\tRewrite: func(pr *httputil.ProxyRequest) {\n\t\t\tpr.Out = pr.Out.WithContext(withOriginalProxyHost(pr.Out.Context(), pr.In.Host))\n\t\t\tpr.SetURL(target)\n"
if old not in s:
    raise SystemExit("Rewrite patch target not found")
p.write_text(s.replace(old, new, 1))

p = Path("internal/handler/proxy_unix.go")
s = p.read_text()
old = "type unixDialTargetKey struct{}\n\nfunc unixDialTarget(ctx context.Context) (string, bool) {\n"
new = "type unixDialTargetKey struct{}\ntype originalProxyHostKey struct{}\n\nfunc withOriginalProxyHost(ctx context.Context, host string) context.Context {\n\treturn context.WithValue(ctx, originalProxyHostKey{}, host)\n}\n\nfunc originalProxyHost(ctx context.Context) string {\n\thost, _ := ctx.Value(originalProxyHostKey{}).(string)\n\treturn host\n}\n\nfunc unixDialTarget(ctx context.Context) (string, bool) {\n"
if old not in s:
    raise SystemExit("unix context helper patch target not found")
s = s.replace(old, new, 1)
old = "\t\tkey := unixHTTPPoolKey(b.Identity())\n\t\treq.URL.Scheme = \"http\"\n\t\treq.URL.Host = key\n\t\treq = req.WithContext(context.WithValue(req.Context(), unixDialTargetKey{}, b.Address))\n"
new = "\t\tkey := unixHTTPPoolKey(b.Identity())\n\t\treq.URL.Scheme = \"http\"\n\t\treq.URL.Host = key\n\t\tif req.Host == \"\" {\n\t\t\treq.Host = originalProxyHost(req.Context())\n\t\t}\n\t\treq = req.WithContext(context.WithValue(req.Context(), unixDialTargetKey{}, b.Address))\n"
if old not in s:
    raise SystemExit("unix Host patch target not found")
p.write_text(s.replace(old, new, 1))

p = Path("internal/config/validate_unix_http_test.go")
lines = p.read_text().splitlines()
if len(lines) < 27:
    raise SystemExit("generated validation test unexpectedly short")
lines[26] = "\tif len(errs) == 0 {"
p.write_text("\n".join(lines) + "\n")
