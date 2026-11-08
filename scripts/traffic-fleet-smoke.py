#!/usr/bin/env python3
"""Run one real center, five independent node Agents, and ten Xray workers."""

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
import subprocess
import tempfile
import threading


# Keep the wire protocol and local-only Xray test exception in one place.
SPEC = importlib.util.spec_from_file_location("single_node_smoke", Path(__file__).with_name("traffic-smoke.py"))
SINGLE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(SINGLE)
ROOT = SINGLE.ROOT
API = SINGLE.API
wait_for = SINGLE.wait_for


def reserve_pair():
    """Keep proxy ports below the normal ephemeral range used by StatsService."""
    for _ in range(200):
        start = 20000 + secrets.randbelow(12000)
        sockets = []
        try:
            for port in (start, start + 1):
                sock = socket.socket()
                sockets.append(sock)
                sock.bind(("127.0.0.1", port))
            return start, sockets
        except OSError:
            for sock in sockets:
                sock.close()
    raise RuntimeError("cannot reserve a pair of local proxy ports")


def private_json(path, data):
    path.write_text(json.dumps(data, indent=2))
    path.chmod(0o600)


def echo_bytes(stream, data):
    stream.settimeout(3)
    stream.sendall(data)
    assert SINGLE.read_exact(stream, len(data)) == data


