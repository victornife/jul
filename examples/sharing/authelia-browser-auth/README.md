# Authelia browser login through Jul (#627)

This disposable recipe puts Authelia's browser session in front of one private
dashboard through Jul forward-auth, while a different host serves a public
static page on the same Jul instance. Jul and Authelia keep separate control
planes; the dashboard origin, Authelia port, and Jul admin listener are
loopback-only.

See the [operator runbook](../../../docs/sharing/authelia-browser-auth-runbook.md)
for trust boundaries, session limits, recovery, isolation, and the evidence
status.

## Local quick start

Run these commands from this directory. The generated CA, passwords, admin
token, provider secrets, and SQLite data stay under ignored `.lab/` files.
The checkout must be under trusted directories without non-sticky
group/world-writable ancestors so Jul can protect its effective-version key.

```bash
python3 -m venv .venv
. .venv/bin/activate
python -m pip install -r requirements.txt
python generate_lab_assets.py
docker compose up -d
curl --fail http://127.0.0.1:19091/api/health

# Terminal 1: the sample application binds to loopback only.
python3 dashboard_backend.py

# Terminal 2: build Jul from this checkout, then start the fixture.
go build -o jul ../../../cmd/jul
./jul version-key init --file .lab/effective-version.key
set -a
. .lab/jul.env
set +a
./jul check -config jul.toml
./jul -config jul.toml --effective-version-key-file .lab/effective-version.key
```

In another terminal, prepare a headless Chromium profile that trusts only the
generated test CA, then exercise login, denial, cookie scope, and idle expiry:

```bash
python3 -m venv .venv-browser
. .venv-browser/bin/activate
python -m pip install -r requirements-browser.txt
python -m playwright install chromium
python run_browser_checks.py
```

The browser test requires `certutil` (`libnss3-tools` on Debian/Ubuntu). It
does not disable TLS validation, alter the system certificate store, edit
`/etc/hosts`, or require a desktop environment.
