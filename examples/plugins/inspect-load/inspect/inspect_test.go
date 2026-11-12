// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package inspect

import (
	"errors"
	"testing"
)

type fakeReq struct {
	body       []byte
	headers    map[string]string
	status     int
	written    string
	subscribed []int
}

func (r *fakeReq) Body() []byte                  { return r.body }
func (r *fakeReq) SetHeader(name, value string)  { r.headers[name] = value }
func (r *fakeReq) SetResponseStatus(code int)    { r.status = code }
func (r *fakeReq) WriteResponseBody(b []byte)    { r.written += string(b) }
func (r *fakeReq) SubscribeResponse(m int) error { r.subscribed = append(r.subscribed, m); return nil }

type fakeResp struct {
	body     []byte
	err      error
	state    uint32
	headers  map[string]string
	replaced []byte
}

func (r *fakeResp) Body() ([]byte, error) { return r.body, r.err }
func (r *fakeResp) BodyState() uint32     { return r.state }
func (r *fakeResp) SetHeader(name, value string) error {
	r.headers[name] = value
	return nil
}
func (r *fakeResp) ReplaceBody(b []byte) error { r.replaced = b; return nil }

const (
	stop, cont      = "stop", "continue"
	deliver, reject = "deliver", "reject"
	bodyMode        = 1
)

func install(t *testing.T, config string) (func(*fakeReq) string, func(*fakeResp) string) {
	t.Helper()
	var hreq func(*fakeReq) string
	var hresp func(*fakeResp) string
	if !Install(&hreq, &hresp, func() []byte { return []byte(config) }, bodyMode, cont, stop, deliver, reject) {
		t.Fatal("Install returned false")
	}
	return hreq, hresp
}

func TestRequestEvidenceBlockAndSubscription(t *testing.T) {
	hreq, _ := install(t, `{"name":"a","read_body":"true","subscribe":"true"}`)
	r := &fakeReq{body: []byte("12345"), headers: map[string]string{}}
	if got := hreq(r); got != cont || r.headers["X-Assess-a-Req-Len"] != "5" || len(r.subscribed) != 1 || r.subscribed[0] != bodyMode {
		t.Fatalf("clean request: action=%s headers=%v subscribed=%v", got, r.headers, r.subscribed)
	}
	blocked := &fakeReq{body: append([]byte("x"), ReqBlock...), headers: map[string]string{}}
	if got := hreq(blocked); got != stop || blocked.status != 403 || blocked.written != "blocked by inspect-load a\n" || len(blocked.subscribed) != 0 {
		t.Fatalf("blocked request: action=%s %+v", got, blocked)
	}
}

func TestRequestWithoutReadingOrSubscribing(t *testing.T) {
	hreq, _ := install(t, `not json`)
	r := &fakeReq{body: ReqBlock, headers: map[string]string{}}
	if got := hreq(r); got != cont || len(r.headers) != 0 || len(r.subscribed) != 0 {
		t.Fatalf("defaults must neither read nor subscribe: %s %+v", got, r)
	}
	if Load(nil).Name != "x" {
		t.Fatal("default name must be x")
	}
}

func TestResponseEvidenceReplaceRejectAndUnavailable(t *testing.T) {
	_, hresp := install(t, `{"name":"b","replace":"true"}`)
	r := &fakeResp{body: []byte("abcdef"), headers: map[string]string{}}
	if got := hresp(r); got != deliver || r.headers["X-Assess-b-Resp"] != "6" || string(r.replaced) != "abcdef" {
		t.Fatalf("replace path: %s %+v", got, r)
	}
	rej := &fakeResp{body: RespBlock, headers: map[string]string{}}
	if got := hresp(rej); got != reject || len(rej.headers) != 0 {
		t.Fatalf("response marker must reject: %s %+v", got, rej)
	}
	un := &fakeResp{err: errors.New("unavailable"), state: 3, headers: map[string]string{}}
	if got := hresp(un); got != deliver || un.headers["X-Assess-b-Resp"] != "unavailable-3" {
		t.Fatalf("unavailable body: %s %+v", got, un)
	}
	_, plain := install(t, `{"name":"a"}`)
	p := &fakeResp{body: []byte("abc"), headers: map[string]string{}}
	if got := plain(p); got != deliver || p.replaced != nil || p.headers["X-Assess-a-Resp"] != "3" {
		t.Fatalf("no-replace path: %s %+v", got, p)
	}
}
