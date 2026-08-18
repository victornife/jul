// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build kubernetes

package upstream

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"jul/internal/config"
)

const (
	k8sTokenFile = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	k8sCAFile    = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
)

// k8sDiscoverer resolves a Service's endpoints from the Kubernetes API server's
// EndpointSlice REST endpoint over HTTPS. client-go is not linked in; only the
// documented discovery.k8s.io/v1 list endpoint is queried, keeping the binary
// lean. In a pod the API server URL and service-account credentials are read
// from the standard in-cluster locations; config fields override them.
type k8sDiscoverer struct {
	client   *http.Client
	url      string
	token    string
	port     string // selected port name or number ("" = first port)
	describe string
	log      *slog.Logger
}

// SetLogger attaches a logger for detailed resolve diagnostics.
func (d *k8sDiscoverer) SetLogger(log *slog.Logger) {
	d.log = log
}

func newKubernetesDiscoverer(cfg config.DiscoveryConfig, dial DialFunc) (Discoverer, error) {
	k := cfg.Kubernetes
	if k == nil || strings.TrimSpace(k.Service) == "" || strings.TrimSpace(k.Namespace) == "" {
		return nil, fmt.Errorf("kubernetes discovery requires kubernetes.namespace and kubernetes.service")
	}

	base := strings.TrimSpace(k.APIServer)
	if base == "" {
		host := os.Getenv("KUBERNETES_SERVICE_HOST")
		port := os.Getenv("KUBERNETES_SERVICE_PORT")
		if host == "" || port == "" {
			return nil, fmt.Errorf("kubernetes discovery: no api_server set and not running in-cluster (KUBERNETES_SERVICE_HOST/PORT unset)")
		}
		base = "https://" + net.JoinHostPort(host, port)
	}
	base = strings.TrimRight(base, "/")

	token := strings.TrimSpace(k.Token)
	if token == "" {
		if b, err := os.ReadFile(k8sTokenFile); err == nil {
			token = strings.TrimSpace(string(b))
		}
	}

	tlsConf := &tls.Config{MinVersion: tls.VersionTLS12}
	if k.InsecureSkipTLSVerify {
		tlsConf.InsecureSkipVerify = true
	} else {
		caFile := strings.TrimSpace(k.CAFile)
		if caFile == "" {
			caFile = k8sCAFile
		}
		if b, err := os.ReadFile(caFile); err == nil {
			pool := x509.NewCertPool()
			if pool.AppendCertsFromPEM(b) {
				tlsConf.RootCAs = pool
			}
		}
	}

	endpoint := fmt.Sprintf("%s/apis/discovery.k8s.io/v1/namespaces/%s/endpointslices?labelSelector=%s",
		base, url.PathEscape(k.Namespace), url.QueryEscape("kubernetes.io/service-name="+k.Service))

	transport := &http.Transport{TLSClientConfig: tlsConf}
	if dial != nil {
		transport.DialContext = dial
	}

	return &k8sDiscoverer{
		client: &http.Client{
			Timeout:   10 * time.Second,
			Transport: transport,
		},
		url:      endpoint,
		token:    token,
		port:     strings.TrimSpace(k.Port),
		describe: "kubernetes:" + k.Namespace + "/" + k.Service,
	}, nil
}

// k8sPort is one port entry of an EndpointSlice.
type k8sPort struct {
	Name string `json:"name"`
	Port int    `json:"port"`
}

// k8sEndpoint is one endpoint (set of addresses with a readiness condition).
type k8sEndpoint struct {
	Addresses  []string `json:"addresses"`
	Conditions struct {
		Ready *bool `json:"ready"`
	} `json:"conditions"`
	// TargetRef names the object behind the endpoint. Its UID is the only
	// identity Kubernetes offers that survives a pod IP being recycled, which
	// it does within seconds.
	TargetRef *struct {
		UID string `json:"uid"`
	} `json:"targetRef"`
}

// k8sEndpointSliceList is the subset of the EndpointSlice list response read.
type k8sEndpointSliceList struct {
	Items []struct {
		Ports     []k8sPort     `json:"ports"`
		Endpoints []k8sEndpoint `json:"endpoints"`
	} `json:"items"`
}

func (d *k8sDiscoverer) Resolve(ctx context.Context) ([]Target, error) {
	if d.log != nil {
		d.log.Warn("kubernetes resolve request", "url", d.url)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if d.token != "" {
		req.Header.Set("Authorization", "Bearer "+d.token)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		if d.log != nil {
			d.log.Warn("kubernetes resolve request failed", "url", d.url, "error", err)
		}
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if d.log != nil {
		d.log.Warn("kubernetes resolve response", "url", d.url, "status", resp.Status)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kubernetes: unexpected status %s", resp.Status)
	}
	var list k8sEndpointSliceList
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, fmt.Errorf("kubernetes: decode response: %w", err)
	}

	var out []Target
	for i, slice := range list.Items {
		port := d.selectPort(slice.Ports)
		if d.log != nil {
			d.log.Warn("kubernetes resolve slice", "index", i, "ports", len(slice.Ports), "selected_port", port, "endpoints", len(slice.Endpoints))
		}
		if port == 0 {
			continue
		}
		for _, ep := range slice.Endpoints {
			// Skip endpoints explicitly marked not ready; a nil condition means
			// readiness is unknown, which Kubernetes treats as ready.
			if ep.Conditions.Ready != nil && !*ep.Conditions.Ready {
				continue
			}
			for _, addr := range ep.Addresses {
				if strings.TrimSpace(addr) == "" {
					continue
				}
				var id string
				if ep.TargetRef != nil {
					id = strings.TrimSpace(ep.TargetRef.UID)
				}
				out = append(out, Target{Address: net.JoinHostPort(addr, strconv.Itoa(port)), ID: id})
			}
		}
	}
	if d.log != nil {
		d.log.Warn("kubernetes resolve result", "url", d.url, "targets", len(out))
	}
	return out, nil
}

// selectPort picks the configured port (by name or number) from a slice's port
// list, or the first port when none is configured. It returns 0 when no port
// matches, in which case the slice is skipped.
func (d *k8sDiscoverer) selectPort(ports []k8sPort) int {
	if len(ports) == 0 {
		return 0
	}
	if d.port == "" {
		return ports[0].Port
	}
	if n, err := strconv.Atoi(d.port); err == nil {
		for _, p := range ports {
			if p.Port == n {
				return n
			}
		}
		return 0
	}
	for _, p := range ports {
		if p.Name == d.port {
			return p.Port
		}
	}
	return 0
}

func (d *k8sDiscoverer) Describe() string { return d.describe }
