// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"jul/internal/adminapi"
	"jul/internal/adminclient"
)

type remoteCommon struct {
	endpoint   string
	profile    string
	tokenFile  string
	caFile     string
	clientCert string
	clientKey  string
	timeout    time.Duration
	json       bool
	verbose    bool
}

func (c *remoteCommon) bind(fs *flag.FlagSet) {
	fs.StringVar(&c.endpoint, "endpoint", "", "remote Jul admin endpoint (or JUL_ENDPOINT)")
	fs.StringVar(&c.profile, "profile", "", "named profile from the Jul CLI profile file")
	fs.StringVar(&c.tokenFile, "token-file", "", "bearer token file; use - to read from stdin")
	fs.StringVar(&c.caFile, "ca-file", "", "additional PEM CA bundle")
	fs.StringVar(&c.clientCert, "client-cert", "", "mTLS client certificate file")
	fs.StringVar(&c.clientKey, "client-key", "", "mTLS client private-key file")
	fs.DurationVar(&c.timeout, "timeout", adminclient.DefaultTimeout, "ordinary HTTP request timeout")
	fs.BoolVar(&c.json, "json", false, "emit exactly one machine-readable JSON object")
	fs.BoolVar(&c.verbose, "verbose", false, "show bounded request/phase diagnostics (never credentials or bodies)")
}

func (c remoteCommon) client() (*adminclient.Client, []string, error) {
	cfg, warnings, err := adminclient.ResolveConnection(adminclient.ResolveOptions{
		Endpoint: c.endpoint, ProfileName: c.profile, TokenFile: c.tokenFile,
		CAFile: c.caFile, ClientCertFile: c.clientCert, ClientKeyFile: c.clientKey,
		Timeout: c.timeout, Stdin: os.Stdin,
	})
	if err != nil {
		return nil, warnings, err
	}
	client, err := adminclient.New(cfg)
	return client, warnings, err
}

func dispatchRemoteSubcommand(args []string) (handled bool, code int) {
	if len(args) == 0 {
		return false, 0
	}
	switch args[0] {
	case "plan":
		return true, cmdRemotePlan(args[1:])
	case "diff":
		return true, cmdRemoteDiff(args[1:])
	case "apply":
		return true, cmdRemoteApply(args[1:])
	case "stage":
		return true, cmdRemoteStage(args[1:])
	case "status":
		return true, cmdRemoteStatus(args[1:])
	case "rollback":
		return true, cmdRemoteRollback(args[1:])
	case "export":
		return true, cmdRemoteExport(args[1:])
	case "diagnostics":
		return true, cmdRemoteDiagnostics(args[1:])
	default:
		return false, 0
	}
}

func remoteContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

func parseRemote(fs *flag.FlagSet, args []string, common *remoteCommon) (*adminclient.Client, int, bool) {
	jsonRequested := false
	for _, arg := range args {
		if arg == "--json" || arg == "-json" {
			jsonRequested = true
			break
		}
	}
	if jsonRequested {
		fs.SetOutput(io.Discard)
	} else {
		fs.SetOutput(stderr)
	}
	common.bind(fs)
	if err := fs.Parse(args); err != nil {
		if jsonRequested {
			return nil, renderUsageError(true, fs.Name(), err.Error()), false
		}
		return nil, 2, false
	}
	client, warnings, err := common.client()
	if err != nil {
		return nil, renderRemoteError(common.json, fs.Name(), err, ""), false
	}
	if !common.json {
		for _, w := range warnings {
			fmt.Fprintln(stderr, w)
		}
	}
	return client, 0, true
}

func readCandidate(filename string) ([]byte, error) {
	if filename == "" {
		return nil, errors.New("--config is required (use - for stdin)")
	}
	if filename == "-" {
		return io.ReadAll(io.LimitReader(os.Stdin, 2<<20))
	}
	return os.ReadFile(filename)
}

