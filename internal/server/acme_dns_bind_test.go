// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build acme && http3 && linux

package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"

	"jul/internal/config"
)

const bind9Image = "ubuntu/bind9@sha256:45d541e9688f2c9010d22a78200117b66ed2df31b78f3464876f7e6e15cfaf77"

type bind9Fixture struct {
	primary, secondary string
	primaryID          string
	secondaryID        string
}

type bindChallengeTransport struct {
	next     http.RoundTripper
	attempts atomic.Int32
}

func (t *bindChallengeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.HasPrefix(req.URL.Path, "/chalZ/") && req.Method == http.MethodPost {
		t.attempts.Add(1)
	}
	return t.next.RoundTrip(req)
}

func TestDNS01Bind9Interoperability(t *testing.T) {
	if os.Getenv("JUL_BIND_E2E") != "1" {
		t.Skip("set JUL_BIND_E2E=1 to run the pinned Docker/BIND9 and Pebble integration test")
	}
	t.Setenv("JUL_PEBBLE_AUTHZREUSE", "0")
	bind := startBind9Fixture(t)
	pebble := startDNS01Pebble(t, bind.secondary)
	tracker := &bindChallengeTransport{next: pebble.client.Transport}
	pebble.client.Transport = tracker

	newManager := func(zone string, delegations map[string]string) *dnsACMEManager {
		t.Helper()
		cfg := &config.ACMEConfig{
			Email: "ops@example.test", CA: pebble.directory, CacheDir: filepath.Join(t.TempDir(), "cache"),
			DNSProvider: "rfc2136", Challenge: "dns-01",
			RFC2136: &config.RFC2136Config{
				Server: bind.primary, Observers: []string{bind.secondary}, Zone: zone,
				TSIGKeyName: testTSIGName, TSIGSecret: testTSIGSecret, CNAMEDelegations: delegations,
				PropagationTimeout: config.Duration(15 * time.Second), PollInterval: config.Duration(50 * time.Millisecond),
			},
		}
		manager, err := newDNSACMEManager(cfg, nil, pebble.client, nil, ACMEOptions{DNSDialContext: (&net.Dialer{}).DialContext})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := manager.Close(); err != nil {
				t.Errorf("close DNS manager: %v", err)
			}
		})
		return manager
	}

	t.Run("NS delegated issuance and renewal with delayed secondary", func(t *testing.T) {
		manager := newManager("_acme-challenge.example.test", nil)
		names := []string{"*.example.test", "example.test"}
		first := issueBindCertificate(t, bind, pebble, tracker, manager, names,
			"_acme-challenge.example.test.", "_acme-challenge.example.test.", "unrelated")
		renewed := issueBindCertificate(t, bind, pebble, tracker, manager, names,
			"_acme-challenge.example.test.", "_acme-challenge.example.test.", "unrelated")
		if first.Leaf.SerialNumber.Cmp(renewed.Leaf.SerialNumber) == 0 {
			t.Fatal("renewal returned the original certificate")
		}
	})

	t.Run("one-hop CNAME issuance and renewal", func(t *testing.T) {
		delegations := map[string]string{"_acme-challenge.alias.test": "txt.validation.test"}
		manager := newManager("validation.test", delegations)
		names := []string{"*.alias.test", "alias.test"}
		first := issueBindCertificate(t, bind, pebble, tracker, manager, names,
			"validation.test.", "txt.validation.test.", "unrelated-cname")
		renewed := issueBindCertificate(t, bind, pebble, tracker, manager, names,
			"validation.test.", "txt.validation.test.", "unrelated-cname")
		if first.Leaf.SerialNumber.Cmp(renewed.Leaf.SerialNumber) == 0 {
			t.Fatal("CNAME renewal returned the original certificate")
		}
	})

	t.Run("mismatched and unconfigured aliases fail before update", func(t *testing.T) {
		manager := newManager("validation.test", map[string]string{"_acme-challenge.alias.test": "wrong.validation.test"})
		before := bindSOASerial(t, bind.primary, "validation.test.")
		_, err := manager.provider.Present(context.Background(), "_acme-challenge.alias.test.", "must-not-be-written")
		if err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("mismatched CNAME error = %v", err)
		}
		if after := bindSOASerial(t, bind.primary, "validation.test."); after != before {
			t.Fatalf("mismatched CNAME mutated zone serial: %d -> %d", before, after)
		}
		values := bindTXT(t, bind.primary, "wrong.validation.test.")
		if len(values) != 0 {
			t.Fatalf("unexpected records at mismatched target: %v", values)
		}

		unconfigured := newManager("alias.test", nil)
		before = bindSOASerial(t, bind.primary, "alias.test.")
		_, err = unconfigured.provider.Present(context.Background(), "_acme-challenge.alias.test.", "must-not-be-written")
		if err == nil || !strings.Contains(err.Error(), "unconfigured CNAME") {
			t.Fatalf("unconfigured CNAME error = %v", err)
		}
		if after := bindSOASerial(t, bind.primary, "alias.test."); after != before {
			t.Fatalf("unconfigured CNAME mutated zone serial: %d -> %d", before, after)
		}
	})

	t.Run("TSIG authentication, clock, and least privilege", func(t *testing.T) {
		manager := newManager("_acme-challenge.example.test", nil)
		provider := manager.provider.(*rfc2136Provider)
		before := bindSOASerial(t, bind.primary, "_acme-challenge.example.test.")
		wrongKey := *provider
		wrongKey.secret = "d3JvbmctdGVzdC1rZXk="
		if _, err := wrongKey.Present(context.Background(), testOwner, "wrong-key-value"); err == nil ||
			!strings.Contains(err.Error(), "TSIG authentication") {
			t.Fatalf("wrong-key error = %v (cause: %v, inner: %v)", err, errors.Unwrap(err), errors.Unwrap(errors.Unwrap(err)))
		}
		skewed := *provider
		skewed.now = func() time.Time { return time.Now().Add(-time.Hour) }
		if _, err := skewed.Present(context.Background(), testOwner, "skew-value"); err == nil ||
			!strings.Contains(err.Error(), "synchronize") {
			t.Fatalf("clock-skew error = %v", err)
		}
		msg := new(dns.Msg)
		msg.SetUpdate("_acme-challenge.example.test.")
		msg.Insert([]dns.RR{&dns.TXT{
			Hdr: dns.RR_Header{Name: "other._acme-challenge.example.test.", Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: 60},
			Txt: []string{"must-not-be-written"},
		}})
		msg.SetTsig(testTSIGName, dns.HmacSHA256, 300, time.Now().Unix())
		client := &dns.Client{Net: "tcp", TsigSecret: map[string]string{testTSIGName: testTSIGSecret}, Timeout: 3 * time.Second}
		response, _, err := client.Exchange(msg, bind.primary)
		if err != nil || response.Rcode != dns.RcodeRefused {
			t.Fatalf("out-of-scope TXT update response = %v, %v", response, err)
		}
		if after := bindSOASerial(t, bind.primary, "_acme-challenge.example.test."); after != before {
			t.Fatalf("rejected TSIG updates mutated zone serial: %d -> %d", before, after)
		}
	})
}

