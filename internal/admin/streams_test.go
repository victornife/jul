package admin

import (
	"net/http/httptest"
	"strings"
	"testing"

	"jul/internal/config"
)

// streamPatchConfig returns a config with one tcp stream and one upstream, for
// exercising the Phase 4i stream patch ops.
func streamPatchConfig() *config.Config {
	return &config.Config{
		Upstreams: []config.UpstreamConfig{{
			Name:    "db",
			Servers: []config.UpstreamServer{{Address: "127.0.0.1:5432", Weight: 1}},
		}},
		Streams: []config.StreamServer{{
			Listen:    ":5432",
			Protocol:  "tcp",
			ProxyPass: "db",
		}},
	}
}

func TestApplyPatchStreamAdd(t *testing.T) {
	c := streamPatchConfig()
	summary, err := applyPatch(c, patchRequest{
		Op:     "stream_add",
		Stream: &streamDef{Listen: ":6379", Protocol: "tcp", ProxyPass: "127.0.0.1:6379", IdleTimeout: "2m"},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(c.Streams) != 2 {
		t.Fatalf("got %d streams, want 2", len(c.Streams))
	}
	added := c.Streams[1]
	if added.Listen != ":6379" || added.ProxyPass != "127.0.0.1:6379" {
		t.Errorf("unexpected stream: %+v", added)
	}
	if added.IdleTimeout.Std().String() != "2m0s" {
		t.Errorf("idle timeout = %s, want 2m0s", added.IdleTimeout.Std())
	}
	if !strings.Contains(summary, "added") {
		t.Errorf("summary = %q, want added", summary)
	}
}

func TestApplyPatchStreamAddDuplicateRejected(t *testing.T) {
	c := streamPatchConfig()
	if _, err := applyPatch(c, patchRequest{
		Op:     "stream_add",
		Stream: &streamDef{Listen: ":5432", Protocol: "tcp", ProxyPass: "db"},
	}); err == nil {
		t.Error("expected error adding a duplicate tcp/:5432 stream")
	}
}

func TestApplyPatchStreamAddDifferentProtoAllowed(t *testing.T) {
	c := streamPatchConfig()
	// Same listen but udp is a distinct identity from the existing tcp stream.
	if _, err := applyPatch(c, patchRequest{
		Op:     "stream_add",
		Stream: &streamDef{Listen: ":5432", Protocol: "udp", ProxyPass: "127.0.0.1:5432"},
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(c.Streams) != 2 {
		t.Fatalf("got %d streams, want 2", len(c.Streams))
	}
}

func TestApplyPatchStreamAddRequiresTarget(t *testing.T) {
	c := streamPatchConfig()
	if _, err := applyPatch(c, patchRequest{
		Op:     "stream_add",
		Stream: &streamDef{Listen: ":7000", Protocol: "tcp"},
	}); err == nil {
		t.Error("expected error when a stream has no proxy_pass and no sni_routes")
	}
}

func TestApplyPatchStreamAddUDPRejectsSNI(t *testing.T) {
	c := streamPatchConfig()
	if _, err := applyPatch(c, patchRequest{
		Op:     "stream_add",
		Stream: &streamDef{Listen: ":53", Protocol: "udp", SNIRoutes: map[string]string{"x": "y:1"}},
	}); err == nil {
		t.Error("expected error: sni_routes on a udp stream")
	}
}

func TestApplyPatchStreamAddInvalidProxyProtocol(t *testing.T) {
	c := streamPatchConfig()
	if _, err := applyPatch(c, patchRequest{
		Op:     "stream_add",
		Stream: &streamDef{Listen: ":9000", Protocol: "tcp", ProxyPass: "x:1", ProxyProtocol: "sideways"},
	}); err == nil {
		t.Error("expected error: invalid proxy_protocol")
	}
}

func TestApplyPatchStreamSet(t *testing.T) {
	c := streamPatchConfig()
	summary, err := applyPatch(c, patchRequest{
		Op:             "stream_set",
		Listen:         ":5432",
		StreamProtocol: "tcp",
		Stream:         &streamDef{Listen: ":5432", Protocol: "tcp", ProxyPass: "127.0.0.1:5433", ProxyProtocol: "out"},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if c.Streams[0].ProxyPass != "127.0.0.1:5433" || c.Streams[0].ProxyProtocol != "out" {
		t.Errorf("unexpected stream: %+v", c.Streams[0])
	}
	if !strings.Contains(summary, "updated") {
		t.Errorf("summary = %q, want updated", summary)
	}
}

func TestApplyPatchStreamSetDefaultsProtocolToTCP(t *testing.T) {
	c := streamPatchConfig()
	// Omitting StreamProtocol targets the tcp stream (tcp is the default).
	if _, err := applyPatch(c, patchRequest{
		Op:     "stream_set",
		Listen: ":5432",
		Stream: &streamDef{Listen: ":5432", ProxyPass: "db"},
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
}

func TestApplyPatchStreamSetNotFound(t *testing.T) {
	c := streamPatchConfig()
	if _, err := applyPatch(c, patchRequest{
		Op:     "stream_set",
		Listen: ":9999",
		Stream: &streamDef{Listen: ":9999", ProxyPass: "db"},
	}); err == nil {
		t.Error("expected error targeting a missing stream")
	}
}

func TestApplyPatchStreamSetIdentityCollision(t *testing.T) {
	c := streamPatchConfig()
	c.Streams = append(c.Streams, config.StreamServer{Listen: ":6379", Protocol: "tcp", ProxyPass: "127.0.0.1:6379"})
	// Editing the :5432 stream to also listen on :6379 would collide.
	if _, err := applyPatch(c, patchRequest{
		Op:     "stream_set",
		Listen: ":5432",
		Stream: &streamDef{Listen: ":6379", Protocol: "tcp", ProxyPass: "db"},
	}); err == nil {
		t.Error("expected error: stream_set identity collides with another stream")
	}
}

func TestApplyPatchStreamRemove(t *testing.T) {
	c := streamPatchConfig()
	summary, err := applyPatch(c, patchRequest{
		Op:     "stream_remove",
		Listen: ":5432",
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(c.Streams) != 0 {
		t.Fatalf("got %d streams, want 0", len(c.Streams))
	}
	if !strings.Contains(summary, "removed") {
		t.Errorf("summary = %q, want removed", summary)
	}
}

func TestApplyPatchStreamRemoveNotFound(t *testing.T) {
	c := streamPatchConfig()
	if _, err := applyPatch(c, patchRequest{
		Op:             "stream_remove",
		Listen:         ":5432",
		StreamProtocol: "udp",
	}); err == nil {
		t.Error("expected error removing a non-existent udp stream")
	}
}

func TestDiffStreams(t *testing.T) {
	before := streamPatchConfig()
	after := streamPatchConfig()
	// Modify the existing stream's target, add a new one, and the diff should
	// also fire for a removal when we drop one.
	after.Streams[0].ProxyPass = "127.0.0.1:5433"
	after.Streams = append(after.Streams, config.StreamServer{Listen: ":6379", Protocol: "tcp", ProxyPass: "127.0.0.1:6379"})

	d := diffConfigs(before, after)
	if !diffHas(d, "Change default backend for stream tcp/:5432") {
		t.Errorf("expected proxy_pass change, got %+v", allDiffEntries(d))
	}
	if !diffHas(d, "Add L4 stream listener tcp/:6379") {
		t.Errorf("expected stream addition, got %+v", allDiffEntries(d))
	}
	if !warnHas(d, "stream tag") {
		t.Errorf("expected lean-build warning, got %+v", d.Warnings)
	}

	// Removing a stream surfaces a removal entry + warning.
	d2 := diffConfigs(before, &config.Config{Upstreams: before.Upstreams})
	if !diffHas(d2, "Remove L4 stream listener tcp/:5432") {
		t.Errorf("expected stream removal, got %+v", allDiffEntries(d2))
	}
	if !warnHas(d2, "stops L4 proxying") {
		t.Errorf("expected removal warning, got %+v", d2.Warnings)
	}
}

func TestDiffStreamsProtocolDefaultNotSpurious(t *testing.T) {
	// A stream with an empty Protocol (tcp default) must not diff against one
	// that spells out "tcp".
	before := &config.Config{Streams: []config.StreamServer{{Listen: ":5432", ProxyPass: "db"}}}
	after := &config.Config{Streams: []config.StreamServer{{Listen: ":5432", Protocol: "tcp", ProxyPass: "db"}}}
	if d := diffConfigs(before, after); len(allDiffEntries(d)) != 0 {
		t.Errorf("unexpected spurious diff: %+v", allDiffEntries(d))
	}
}

func TestProjectStreams(t *testing.T) {
	c := streamPatchConfig()
	c.Streams = append(c.Streams, config.StreamServer{
		Listen:    ":443",
		Protocol:  "tcp",
		SNIRoutes: map[string]string{"a.example": "back:443"},
	})
	proj := projectStreams(c, true)
	if !proj.Compiled {
		t.Error("compiled flag not propagated")
	}
	if len(proj.Streams) != 2 {
		t.Fatalf("got %d streams, want 2", len(proj.Streams))
	}
	if proj.Streams[0].Protocol != "tcp" || proj.Streams[0].ProxyPass != "db" {
		t.Errorf("stream 0 projection = %+v", proj.Streams[0])
	}
	if len(proj.Streams[1].SNIRoutes) != 1 {
		t.Errorf("stream 1 SNI routes = %+v", proj.Streams[1].SNIRoutes)
	}
}

func TestHandleStreamsEndpoint(t *testing.T) {
	cfg := streamPatchConfig()
	srv := newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig:     func() (*config.Config, error) { return cfg, nil },
		StreamCompiled: true,
	})
	req := httptest.NewRequest("GET", "/api/streams", nil)
	rec := httptest.NewRecorder()
	srv.handleStreams(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"compiled":true`) || !strings.Contains(body, `":5432"`) {
		t.Errorf("unexpected body: %s", body)
	}
}
