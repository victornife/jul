import os
import time
import urllib.error
import urllib.request

server = "http://127.0.0.1:35747"
origin = "http://127.0.0.1:32977/__count"
admin = "http://127.0.0.1:33567/metrics"
upload = "/home/victornf/.copilot/session-state/81ab5976-605d-4542-a731-cd69382adaff/files/pr-721-closeout/upload"
headers = {"Host": "localhost", "User-Agent": "jul-695-assessment", "Accept": "text/html"}


def count():
    with urllib.request.urlopen(origin, timeout=5) as response:
        return int(response.read())


def metrics():
    req = urllib.request.Request(
        admin, headers={"Authorization": "Bearer assessment-695-token-not-a-secret"}
    )
    with urllib.request.urlopen(req, timeout=5) as response:
        return response.read().decode()


def inspection_errors(text):
    return sum(
        float(line.rsplit(" ", 1)[1])
        for line in text.splitlines()
        if line.startswith("jul_waf_transactions_total{")
        and 'outcome="inspection_error"' in line
    )


def request(path, body=None, content_type="application/json"):
    h = dict(headers)
    if body is not None:
        h["Content-Type"] = content_type
    req = urllib.request.Request(server + path, data=body, headers=h)
    try:
        response = urllib.request.urlopen(req, timeout=10)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        return response.status, response.headers, response.read()


def check_case(label, path, body, expected_status, expected_count,
               content_type="application/json", healthy=False):
    status, h, out = request(path, body, content_type)
    calls = count()
    assert status == expected_status, (
        f"{label}: status={status}, expected={expected_status}, body={out[:300]!r}"
    )
    assert calls == expected_count, f"{label}: origin calls={calls}, expected={expected_count}"
    if healthy:
        assert h.get("X-Origin-Req-Len") == str(len(body)), f"{label}: origin body length: {dict(h)}"
        assert h.get("X-Echo-A-Req-Len") == str(len(body)), f"{label}: guest a request: {dict(h)}"
        assert h.get("X-Echo-B-Req-Len") == str(len(body)), f"{label}: guest b request: {dict(h)}"
        assert h.get("X-Assess-a-Resp") == "1024", f"{label}: guest a response: {dict(h)}"
        assert h.get("X-Assess-b-Resp") == "1024", f"{label}: guest b response: {dict(h)}"
    print(
        f"{label}: HTTP {status}, origin calls {calls}"
        + (", both guest request/response echoes verified" if healthy else "")
    )


assert count() == 0
clean = b'{"message":"clean"}'
check_case("healthy combined route", "/proxy/echo", clean, 200, 1, healthy=True)
check_case("WAF request denial", "/proxy/echo", b'{"message":"ASSESS-WAF-REQ-BLOCK"}', 403, 1)
check_case("WASM request denial", "/proxy/echo", b'{"message":"ASSESS-WASM-REQ-BLOCK"}', 403, 1)
check_case("WAF response denial", "/proxy/echo?canary=waf", None, 403, 2)
boundary = "jul-closeout-boundary"
multipart = (
    b'--jul-closeout-boundary\r\nContent-Disposition: form-data; name="file"; filename="one.txt"\r\n'
    b"Content-Type: text/plain\r\n\r\none\r\n--jul-closeout-boundary--\r\n"
)
content_type = "multipart/form-data; boundary=" + boundary
check_case("healthy upload before fault", "/fault/echo", multipart, 200, 3, content_type, True)
before = inspection_errors(metrics())
os.rmdir(upload)
try:
    check_case(
        "surfaced body error with ruleRemoveById in detect mode",
        "/fault/echo", multipart, 400, 3, content_type,
    )
    after = inspection_errors(metrics())
    assert after == before + 1, f"inspection_error counter {before} -> {after}"
    print(f"body-error outcome: inspection_error {before:g} -> {after:g}")
finally:
    os.mkdir(upload, 0o700)
check_case("upload recovery", "/fault/echo", multipart, 200, 4, content_type, True)
check_case("combined route recovery", "/proxy/echo", clean, 200, 5, healthy=True)
time.sleep(0.5)
m = metrics()
for gauge in (
    "jul_inspection_leases", 'jul_inspection_bytes{class="base"}',
    'jul_inspection_bytes{class="buffer"}', 'jul_inspection_bytes{class="waf_state"}',
    'jul_inspection_evaluations{state="active"}', 'jul_inspection_evaluations{state="waiting"}',
):
    lines = [line for line in m.splitlines() if line.startswith(gauge + " ")]
    assert len(lines) == 1 and float(lines[0].rsplit(" ", 1)[1]) == 0, f"{gauge}: {lines}"
print("recovery: all six request-lease, byte and evaluation gauges drained to zero")