func cmdRemotePlan(args []string) int {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	var common remoteCommon
	configFile := fs.String("config", "", "candidate TOML file; - reads stdin")
	base := fs.String("base-version", "", "optional reviewed canonical base version")
	client, code, ok := parseRemote(fs, args, &common)
	if !ok {
		return code
	}
	if fs.NArg() != 0 {
		return renderUsageError(common.json, "plan", "unexpected positional arguments")
	}
	candidate, err := readCandidate(*configFile)
	if err != nil {
		return renderUsageError(common.json, "plan", err.Error())
	}
	ctx, cancel := remoteContext()
	defer cancel()
	r, err := client.Plan(ctx, candidate, *base)
	if err != nil {
		return renderRemoteError(common.json, "plan", err, r.RequestID)
	}
	return renderRawSuccess(common, "plan", r, "candidate assessed by remote server")
}

func cmdRemoteDiff(args []string) int {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	var common remoteCommon
	configFile := fs.String("config", "", "candidate TOML file; - reads stdin")
	historyID := fs.String("history-id", "", "historical revision to diff against current persisted config")
	base := fs.String("base-version", "", "optional base version for candidate assessment")
	client, code, ok := parseRemote(fs, args, &common)
	if !ok {
		return code
	}
	if (*configFile == "") == (*historyID == "") {
		return renderUsageError(common.json, "diff", "exactly one of --config or --history-id is required")
	}
	ctx, cancel := remoteContext()
	defer cancel()
	var r adminclient.RawResponse
	var err error
	if *historyID != "" {
		r, err = client.HistoryDiff(ctx, *historyID)
	} else {
		var candidate []byte
		candidate, err = readCandidate(*configFile)
		if err == nil {
			r, err = client.Plan(ctx, candidate, *base)
		}
	}
	if err != nil {
		return renderRemoteError(common.json, "diff", err, r.RequestID)
	}
	return renderRawSuccess(common, "diff", r, "diff calculated by remote server")
}

func cmdRemoteStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	var common remoteCommon
	applyID := fs.String("apply-id", "", "show one managed transaction by apply id")
	client, code, ok := parseRemote(fs, args, &common)
	if !ok {
		return code
	}
	if fs.NArg() != 0 {
		return renderUsageError(common.json, "status", "unexpected positional arguments")
	}
	ctx, cancel := remoteContext()
	defer cancel()
	if *applyID != "" {
		v, r, err := client.ApplyResult(ctx, *applyID)
		if err != nil {
			return renderRemoteError(common.json, "status", err, r.RequestID)
		}
		if common.json {
			return renderJSONMap("status", true, r.Body, nil)
		}
		fmt.Fprintf(stdout, "apply %s: state=%s terminal=%t outcome=%s\n", v.ApplyID, v.State, v.Terminal, emptyDash(v.Outcome))
		fmt.Fprintf(stdout, "serving=%s persisted=%s boot_id=%s\n", emptyDash(v.ServingVersion), emptyDash(v.PersistedVersion), emptyDash(v.BootID))
		if len(v.Degraded) != 0 {
			fmt.Fprintf(stdout, "degraded: %d condition(s)\n", len(v.Degraded))
		}
		return 0
	}
	v, r, err := client.Status(ctx)
	if err != nil {
		return renderRemoteError(common.json, "status", err, r.RequestID)
	}
	if common.json {
		return renderJSONMap("status", true, r.Body, nil)
	}
	fmt.Fprintf(stdout, "authority: %s (%s)\n", v.ConfigAuthority, v.ConfigAuthoritySource)
	fmt.Fprintf(stdout, "config state: %s\n", emptyDash(v.ConfigState))
	fmt.Fprintf(stdout, "serving: %s\npersisted: %s\n", emptyDash(v.ServingVersion), emptyDash(v.PersistedVersion))
	fmt.Fprintf(stdout, "drift: %t\npending restart: %t\nboot_id: %s\n", v.Drift.Detected, v.PendingRestart.Pending, v.BootID)
	if v.LastApply != nil {
		fmt.Fprintf(stdout, "last apply: %s state=%s outcome=%s\n", v.LastApply.ApplyID, v.LastApply.State, emptyDash(v.LastApply.Outcome))
	}
	return 0
}

