from pathlib import Path

p = Path("internal/handler/proxy_unix_test.go")
s = p.read_text()
s = s.replace('\t"net/http/httptest"\n\t"path/filepath"', '\t"net/http/httptest"\n\t"os"\n\t"path/filepath"', 1)
old = '''\tpath := filepath.Join(t.TempDir(), "backend.sock")
\tln, err := net.Listen("unix", path)
'''
new = '''\tdir, err := os.MkdirTemp("/tmp", "jul407-")
\tif err != nil {
\t\tt.Fatalf("create short Unix fixture dir: %v", err)
\t}
\tt.Cleanup(func() { _ = os.RemoveAll(dir) })
\tpath := filepath.Join(dir, "backend.sock")
\tln, err := net.Listen("unix", path)
'''
if old not in s:
    raise SystemExit("fixture patch target not found")
p.write_text(s.replace(old, new, 1))
