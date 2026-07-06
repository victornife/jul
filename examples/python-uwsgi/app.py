# Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
# SPDX-License-Identifier: agpl

"""Minimal WSGI application served behind Jul via the uWSGI protocol."""


def application(environ, start_response):
    status = "200 OK"
    body = b"Hello from a Python WSGI app behind Jul (uWSGI)!\n"
    headers = [
        ("Content-Type", "text/plain; charset=utf-8"),
        ("Content-Length", str(len(body))),
    ]
    start_response(status, headers)
    return [body]