func cmdRemoteApply(args []string) int { return cmdRemoteMutation(args, "apply", "hot") }
func cmdRemoteStage(args []string) int { return cmdRemoteMutation(args, "stage", "stage_restart") }

func cmdRemoteMutation(args []string, command, fixedMode string) int {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	var common remoteCommon
	configFile := fs.String("config", "", "candidate TOML file; - reads stdin")
	base := fs.String("base-version", "", "reviewed canonical base version (required)")
	idem := fs.String("idempotency-key", "", "stable 8-128 byte [A-Za-z0-9_-] automation key")
	confirmAdmin := fs.Bool("confirm-admin", false, "explicitly confirm a control-plane reachability change")
	adoptExternal := fs.Bool("adopt-external", false, "preview and adopt the current external configuration (apply only)")
	adoptMode := fs.String("adopt-mode", "hot", "adoption mode: hot or stage_restart")
	pollTimeout := fs.Duration("poll-timeout", adminclient.DefaultPollTimeout, "maximum local wait for a terminal transaction")
	client, code, ok := parseRemote(fs, args, &common)
	if !ok {
		return code
	}
	if fs.NArg() != 0 {
		return renderUsageError(common.json, command, "unexpected positional arguments")
	}
	if *pollTimeout <= 0 {
		return renderUsageError(common.json, command, "--poll-timeout must be positive")
	}
	if *adoptExternal {
		if command != "apply" {
			return renderUsageError(common.json, command, "--adopt-external is available only under jul apply")
		}
		if *configFile != "" {
			return renderUsageError(common.json, command, "--config cannot be used with --adopt-external")
		}
		return runAdoption(client, common, *base, *idem, *adoptMode, *pollTimeout)
	}
	if *base == "" {
		return renderUsageError(common.json, command, "--base-version is required")
	}
	candidate, err := readCandidate(*configFile)
	if err != nil {
		return renderUsageError(common.json, command, err.Error())
	}
	prepared, err := client.PrepareApply(candidate, *base, fixedMode, *idem, *confirmAdmin)
	if err != nil {
		return renderUsageError(common.json, command, err.Error())
	}
	return executeMutation(client, common, command, prepared, *pollTimeout)
}

func runAdoption(client *adminclient.Client, common remoteCommon, base, idem, mode string, pollTimeout time.Duration) int {
	if mode != "hot" && mode != "stage_restart" {
		return renderUsageError(common.json, "apply", "--adopt-mode must be hot or stage_restart")
	}
	ctx, cancel := remoteContext()
	defer cancel()
	previewReq := adminclient.AdoptRequest{BaseVersion: base, Mode: mode}
	preview, err := client.AdoptPreview(ctx, previewReq)
	if err != nil {
		return renderRemoteError(common.json, "apply", err, preview.RequestID)
	}
	var p map[string]any
	if err := json.Unmarshal(preview.Body, &p); err != nil {
		return renderRemoteError(common.json, "apply", err, preview.RequestID)
	}
	observed, _ := p["observed_digest"].(string)
	previewBase, _ := p["base_version"].(string)
	if previewBase == "" {
		previewBase = base
	}
	if previewBase == "" || observed == "" {
		return renderUsageError(common.json, "apply", "adoption preview did not return base_version and observed_digest")
	}
	if base != "" && previewBase != base {
		return renderUsageError(common.json, "apply", "adoption preview base_version differs from --base-version; re-review before applying")
	}
	if !common.json {
		fmt.Fprintf(stdout, "adoption preview: base=%s restart_required=%v\n", previewBase, p["restart_required"])
	}
	prepared, err := client.PrepareAdopt(adminclient.AdoptRequest{BaseVersion: previewBase, ObservedDigest: observed, Mode: mode, Confirm: true}, idem)
	if err != nil {
		return renderUsageError(common.json, "apply", err.Error())
	}
	return executeMutationWithContext(ctx, client, common, "apply", prepared, pollTimeout)
}