func issueBindCertificate(t *testing.T, bind *bind9Fixture, pebble *testDNSPebble, tracker *bindChallengeTransport,
	manager *dnsACMEManager, names []string, zone, owner, unrelated string,
) *tls.Certificate {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := manager.ensureAccountKey(ctx); err != nil {
		t.Fatal(err)
	}
	type result struct {
		cert *tls.Certificate
		err  error
	}
	done := make(chan result, 1)
	before := tracker.attempts.Load()
	go func() {
		for {
			cert, err := manager.issueCertificate(ctx, names, dnsECDSA)
			if !errors.Is(err, errDNSAuthorizationBusy) {
				done <- result{cert, err}
				return
			}
			select {
			case <-ctx.Done():
				done <- result{nil, err}
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	}()
	var premature *result
	waitBind(t, bind, "primary receives both owned challenges", func() bool {
		select {
		case result := <-done:
			premature = &result
			return true
		default:
		}
		values, err := queryBindTXT(bind.primary, owner)
		return err == nil && values[unrelated] && len(values) >= 5
	})
	if premature != nil {
		logs, _ := exec.Command("docker", "logs", pebble.containerID).CombinedOutput()
		t.Fatalf("issuance stopped before DNS propagation: %v\n%s", premature.err, logs)
	}
	secondary := bindTXT(t, bind.secondary, owner)
	if len(secondary) != 1 || !secondary[unrelated] {
		t.Fatalf("secondary unexpectedly has new challenge records: %v", secondary)
	}
	time.Sleep(250 * time.Millisecond)
	if got := tracker.attempts.Load(); got != before {
		t.Fatalf("submitted %d challenge(s) before the CA-facing secondary caught up", got-before)
	}
	bindNotify(t, bind.secondary, zone)
	var issued result
	select {
	case issued = <-done:
	case <-ctx.Done():
		t.Fatal("timed out waiting for BIND/Pebble issuance")
	}
	if issued.err != nil {
		logs, _ := exec.Command("docker", "logs", pebble.containerID).CombinedOutput()
		t.Fatalf("BIND/Pebble issuance: %v\n%s", issued.err, logs)
	}
	if tracker.attempts.Load() <= before {
		t.Fatal("Pebble challenge submissions were not observed")
	}
	intermediates := x509.NewCertPool()
	for _, der := range issued.cert.Certificate[1:] {
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		intermediates.AddCert(cert)
	}
	if _, err := issued.cert.Leaf.Verify(x509.VerifyOptions{
		Roots: pebble.roots, Intermediates: intermediates, DNSName: "www." + strings.TrimPrefix(names[0], "*."),
	}); err != nil {
		t.Fatalf("issued certificate did not verify: %v", err)
	}
	waitBind(t, bind, "exact owned-record cleanup on the primary", func() bool {
		values, err := queryBindTXT(bind.primary, owner)
		return err == nil && len(values) == 1 && values[unrelated]
	})
	bindNotify(t, bind.secondary, zone)
	waitBind(t, bind, "cleanup replication to the secondary", func() bool {
		values, err := queryBindTXT(bind.secondary, owner)
		return err == nil && len(values) == 1 && values[unrelated]
	})
	return issued.cert
}

func startBind9Fixture(t *testing.T) *bind9Fixture {
	t.Helper()
	dir := t.TempDir()
	for _, subdir := range []string{"primary", "secondary"} {
		if err := os.Mkdir(filepath.Join(dir, subdir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	primaryPort, secondaryPort := freeTCPPort(t), freeTCPPort(t)
	for primaryPort == secondaryPort {
		secondaryPort = freeTCPPort(t)
	}
	zones := map[string]string{
		"example.test":                 "_acme-challenge IN NS ns.example.test.\nns IN A 127.0.0.1\n",
		"_acme-challenge.example.test": "@ IN TXT \"unrelated\"\n",
		"alias.test":                   "_acme-challenge IN CNAME txt.validation.test.\nns IN A 127.0.0.1\n",
		"validation.test":              "txt IN TXT \"unrelated-cname\"\nns IN A 127.0.0.1\n",
	}
	primaryZones, secondaryZones := "", ""
	for zone, records := range zones {
		filename := strings.ReplaceAll(zone, ".", "-") + ".db"
		server := "ns." + zone + "."
		if strings.HasPrefix(zone, "_acme-challenge.") {
			server = "ns.example.test."
		}
		zoneFile := fmt.Sprintf("$TTL 60\n@ IN SOA %s hostmaster.example.test. ( 1 3600 60 86400 60 )\n@ IN NS %s\n%s", server, server, records)
		if err := os.WriteFile(filepath.Join(dir, "primary", filename), []byte(zoneFile), 0o644); err != nil {
			t.Fatal(err)
		}
		policy := ""
		switch zone {
		case "_acme-challenge.example.test":
			policy = `update-policy { grant "jul.example.test." name "_acme-challenge.example.test." TXT; };`
		case "validation.test":
			policy = `update-policy { grant "jul.example.test." name "txt.validation.test." TXT; };`
		}
		primaryZones += fmt.Sprintf("zone %q { type primary; file %q; %s };\n", zone, "/work/primary/"+filename, policy)
		secondaryZones += fmt.Sprintf("zone %q { type secondary; primaries { 127.0.0.1 port %d; }; file %q; };\n",
			zone, primaryPort, "/work/secondary/"+filename)
	}
	options := func(port int, directory string, recursive bool) string {
		recursion := "recursion no;"
		if recursive {
			recursion = "recursion yes; allow-recursion { 127.0.0.1; };"
		}
		return fmt.Sprintf(`options {
 directory %q; listen-on port %d { 127.0.0.1; }; listen-on-v6 { none; };
 %s dnssec-validation no; allow-query { 127.0.0.1; };
 allow-transfer { 127.0.0.1; }; allow-notify { 127.0.0.1; };
 notify no; min-refresh-time 3600; pid-file %q; session-keyfile %q;
};
controls { };
`, directory, port, recursion, directory+"/named.pid", directory+"/session.key")
	}
	primaryConfig := options(primaryPort, "/work/primary", false) +
		fmt.Sprintf("key %q { algorithm hmac-sha256; secret %q; };\n", testTSIGName, testTSIGSecret) + primaryZones
	secondaryConfig := options(secondaryPort, "/work/secondary", true) + secondaryZones
	for name, contents := range map[string]string{"primary": primaryConfig, "secondary": secondaryConfig} {
		if err := os.WriteFile(filepath.Join(dir, name, "named.conf"), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	start := func(name string) string {
		t.Helper()
		containerName := fmt.Sprintf("jul-bind9-%s-%d-%d", name, os.Getpid(), time.Now().UnixNano())
		cmd := exec.Command("docker", "run", "-d", "--name", containerName, "--network", "host",
			"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
			"--entrypoint", "/usr/sbin/named", "-v", dir+":/work", bind9Image,
			"-g", "-c", "/work/"+name+"/named.conf", "-n", "2")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("start pinned BIND %s: %v: %s", name, err, output)
		}
		id := strings.TrimSpace(string(output))
		t.Cleanup(func() {
			if output, err := exec.Command("docker", "rm", "-f", id).CombinedOutput(); err != nil {
				t.Errorf("remove BIND %s: %v: %s", name, err, output)
			}
		})
		return id
	}
	fixture := &bind9Fixture{
		primary: fmt.Sprintf("127.0.0.1:%d", primaryPort), secondary: fmt.Sprintf("127.0.0.1:%d", secondaryPort),
	}
	fixture.primaryID = start("primary")
	fixture.secondaryID = start("secondary")
	for _, endpoint := range []string{fixture.primary, fixture.secondary} {
		waitBind(t, fixture, "authoritative zones to load", func() bool {
			direct, err := queryBindTXT(endpoint, testOwner)
			if err != nil || !direct["unrelated"] {
				return false
			}
			target, err := queryBindTXT(endpoint, "txt.validation.test.")
			return err == nil && target["unrelated-cname"]
		})
	}
	return fixture
}

func queryBindTXT(endpoint, owner string) (map[string]bool, error) {
	msg := new(dns.Msg)
	msg.SetQuestion(owner, dns.TypeTXT)
	msg.RecursionDesired = false
	response, _, err := (&dns.Client{Net: "tcp", Timeout: time.Second}).Exchange(msg, endpoint)
	if err != nil {
		return nil, err
	}
	if !response.Authoritative || response.Rcode != dns.RcodeSuccess && response.Rcode != dns.RcodeNameError {
		return nil, fmt.Errorf("non-authoritative or unsuccessful DNS response: %s", dns.RcodeToString[response.Rcode])
	}
	values := make(map[string]bool)
	for _, answer := range response.Answer {
		if txt, ok := answer.(*dns.TXT); ok && strings.EqualFold(txt.Hdr.Name, owner) {
			values[strings.Join(txt.Txt, "")] = true
		}
	}
	return values, nil
}

func bindTXT(t *testing.T, endpoint, owner string) map[string]bool {
	t.Helper()
	values, err := queryBindTXT(endpoint, owner)
	if err != nil {
		t.Fatalf("query %s on %s: %v", owner, endpoint, err)
	}
	return values
}

func bindSOASerial(t *testing.T, endpoint, zone string) uint32 {
	t.Helper()
	msg := new(dns.Msg)
	msg.SetQuestion(zone, dns.TypeSOA)
	response, _, err := (&dns.Client{Net: "tcp", Timeout: time.Second}).Exchange(msg, endpoint)
	if err != nil || !response.Authoritative || response.Rcode != dns.RcodeSuccess {
		t.Fatalf("query SOA %s: %v, %v", zone, response, err)
	}
	for _, answer := range response.Answer {
		if soa, ok := answer.(*dns.SOA); ok {
			return soa.Serial
		}
	}
	t.Fatalf("no SOA for %s", zone)
	return 0
}

func bindNotify(t *testing.T, endpoint, zone string) {
	t.Helper()
	msg := new(dns.Msg)
	msg.SetNotify(zone)
	response, _, err := (&dns.Client{Net: "udp", Timeout: time.Second}).Exchange(msg, endpoint)
	if err != nil || response.Rcode != dns.RcodeSuccess {
		t.Fatalf("notify secondary for %s: %v, %v", zone, response, err)
	}
}

func waitBind(t *testing.T, fixture *bind9Fixture, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			primary, _ := exec.Command("docker", "logs", fixture.primaryID).CombinedOutput()
			secondary, _ := exec.Command("docker", "logs", fixture.secondaryID).CombinedOutput()
			t.Fatalf("timed out waiting for %s\nprimary: %s\nsecondary: %s", description, primary, secondary)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