def run(args, directory):
    backend, agent, core = (Path(value).resolve() for value in (args.backend, args.agent, args.core))
    for binary in (backend, agent, core):
        if not binary.is_file():
            raise AssertionError(f"missing executable: {binary}")
    print(f"Xray sha256: {hashlib.sha256(core.read_bytes()).hexdigest()}", flush=True)
    base = f"http://127.0.0.1:{SINGLE.free_port()}"
    password = secrets.token_urlsafe(24)
    env = dict(os.environ, PUBLIC_URL=base, DEMO_MODE="0",
               BOOTSTRAP_ADMIN_EMAIL="admin@fleet.test", BOOTSTRAP_ADMIN_PASSWORD=password)
    processes, streams, reservations, nodes = [], {}, [], []
    echo = socketserver.ThreadingTCPServer(("127.0.0.1", 0), SINGLE.Echo)
    echo.daemon_threads = True
    threading.Thread(target=echo.serve_forever, daemon=True).start()
    origin_port = echo.server_address[1]
    wrapper = SINGLE.local_xray_wrapper(directory, core, origin_port)

    with contextlib.ExitStack() as resources:
        def launch_center():
            log = resources.enter_context(open(directory / "center.log", "ab"))
            process = subprocess.Popen([str(backend), "-listen", base.removeprefix("http://"),
                                        "-db", str(directory / "center.db")],
                                       env=env, stdout=log, stderr=subprocess.STDOUT)
            processes.append(process)
            wait_for("center health", lambda: API(base).call("GET", "/api/health"))
            return process

        def launch_node(node):
            log = resources.enter_context(open(node["directory"] / "agent.log", "ab"))
            child_env = dict(os.environ)
            child_env.pop("NICECLOUD_NODE_TOKEN", None)
            process = subprocess.Popen([str(agent), "-config", str(node["config"])],
                                       env=child_env, stdout=log, stderr=subprocess.STDOUT)
            processes.append(process)
            node["process"] = process

        def states():
            return {(row["node_id"], row["user_id"]): row
                    for row in admin.call("GET", "/api/admin/traffic/enforcement")}

        def confirmed(uid, enabled, reason=None):
            current = states()
            return all((row := current.get((node["id"], uid))) is not None
                       and row["desired_enabled"] is enabled
                       and row["applied_enabled"] is enabled
                       and (reason is None or row["reason"] == reason) for node in nodes)

        def policy_user(node, uid):
            return next(user for user in node["api"].call("GET", "/api/node/policy")["users"]
                        if user["user_id"] == uid)

        try:
            center = launch_center()
            admin = API(base)
            admin.call("POST", "/api/auth/login", {"email": "admin@fleet.test", "password": password})
            users = []
            plan = admin.call("POST", "/api/admin/plans", {
                "name": "Fleet 4 MiB", "kind": "recurring", "monthly_price": 100,
                "traffic_gb": 4 / 1024})
            for index in range(2):
                api = API(base)
                account = api.call("POST", "/api/auth/register", {
                    "email": f"user-{index}@fleet.test", "name": f"Fleet user {index}", "password": password})
                uid = account["id"]
                admin.call("PATCH", f"/api/admin/users/{uid}", {"balance_delta": 10000})
                order = api.call("POST", "/api/orders", {"plan_id": plan["id"], "period": "monthly"})
                api.call("POST", f"/api/orders/{order['id']}/pay", {})
                users.append({"id": uid, "api": api})
            charged, unaffected = users

            for index, multiplier in enumerate((1, 2, 0.5, 1.5, 1.25), start=1):
                node_dir = directory / f"node-{index}"
                node_dir.mkdir(mode=0o700)
                port, held = reserve_pair()
                reservations.extend(held)
                node = admin.call("POST", "/api/admin/nodes", {
                    "name": f"Fleet node {index}", "host": "127.0.0.1", "port": port,
                    "protocol": "vless", "tls": False, "active": True,
                    "multiplier": multiplier, "status": "not_connected"})
                token = admin.call("POST", f"/api/admin/nodes/{node['id']}/agent-token", {
                    "engine": "xray", "port_start": port, "port_end": port + 1})["token"]
                token_file = node_dir / "node.token"
                token_file.write_text(token + "\n")
                token_file.chmod(0o600)
                config = node_dir / "config.json"
                private_json(config, {"panel": base, "engine": "xray", "core": str(wrapper),
                                      "state_dir": str(node_dir / "state"), "bind": "127.0.0.1",
                                      "token_file": str(token_file), "poll_interval": "1s"})
                nodes.append({"id": node["id"], "api": API(base, token), "directory": node_dir,
                              "config": config, "ppm": round(multiplier * 1000000)})
            for sock in reservations:
                sock.close()
            reservations.clear()
            for node in nodes:
                launch_node(node)
            wait_for("all ten user instances acknowledged", lambda: all(confirmed(user["id"], True)
                                                                        for user in users), timeout=30)
            for node in nodes:
                for user in users:
                    streams[(node["id"], user["id"])] = SINGLE.connect_vless(policy_user(node, user["id"]), origin_port)
            print("PASS: one center and five independent configured Agents authenticate ten Xray instances", flush=True)

            payloads = [bytes([index + 65]) * ((index + 1) * 8192) for index in range(5)]
            with ThreadPoolExecutor(max_workers=5) as pool:
                list(pool.map(lambda pair: echo_bytes(streams[(pair[0]["id"], charged["id"])], pair[1]), zip(nodes, payloads)))

            def accounted():
                records = charged["api"].call("GET", "/api/traffic")
                summary = {node["id"]: {"up": 0, "down": 0, "bill": 0} for node in nodes}
                for record in records:
                    row = summary[record["node_id"]]
                    row["up"] += record["upload_bytes"]
                    row["down"] += record["download_bytes"]
                    row["bill"] += record["billed_bytes"]
                for node, data in zip(nodes, payloads):
                    row = summary[node["id"]]
                    if row["up"] < len(data) + 6144 or row["down"] < len(data) + 6144:
                        return None
                    if row["bill"] != (row["up"] + row["down"]) * node["ppm"] // 1000000:
                        return None
                sub = charged["api"].call("GET", "/api/subscription")["subscription"]
                if sub["traffic_used_bytes"] != sum(row["bill"] for row in summary.values()):
                    return None
                return sub, records

            subscription, records = wait_for("five-node weighted usage in one account", accounted)
            assert subscription["service_enabled"] is True
            original_used = subscription["traffic_used_bytes"]
            for node in nodes:
                sessions = {}
                for record in records:
                    if record["node_id"] != node["id"]:
                        continue
                    sample = sessions.setdefault(record["session_id"], {
                        "user_id": charged["id"], "cycle_id": subscription["cycle_id"],
                        "session_id": record["session_id"], "upload_bytes": 0, "download_bytes": 0})
                    sample["upload_bytes"] += record["upload_bytes"]
                    sample["download_bytes"] += record["download_bytes"]
                for _ in range(2):
                    node["api"].call("POST", "/api/node/traffic", {"samples": list(sessions.values())})
                older = [{**sample, "upload_bytes": 0, "download_bytes": 0} for sample in sessions.values()]
                node["api"].call("POST", "/api/node/traffic", {"samples": older})
            assert charged["api"].call("GET", "/api/subscription")["subscription"]["traffic_used_bytes"] == original_used
            print("PASS: five-node upload/download totals, distinct multipliers, duplicate and reordered samples", flush=True)

            # Exhaust through just one node; all five pre-existing connections
            # for this user must close, while the second user's connections work.
            with SINGLE.connect_vless(policy_user(nodes[0], charged["id"]), origin_port) as trigger:
                for _ in range(80):
                    try:
                        echo_bytes(trigger, b"exhaust" * 8192)
                    except (OSError, AssertionError):
                        break
            wait_for("global quota disabled on five nodes", lambda: confirmed(charged["id"], False, "quota_exceeded"))
            with ThreadPoolExecutor(max_workers=5) as pool:
                closed = list(pool.map(lambda node: SINGLE.wait_closed(streams[(node["id"], charged["id"])], 4), nodes))
            assert all(closed), "some existing connections survived global quota stop"
            for node in nodes:
                echo_bytes(streams[(node["id"], unaffected["id"])], b"other-user-stays-connected")
            assert confirmed(unaffected["id"], True)
            print("PASS: exceeding the shared quota on one node closes this user's connections on all five; other user stays online", flush=True)

            admin.call("POST", f"/api/admin/users/{charged['id']}/traffic/reset", {})
            wait_for("five nodes apply new cycle", lambda: confirmed(charged["id"], True))
            nodes[0]["api"].call("POST", "/api/node/traffic", {"samples": [{
                "user_id": charged["id"], "cycle_id": subscription["cycle_id"],
                "session_id": "fleet-old-cycle", "upload_bytes": 99, "download_bytes": 101}]})
            assert charged["api"].call("GET", "/api/subscription")["subscription"]["traffic_used_bytes"] == 0

            SINGLE.stop(nodes[-1]["process"])
            assert SINGLE.wait_closed(streams[(nodes[-1]["id"], unaffected["id"])], 4)
            for node in nodes[:-1]:
                echo_bytes(streams[(node["id"], unaffected["id"])], b"independent-node")
            print("PASS: reset restores the shared cycle, delayed old traffic is isolated, stopping one Agent leaves four nodes working", flush=True)

            SINGLE.stop(center)
            outage_payload = b"meter-offline" * 2048
            for node in nodes[:-1]:
                echo_bytes(streams[(node["id"], unaffected["id"])], outage_payload)
            with ThreadPoolExecutor(max_workers=4) as pool:
                assert all(pool.map(lambda node: SINGLE.wait_closed(streams[(node["id"], unaffected["id"])], 20), nodes[:-1]))
            for node in nodes[:-1]:
                state = json.loads((node["directory"] / "state/state.json").read_text())
                assert any(sample["user_id"] == unaffected["id"]
                           and sample["upload_bytes"] >= len(outage_payload)
                           and sample["download_bytes"] >= len(outage_payload)
                           for sample in state["outbox"].values()), "offline bytes missing at node"
            print("PASS: center outage expires independent node leases and preserves offline usage locally", flush=True)

            center = launch_center()
            launch_node(nodes[-1])
            wait_for("center recovery and all node reports", lambda: all(confirmed(user["id"], True)
                                                                        for user in users), timeout=30)

            def offline_reports_recovered():
                totals = {node["id"]: [0, 0] for node in nodes[:-1]}
                for record in unaffected["api"].call("GET", "/api/traffic"):
                    if record["node_id"] in totals:
                        totals[record["node_id"]][0] += record["upload_bytes"]
                        totals[record["node_id"]][1] += record["download_bytes"]
                return all(upload >= len(outage_payload) and download >= len(outage_payload)
                           for upload, download in totals.values())

            wait_for("persisted offline bytes replayed into center accounting", offline_reports_recovered)
            for node in nodes:
                with SINGLE.connect_vless(policy_user(node, unaffected["id"]), origin_port) as resumed:
                    echo_bytes(resumed, b"center-and-node-recovered")
            print("PASS: center database and node restart recover the fleet and replay persisted offline bytes", flush=True)
        finally:
            for stream in streams.values():
                stream.close()
            for process in reversed(processes):
                SINGLE.stop(process)
            for sock in reservations:
                sock.close()
            echo.shutdown()
            echo.server_close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--backend", default=str(ROOT / ".local/cloud"))
    parser.add_argument("--agent", default=str(ROOT / ".local/node-agent"))
    parser.add_argument("--core", required=True, help="Official Xray executable")
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="nicecloud-five-nodes-") as temporary:
        directory = Path(temporary)
        try:
            run(args, directory)
        except Exception:
            for path in [directory / "center.log", *directory.glob("node-*/agent.log"),
                         *directory.glob("node-*/state/user-*.log")]:
                if path.exists():
                    print(f"\n{path.relative_to(directory)}:\n{path.read_text(errors='replace')[-6000:]}", flush=True)
            raise
    print("Five-node fleet smoke checks passed.", flush=True)


if __name__ == "__main__":
    main()
