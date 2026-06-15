# Python app behind Jul.IA (uWSGI protocol)

Serves a minimal WSGI app through the native **uWSGI protocol** using
`uwsgi_pass`. This mirrors a classic NGINX + uWSGI deployment.

| Setting | Value |
| --- | --- |
| Backend app | uWSGI socket on `127.0.0.1:3031` |
| Jul.IA front | `http://127.0.0.1:8099` |
| Action | `uwsgi_pass` |

## Run

1. Start the app under uWSGI (listens on the uWSGI socket):

   ```bash
   uwsgi --socket 127.0.0.1:3031 --wsgi-file app.py --callable application
   ```

2. In another terminal, start Jul.IA with this config:

   ```bash
   go run ./cmd/jul -config examples/python-uwsgi/jul.toml
   ```

3. Open <http://127.0.0.1:8099/>.

## Notes

`uwsgi_pass` accepts several address forms:

```text
127.0.0.1:3031            # TCP (default)
tcp://127.0.0.1:3031      # TCP (explicit)
unix:/run/app/uwsgi.sock  # Unix socket
```

> **Platform:** the `uwsgi` server itself does **not** build natively on
> Windows. Use WSL, Docker, or a Linux/macOS host for this example. On Windows,
> prefer the [`python-proxy`](../python-proxy) example instead.
