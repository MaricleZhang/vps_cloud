#!/usr/bin/env python3
"""Check reference protocol shapes using real Mihomo and sing-box connections.

Only temporary local keys/endpoints are used. The user's clash.yaml is never
loaded by a client, modified, or sent to its configured remote servers.
"""

import argparse
from concurrent.futures import ThreadPoolExecutor
import contextlib
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import secrets
import socket
import socketserver
import struct
import subprocess
import tempfile
import threading


SPEC = importlib.util.spec_from_file_location("traffic_smoke", Path(__file__).with_name("traffic-smoke.py"))
COMMON = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(COMMON)
ROOT, API, wait_for = COMMON.ROOT, COMMON.API, COMMON.wait_for


def private_json(path, value):
    path.write_text(json.dumps(value, indent=2))
    path.chmod(0o600)


def local_ready(port):
    with socket.create_connection(("127.0.0.1", port), timeout=0.3):
        return True


def socks_address(stream):
    kind = COMMON.read_exact(stream, 1)[0]
    if kind == 1:
        host = socket.inet_ntoa(COMMON.read_exact(stream, 4))
    elif kind == 4:
        host = socket.inet_ntop(socket.AF_INET6, COMMON.read_exact(stream, 16))
    elif kind == 3:
        host = COMMON.read_exact(stream, COMMON.read_exact(stream, 1)[0]).decode()
    else:
        raise AssertionError("invalid SOCKS5 address type")
    return host, struct.unpack("!H", COMMON.read_exact(stream, 2))[0]


def socks_request(port, command, destination_port):
    stream = socket.create_connection(("127.0.0.1", port), timeout=4)
    try:
        stream.sendall(b"\x05\x01\x00")
        assert COMMON.read_exact(stream, 2) == b"\x05\x00", "SOCKS5 greeting failed"
        stream.sendall(bytes((5, command, 0, 1)) + b"\x7f\x00\x00\x01" + struct.pack("!H", destination_port))
        reply = COMMON.read_exact(stream, 3)
        assert reply[:2] == b"\x05\x00", f"SOCKS5 request failed with status {reply[1]}"
        return stream, socks_address(stream)
    except Exception:
        stream.close()
        raise


def echo_bytes(stream, payload):
    stream.settimeout(4)
    stream.sendall(payload)
    assert COMMON.read_exact(stream, len(payload)) == payload


class UDPEcho(socketserver.BaseRequestHandler):
    def handle(self):
        payload, sock = self.request
        sock.sendto(payload, self.client_address)


def udp_echo(client_port, destination_port):
    association, address = socks_request(client_port, 3, 0)
    if address[0] in ("0.0.0.0", "::"):
        address = ("127.0.0.1", address[1])
    datagram = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    datagram.settimeout(4)
    payload = b"local-reference-udp-echo"
    frame = b"\x00\x00\x00\x01\x7f\x00\x00\x01" + struct.pack("!H", destination_port) + payload
    try:
        datagram.sendto(frame, address)
        response, _ = datagram.recvfrom(65536)
        assert response[:4] == b"\x00\x00\x00\x01" and response[10:] == payload, "UDP echo mismatch"
    finally:
        association.close()
        datagram.close()


