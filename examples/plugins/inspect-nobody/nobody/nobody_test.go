// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package nobody

import (
	"errors"
	"testing"
)

type fakeReq struct {
	headers    map[string]string
	status     int
	written    string
	state      []byte
	stateErr   error
	subscribed []int
}

func (r *fakeReq) Header(name string) (string, bool) { v, ok := r.headers[name]; return v, ok }
func (r *fakeReq) SetHeader(name, value string)      { r.headers[name] = value }
func (r *fakeReq) SetResponseStatus(code int)        { r.status = code }
func (r *fakeReq) WriteResponseBody(b []byte)        { r.written += string(b) }
func (r *fakeReq) SetState(b []byte) error {
	if r.stateErr != nil {
		return r.stateErr
	}
	r.state = b
	return nil
}
func (r *fakeReq) SubscribeResponse(m int) error { r.subscribed = append(r.subscribed, m); return nil }

type fakeResp struct {
	state   []byte
	headers map[string]string
}

func (r *fakeResp) State() []byte { return r.state }
func (r *fakeResp) SetHeader(name, value string) error {
	r.headers[name] = value
	return nil
}

const headersMode = 7

func install(t *testing.T, config string) (func(*fakeReq) string, func(*fakeResp) string) {
	t.Helper()
	var hreq func(*fakeReq) string
	var hresp func(*fakeResp) string
	if !Install(&hreq, &hresp, func() []byte { return []byte(config) }, headersMode, "continue", "stop", "deliver") {
		t.Fatal("Install returned false")
	}
	return hreq, hresp
}

func TestStateEvidenceAndHeaderSubscription(t *testing.T) {
	hreq, hresp := install(t, `{"name":"a1","state_bytes":"100"}`)
	r := &fakeReq{headers: map[string]string{}}
	if got := hreq(r); got != "continue" || len(r.state) != 100 || r.headers["X-Assess-a1-State"] != "100" {
		t.Fatalf("request: %q state=%d headers=%v", got, len(r.state), r.headers)
	}
	if len(r.subscribed) != 1 || r.subscribed[0] != headersMode {
		t.Fatalf("subscription = %v, want headers mode only", r.subscribed)
	}
	resp := &fakeResp{state: r.state, headers: map[string]string{}}
	if got := hresp(resp); got != "deliver" || resp.headers["X-Assess-a1-Resp"] != "state-100" {
		t.Fatalf("response: %q headers=%v", got, resp.headers)
	}
}

func TestBlockAndStateFailureStop(t *testing.T) {
	hreq, _ := install(t, `{}`)
	b := &fakeReq{headers: map[string]string{BlockHeader: "1"}}
	if got := hreq(b); got != "stop" || b.status != 403 || b.written != "blocked by inspect-nobody x\n" || b.state != nil {
		t.Fatalf("block: %q status=%d body=%q state=%d", got, b.status, b.written, len(b.state))
	}
	f := &fakeReq{headers: map[string]string{}, stateErr: errors.New("exhausted")}
	if got := hreq(f); got != "stop" || f.status != 500 || len(f.subscribed) != 0 {
		t.Fatalf("state failure: %q status=%d subscribed=%v", got, f.status, f.subscribed)
	}
}

func TestLoadDefaults(t *testing.T) {
	for raw, want := range map[string]int{`{}`: MaxState, `{"state_bytes":"x"}`: MaxState, `{"state_bytes":"-1"}`: MaxState,
		`{"state_bytes":"4097"}`: MaxState, `{"state_bytes":"0"}`: 0, `not json`: MaxState} {
		if name, n := Load([]byte(raw)); name != "x" || n != want {
			t.Errorf("Load(%s) = %q, %d; want x, %d", raw, name, n, want)
		}
	}
}