func cmdRemoteRollback(args []string) int {
	fs := flag.NewFlagSet("rollback", flag.ContinueOnError)
	var common remoteCommon
	historyID := fs.String("history-id", "", "history revision to restore")
	base := fs.String("base-version", "", "reviewed canonical base version; must match preview when supplied")
	idem := fs.String("idempotency-key", "", "stable 8-128 byte [A-Za-z0-9_-] automation key")
	confirmAdmin := fs.Bool("confirm-admin", false, "explicitly confirm a control-plane reachability change")
	pollTimeout := fs.Duration("poll-timeout", adminclient.DefaultPollTimeout, "maximum local wait for a terminal transaction")
	client, code, ok := parseRemote(fs, args, &common)
	if !ok {
		return code
	}
	if *historyID == "" {
		return renderUsageError(common.json, "rollback", "--history-id is required")
	}
	ctx, cancel := remoteContext()
	defer cancel()
	preview, err := client.HistoryDiff(ctx, *historyID)
	if err != nil {
		return renderRemoteError(common.json, "rollback", err, preview.RequestID)
	}
	var p map[string]any
	if err := json.Unmarshal(preview.Body, &p); err != nil {
		return renderRemoteError(common.json, "rollback", err, preview.RequestID)
	}
	previewBase, _ := p["base_version"].(string)
	if previewBase == "" {
		return renderUsageError(common.json, "rollback", "rollback preview did not return base_version")
	}
	if *base != "" && *base != previewBase {
		return renderUsageError(common.json, "rollback", "preview base_version differs from --base-version; re-review before rollback")
	}
	if !common.json {
		fmt.Fprintf(stdout, "rollback preview for %s against base %s\n", *historyID, previewBase)
	}
	prepared, err := client.PrepareRollback(*historyID, previewBase, *idem, *confirmAdmin)
	if err != nil {
		return renderUsageError(common.json, "rollback", err.Error())
	}
	return executeMutationWithContext(ctx, client, common, "rollback", prepared, *pollTimeout)
}

func executeMutation(client *adminclient.Client, common remoteCommon, command string, prepared adminclient.PreparedMutation, pollTimeout time.Duration) int {
	ctx, cancel := remoteContext()
	defer cancel()
	return executeMutationWithContext(ctx, client, common, command, prepared, pollTimeout)
}

func executeMutationWithContext(ctx context.Context, client *adminclient.Client, common remoteCommon, command string, prepared adminclient.PreparedMutation, pollTimeout time.Duration) int {
	status, _, err := client.Status(ctx)
	if err != nil {
		return renderRemoteError(common.json, command, err, "")
	}
	if common.verbose && !common.json {
		fmt.Fprintf(stderr, "operation=%s phase=submit boot_id=%s\n", prepared.Operation.ID, status.BootID)
	}
	v, r, err := client.Mutate(ctx, prepared, status.BootID)
	if err != nil {
		return renderRemoteError(common.json, command, err, r.RequestID)
	}
	if !v.Terminal {
		if v.ApplyID == "" {
			return renderRemoteError(common.json, command, &adminclient.TransportError{Phase: "decode", Err: errors.New("non-terminal response omitted apply_id")}, r.RequestID)
		}
		if common.verbose && !common.json {
			fmt.Fprintf(stderr, "operation=%s phase=poll apply_id=%s\n", prepared.Operation.ID, v.ApplyID)
		}
		terminal, pollRaw, pollErr := client.Poll(ctx, v.ApplyID, status.BootID, pollTimeout)
		if pollErr != nil {
			if !common.json {
				fmt.Fprintf(stderr, "local wait ended; server transaction was not cancelled; inspect with: jul status --apply-id %s --endpoint <endpoint>\n", v.ApplyID)
			}
			return renderMutationWaitError(common.json, command, pollErr, v.ApplyID, prepared.IdempotencyKey, pollRaw.RequestID)
		}
		return renderTerminal(common, command, terminal.Outcome, terminal.Restored, terminal.RestoreError, terminal.Degraded, terminal.ApplyID, prepared.IdempotencyKey, terminal.BootID, pollRaw.Body)
	}
	return renderTerminal(common, command, v.Outcome, v.Restored, v.RestoreError, v.Degraded, v.ApplyID, prepared.IdempotencyKey, v.BootID, r.Body)
}

