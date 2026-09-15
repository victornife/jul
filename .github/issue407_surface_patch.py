from pathlib import Path


def replace(path: str, old: str, new: str) -> None:
    p = Path(path)
    s = p.read_text()
    if old not in s:
        raise SystemExit(f"patch target not found in {path}: {old[:120]!r}")
    p.write_text(s.replace(old, new, 1))


# Repair the missing outer-loop brace caught by gofmt.
replace(
    "cmd/jul/import_corpus_test.go",
    "\t\t\t}\n\t}\n\tif len(paths) == 0 {",
    "\t\t\t}\n\t\t}\n\t}\n\tif len(paths) == 0 {",
)

# Runtime snapshot: carry bounded network identity separately from normalized
# dial address so operator/API code never has to infer backend kind from a path.
replace(
    "internal/upstream/registry.go",
    'type BackendStatus struct {\n\tAddress string\n\tWeight  int\n',
    'type BackendStatus struct {\n\tAddress string\n\tNetwork string\n\tWeight  int\n',
)
replace(
    "internal/upstream/registry.go",
    "\t\t\tps.Backends = append(ps.Backends, BackendStatus{\n\t\t\t\tAddress:  b.Address,\n\t\t\t\tWeight:   b.Weight(),",
    "\t\t\tps.Backends = append(ps.Backends, BackendStatus{\n\t\t\t\tAddress:  b.Address,\n\t\t\t\tNetwork:  b.Network,\n\t\t\t\tWeight:   b.Weight(),",
)

# Internal admin status surface.
replace(
    "internal/admin/api.go",
    'type BackendStatus struct {\n\tAddress string `json:"address"`\n\tWeight  int    `json:"weight"`\n',
    'type BackendStatus struct {\n\tAddress string `json:"address"`\n\tNetwork string `json:"network,omitempty"`\n\tWeight  int    `json:"weight"`\n',
)
replace(
    "internal/app/admin_deps.go",
    "\t\t\tps.Backends = append(ps.Backends, admin.BackendStatus{\n\t\t\t\tAddress:  b.Address,\n\t\t\t\tWeight:   b.Weight,",
    "\t\t\tps.Backends = append(ps.Backends, admin.BackendStatus{\n\t\t\t\tAddress:  b.Address,\n\t\t\t\tNetwork:  b.Network,\n\t\t\t\tWeight:   b.Weight,",
)

