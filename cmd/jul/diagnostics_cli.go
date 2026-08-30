// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"jul/internal/diagnostics"
	"jul/internal/doctor"
	"jul/internal/plugins"
	"jul/internal/stream"
	"jul/internal/supportbundle"
	"jul/internal/waf"
)

func dispatchDiagnosticsSubcommand(args []string) (handled bool, code int) {
	if len(args) == 0 {
		return false, 0
	}
	switch args[0] {
	case "doctor":
		return true, cmdDoctor(args[1:])
	case "support-bundle":
		return true, cmdSupportBundle(args[1:])
	default:
		return false, 0
	}
}

func extendedUsage() {
	usage()
	fmt.Fprint(stderr, `
Local diagnostics:
  jul doctor [-config f] [-json] [-strict] [-check-network]
                                       run deterministic read-only checks
  jul support-bundle [-config f] [-output file] [-json] [-include-logs]
                                       write a bounded, secret-safe local archive
`)
}

func cmdDoctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "server.toml", "path to the TOML configuration file")
	jsonOutput := fs.Bool("json", false, "emit the versioned report as JSON")
	strict := fs.Bool("strict", false, "exit 2 when warning results are present")
	checkNetwork := fs.Bool("check-network", false, "enable bounded secret-resolution, runtime preflight and local bind probes")
	totalTimeout := fs.Duration("timeout", 20*time.Second, "total diagnostic timeout")
	perCheckTimeout := fs.Duration("per-check-timeout", 5*time.Second, "timeout for each diagnostic check")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: jul doctor [-config f] [-json] [-strict] [-check-network]")
		return 2
	}
	if *totalTimeout <= 0 || *perCheckTimeout <= 0 {
		fmt.Fprintln(stderr, "error: diagnostic timeouts must be greater than zero")
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	metadata := collectBuildMetadata()
	capabilities := diagnosticCapabilities()
	report := doctor.Run(ctx, doctor.Options{
		ConfigPath:      *configPath,
		CheckNetwork:    *checkNetwork,
		TotalTimeout:    *totalTimeout,
		PerCheckTimeout: *perCheckTimeout,
		Product:         metadata.Product,
		Version:         metadata.Version,
		Commit:          metadata.Commit,
		BuildProfile:    diagnosticBuildProfile(capabilities),
		Capabilities:    capabilities,
	})
	var err error
	if *jsonOutput {
		err = doctor.WriteJSON(stdout, report)
	} else {
		err = doctor.RenderText(stdout, report)
	}
	if err != nil {
		fmt.Fprintf(stderr, "error: write doctor output: %v\n", err)
		return 1
	}
	return doctor.ExitCode(report, *strict)
}

func cmdSupportBundle(args []string) int {
	fs := flag.NewFlagSet("support-bundle", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "server.toml", "path to the TOML configuration file")
	output := fs.String("output", "", "output tar.gz path (default: timestamped file in the current directory)")
	jsonOutput := fs.Bool("json", false, "emit generation status and manifest as JSON")
	checkNetwork := fs.Bool("check-network", false, "include bounded network-capable doctor checks")
	includeLogs := fs.Bool("include-logs", false, "include a bounded tail of the configured Jul access-log file")
	logTailBytes := fs.Int64("log-tail-bytes", 64<<10, "maximum configured access-log bytes to inspect")
	totalTimeout := fs.Duration("timeout", 30*time.Second, "total bundle collection timeout")
	perCollectorTimeout := fs.Duration("per-collector-timeout", 8*time.Second, "timeout for each collector")
	maxArtifactBytes := fs.Int64("max-artifact-bytes", 2<<20, "maximum uncompressed bytes per artifact")
	maxUncompressedBytes := fs.Int64("max-uncompressed-bytes", 12<<20, "maximum total uncompressed artifact and manifest bytes")
	maxCompressedBytes := fs.Int64("max-compressed-bytes", 8<<20, "maximum compressed archive bytes")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: jul support-bundle [-config f] [-output file] [-json] [-include-logs]")
		return 2
	}
	if *totalTimeout <= 0 || *perCollectorTimeout <= 0 || *maxArtifactBytes <= 0 || *maxUncompressedBytes <= 0 || *maxCompressedBytes <= 0 || *logTailBytes < 0 {
		fmt.Fprintln(stderr, "error: time and size limits must be positive (log-tail-bytes may be zero for the default)")
		return 2
	}
	if *output == "" {
		*output = "jul-support-" + time.Now().UTC().Format("20060102T150405Z") + ".tar.gz"
	}

	limits := supportbundle.DefaultLimits()
	limits.TotalTimeout = *totalTimeout
	limits.PerCollectorTimeout = *perCollectorTimeout
	limits.MaxArtifactBytes = *maxArtifactBytes
	limits.MaxUncompressedBytes = *maxUncompressedBytes
	limits.MaxCompressedBytes = *maxCompressedBytes
	metadata := collectBuildMetadata()
	capabilities := diagnosticCapabilities()
	generator := supportbundle.NewGenerator(supportbundle.DefaultCollectors(), limits, 1)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	result, err := generator.WriteFile(ctx, *output, supportbundle.Snapshot{
		Product:      metadata.Product,
		Version:      metadata.Version,
		Commit:       metadata.Commit,
		BuildProfile: diagnosticBuildProfile(capabilities),
		ConfigPath:   *configPath,
		Capabilities: capabilities,
		CheckNetwork: *checkNetwork,
		IncludeLogs:  *includeLogs,
		LogTailBytes: *logTailBytes,
	})
	if err != nil {
		fmt.Fprintf(stderr, "error: %s\n", diagnostics.SanitizeErrorString(err.Error()))
		return 1
	}
	if *jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(result); err != nil {
			fmt.Fprintf(stderr, "error: write support-bundle output: %v\n", err)
			return 1
		}
	} else {
		fmt.Fprintf(stdout, "support bundle written to %s (%d compressed bytes)\n", result.Path, result.CompressedBytes)
		fmt.Fprintln(stdout, "review every artifact before sharing; the bundle has not been uploaded")
	}
	for _, collector := range result.Manifest.Collectors {
		if collector.Status == supportbundle.CollectorError {
			return 1
		}
	}
	return 0
}

func diagnosticCapabilities() map[string]bool {
	return map[string]bool{
		"waf":          waf.Compiled,
		"stream_proxy": stream.Compiled,
		"wasm_plugins": plugins.Compiled,
		"acme":         tagACME,
		"grpc":         tagGRPC,
		"http3":        tagHTTP3,
		"otel":         tagOTel,
		"console":      tagConsole,
		"brotli":       tagBrotli,
		"zstd":         tagZstd,
		"importer":     tagImporter,
		"consul":       tagConsul,
		"kubernetes":   tagKubernetes,
	}
}

func diagnosticBuildProfile(capabilities map[string]bool) string {
	keys := []string{"waf", "stream_proxy", "wasm_plugins", "acme", "grpc", "http3", "otel", "console", "brotli", "zstd", "importer", "consul", "kubernetes"}
	anyEnabled := false
	allEnabled := true
	for _, key := range keys {
		enabled := capabilities[key]
		anyEnabled = anyEnabled || enabled
		allEnabled = allEnabled && enabled
	}
	if allEnabled {
		return "full"
	}
	if anyEnabled {
		return "custom"
	}
	return "lean"
}
