"""Controlled CI trust and network fixtures; never a production certification."""

import hashlib
import json
from pathlib import Path
import secrets
import subprocess
import uuid


def command(*arguments):
    result = subprocess.run(list(map(str, arguments)), text=True, capture_output=True, timeout=60)
    if result.returncode:
        raise RuntimeError(f"{arguments[0]} failed ({result.returncode}): {result.stderr[-1200:]}")
    return result.stdout.strip()


def certificate_chain(directory, hostname="jul516.test", addresses=()):
    directory = Path(directory)
    directory.mkdir(parents=True, exist_ok=True)
    directory.chmod(0o700)
    root, intermediate, leaf = [directory / (name + ".pem") for name in ("root", "intermediate", "leaf")]
    root_key, intermediate_key, key = [directory / (name + ".key") for name in ("root", "intermediate", "leaf")]
    command("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "2", "-sha256",
            "-subj", "/CN=Jul 516 CI root", "-addext", "basicConstraints=critical,CA:TRUE,pathlen:1",
            "-addext", "keyUsage=critical,keyCertSign,cRLSign", "-keyout", root_key, "-out", root)
    for name, private_key, subject in (("intermediate", intermediate_key, "Jul 516 CI intermediate"),
                                       ("leaf", key, hostname)):
        command("openssl", "req", "-new", "-newkey", "rsa:2048", "-nodes", "-sha256",
                "-subj", "/CN=" + subject, "-keyout", private_key, "-out", directory / (name + ".csr"))
    (directory / "intermediate.ext").write_text("basicConstraints=critical,CA:TRUE,pathlen:0\n"
                                               "keyUsage=critical,keyCertSign,cRLSign\n"
                                               "subjectKeyIdentifier=hash\nauthorityKeyIdentifier=keyid,issuer\n")
    command("openssl", "x509", "-req", "-in", directory / "intermediate.csr", "-CA", root,
            "-CAkey", root_key, "-CAcreateserial", "-days", "2", "-sha256", "-extfile",
            directory / "intermediate.ext", "-out", intermediate)
    sans = ",".join(["DNS:" + hostname, "IP:127.0.0.1", *("IP:" + item for item in addresses)])
    (directory / "leaf.ext").write_text("basicConstraints=critical,CA:FALSE\n"
                                      "keyUsage=critical,digitalSignature,keyEncipherment\n"
                                      "extendedKeyUsage=serverAuth\n"
                                      f"subjectAltName={sans}\n")
    command("openssl", "x509", "-req", "-in", directory / "leaf.csr", "-CA", intermediate,
            "-CAkey", intermediate_key, "-CAcreateserial", "-days", "2", "-sha256", "-extfile",
            directory / "leaf.ext", "-out", leaf)
    bundle = directory / "chain.pem"
    bundle.write_bytes(leaf.read_bytes() + intermediate.read_bytes())
    wrong = directory / "wrong-root.pem"
    command("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "2", "-sha256",
            "-subj", "/CN=Untrusted CI root", "-addext", "basicConstraints=critical,CA:TRUE",
            "-keyout", directory / "wrong-root.key", "-out", wrong)
    for private in directory.glob("*.key"):
        private.chmod(0o600)
    return {"root": root, "leaf": leaf, "chain": bundle, "key": key, "wrong_root": wrong,
            "hostname": hostname, "root_sha256": hashlib.sha256(root.read_bytes()).hexdigest()}