# Console v2 projection and normalized live-state join.
replace(
    "internal/admin/projection_types.go",
    'type BackendProjection struct {\n\tAddress string `json:"address"`\n\tWeight  int    `json:"weight"`\n',
    'type BackendProjection struct {\n\tAddress string `json:"address"`\n\tNetwork string `json:"network,omitempty"`\n\tWeight  int    `json:"weight"`\n',
)
replace(
    "internal/admin/projections.go",
    "func projectApps(c *config.Config, live map[string]UpstreamStatus) []AppProjection {",
    '''func backendProjectionKey(network, address string) string {
\treturn network + "\\x00" + address
}

func configuredBackendIdentity(raw string) (network, address string) {
\tif strings.HasPrefix(raw, "unix:") {
\t\treturn "unix", strings.TrimPrefix(raw, "unix:")
\t}
\treturn "tcp", raw
}

func projectApps(c *config.Config, live map[string]UpstreamStatus) []AppProjection {''',
)
replace(
    "internal/admin/projections.go",
    '''\t\tlivePool := live[up.Name]
\t\tliveMap := make(map[string]BackendStatus, len(livePool.Backends))
\t\tfor _, b := range livePool.Backends {
\t\t\tliveMap[b.Address] = b
\t\t}
\t\tseen := make(map[string]bool, len(up.Servers))
\t\tfor _, b := range up.Servers {
\t\t\tseen[b.Address] = true
\t\t\tbp := BackendProjection{Address: b.Address, Weight: b.Weight}
\t\t\tif lb, ok := liveMap[b.Address]; ok {
\t\t\t\tbp.State = lb.State
\t\t\t\tbp.Inflight = lb.Inflight
\t\t\t}
\t\t\tap.Backends = append(ap.Backends, bp)
\t\t}
''',
    '''\t\tlivePool := live[up.Name]
\t\tliveMap := make(map[string]BackendStatus, len(livePool.Backends))
\t\tfor _, b := range livePool.Backends {
\t\t\tliveMap[backendProjectionKey(b.Network, b.Address)] = b
\t\t}
\t\tseen := make(map[string]bool, len(up.Servers))
\t\tfor _, b := range up.Servers {
\t\t\tnetwork, address := configuredBackendIdentity(b.Address)
\t\t\tkey := backendProjectionKey(network, address)
\t\t\tseen[key] = true
\t\t\tbp := BackendProjection{Address: b.Address, Network: network, Weight: b.Weight}
\t\t\tif lb, ok := liveMap[key]; ok {
\t\t\t\tbp.State = lb.State
\t\t\t\tbp.Inflight = lb.Inflight
\t\t\t}
\t\t\tap.Backends = append(ap.Backends, bp)
\t\t}
''',
)
replace(
    "internal/admin/projections.go",
    '''\t\tfor _, b := range livePool.Backends {
\t\t\tif seen[b.Address] {
\t\t\t\tcontinue
\t\t\t}
\t\t\tbp := BackendProjection{Address: b.Address, Weight: b.Weight}
''',
    '''\t\tfor _, b := range livePool.Backends {
\t\t\tif seen[backendProjectionKey(b.Network, b.Address)] {
\t\t\t\tcontinue
\t\t\t}
\t\t\tbp := BackendProjection{Address: b.Address, Network: b.Network, Weight: b.Weight}
''',
)

# External API: additive network field and correct live-state join for Unix.
replace(
    "internal/adminapi/v1_resources.go",
    'type UpstreamBackend struct {\n\tAddress string `json:"address"`\n\tWeight  int    `json:"weight"`\n',
    'type UpstreamBackend struct {\n\tAddress string `json:"address"`\n\tNetwork string `json:"network"`\n\tWeight  int    `json:"weight"`\n',
)
replace(
    "internal/admin/apiv1_resources.go",
    '''\t\tbyAddress := map[string]BackendStatus{}
\t\tfor _, b := range live[up.Name].Backends {
\t\t\tbyAddress[b.Address] = b
\t\t}
\t\tfor j := range up.Servers {
\t\t\tsrv := &up.Servers[j]
\t\t\tb := adminapi.UpstreamBackend{Address: srv.Address, Weight: srv.Weight}
\t\t\tif l, ok := byAddress[srv.Address]; ok {
''',
    '''\t\tbyAddress := map[string]BackendStatus{}
\t\tfor _, b := range live[up.Name].Backends {
\t\t\tbyAddress[backendProjectionKey(b.Network, b.Address)] = b
\t\t}
\t\tfor j := range up.Servers {
\t\t\tsrv := &up.Servers[j]
\t\t\tnetwork, address := configuredBackendIdentity(srv.Address)
\t\t\tb := adminapi.UpstreamBackend{Address: srv.Address, Network: network, Weight: srv.Weight}
\t\t\tif l, ok := byAddress[backendProjectionKey(network, address)]; ok {
''',
)

# Console client validates the bounded network field instead of inferring it.
p = Path("internal/admin/ui/src/api/client.ts")
s = p.read_text()
marker = "export const BackendProjectionSchema = z.object({"
i = s.find(marker)
if i < 0:
    raise SystemExit("BackendProjectionSchema marker not found")
end = s.find("});", i)
j = s.find("  address: z.string(),", i, end)
if j < 0:
    raise SystemExit("BackendProjectionSchema address field not found")
field = '  network: z.enum(["tcp", "unix"]).optional(),'
if field not in s[i:end]:
    line = "  address: z.string(),"
    s = s[:j] + line + "\n" + field + s[j + len(line):]
p.write_text(s)