func cmdRemoteExport(args []string) int {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	var common remoteCommon
	client, code, ok := parseRemote(fs, args, &common)
	if !ok {
		return code
	}
	if fs.NArg() != 0 {
		return renderUsageError(common.json, "export", "unexpected positional arguments")
	}
	ctx, cancel := remoteContext()
	defer cancel()
	r, err := client.Export(ctx)
	if err != nil {
		return renderRemoteError(common.json, "export", err, r.RequestID)
	}
	if common.json {
		return renderJSONMap("export", true, r.Body, nil)
	}
	_, _ = stdout.Write(r.Body)
	if len(r.Body) == 0 || r.Body[len(r.Body)-1] != '\n' {
		fmt.Fprintln(stdout)
	}
	return 0
}

func cmdRemoteDiagnostics(args []string) int {
	fs := flag.NewFlagSet("diagnostics", flag.ContinueOnError)
	var common remoteCommon
	client, code, ok := parseRemote(fs, args, &common)
	if !ok {
		return code
	}
	if fs.NArg() != 0 {
		return renderUsageError(common.json, "diagnostics", "unexpected positional arguments")
	}
	ctx, cancel := remoteContext()
	defer cancel()
	status, sr, err := client.Status(ctx)
	if err != nil {
		return renderRemoteError(common.json, "diagnostics", err, sr.RequestID)
	}
	caps, cr, err := client.Capabilities(ctx)
	if err != nil {
		return renderRemoteError(common.json, "diagnostics", err, cr.RequestID)
	}
	if common.json {
		obj := map[string]any{"command": "diagnostics", "ok": true, "scope": "status_and_capabilities", "status": status, "capabilities": caps, "error": nil}
		return writeJSON(obj, 0)
	}
	fmt.Fprintln(stdout, "remote diagnostics (v1 reduced scope: status + capabilities; no support-bundle endpoint)")
	fmt.Fprintf(stdout, "authority: %s (%s), config_state=%s, drift=%t, pending_restart=%t\n", status.ConfigAuthority, status.ConfigAuthoritySource, emptyDash(status.ConfigState), status.Drift.Detected, status.PendingRestart.Pending)
	fmt.Fprintf(stdout, "api=%s schema=%d boot_id=%s external_endpoints=%d\n", caps.APIVersion, caps.ConfigSchemaVersion, caps.BootID, len(caps.Endpoints))
	return 0
}

func renderTerminal(common remoteCommon, command, outcome string, restored bool, restoreError string, degraded []adminapi.Degradation, applyID, idemKey, bootID string, raw []byte) int {
	exit, terminal := adminclient.OutcomeExit(outcome, restored, restoreError, degraded)
	if !terminal {
		return renderRemoteError(common.json, command, &adminclient.TransportError{Phase: "decode", Err: fmt.Errorf("unknown or non-terminal outcome %q", outcome)}, "")
	}
	if common.json {
		var obj map[string]any
		_ = json.Unmarshal(raw, &obj)
		if obj == nil {
			obj = map[string]any{}
		}
		obj["command"] = command
		obj["ok"] = exit == 0 || exit == 3 || exit == 4
		obj["idempotency_key"] = idemKey
		obj["error"] = nil
		return writeJSON(obj, exit)
	}
	fmt.Fprintf(stdout, "%s: outcome=%s apply_id=%s\n", command, outcome, emptyDash(applyID))
	if len(degraded) != 0 {
		fmt.Fprintf(stdout, "degraded: %d condition(s); inspect JSON/status before continuing\n", len(degraded))
	}
	if exit == 3 {
		fmt.Fprintln(stdout, "configuration is not yet serving; restart/convergence is required")
	}
	if bootID != "" && common.verbose {
		fmt.Fprintf(stderr, "operation=%s phase=terminal boot_id=%s\n", command, bootID)
	}
	return exit
}

