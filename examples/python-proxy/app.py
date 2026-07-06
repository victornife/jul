# Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
# SPDX-License-Identifier: agpl

"""Minimal WSGI app served over plain HTTP behind Jul via proxy_pass.

This is the Windows-friendly alternative to the uWSGI example: it runs as a
normal HTTP server (waitress), so no C build toolchain is required.

Run it with waitress (recommended):
    pip install waitress
    waitress-serve --listen=127.0.0.1:3031 app:application

Or with the Python standard library only (no extra install):
    python app.py
"""


def application(environ, start_response):
    status = "200 OK"
    body = b"Hello from a Python app behind Jul (proxy_pass over HTTP)!\n"
    headers = [
        ("Content-Type", "text/plain; charset=utf-8"),
        ("Content-Length", str(len(body))),
    ]
    start_response(status, headers)
    return [body]


if __name__ == "__main__":
    # Fallback runner using only the standard library. A threading server is
    # required because Jul reuses (keep-alive) upstream connections; a
    # single-threaded server would deadlock on an idle connection.
    from wsgiref.simple_server import make_server, WSGIServer
    from socketserver import ThreadingMixIn

    class ThreadingWSGIServer(ThreadingMixIn, WSGIServer):
        daemon_threads = True

    host, port = "127.0.0.1", 3031
    print(f"Serving on http://{host}:{port} (Ctrl+C to stop)")
    with make_server(host, port, application, server_class=ThreadingWSGIServer) as httpd:
        httpd.serve_forever()
