#!/usr/bin/env python3
"""Verify that CI trust fixtures exercise real chain and hostname validation."""

import http.server
from pathlib import Path
import ssl
import tempfile
import threading
import unittest
import urllib.request

from deployment_lab import certificate_chain


class TrustTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temporary = tempfile.TemporaryDirectory()
        cls.pki = certificate_chain(Path(cls.temporary.name), hostname="localhost")

    @classmethod
    def tearDownClass(cls):
        cls.temporary.cleanup()

    def probe(self, server_cert, client_ca, hostname="localhost"):
        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                self.send_response(200)
                self.end_headers()
                self.wfile.write(b"verified\n")

            def log_message(self, *unused):
                pass

        server = http.server.HTTPServer(("127.0.0.1", 0), Handler)
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(server_cert, self.pki["key"])
        server.socket = context.wrap_socket(server.socket, server_side=True)
        worker = threading.Thread(target=server.serve_forever)
        worker.start()
        try:
            trusted = ssl.create_default_context(cafile=str(client_ca))
            # Connect locally but set an independent TLS hostname for negatives.
            import socket
            with socket.create_connection(("127.0.0.1", server.server_port), timeout=3) as connection:
                with trusted.wrap_socket(connection, server_hostname=hostname) as tls:
                    tls.sendall(b"GET / HTTP/1.0\r\nHost: localhost\r\n\r\n")
                    self.assertIn(b"200", tls.recv(4096))
        finally:
            server.shutdown()
            server.server_close()
            worker.join()

    def test_valid_root_intermediate_leaf(self):
        self.probe(self.pki["chain"], self.pki["root"])

    def test_wrong_ca_rejected(self):
        with self.assertRaises(ssl.SSLCertVerificationError):
            self.probe(self.pki["chain"], self.pki["wrong_root"])

    def test_wrong_hostname_rejected(self):
        with self.assertRaises(ssl.SSLCertVerificationError):
            self.probe(self.pki["chain"], self.pki["root"], "wrong.jul516.test")

    def test_missing_intermediate_rejected(self):
        with self.assertRaises(ssl.SSLCertVerificationError):
            self.probe(self.pki["leaf"], self.pki["root"])


if __name__ == "__main__":
    unittest.main(verbosity=2)
