#!/usr/bin/env python3
"""Exercise the real panel, node-agent, and Xray with a local VLESS echo stream."""

import argparse
import contextlib
import hashlib
import http.cookiejar
import json
import os
from pathlib import Path
import secrets
import socket
import socketserver
import struct
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request
import uuid


ROOT = Path(__file__).resolve().parents[1]


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


class API:
    def __init__(self, base, token=None):
        self.base = base
        self.token = token
        self.opener = urllib.request.build_opener(
            urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()),
            urllib.request.ProxyHandler({}),
        )

    def call(self, method, path, data=None):
        headers = {"Content-Type": "application/json"}
        if self.token:
            headers["Authorization"] = "Bearer " + self.token
        req = urllib.request.Request(
            self.base + path,
            data=None if data is None else json.dumps(data).encode(),
            headers=headers,
            method=method,
        )
        try:
            with self.opener.open(req, timeout=3) as response:
                result = json.load(response)
                return result.get("data", result)
        except urllib.error.HTTPError as error:
            raise RuntimeError(f"{method} {path}: {error.code} {error.read().decode()}") from error


def wait_for(description, check, timeout=15):
    deadline = time.monotonic() + timeout
    last_error = None
    while time.monotonic() < deadline:
        try:
            result = check()
            if result:
                return result
        except (OSError, RuntimeError) as error:
            last_error = error
        time.sleep(0.2)
    raise AssertionError(f"timed out: {description}; {last_error or 'condition unmet'}")


class Echo(socketserver.BaseRequestHandler):
    def handle(self):
        with contextlib.suppress(OSError):
            while data := self.request.recv(65536):
                self.request.sendall(data)


def read_exact(sock, size):
    data = bytearray()
    while len(data) < size:
        chunk = sock.recv(size - len(data))
        if not chunk:
            raise AssertionError("proxy closed before echo completed")
        data.extend(chunk)
    return bytes(data)


def connect_vless(policy_user, origin_port):
    sock = socket.create_connection(("127.0.0.1", policy_user["port"]), timeout=3)
    # VLESS v0: UUID, empty addons, TCP, destination port, IPv4 address.
    header = (b"\x00" + uuid.UUID(policy_user["uuid"]).bytes + b"\x00\x01"
              + struct.pack("!H", origin_port) + b"\x01\x7f\x00\x00\x01")
    payload = b"initial-echo" * 512
    sock.sendall(header + payload)
    response = read_exact(sock, 2)
    assert response[0] == 0, "unexpected VLESS response version"
    read_exact(sock, response[1])
    assert read_exact(sock, len(payload)) == payload
    return sock


def wait_closed(sock, timeout):
    deadline = time.monotonic() + timeout
    sock.settimeout(0.3)
    while time.monotonic() < deadline:
        try:
            if not sock.recv(65536):
                return True
        except (ConnectionResetError, BrokenPipeError):
            return True
        except socket.timeout:
            continue
    return False


def stop(process):
    if process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=5)


def local_xray_wrapper(directory, core, origin_port):
    """Permit just the local test destination in temporary Xray configs."""
    wrapper = directory / "xray-local-smoke"
    wrapper.write_text(
        f"#!{sys.executable}\n"
        "import json, os, sys\n"
        "path = sys.argv[sys.argv.index('-config') + 1]\n"
        "with open(path) as stream: config = json.load(stream)\n"
        "for outbound in config.get('outbounds', []):\n"
        "    if outbound.get('protocol') == 'freedom':\n"
        "        outbound.setdefault('settings', {})['finalRules'] = ["
        "{'action':'allow','network':'tcp','ip':['127.0.0.1/32'],"
        f"'port':'{origin_port}'}}]\n"
        "with open(path, 'w') as stream: json.dump(config, stream)\n"
        f"os.execv({str(core)!r}, [{str(core)!r}] + sys.argv[1:])\n"
    )
    wrapper.chmod(0o700)
    return wrapper


