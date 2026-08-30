// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package supportbundle

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"jul/internal/config"
	"jul/internal/doctor"
)

const (
	defaultLogTailBytes = 64 << 10
	maximumLogTailBytes = 512 << 10
)

// DefaultCollectors is the closed local collector registry. Later API/runtime
// collectors must be added explicitly here; operators cannot register paths or
// commands dynamically.
func DefaultCollectors() []Collector {
	return []Collector{
		CollectorFunc{ID: "notice", Fn: collectNotice},
		CollectorFunc{ID: "build", Fn: collectBuild},
		CollectorFunc{ID: "configuration_metadata", Fn: collectConfigurationMetadata},
		CollectorFunc{ID: "doctor", Fn: collectDoctor},
		CollectorFunc{ID: "access_log", Fn: collectAccessLog},
	}
}

func collectNotice(context.Context, Snapshot) ([]Artifact, error) {
	const notice = `Jul support bundle

This archive was generated only after an explicit local operator request.
It has not been uploaded and contains no installation identifier.

Review every artifact before sharing. Structural exclusions and redaction reduce
risk, but cannot mathematically guarantee that every business-sensitive name or
identifier has been removed.

The local/offline bundle does not claim live runtime, reload, metrics or remote
admin evidence. Those sections require a separately supported authenticated API
contract and are deliberately not collected through private Console routes.
`
	return []Artifact{{
		Path:        "NOTICE.txt",
		ContentType: "text/plain; charset=utf-8",
		Sensitivity: "operator review required",
		Data:        []byte(notice),
	}}, nil
}

func collectBuild(ctx context.Context, snapshot Snapshot) ([]Artifact, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	payload := struct {
		Product      string          `json:"product,omitempty"`
		Version      string          `json:"version,omitempty"`
		Commit       string          `json:"commit,omitempty"`
		BuildProfile string          `json:"build_profile,omitempty"`
		GoVersion    string          `json:"go_version"`
		GOOS         string          `json:"goos"`
		GOARCH       string          `json:"goarch"`
		NumCPU       int             `json:"num_cpu"`
		GOMAXPROCS   int             `json:"gomaxprocs"`
		Goroutines   int             `json:"goroutines"`
		AllocBytes   uint64          `json:"alloc_bytes"`
		SystemBytes  uint64          `json:"system_bytes"`
		Capabilities map[string]bool `json:"capabilities,omitempty"`
	}{
		Product:      snapshot.Product,
		Version:      snapshot.Version,
		Commit:       snapshot.Commit,
		BuildProfile: snapshot.BuildProfile,
		GoVersion:    runtime.Version(),
		GOOS:         runtime.GOOS,
		GOARCH:       runtime.GOARCH,
		NumCPU:       runtime.NumCPU(),
		GOMAXPROCS:   runtime.GOMAXPROCS(0),
		Goroutines:   runtime.NumGoroutine(),
		AllocBytes:   memory.Alloc,
		SystemBytes:  memory.Sys,
		Capabilities: cloneCapabilities(snapshot.Capabilities),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return []Artifact{{
		Path:        "build/runtime.json",
		ContentType: "application/json",
		Sensitivity: "safe build and process summary",
		Data:        data,
	}}, nil
}

func collectConfigurationMetadata(ctx context.Context, snapshot Snapshot) ([]Artifact, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cfg, err := loadSnapshotConfig(snapshot)
	if err != nil {
		return nil, err
	}
	metadata := doctor.SafeConfigMetadata(cfg, snapshot.Capabilities)
	data, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	return []Artifact{{
		Path:        "configuration/metadata.json",
		ContentType: "application/json",
		Sensitivity: "counts and enabled-state metadata only; no raw values",
		Data:        data,
	}}, nil
}

func collectDoctor(ctx context.Context, snapshot Snapshot) ([]Artifact, error) {
	report := doctor.Run(ctx, doctor.Options{
		ConfigPath:      configPath(snapshot),
		CheckNetwork:    snapshot.CheckNetwork,
		TotalTimeout:    0,
		PerCheckTimeout: 0,
		Product:         snapshot.Product,
		Version:         snapshot.Version,
		Commit:          snapshot.Commit,
		BuildProfile:    snapshot.BuildProfile,
		Capabilities:    snapshot.Capabilities,
	})
	jsonData, err := json.Marshal(report)
	if err != nil {
		return nil, err
	}
	var human bytes.Buffer
	if err := doctor.RenderText(&human, report); err != nil {
		return nil, err
	}
	return []Artifact{
		{
			Path:        "diagnostics/doctor.json",
			ContentType: "application/json",
			Sensitivity: "secret-safe diagnostic results",
			Data:        jsonData,
		},
		{
			Path:        "diagnostics/doctor.txt",
			ContentType: "text/plain; charset=utf-8",
			Sensitivity: "secret-safe diagnostic results",
			Data:        human.Bytes(),
		},
	}, nil
}

func collectAccessLog(ctx context.Context, snapshot Snapshot) ([]Artifact, error) {
	if !snapshot.IncludeLogs {
		return nil, nil
	}
	cfg, err := loadSnapshotConfig(snapshot)
	if err != nil {
		return nil, err
	}
	accessLog := cfg.Observability.AccessLog
	if !accessLog.IsEnabled() || accessLog.File == "" || !containsString(accessLog.Sinks, "file") {
		return nil, nil
	}
	limit := snapshot.LogTailBytes
	if limit <= 0 {
		limit = defaultLogTailBytes
	}
	if limit > maximumLogTailBytes {
		limit = maximumLogTailBytes
	}
	data, truncated, err := tailRegularFile(ctx, accessLog.File, limit)
	if err != nil {
		return nil, err
	}
	return []Artifact{{
		Path:        "logs/access.log.tail",
		ContentType: "text/plain; charset=utf-8",
		Sensitivity: "bounded configured Jul access-log tail; review before sharing",
		Data:        data,
		Truncated:   truncated,
	}}, nil
}

func loadSnapshotConfig(snapshot Snapshot) (*config.Config, error) {
	return config.NewTOMLSource(configPath(snapshot)).Load()
}

func configPath(snapshot Snapshot) string {
	if snapshot.ConfigPath == "" {
		return "server.toml"
	}
	return snapshot.ConfigPath
}

func cloneCapabilities(input map[string]bool) map[string]bool {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]bool, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if strings.EqualFold(value, expected) {
			return true
		}
	}
	return false
}

