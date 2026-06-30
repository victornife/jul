package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"jul/internal/atomicfile"
	"jul/internal/config"
	"jul/internal/signals"
)

// stdout and stderr are package-level so subcommand handlers are testable: tests
// swap in buffers to capture output. Production code leaves them at the standard
// streams.
var (
	stdout io.Writer = os.Stdout
	stderr io.Writer = os.Stderr
)

// dispatchSubcommand routes the first CLI argument to a subcommand handler. It
// reports whether a subcommand was recognized along with its exit code; when no
// subcommand matches the caller falls back to the legacy flag-based behavior so
// existing invocations (jul, jul -config, jul -check, jul -version) are
// unchanged.
func dispatchSubcommand(args []string) (handled bool, code int) {
	if len(args) == 0 {
		return false, 0
	}
	switch args[0] {
	case "lint":
		return true, cmdLint(args[1:])
	case "fmt":
		return true, cmdFmt(args[1:])
	case "run":
		return true, cmdRun(args[1:])
	case "check":
		return true, cmdCheck(args[1:])
	case "import":
		return true, cmdImport(args[1:])
	default:
		return false, 0
	}
}

// usage prints help for the top-level command, including the additive
// subcommands, then the legacy flags.
func usage() {
	fmt.Fprint(stderr, `jul - an NGINX-inspired HTTP edge server

Usage:
  jul [flags]                          run the server (default)
  jul check [-config f] [-json] [-quiet]
                                       full runtime preflight check
  jul lint [-config f] [-strict] [-json] [-quiet]
                                       validate and report best-practice warnings
  jul fmt  [-config f] [-w]            rewrite the config in canonical TOML
  jul run  --serve <dir> | --proxy <target> [--listen addr]
                                       run a zero-config server (no file needed)
  jul import nginx [-o out.toml] [-strict] <nginx.conf>
                                       translate an NGINX config (needs -tags importer)

Flags:
`)
	flag.CommandLine.SetOutput(stderr)
	flag.PrintDefaults()
}

// lintOutput is the shape written by cmdLint when -json is used.
type lintOutput struct {
	Source   string              `json:"source"`
	Errors   []string            `json:"errors,omitempty"`
	Warnings []config.Diagnostic `json:"warnings,omitempty"`
}

