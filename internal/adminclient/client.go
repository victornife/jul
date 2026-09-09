// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package adminclient is the thin remote automation transport for Jul's
// supported /api/v1 control-plane contract. It deliberately owns transport,
// credentials, retries and polling only; configuration semantics stay server-side.
package adminclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"jul/internal/adminapi"
)

const (
	DefaultTimeout      = 15 * time.Second
	DefaultPollInterval = 250 * time.Millisecond
	DefaultPollTimeout  = 2 * time.Minute
	maxResponseBytes    = 4 << 20
)

// Operation is one stable external operation consumed by AUTO-04.
type Operation struct {
	ID          string
	Method      string
	Path        string
	ContentType string
	Mutation    bool
}

var operations = []Operation{
	{ID: "getStatus", Method: http.MethodGet, Path: "/api/v1/status"},
	{ID: "getCapabilities", Method: http.MethodGet, Path: "/api/v1/capabilities"},
	{ID: "planConfig", Method: http.MethodPost, Path: "/api/v1/config/plan", ContentType: "application/toml"},
	{ID: "getApplyResult", Method: http.MethodGet, Path: "/api/v1/config/applies/{apply_id}"},
	{ID: "listConfigHistory", Method: http.MethodGet, Path: "/api/v1/config/history"},
	{ID: "diffHistoryRevision", Method: http.MethodGet, Path: "/api/v1/config/history/{id}/diff"},
	{ID: "exportConfig", Method: http.MethodGet, Path: "/api/v1/config/export"},
	{ID: "applyConfig", Method: http.MethodPost, Path: "/api/v1/config/apply", ContentType: "application/toml", Mutation: true},
	{ID: "rollbackConfig", Method: http.MethodPost, Path: "/api/v1/config/rollback", ContentType: "application/json", Mutation: true},
	{ID: "previewAdoptExternal", Method: http.MethodPost, Path: "/api/v1/config/adopt-external/preview", ContentType: "application/json"},
	{ID: "adoptExternal", Method: http.MethodPost, Path: "/api/v1/config/adopt-external", ContentType: "application/json", Mutation: true},
}

// Operations returns a defensive copy for the OpenAPI drift guard.
func Operations() []Operation {
	out := make([]Operation, len(operations))
	copy(out, operations)
	return out
}

func operation(id string) Operation {
	for _, op := range operations {
		if op.ID == id {
			return op
		}
	}
	panic("unknown adminclient operation: " + id)
}

// Config is the already-resolved connection configuration.
type Config struct {
	Endpoint       string
	Token          string
	CAFile         string
	ClientCertFile string
	ClientKeyFile  string
	Timeout        time.Duration
}

// Client is safe for concurrent use after construction.
type Client struct {
	base        *url.URL
	token       string
	http        *http.Client
	timeout     time.Duration
	pollEvery   time.Duration
	pollTimeout time.Duration
}

// APIError preserves the stable server error envelope exactly.
type APIError struct {
	Status   int
	Envelope adminapi.Envelope
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s: %s", e.Envelope.Error.Code, e.Envelope.Error.Message)
}

// TransportError is a local failure with no authoritative API envelope.
type TransportError struct {
	Phase string `json:"phase"`
	Err   error  `json:"-"`
}

func (e *TransportError) Error() string {
	if e == nil || e.Err == nil {
		return "transport failure"
	}
	return fmt.Sprintf("%s: %v", e.Phase, e.Err)
}
func (e *TransportError) Unwrap() error { return e.Err }

// BootChangedError reports that a mutation retry/poll crossed the server's
// boot-scoped idempotency/ledger boundary. The prior outcome is ambiguous.
type BootChangedError struct {
	Previous string
	Current  string
	ApplyID  string
}

func (e *BootChangedError) Error() string {
	return "server boot identity changed; re-read authoritative state before retrying"
}