def run(args, directory):
    backend, agent, kernel, client = (Path(value).resolve() for value in (args.backend, args.agent, args.kernel, args.client))
    for binary in (backend, agent, kernel, client):
        if not binary.is_file():
            raise AssertionError(f"missing executable: {binary}")
    metadata = json.loads(subprocess.check_output([str(kernel), "version"]))
    assert metadata["with_utls"] and metadata["with_v2ray_api"], "kernel is missing required build features"
    print(f"Server: sing-box {metadata['upstream_version']}; client: "
          + subprocess.check_output([str(client), "-v"], text=True).splitlines()[0], flush=True)
    processes, streams, nodes = [], [], []
    tcp = socketserver.ThreadingTCPServer(("127.0.0.1", 0), COMMON.Echo)
    tcp.daemon_threads = True
    udp = socketserver.ThreadingUDPServer(("127.0.0.1", 0), UDPEcho)
    udp.daemon_threads = True
    for server in (tcp, udp):
        threading.Thread(target=server.serve_forever, daemon=True).start()

    with contextlib.ExitStack() as files:
        def launch(command, log_path, env=None):
            log = files.enter_context(open(log_path, "ab"))
            process = subprocess.Popen(command, env=env, stdout=log, stderr=subprocess.STDOUT)
            processes.append(process)
            return process

        def generate(kind, target, *extra):
            return json.loads(subprocess.check_output([str(kernel), "generate", kind, "--out", str(target), *extra]))

        def start_client(proxy, name, pin=False):
            client_dir = directory / f"client-{name}"
            client_dir.mkdir(mode=0o700)
            port = COMMON.free_port()
            profile = dict(proxy)
            if pin:
                # Trust only this test certificate; never disable verification
                # or change the operating system's certificate store.
                profile["fingerprint"] = certificate_fingerprint
            config = {"mixed-port": port, "allow-lan": False, "bind-address": "127.0.0.1",
                      "mode": "rule", "log-level": "warning", "ipv6": False,
                      "dns": {"enable": False}, "proxies": [profile],
                      "proxy-groups": [], "rules": [f"MATCH,{profile['name']}"]}
            config_path = client_dir / "config.json"
            private_json(config_path, config)
            launch([str(client), "-d", str(client_dir), "-f", str(config_path)], client_dir / "client.log")
            wait_for(f"Mihomo {name} SOCKS listener", lambda: local_ready(port))
            return port

        try:
            certificate, private_key = directory / "test-cert.pem", directory / "test-key.pem"
            certificate_log = files.enter_context(open(directory / "certificate.log", "wb"))
            subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
                            "-keyout", str(private_key), "-out", str(certificate), "-subj", "/CN=cover.example.test",
                            "-addext", "subjectAltName=DNS:cover.example.test,DNS:inner.example.test,DNS:public.example.test"],
                           check=True, stdout=certificate_log, stderr=subprocess.STDOUT)
            private_key.chmod(0o600)
            der = subprocess.check_output(["openssl", "x509", "-in", str(certificate), "-outform", "DER"])
            digest = hashlib.sha256(der).hexdigest().upper()
            certificate_fingerprint = ":".join(digest[index:index + 2] for index in range(0, len(digest), 2))
            cover_port = COMMON.free_port()
            launch(["openssl", "s_server", "-accept", f"127.0.0.1:{cover_port}", "-cert", str(certificate),
                    "-key", str(private_key), "-tls1_3", "-groups", "X25519", "-quiet", "-www"], directory / "cover.log")
            wait_for("local TLS 1.3 REALITY handshake target", lambda: local_ready(cover_port))
            reality = generate("reality-keypair", directory / "reality-keys")
            ech = generate("ech-keypair", directory / "ech-keys", "--server-name", "public.example.test")
            base = f"http://127.0.0.1:{COMMON.free_port()}"
            password = secrets.token_urlsafe(24)
            env = dict(os.environ, PUBLIC_URL=base, COOKIE_SECURE="0", DEMO_MODE="0",
                       BOOTSTRAP_ADMIN_EMAIL="admin@protocol.test", BOOTSTRAP_ADMIN_PASSWORD=password)
            launch([str(backend), "-listen", base.removeprefix("http://"), "-db", str(directory / "center.db")],
                   directory / "center.log", env)
            admin, user = API(base), API(base)
            wait_for("center health", lambda: admin.call("GET", "/api/health"))
            admin.call("POST", "/api/auth/login", {"email": "admin@protocol.test", "password": password})
            account = user.call("POST", "/api/auth/register", {"email": "user@protocol.test", "name": "Protocol smoke", "password": password})
            uid = account["id"]
            admin.call("PATCH", f"/api/admin/users/{uid}", {"balance_delta": 10000})
            plan = admin.call("POST", "/api/admin/plans", {"name": "1 MiB protocol test", "traffic_gb": 1 / 1024,
                                                           "kind": "recurring", "monthly_price": 100})
            order = user.call("POST", "/api/orders", {"plan_id": plan["id"], "period": "monthly"})
            user.call("POST", f"/api/orders/{order['id']}/pay", {})

            for protocol, security, public, local in (
                ("vless", "reality", {**reality, "flow": "xtls-rprx-vision", "server_name": "cover.example.test",
                                      "reality_handshake_server": "127.0.0.1", "reality_handshake_port": cover_port},
                 {"reality_private_key_file": str(directory / "reality-keys/reality.key")}),
                ("anytls", "tls", {**ech, "ech_enabled": True, "server_name": "inner.example.test"},
                 {"ech_key_file": str(directory / "ech-keys/ech.pem"), "tls_cert": str(certificate), "tls_key": str(private_key)}),
            ):
                node_dir = directory / f"node-{protocol}"
                node_dir.mkdir(mode=0o700)
                port = COMMON.free_port()
                node = admin.call("POST", "/api/admin/nodes", {"name": f"local-{protocol}", "host": "127.0.0.1", "port": port,
                    "protocol": protocol, "tls_mode": security, "network": "tcp", "udp": True, "active": True,
                    "multiplier": 1, "client_fingerprint": "chrome", **public})
                token = admin.call("POST", f"/api/admin/nodes/{node['id']}/agent-token", {
                    "engine": "sing-box", "port_start": port, "port_end": port})["token"]
                token_path = node_dir / "node.token"
                token_path.write_text(token + "\n")
                token_path.chmod(0o600)
                config_path = node_dir / "config.json"
                private_json(config_path, {"panel": base, "engine": "sing-box", "core": str(kernel),
                    "state_dir": str(node_dir / "state"), "bind": "127.0.0.1", "token_file": str(token_path),
                    "poll_interval": "1s", **local})
                child_env = dict(os.environ)
                child_env.pop("NICECLOUD_NODE_TOKEN", None)
                launch([str(agent), "-config", str(config_path)], node_dir / "agent.log", child_env)
                nodes.append(node)

            def confirmed(enabled):
                rows = admin.call("GET", "/api/admin/traffic/enforcement")
                return len(rows) == 2 and all(row["desired_enabled"] is enabled and row["applied_enabled"] is enabled for row in rows)

            wait_for("both secure server profiles ready and acknowledged", lambda: confirmed(True), timeout=25)
            url = user.call("GET", "/api/subscription")["url"].removeprefix(base)
            proxies = user.call("GET", url + "?format=json")["proxies"]
            reality_proxy = next(proxy for proxy in proxies if proxy["type"] == "vless")
            ech_proxy = next(proxy for proxy in proxies if proxy["type"] == "anytls")
            assert reality_proxy["flow"] == "xtls-rprx-vision" and reality_proxy["reality-opts"]["public-key"] == reality["reality_public_key"]
            assert ech_proxy["ech-opts"]["config"] == ech["ech_config"] and ech_proxy["skip-cert-verify"] is False
            clients = {}
            for fingerprint in ("chrome", "firefox", "safari"):
                profile = {**reality_proxy, "client-fingerprint": fingerprint}
                client_port = start_client(profile, f"reality-{fingerprint}")
                stream, _ = socks_request(client_port, 1, tcp.server_address[1])
                streams.append(stream)
                echo_bytes(stream, b"real-reality-vision" * 1024)
                clients[fingerprint] = client_port
            print("PASS: real Mihomo VLESS Vision/REALITY with chrome, firefox and safari fingerprints", flush=True)
            ech_client = start_client(ech_proxy, "anytls-ech", pin=True)
            stream, _ = socks_request(ech_client, 1, tcp.server_address[1])
            streams.append(stream)
            echo_bytes(stream, b"real-anytls-ech" * 1024)
            print("PASS: real Mihomo AnyTLS/ECH with certificate verification enabled and test-certificate pin", flush=True)
            udp_echo(clients["chrome"], udp.server_address[1])
            udp_echo(ech_client, udp.server_address[1])
            print("PASS: UDP echo through both reference protocol profiles", flush=True)

            def metered():
                records = user.call("GET", "/api/traffic")
                sub = user.call("GET", "/api/subscription")["subscription"]
                return (set(record["node_id"] for record in records) == {node["id"] for node in nodes}
                        and sub["traffic_used_bytes"] > 65536
                        and sub["traffic_used_bytes"] == sum(record["billed_bytes"] for record in records))

            wait_for("secure profiles deliver real metered bytes", metered)
            # Both live paths belong to the same subscription and must stop.
            trigger, _ = socks_request(clients["chrome"], 1, tcp.server_address[1])
            with trigger:
                for _ in range(48):
                    try:
                        echo_bytes(trigger, b"quota" * 16384)
                    except (OSError, AssertionError):
                        break
            wait_for("secure profiles acknowledge shared quota stop", lambda: confirmed(False))
            with ThreadPoolExecutor(max_workers=4) as pool:
                assert all(pool.map(lambda connection: COMMON.wait_closed(connection, 10), streams))
            print("PASS: real bytes counted centrally; shared quota closes existing REALITY and AnyTLS/ECH streams", flush=True)
        finally:
            for stream in streams:
                stream.close()
            for process in reversed(processes):
                COMMON.stop(process)
            for server in (tcp, udp):
                server.shutdown()
                server.server_close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--backend", default=str(ROOT / ".local/cloud"))
    parser.add_argument("--agent", default=str(ROOT / ".local/node-agent"))
    parser.add_argument("--kernel", default=str(ROOT / ".local/nicecloud-kernel"))
    parser.add_argument("--client", required=True, help="Mihomo executable (not the running desktop client's API)")
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="nicecloud-clash-protocols-") as temporary:
        directory = Path(temporary)
        try:
            run(args, directory)
        except Exception:
            for path in [directory / "center.log", directory / "cover.log", *directory.glob("node-*/agent.log"),
                         *directory.glob("node-*/state/user-*.log"), *directory.glob("client-*/client.log")]:
                if path.exists():
                    print(f"\n{path.relative_to(directory)}:\n{path.read_text(errors='replace')[-6000:]}", flush=True)
            raise
    print("Reference Clash protocol smoke checks passed.", flush=True)


if __name__ == "__main__":
    main()