func tailRegularFile(ctx context.Context, path string, limit int64) ([]byte, bool, error) {
	file, info, err := openVerifiedRegularFile(path)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()

	start := info.Size() - limit
	truncated := start > 0
	if start < 0 {
		start = 0
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return nil, false, fmt.Errorf("seek configured access log: %w", err)
	}
	reader := io.LimitReader(file, limit)
	data, err := io.ReadAll(&contextReader{ctx: ctx, reader: reader})
	if err != nil {
		return nil, false, fmt.Errorf("read configured access log: %w", err)
	}
	if truncated {
		if newline := bytes.IndexByte(data, '\n'); newline >= 0 {
			data = data[newline+1:]
		} else {
			data = nil
		}
	}
	return data, truncated, nil
}

func openVerifiedRegularFile(path string) (*os.File, os.FileInfo, error) {
	cleanPath := filepath.Clean(path)
	if err := rejectSymlinkComponents(cleanPath); err != nil {
		return nil, nil, fmt.Errorf("configured access log path is unsafe: %w", err)
	}
	before, err := os.Lstat(cleanPath)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect configured access log: %w", err)
	}
	if before.Mode()&os.ModeSymlink != 0 {
		return nil, nil, fmt.Errorf("configured access log is a symbolic link; refusing to follow it")
	}
	if !before.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("configured access log is not a regular file")
	}

	file, err := os.Open(cleanPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open configured access log: %w", err)
	}
	closeWithError := func(message string, cause error) (*os.File, os.FileInfo, error) {
		_ = file.Close()
		return nil, nil, fmt.Errorf("%s: %w", message, cause)
	}
	opened, err := file.Stat()
	if err != nil {
		return closeWithError("stat opened access log", err)
	}
	after, err := os.Lstat(cleanPath)
	if err != nil {
		return closeWithError("reinspect configured access log", err)
	}
	if after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() || !opened.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, fmt.Errorf("configured access log changed type during open; refusing to read it")
	}
	if !os.SameFile(before, opened) || !os.SameFile(after, opened) {
		_ = file.Close()
		return nil, nil, fmt.Errorf("configured access log changed identity during open; refusing to read it")
	}
	return file, opened, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader *contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}