class RoutedClient:
    """An independent client network namespace, sharing only the runner kernel."""

    def __init__(self, directory):
        suffix = uuid.uuid4().hex[:7]
        self.namespace = "jul516-" + suffix
        self.host_link, self.client_link = "j516h" + suffix, "j516c" + suffix
        self.chain = "JL516" + suffix.upper()
        self.gateway = f"198.19.{secrets.randbelow(250) + 1}.1"
        self.address = self.gateway[:-1] + "2"
        self.directory = Path(directory)
        self.namespace_created = self.link_created = self.chain_created = self.jump_created = False

    def __enter__(self):
        try:
            command("sudo", "ip", "netns", "add", self.namespace)
            self.namespace_created = True
            command("sudo", "ip", "link", "add", self.host_link, "type", "veth", "peer", "name", self.client_link)
            self.link_created = True
            command("sudo", "ip", "link", "set", self.client_link, "netns", self.namespace)
            command("sudo", "ip", "addr", "add", self.gateway + "/30", "dev", self.host_link)
            command("sudo", "ip", "link", "set", self.host_link, "up")
            command("sudo", "ip", "-n", self.namespace, "addr", "add", self.address + "/30", "dev", self.client_link)
            command("sudo", "ip", "-n", self.namespace, "link", "set", self.client_link, "up")
            command("sudo", "ip", "-n", self.namespace, "link", "set", "lo", "up")
            command("sudo", "ip", "-n", self.namespace, "route", "add", "default", "via", self.gateway)
            return self
        except Exception:
            self.__exit__(None, None, None)
            raise

    def firewall(self, *allowed_ports):
        command("sudo", "iptables", "-N", self.chain)
        self.chain_created = True
        command("sudo", "iptables", "-I", "INPUT", "1", "-i", self.host_link, "-j", self.chain)
        self.jump_created = True
        for allowed_port in allowed_ports:
            command("sudo", "iptables", "-A", self.chain, "-p", "tcp", "--dport", allowed_port, "-j", "ACCEPT")
        command("sudo", "iptables", "-A", self.chain, "-j", "REJECT")

    def allow(self, port, enabled):
        command("sudo", "iptables", "-R", self.chain, "1", "-p", "tcp", "--dport", port,
                "-j", "ACCEPT" if enabled else "REJECT")

    def fetch(self, port, ca, path="/", hostname="jul516.test", token=None, method="GET", data=None,
              content_type=None, tls=True, failure=None, headers=None):
        request_id = uuid.uuid4().hex
        body_file, header_file = [self.directory / (request_id + extension) for extension in (".body", ".headers")]
        args = ["sudo", "ip", "netns", "exec", self.namespace, "curl", "--silent", "--show-error",
                "--noproxy", "*", "--max-time", "5", "--output", str(body_file), "--dump-header", str(header_file),
                "--write-out", "%{http_code}", "--resolve", f"{hostname}:{port}:{self.gateway}", "--request", method]
        if tls:
            args += ["--cacert", str(ca)]
        if token:
            args += ["--header", "Authorization: Bearer " + token]
        for name, value in (headers or {}).items():
            args += ["--header", name + ": " + value]
        if content_type:
            args += ["--header", "Content-Type: " + content_type]
        if data is not None:
            payload = self.directory / (request_id + ".request")
            payload.write_bytes(data)
            payload.chmod(0o600)
            args += ["--data-binary", "@" + str(payload)]
        args += [f"{'https' if tls else 'http'}://{hostname}:{port}{path}"]
        result = subprocess.run(args, text=True, capture_output=True, timeout=15)
        if failure is not None:
            assert result.returncode == failure, f"expected curl failure {failure}, got {result.returncode}"
            return None
        assert result.returncode == 0, f"client request failed ({result.returncode}): {result.stderr[-500:]}"
        response_headers = {}
        for line in header_file.read_text().splitlines():
            if ":" in line:
                name, value = line.split(":", 1)
                response_headers[name.lower()] = value.strip()
        return int(result.stdout), body_file.read_bytes(), response_headers

    def evidence(self):
        return {"topology": "separate-network-namespace/shared-runner-kernel", "client_address": self.address,
                "server_gateway": self.gateway, "host_interface": self.host_link}

    def __exit__(self, *unused):
        if self.jump_created:
            command("sudo", "iptables", "-D", "INPUT", "-i", self.host_link, "-j", self.chain)
        if self.chain_created:
            command("sudo", "iptables", "-F", self.chain)
            command("sudo", "iptables", "-X", self.chain)
        if self.namespace_created:
            command("sudo", "ip", "netns", "del", self.namespace)
        if self.link_created:
            subprocess.run(["sudo", "ip", "link", "del", self.host_link], capture_output=True)


def write_report(path, data):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, indent=2) + "\n")