// cmdLint parses and validates a config and reports best-practice warnings,
// surfacing every error and warning in a single pass. Exit codes: 0 = clean (no
// errors), 1 = validation error(s), 2 = warnings present under -strict.
func cmdLint(args []string) int {
	fs := flag.NewFlagSet("lint", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "server.toml", "path to the TOML configuration file")
	strict := fs.Bool("strict", false, "exit non-zero when warnings are present")
	jsonOut := fs.Bool("json", false, "emit findings as JSON")
	quiet := fs.Bool("quiet", false, "suppress warnings (errors still reported)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	src := config.NewTOMLSource(*configPath)
	cfg, err := src.Load()
	if err != nil {
		if *jsonOut {
			_ = json.NewEncoder(stdout).Encode(lintOutput{Source: src.Name(), Errors: []string{err.Error()}})
		} else {
			fmt.Fprintln(stderr, config.FormatError(err))
		}
		return 1
	}

	verrs := flattenErrors(config.Validate(cfg))
	warns := config.Lint(cfg)
	if *quiet {
		warns = nil
	}

	if *jsonOut {
		out := lintOutput{Source: src.Name()}
		for _, e := range verrs {
			out.Errors = append(out.Errors, e.Error())
		}
		out.Warnings = warns
		_ = json.NewEncoder(stdout).Encode(out)
	} else {
		color := wantColor(stdout)
		for _, e := range verrs {
			printDiagnostic(stdout, config.Diagnostic{Severity: config.SeverityError, Message: e.Error()}, color)
		}
		for _, d := range warns {
			printDiagnostic(stdout, d, color)
		}
		fmt.Fprintf(stdout, "\n%s: %d error(s), %d warning(s)\n", src.Name(), len(verrs), len(warns))
	}

	switch {
	case len(verrs) > 0:
		return 1
	case *strict && len(warns) > 0:
		return 2
	default:
		if !*jsonOut {
			fmt.Fprintf(stdout, "%s is valid\n", src.Name())
		}
		return 0
	}
}

// cmdFmt rewrites a config into canonical TOML. By default it prints to stdout;
// with -w it writes back to the file in place. Comments and original formatting
// are not preserved (see config.Marshal). Exit codes: 0 = ok, 1 = read/parse
// error.
func cmdFmt(args []string) int {
	fs := flag.NewFlagSet("fmt", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "server.toml", "path to the TOML configuration file")
	write := fs.Bool("w", false, "write the result back to the file instead of stdout")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	orig, err := os.ReadFile(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	cfg, err := config.Parse(orig)
	if err != nil {
		fmt.Fprintln(stderr, config.FormatError(err))
		return 1
	}
	out, err := config.Marshal(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	if !*write {
		_, _ = stdout.Write(out)
		return 0
	}
	if bytes.Equal(orig, out) {
		return 0
	}
	if err := atomicfile.Write(*configPath, out, 0o600); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	fmt.Fprintf(stderr, "formatted %s\n", *configPath)
	return 0
}

// cmdRun starts the server from a synthesized zero-config profile: --serve runs
// a static file server for a directory, --proxy reverse-proxies to a target. No
// config file is involved. Exactly one of --serve/--proxy is required.
func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	serveDir := fs.String("serve", "", "serve this directory of static files")
	proxyTarget := fs.String("proxy", "", "reverse-proxy all requests to this target (URL, host:port, or :port)")
	listen := fs.String("listen", config.DefaultZeroConfigListen, "address to listen on")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	var (
		cfg  *config.Config
		name string
	)
	switch {
	case *serveDir != "" && *proxyTarget != "":
		fmt.Fprintln(stderr, "error: --serve and --proxy are mutually exclusive")
		return 2
	case *serveDir != "":
		cfg = config.ServeDir(*serveDir, *listen)
		name = fmt.Sprintf("<zero-config: serve %s>", *serveDir)
	case *proxyTarget != "":
		cfg = config.ProxyTarget(*proxyTarget, *listen)
		name = fmt.Sprintf("<zero-config: proxy %s>", *proxyTarget)
	default:
		fmt.Fprintln(stderr, "error: provide --serve <dir> or --proxy <target>")
		fs.Usage()
		return 2
	}

	if err := config.Validate(cfg); err != nil {
		fmt.Fprintf(stderr, "error: synthesized configuration is invalid: %v\n", err)
		return 1
	}

	ctx, reloadSig, stop := signals.Listen(context.Background())
	defer stop()
	return serve(ctx, reloadSig, memorySource{name: name, cfg: cfg}, cfg)
}

// memorySource adapts a synthesized Config to the config.Source interface for
// the zero-config run path. Reloads re-serve the same in-memory config; file
// watching is skipped because there is no file (see serve).
type memorySource struct {
	name string
	cfg  *config.Config
}

func (m memorySource) Load() (*config.Config, error) { return m.cfg, nil }
func (m memorySource) Name() string                  { return m.name }

// flattenErrors expands an errors.Join tree into a flat slice so each problem
// prints on its own line.
func flattenErrors(err error) []error {
	if err == nil {
		return nil
	}
	if u, ok := err.(interface{ Unwrap() []error }); ok {
		var out []error
		for _, e := range u.Unwrap() {
			out = append(out, flattenErrors(e)...)
		}
		return out
	}
	return []error{err}
}

// printDiagnostic writes one finding, optionally colorized, with an indented
// hint line when present.
func printDiagnostic(w io.Writer, d config.Diagnostic, color bool) {
	label := d.Severity.String()
	if color {
		c := "\x1b[33m" // yellow for warnings
		if d.Severity == config.SeverityError {
			c = "\x1b[31m" // red for errors
		}
		label = c + label + "\x1b[0m"
	}
	if d.Field != "" {
		fmt.Fprintf(w, "%s: %s: %s\n", label, d.Field, d.Message)
	} else {
		fmt.Fprintf(w, "%s: %s\n", label, d.Message)
	}
	if d.Hint != "" {
		fmt.Fprintf(w, "    hint: %s\n", d.Hint)
	}
}

// wantColor reports whether ANSI color should be used for w: only when it is a
// terminal (character device) and NO_COLOR is unset.
func wantColor(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// cmdCheck performs a full runtime preflight of the configuration. It validates
// structurally *and* dry-runs every component that could fail during serve/reload
// (WAF compilation, auth initialisation, compression encoder availability, etc.).
// Exit codes: 0 = ok, 1 = validation/runtime error.
func cmdCheck(args []string) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "server.toml", "path to the TOML configuration file")
	jsonOut := fs.Bool("json", false, "emit result as JSON")
	quiet := fs.Bool("quiet", false, "suppress non-error output")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	src := config.NewTOMLSource(*configPath)
	cfg, err := src.Load()
	if err != nil {
		if *jsonOut {
			_ = json.NewEncoder(stdout).Encode(map[string]any{"source": src.Name(), "ok": false, "error": err.Error()})
		} else {
			fmt.Fprintln(stderr, config.FormatError(err))
		}
		return 1
	}

	if errs := flattenErrors(config.Validate(cfg)); len(errs) > 0 {
		if *jsonOut {
			_ = json.NewEncoder(stdout).Encode(map[string]any{"source": src.Name(), "ok": false, "errors": errs})
		} else {
			for _, e := range errs {
				fmt.Fprintln(stderr, e)
			}
			fmt.Fprintf(stderr, "%s: %d error(s)\n", src.Name(), len(errs))
		}
		return 1
	}
	if err := validateRuntimeConfig(cfg); err != nil {
		if *jsonOut {
			_ = json.NewEncoder(stdout).Encode(map[string]any{"source": src.Name(), "ok": false, "error": err.Error()})
		} else {
			fmt.Fprintf(stderr, "runtime check: %v\n", err)
		}
		return 1
	}
	if *jsonOut {
		_ = json.NewEncoder(stdout).Encode(map[string]any{"source": src.Name(), "ok": true})
	} else if !*quiet {
		fmt.Fprintf(stdout, "%s is valid (structural + runtime)\n", src.Name())
	}
	return 0
}

// cmdFmt rewrites a config into canonical TOML.