// New constructs a secure client. Plain HTTP is accepted only for loopback.
func New(cfg Config) (*Client, error) {
	base, err := ParseEndpoint(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}

	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read CA file: %w", err)
		}
		roots, err := x509.SystemCertPool()
		if err != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("CA file contains no usable certificates")
		}
		tlsConfig.RootCAs = roots
	}
	if (cfg.ClientCertFile == "") != (cfg.ClientKeyFile == "") {
		return nil, errors.New("client certificate and key must be supplied together")
	}
	if cfg.ClientCertFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.ClientCertFile, cfg.ClientKeyFile)
		if err != nil {
			return nil, fmt.Errorf("load client certificate: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig
	hc := &http.Client{
		Transport: transport,
		Timeout:   cfg.Timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			// Control-plane redirects are never followed. This prevents bearer
			// forwarding across origins and HTTPS -> HTTP downgrade by construction.
			return http.ErrUseLastResponse
		},
	}
	return &Client{
		base: base, token: strings.TrimSpace(cfg.Token), http: hc, timeout: cfg.Timeout,
		pollEvery: DefaultPollInterval, pollTimeout: DefaultPollTimeout,
	}, nil
}

// ParseEndpoint validates the public endpoint boundary without echoing URL
// credentials back through errors.
func ParseEndpoint(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("remote endpoint is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("invalid remote endpoint")
	}
	if u.User != nil {
		return nil, errors.New("remote endpoint must not contain credentials")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, errors.New("remote endpoint scheme must be https or loopback http")
	}
	if u.Host == "" {
		return nil, errors.New("remote endpoint must include a host")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("remote endpoint must not include query or fragment")
	}
	if u.Path != "" && u.Path != "/" {
		return nil, errors.New("remote endpoint must not include a path")
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return nil, errors.New("plaintext remote administration is prohibited; use HTTPS")
	}
	u.Path = ""
	return u, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// NewIdempotencyKey returns a URL/header-safe 32-character random key.
func NewIdempotencyKey() (string, error) {
	var b [24]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// ValidIdempotencyKey implements the published 8-128 byte [A-Za-z0-9_-] grammar.
func ValidIdempotencyKey(s string) bool {
	if len(s) < 8 || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

// RawResponse retains exact server bytes so machine mode can project stable API
// shapes without exposing Go internals.
type RawResponse struct {
	Status    int
	RequestID string
	Body      []byte
}

func (c *Client) endpoint(op Operation, params map[string]string, query url.Values) string {
	u := *c.base
	p := op.Path
	for k, v := range params {
		p = strings.ReplaceAll(p, "{"+k+"}", url.PathEscape(v))
	}
	u.Path = path.Join(u.Path, p)
	// path.Join removes a leading double slash but preserves the required root.
	if !strings.HasPrefix(u.Path, "/") {
		u.Path = "/" + u.Path
	}
	u.RawQuery = query.Encode()
	return u.String()
}

func (c *Client) do(ctx context.Context, op Operation, params map[string]string, query url.Values, body []byte, contentType, idemKey string) (RawResponse, error) {
	var zero RawResponse
	req, err := http.NewRequestWithContext(ctx, op.Method, c.endpoint(op, params, query), bytes.NewReader(body))
	if err != nil {
		return zero, &TransportError{Phase: "request", Err: err}
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return zero, &TransportError{Phase: "connect", Err: err}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return zero, &TransportError{Phase: "response", Err: err}
	}
	if len(data) > maxResponseBytes {
		return zero, &TransportError{Phase: "response", Err: errors.New("response exceeds client safety limit")}
	}
	out := RawResponse{Status: resp.StatusCode, RequestID: resp.Header.Get("X-Request-ID"), Body: data}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return out, &TransportError{Phase: "redirect", Err: fmt.Errorf("unexpected HTTP redirect (%d)", resp.StatusCode)}
	}
	if resp.StatusCode >= 400 {
		var env adminapi.Envelope
		if err := json.Unmarshal(data, &env); err != nil || env.Error.Code == "" {
			return out, &TransportError{Phase: "decode", Err: errors.New("server returned a non-contract error response")}
		}
		return out, &APIError{Status: resp.StatusCode, Envelope: env}
	}
	return out, nil
}

func decode[T any](r RawResponse) (T, error) {
	var out T
	if err := json.Unmarshal(r.Body, &out); err != nil {
		return out, &TransportError{Phase: "decode", Err: err}
	}
	return out, nil
}

func (c *Client) Status(ctx context.Context) (adminapi.StatusResponse, RawResponse, error) {
	r, err := c.do(ctx, operation("getStatus"), nil, nil, nil, "", "")
	if err != nil {
		return adminapi.StatusResponse{}, r, err
	}
	v, err := decode[adminapi.StatusResponse](r)
	return v, r, err
}

func (c *Client) Capabilities(ctx context.Context) (adminapi.CapabilitiesResponse, RawResponse, error) {
	r, err := c.do(ctx, operation("getCapabilities"), nil, nil, nil, "", "")
	if err != nil {
		return adminapi.CapabilitiesResponse{}, r, err
	}
	v, err := decode[adminapi.CapabilitiesResponse](r)
	return v, r, err
}

// Plan sends candidate bytes verbatim. Validation/diff/lifecycle remain server-authoritative.
func (c *Client) Plan(ctx context.Context, candidate []byte, baseVersion string) (RawResponse, error) {
	q := make(url.Values)
	if strings.TrimSpace(baseVersion) != "" {
		q.Set("base_version", baseVersion)
	}
	return c.do(ctx, operation("planConfig"), nil, q, candidate, "application/toml", "")
}

func (c *Client) HistoryDiff(ctx context.Context, id string) (RawResponse, error) {
	return c.do(ctx, operation("diffHistoryRevision"), map[string]string{"id": id}, nil, nil, "", "")
}

func (c *Client) History(ctx context.Context, limit int, cursor string) (adminapi.HistoryListResponse, RawResponse, error) {
	q := make(url.Values)
	if limit > 0 {
		q.Set("limit", fmt.Sprint(limit))
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	r, err := c.do(ctx, operation("listConfigHistory"), nil, q, nil, "", "")
	if err != nil {
		return adminapi.HistoryListResponse{}, r, err
	}
	v, err := decode[adminapi.HistoryListResponse](r)
	return v, r, err
}

func (c *Client) Export(ctx context.Context) (RawResponse, error) {
	return c.do(ctx, operation("exportConfig"), nil, nil, nil, "", "")
}

// PreparedMutation freezes every fingerprint-relevant request component before
// the first send. Retry sends the same body slice, query and headers verbatim.
type PreparedMutation struct {
	Operation      Operation
	Params         map[string]string
	Query          url.Values
	Body           []byte
	ContentType    string
	IdempotencyKey string
}

func (c *Client) PrepareApply(candidate []byte, baseVersion, mode, idemKey string, confirmAdmin bool) (PreparedMutation, error) {
	if strings.TrimSpace(baseVersion) == "" {
		return PreparedMutation{}, errors.New("base_version is required")
	}
	if mode != "hot" && mode != "stage_restart" {
		return PreparedMutation{}, errors.New("mode must be hot or stage_restart")
	}
	if idemKey == "" {
		var err error
		idemKey, err = NewIdempotencyKey()
		if err != nil {
			return PreparedMutation{}, err
		}
	}
	if !ValidIdempotencyKey(idemKey) {
		return PreparedMutation{}, errors.New("invalid idempotency key")
	}
	q := url.Values{"base_version": {baseVersion}, "mode": {mode}}
	if confirmAdmin {
		q.Set("confirm_admin", "true")
	}
	return PreparedMutation{Operation: operation("applyConfig"), Query: q, Body: append([]byte(nil), candidate...), ContentType: "application/toml", IdempotencyKey: idemKey}, nil
}

type rollbackRequest struct {
	ID          string `json:"id"`
	BaseVersion string `json:"base_version"`
}

func (c *Client) PrepareRollback(id, baseVersion, idemKey string, confirmAdmin bool) (PreparedMutation, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(baseVersion) == "" {
		return PreparedMutation{}, errors.New("history id and base_version are required")
	}
	if idemKey == "" {
		var err error
		idemKey, err = NewIdempotencyKey()
		if err != nil {
			return PreparedMutation{}, err
		}
	}
	if !ValidIdempotencyKey(idemKey) {
		return PreparedMutation{}, errors.New("invalid idempotency key")
	}
	body, err := json.Marshal(rollbackRequest{ID: id, BaseVersion: baseVersion})
	if err != nil {
		return PreparedMutation{}, err
	}
	q := make(url.Values)
	if confirmAdmin {
		q.Set("confirm_admin", "true")
	}
	return PreparedMutation{Operation: operation("rollbackConfig"), Query: q, Body: body, ContentType: "application/json", IdempotencyKey: idemKey}, nil
}

// AdoptRequest is the published adoption wire request. It is kept here because
// the server's source type lives in internal/admin rather than adminapi.
type AdoptRequest struct {
	BaseVersion    string `json:"base_version"`
	ObservedDigest string `json:"observed_digest,omitempty"`
	Mode           string `json:"mode,omitempty"`
	Confirm        bool   `json:"confirm,omitempty"`
}

func (c *Client) AdoptPreview(ctx context.Context, req AdoptRequest) (RawResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return RawResponse{}, err
	}
	return c.do(ctx, operation("previewAdoptExternal"), nil, nil, body, "application/json", "")
}

func (c *Client) PrepareAdopt(req AdoptRequest, idemKey string) (PreparedMutation, error) {
	if strings.TrimSpace(req.BaseVersion) == "" {
		return PreparedMutation{}, errors.New("base_version is required")
	}
	if idemKey == "" {
		var err error
		idemKey, err = NewIdempotencyKey()
		if err != nil {
			return PreparedMutation{}, err
		}
	}
	if !ValidIdempotencyKey(idemKey) {
		return PreparedMutation{}, errors.New("invalid idempotency key")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return PreparedMutation{}, err
	}
	return PreparedMutation{Operation: operation("adoptExternal"), Query: make(url.Values), Body: body, ContentType: "application/json", IdempotencyKey: idemKey}, nil
}

func (c *Client) sendPrepared(ctx context.Context, p PreparedMutation) (adminapi.ConfigApplyResponse, RawResponse, error) {
	r, err := c.do(ctx, p.Operation, p.Params, cloneValues(p.Query), p.Body, p.ContentType, p.IdempotencyKey)
	if err != nil {
		return adminapi.ConfigApplyResponse{}, r, err
	}
	v, err := decode[adminapi.ConfigApplyResponse](r)
	return v, r, err
}

func cloneValues(v url.Values) url.Values {
	out := make(url.Values, len(v))
	for k, values := range v {
		out[k] = append([]string(nil), values...)
	}
	return out
}

// Mutate sends a prepared mutation. On transport ambiguity it re-reads boot_id
// before one retry; only an unchanged boot permits replay with the frozen bytes.
func (c *Client) Mutate(ctx context.Context, p PreparedMutation, knownBoot string) (adminapi.ConfigApplyResponse, RawResponse, error) {
	v, r, err := c.sendPrepared(ctx, p)
	if err == nil {
		return v, r, nil
	}
	var te *TransportError
	if !errors.As(err, &te) {
		return v, r, err
	}
	// Only failures that can leave commit state ambiguous are replay candidates.
	// Decode/redirect/policy failures are deterministic client observations and
	// must never trigger a hidden second mutation.
	if te.Phase != "connect" && te.Phase != "response" {
		return v, r, err
	}
	status, _, statusErr := c.Status(ctx)
	if statusErr != nil {
		return v, r, err
	}
	if knownBoot != "" && status.BootID != knownBoot {
		return v, r, &BootChangedError{Previous: knownBoot, Current: status.BootID}
	}
	return c.sendPrepared(ctx, p)
}

func (c *Client) ApplyResult(ctx context.Context, applyID string) (adminapi.ApplyResultResponse, RawResponse, error) {
	r, err := c.do(ctx, operation("getApplyResult"), map[string]string{"apply_id": applyID}, nil, nil, "", "")
	if err != nil {
		return adminapi.ApplyResultResponse{}, r, err
	}
	v, err := decode[adminapi.ApplyResultResponse](r)
	return v, r, err
}

// Poll waits only for the server's terminal indicator. Ctrl-C/context
// cancellation stops the local wait; the transaction is not cancelled.
func (c *Client) Poll(ctx context.Context, applyID, bootID string, max time.Duration) (adminapi.ApplyResultResponse, RawResponse, error) {
	if max <= 0 {
		max = c.pollTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, max)
	defer cancel()
	ticker := time.NewTicker(c.pollEvery)
	defer ticker.Stop()
	for {
		v, r, err := c.ApplyResult(ctx, applyID)
		if err != nil {
			return v, r, err
		}
		if bootID != "" && v.BootID != "" && v.BootID != bootID {
			return v, r, &BootChangedError{Previous: bootID, Current: v.BootID, ApplyID: applyID}
		}
		if v.Terminal {
			return v, r, nil
		}
		select {
		case <-ctx.Done():
			return v, r, &TransportError{Phase: "poll", Err: ctx.Err()}
		case <-ticker.C:
		}
	}
}

// ErrorExit is total over adminapi.Codes(). Unknown future stable codes are not
// silently classified: ok=false makes contract tests and callers fail closed.
func ErrorExit(code adminapi.Code) (exit int, ok bool) {
	switch code {
	case adminapi.CodeValidationFailed, adminapi.CodeOperationFailed:
		return 1, true
	case adminapi.CodeInvalidRequest, adminapi.CodeUnsupportedMediaType, adminapi.CodePayloadTooLarge, adminapi.CodeNotFound:
		return 2, true
	case adminapi.CodeStaleBaseVersion, adminapi.CodeDriftDetected, adminapi.CodePendingRestartConf, adminapi.CodeRestartRequired, adminapi.CodeAdminReachabilityConf, adminapi.CodeIdempotencyKeyReused, adminapi.CodeIdempotencyKeyInUse:
		return 5, true
	case adminapi.CodeConfigAuthorityRO:
		return 6, true
	case adminapi.CodeUnauthenticated, adminapi.CodeForbidden, adminapi.CodeInsecureTransport:
		return 7, true
	case adminapi.CodeRateLimited:
		return 8, true
	case adminapi.CodeNotImplemented, adminapi.CodeStorageUnavailable, adminapi.CodeOperationTimeout, adminapi.CodeInternalError:
		return 9, true
	default:
		return 9, false
	}
}

// OutcomeExit maps only terminal server outcomes. saved_not_live intentionally
// has no mapping and must continue polling.
func OutcomeExit(outcome string, restored bool, restoreError string, degraded []adminapi.Degradation) (int, bool) {
	switch outcome {
	case "applied_live":
		if len(degraded) != 0 {
			return 4, true
		}
		return 0, true
	case "applied_degraded":
		return 4, true
	case "staged", "owned_not_serving":
		if len(degraded) != 0 {
			return 4, true
		}
		return 3, true
	case "not_applied":
		if restored && restoreError == "" {
			return 1, true
		}
		return 5, true
	case "saved_not_live", "":
		return 9, false
	default:
		return 9, false
	}
}