def run(args, directory):
    backend = Path(args.backend).resolve()
    agent = Path(args.agent).resolve()
    core = Path(args.core).resolve()
    for binary in (backend, agent, core):
        if not binary.is_file():
            raise AssertionError(f"missing binary: {binary}")
    print(f"Xray sha256: {hashlib.sha256(core.read_bytes()).hexdigest()}", flush=True)
    panel_port, user_port = free_port(), free_port()
    base = f"http://127.0.0.1:{panel_port}"
    password = secrets.token_urlsafe(20)
    env = dict(os.environ, BOOTSTRAP_ADMIN_EMAIL="admin@smoke.test",
               BOOTSTRAP_ADMIN_PASSWORD=password, PUBLIC_URL=base, DEMO_MODE="0")
    processes = []
    echo = socketserver.ThreadingTCPServer(("127.0.0.1", 0), Echo)
    echo.daemon_threads = True
    threading.Thread(target=echo.serve_forever, daemon=True).start()
    # Xray 26.10.31 blocks private destinations by default. Allow only this
    # temporary echo endpoint in test configs; production configs stay unchanged.
    # See XTLS/Xray-core v26.10.31 infra/conf/freedom.go (finalRules).
    wrapper = local_xray_wrapper(directory, core, echo.server_address[1])
    try:
        with open(directory / "panel.log", "wb") as panel_log, open(directory / "agent.log", "wb") as agent_log:
            panel = subprocess.Popen([str(backend), "-listen", f"127.0.0.1:{panel_port}",
                                      "-db", str(directory / "smoke.db")], env=env,
                                     stdout=panel_log, stderr=subprocess.STDOUT)
            processes.append(panel)
            admin, user = API(base), API(base)
            wait_for("panel startup", lambda: admin.call("GET", "/api/health"))
            admin.call("POST", "/api/auth/login", {"email": "admin@smoke.test", "password": password})
            account = user.call("POST", "/api/auth/register", {
                "email": "user@smoke.test", "name": "Traffic smoke", "password": password})
            uid = account["id"]
            admin.call("PATCH", f"/api/admin/users/{uid}", {"balance_delta": 10000})
            plan = admin.call("POST", "/api/admin/plans", {
                "name": "128 KiB smoke quota", "traffic_gb": 128 / 1048576,
                "monthly_price": 100, "kind": "recurring"})

            def purchase():
                order = user.call("POST", "/api/orders", {"plan_id": plan["id"], "period": "monthly"})
                return user.call("POST", f"/api/orders/{order['id']}/pay", {})

            purchase()
            node = admin.call("POST", "/api/admin/nodes", {
                "name": "Local Xray smoke", "host": "127.0.0.1", "port": user_port,
                "protocol": "vless", "tls": False, "multiplier": 1,
                "status": "not_connected", "active": True})
            credential = admin.call("POST", f"/api/admin/nodes/{node['id']}/agent-token", {
                "engine": "xray", "port_start": user_port, "port_end": user_port})
            node_api = API(base, credential["token"])
            child_env = dict(os.environ, NICECLOUD_NODE_TOKEN=credential["token"])
            worker = subprocess.Popen([
                str(agent), "-panel", base, "-engine", "xray", "-core", str(wrapper),
                "-state", str(directory / "agent-state"), "-bind", "127.0.0.1"],
                env=child_env, stdout=agent_log, stderr=subprocess.STDOUT)
            processes.append(worker)

            def enforcement():
                rows = admin.call("GET", "/api/admin/traffic/enforcement")
                return next(row for row in rows if row["node_id"] == node["id"] and row["user_id"] == uid)

            wait_for("agent starts and acknowledges user", lambda: enforcement().get("applied_enabled") is True)
            policy = node_api.call("GET", "/api/node/policy")
            managed_user = next(row for row in policy["users"] if row["user_id"] == uid)
            diagnostic = user.call("GET", user.call("GET", "/api/subscription")["url"].removeprefix(base) + "?format=json")
            assert diagnostic["proxies"][0]["uuid"] == managed_user["uuid"]
            assert diagnostic["proxies"][0]["port"] == managed_user["port"]
            stream = connect_vless(managed_user, echo.server_address[1])
            with stream:
                payload = b"x" * (160 * 1024)
                stream.sendall(payload)
                assert wait_closed(stream, 12), "quota did not close existing TCP connection"
            stopped = wait_for("quota stop acknowledgement", lambda: (
                (row := enforcement())["reason"] == "quota_exceeded"
                and row.get("applied_enabled") is False and row))
            sub = user.call("GET", "/api/subscription")["subscription"]
            assert sub["traffic_used_bytes"] >= sub["traffic_limit_bytes"]
            assert sub["upload_bytes"] > 0 and sub["download_bytes"] > 0
            assert sub["traffic_used_bytes"] == sub["upload_bytes"] + sub["download_bytes"]
            assert sub["service_enabled"] is False
            records = user.call("GET", "/api/traffic")
            assert records and all(row["source"] == "metered" for row in records)
            assert sum(row["billed_bytes"] for row in records) == sub["traffic_used_bytes"]
            print("PASS: real VLESS bytes metered, quota exceeded, existing TCP closed, stop acknowledged", flush=True)

            purchase()
            renewed = user.call("GET", "/api/subscription")["subscription"]
            assert renewed["cycle_id"] == sub["cycle_id"]
            assert renewed["traffic_used_bytes"] == sub["traffic_used_bytes"]
            assert renewed["service_enabled"] is False
            print("PASS: same-plan renewal preserves exhausted quota", flush=True)

            admin.call("PATCH", f"/api/admin/subscriptions/{sub['id']}", {"status": "suspended"})
            admin.call("POST", f"/api/admin/users/{uid}/traffic/reset", {})
            current = user.call("GET", "/api/subscription")["subscription"]
            assert current["cycle_id"] != sub["cycle_id"]
            assert current["traffic_used_bytes"] == 0
            assert current["status"] == "suspended" and current["service_enabled"] is False
            node_api.call("POST", "/api/node/traffic", {"samples": [{
                "user_id": uid, "cycle_id": sub["cycle_id"], "session_id": "delayed-smoke",
                "upload_bytes": 99, "download_bytes": 101}]})
            assert user.call("GET", "/api/subscription")["subscription"]["traffic_used_bytes"] == 0
            admin.call("PATCH", f"/api/admin/subscriptions/{sub['id']}", {"status": "active"})
            wait_for("new cycle resumes after explicit reactivation", lambda: enforcement().get("applied_enabled") is True)
            policy = node_api.call("GET", "/api/node/policy")
            managed_user = next(row for row in policy["users"] if row["user_id"] == uid)
            print("PASS: reset preserves suspension, delayed old-cycle sample does not debit new quota", flush=True)

            with connect_vless(managed_user, echo.server_address[1]) as stream:
                stop(panel)
                outage_payload = b"during-outage" * 2048
                stream.sendall(outage_payload)
                assert read_exact(stream, len(outage_payload)) == outage_payload
                assert wait_closed(stream, 20), "lease expiry did not close connection"
            state = json.loads((directory / "agent-state/state.json").read_text())
            pending = [sample for sample in state["outbox"].values()
                       if sample["cycle_id"] == managed_user["cycle_id"]]
            assert any(sample["upload_bytes"] >= len(outage_payload)
                       and sample["download_bytes"] >= len(outage_payload)
                       for sample in pending), "outage traffic was lost from durable outbox"
            print("PASS: control-plane outage closes existing TCP and preserves outage bytes in outbox", flush=True)
            assert worker.poll() is None, "agent unexpectedly exited"
            return stopped
    finally:
        for process in reversed(processes):
            stop(process)
        echo.shutdown()
        echo.server_close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--backend", default=str(ROOT / ".local/cloud"))
    parser.add_argument("--agent", default=str(ROOT / ".local/node-agent"))
    parser.add_argument("--core", required=True, help="Official Xray executable (not sing-box)")
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="nicecloud-traffic-smoke-") as temporary:
        directory = Path(temporary)
        try:
            run(args, directory)
        except Exception:
            for path in [directory / "panel.log", directory / "agent.log", *directory.glob("agent-state/user-*.log")]:
                if path.exists():
                    print(f"\n{path.name}:\n{path.read_text(errors='replace')[-12000:]}", flush=True)
            raise
    print("Traffic smoke checks passed.", flush=True)


if __name__ == "__main__":
    main()