func renderRawSuccess(common remoteCommon, command string, r adminclient.RawResponse, summary string) int {
	if common.json {
		return renderJSONMap(command, true, r.Body, nil)
	}
	fmt.Fprintln(stdout, summary)
	var obj map[string]any
	if json.Unmarshal(r.Body, &obj) == nil {
		for _, key := range []string{"base_version", "current_version", "valid", "restart_required", "config_state", "outcome"} {
			if value, ok := obj[key]; ok {
				fmt.Fprintf(stdout, "%s: %v\n", strings.ReplaceAll(key, "_", " "), value)
			}
		}
		if diff, ok := obj["diff"].(map[string]any); ok {
			if changes, ok := diff["changes"].([]any); ok {
				fmt.Fprintf(stdout, "changes: %d\n", len(changes))
			}
		}
		if findings, ok := obj["validation_errors"].([]any); ok && len(findings) != 0 {
			fmt.Fprintf(stdout, "validation findings: %d\n", len(findings))
		}
	}
	if common.verbose && r.RequestID != "" {
		fmt.Fprintf(stderr, "operation=%s request_id=%s\n", command, r.RequestID)
	}
	return 0
}

func renderJSONMap(command string, ok bool, raw []byte, errObj any) int {
	var obj map[string]any
	if len(raw) != 0 {
		_ = json.Unmarshal(raw, &obj)
	}
	if obj == nil {
		obj = map[string]any{}
	}
	obj["command"] = command
	obj["ok"] = ok
	obj["error"] = errObj
	return writeJSON(obj, 0)
}

func renderUsageError(jsonMode bool, command, message string) int {
	if jsonMode {
		return writeJSON(map[string]any{"command": command, "ok": false, "outcome": nil, "error": map[string]any{"code": "invalid_request", "message": message}}, 2)
	}
	fmt.Fprintf(stderr, "error: %s\n", message)
	return 2
}

func renderMutationWaitError(jsonMode bool, command string, err error, applyID, idemKey, requestID string) int {
	if jsonMode {
		return writeJSON(map[string]any{"command": command, "ok": false, "outcome": nil, "apply_id": applyID, "idempotency_key": idemKey, "error": localErrorObject(err, requestID)}, localErrorExit(err))
	}
	return renderRemoteError(false, command, err, requestID)
}

func renderRemoteError(jsonMode bool, command string, err error, requestID string) int {
	exit := localErrorExit(err)
	if jsonMode {
		return writeJSON(map[string]any{"command": command, "ok": false, "outcome": nil, "error": localErrorObject(err, requestID)}, exit)
	}
	var apiErr *adminclient.APIError
	if errors.As(err, &apiErr) {
		fmt.Fprintf(stderr, "%s: %s\n", apiErr.Envelope.Error.Code, apiErr.Envelope.Error.Message)
		if apiErr.Envelope.Error.RequestID != "" {
			fmt.Fprintf(stderr, "request_id: %s\n", apiErr.Envelope.Error.RequestID)
		}
		return exit
	}
	fmt.Fprintf(stderr, "error: %v\n", err)
	return exit
}

func localErrorExit(err error) int {
	var apiErr *adminclient.APIError
	if errors.As(err, &apiErr) {
		if exit, ok := adminclient.ErrorExit(apiErr.Envelope.Error.Code); ok {
			return exit
		}
		return 9
	}
	var boot *adminclient.BootChangedError
	if errors.As(err, &boot) {
		return 5
	}
	var transport *adminclient.TransportError
	if errors.As(err, &transport) {
		if transport.Phase == "decode" {
			return 9
		}
		return 8
	}
	return 2
}

func localErrorObject(err error, requestID string) any {
	var apiErr *adminclient.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Envelope.Error
	}
	var boot *adminclient.BootChangedError
	if errors.As(err, &boot) {
		return map[string]any{"code": "boot_changed", "message": boot.Error(), "apply_id": boot.ApplyID, "previous_boot_id": boot.Previous, "current_boot_id": boot.Current}
	}
	var transport *adminclient.TransportError
	if errors.As(err, &transport) {
		return map[string]any{"code": "transport_error", "message": "remote operation failed", "phase": transport.Phase, "request_id": requestID}
	}
	return map[string]any{"code": "invalid_request", "message": err.Error()}
}

func writeJSON(v any, exit int) int {
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return exit
}
func emptyDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}
