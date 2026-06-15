# Python app behind Jul.IA (proxy_pass over HTTP)

Serves a minimal WSGI app as a normal HTTP server and puts Jul.IA in front of it
with `proxy_pass`. This is the **Windows-friendly** Python option — no C build
toolchain required (unlike the uWSGI example).

| Setting | Value |
| --- | --- |
| Backend app | `http://127.0.0.1:3031` |
| Jul.IA front | `http://127.0.0.1:8099` |
| Action | `proxy_pass` |

## Run

1. Start the Python app (pick one):

   ```bash
   # Standard library only, no install:
   python app.py

   # Or a production-grade WSGI server:
   pip install waitress
   waitress-serve --listen=127.0.0.1:3031 app:application
   ```

2. In another terminal, start Jul.IA with this config:

   ```bash
   go run ./cmd/jul -config examples/python-proxy/jul.toml
   ```

3. Open <http://127.0.0.1:8099/>.

## Notes

The standard-library fallback in `app.py` uses a **threading** WSGI server on
purpose. Jul.IA reuses keep-alive upstream connections, and a single-threaded
`wsgiref` server would deadlock on an idle connection. Any backend you put
behind Jul.IA must handle concurrent/keep-alive connections — `waitress`,
`gunicorn`, and `uvicorn` all do.
