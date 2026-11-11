import argparse
import collections
import datetime
import hashlib
import ipaddress
import json
import math
from pathlib import Path
import re
import shlex
import socket
import subprocess
import time
import urllib.parse
import uuid


TARGET = "push2his.eastmoney.com:443"
PINNED_TARGET = None
ZONE = datetime.timezone(datetime.timedelta(hours=8))
STOCKS = [(1, "600519"), (0, "000001"), (0, "300750"), (1, "688981")]
DAILY_DATES = [
    "2026-10-26", "2026-10-27", "2026-10-28", "2026-10-29", "2026-10-30",
    "2026-11-02", "2026-11-03", "2026-11-04", "2026-11-05",
]


def config_quote(value):
    return str(value).replace("\\", "\\\\").replace('"', '\\"').replace("\r", "\\r").replace("\n", "\\n")


def ssh_options(env_path):
    values = {}
    for line in Path(env_path).read_text().splitlines():
        line = line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        name, raw = line.removeprefix("export ").split("=", 1)
        parts = shlex.split(raw, comments=True)
        values[name.strip()] = parts[0] if parts else ""
    key_path = Path(values["SSH_KEY_PRIVATE"]).expanduser()
    if not key_path.is_absolute():
        key_path = Path(env_path).parent / key_path
    return [
        "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10",
        "-o", "StrictHostKeyChecking=yes", "-i", str(key_path),
        "-p", values.get("PORT", "22"), values["USER"] + "@" + values["HOST"],
    ]


def load_token(env_path, remote_config):
    remote_code = (
        "import json;from pathlib import Path;"
        "config=json.loads(Path(" + repr(remote_config) + ").read_text());"
        "print(json.dumps({'api_token':config['api_token']}))"
    )
    reply = subprocess.run(
        ssh_options(env_path) + ["python3 -c " + shlex.quote(remote_code)],
        capture_output=True, text=True, timeout=25,
    )
    if reply.returncode:
        raise RuntimeError("SSH credential loading failed")
    return json.loads(reply.stdout)["api_token"]


def open_control_tunnel(env_path):
    with socket.socket() as reservation:
        reservation.bind(("127.0.0.1", 0))
        port = reservation.getsockname()[1]
    base = ssh_options(env_path)
    process = subprocess.Popen(
        base[:-1] + [
            "-o", "ExitOnForwardFailure=yes", "-o", "ServerAliveInterval=15",
            "-N", "-L", "127.0.0.1:" + str(port) + ":127.0.0.1:8080", base[-1],
        ], stdout=subprocess.DEVNULL, stderr=subprocess.PIPE,
    )
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline and process.poll() is None:
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=0.3):
                return process, "http://127.0.0.1:" + str(port)
        except OSError:
            time.sleep(0.2)
    process.terminate()
    process.wait(timeout=5)
    raise RuntimeError("SSH control tunnel setup failed")


class Recorder:
    def __init__(self, path):
        self.stream = Path(path).open("x", encoding="utf-8")

    def emit(self, kind, **details):
        entry = {"at": datetime.datetime.now(ZONE).isoformat(), "kind": kind, **details}
        text = json.dumps(entry, ensure_ascii=False)
        print(text, flush=True)
        self.stream.write(text + "\n")
        self.stream.flush()


class Pool:
    def __init__(self, base, token, recorder):
        self.base = base.rstrip("/")
        self.token = token
        self.recorder = recorder

    def call(self, path, body=None, timeout=20, method=None):
        settings = 'header = "' + config_quote("Authorization: Bearer " + self.token) + '"\n'
        if body is not None:
            settings += 'header = "Content-Type: application/json"\n'
            settings += 'data = "' + config_quote(json.dumps(body)) + '"\n'
        if method:
            settings += 'request = "' + config_quote(method) + '"\n'
        started = time.monotonic()
        reply = subprocess.run(
            [
                "curl", "-q", "--noproxy", "*", "--connect-timeout", "8",
                "--max-time", str(timeout), "--max-filesize", "1048576",
                "--compressed", "-sS", "-A", "proxy-pool-research/1", "-K", "-",
                "-w", "\n__STATUS__:%{http_code}", self.base + path,
            ],
            input=settings, capture_output=True, text=True, timeout=timeout + 3,
        )
        response, _, status = reply.stdout.rpartition("\n__STATUS__:")
        try:
            payload = json.loads(response)
        except (ValueError, TypeError):
            payload = None
        if reply.returncode or not status.startswith("2"):
            self.recorder.emit(
                "pool_error", path=path, http_status=status, curl_exit=reply.returncode,
                duration=round(time.monotonic() - started, 3),
                error_code=payload.get("error", {}).get("code")
                if isinstance(payload, dict) and isinstance(payload.get("error"), dict) else None,
            )
            return None
        return payload

    def prewarm(self, source, count, timeout):
        body = {
            "count": count, "timeout": timeout, "source_ids": [source],
            "validation": {"mode": "tls", "target": TARGET},
        }
        job = self.call("/api/v1/prewarm", body)
        if not isinstance(job, dict) or not job.get("job_id"):
            return None
        job_id = job["job_id"]
        self.recorder.emit("prewarm_created", source=source, request=body, job=job)
        deadline = time.monotonic() + timeout + 20
        last_state = None
        while time.monotonic() < deadline:
            job = self.call("/api/v1/jobs/" + job_id)
            if not isinstance(job, dict):
                break
            state = (job.get("state"), job.get("ready"), job.get("probed"), job.get("failed"))
            if state != last_state:
                self.recorder.emit("prewarm_progress", source=source, job=job)
                last_state = state
            if job.get("state") in {"COMPLETED", "PARTIAL", "FAILED", "CANCELLED", "INTERRUPTED"}:
                return job
            time.sleep(3)
        cancelled = self.call("/api/v1/jobs/" + job_id + "/cancel", {})
        self.recorder.emit("prewarm_cancel_requested", source=source, confirmed=cancelled is not None, job=cancelled)
        return cancelled


def lease_time(raw):
    normalized = re.sub(
        r"\.(\d+)(?=Z$|[+-]\d{2}:\d{2}$)",
        lambda match: "." + (match.group(1) + "000000")[:6], raw,
    )
    return datetime.datetime.fromisoformat(normalized.replace("Z", "+00:00")).timestamp()


def expected_minutes():
    clocks = []
    for hour, minute in [(9, 35), (13, 5)]:
        start = datetime.datetime(2026, 11, 5, hour, minute)
        clocks.extend((start + datetime.timedelta(minutes=5 * index)).strftime("%Y-%m-%d %H:%M") for index in range(24))
    return clocks


def validate_bars(response, market, code, frequency):
    try:
        payload = json.loads(response)
    except (ValueError, TypeError):
        return {"success": False, "reason": "non_json"}
    if not isinstance(payload, dict):
        return {"success": False, "reason": "non_object_json"}
    data = payload.get("data")
    if payload.get("rc") != 0 or not isinstance(data, dict):
        return {"success": False, "reason": "upstream_error_or_null_data", "upstream_rc": payload.get("rc")}
    if data.get("code") != code or data.get("market") != market:
        return {"success": False, "reason": "stock_identity_mismatch"}
    bars = data.get("klines")
    expected = DAILY_DATES if frequency == "101" else expected_minutes()
    if not isinstance(bars, list) or len(bars) != len(expected):
        return {"success": False, "reason": "bar_count_mismatch", "bars": len(bars) if isinstance(bars, list) else None}
    actual = []
    for raw in bars:
        if not isinstance(raw, str):
            return {"success": False, "reason": "bar_shape_mismatch"}
        fields = raw.split(",")
        if len(fields) != 11:
            return {"success": False, "reason": "bar_shape_mismatch"}
        actual.append(fields[0])
        try:
            numbers = [float(value) for value in fields[1:]]
        except (ValueError, TypeError):
            return {"success": False, "reason": "non_numeric_bar"}
        opening, closing, high, low, volume, amount = numbers[:6]
        if not all(math.isfinite(value) for value in numbers):
            return {"success": False, "reason": "non_finite_bar"}
        if not 0 < low <= min(opening, closing) <= max(opening, closing) <= high or min(volume, amount) < 0:
            return {"success": False, "reason": "invalid_ohlc_or_volume"}
    if actual != expected:
        return {"success": False, "reason": "bar_dates_or_times_mismatch"}
    return {
        "success": True, "reason": "valid_complete_klines", "bars": len(bars),
        "first_bar": bars[0], "last_bar": bars[-1],
        "body_sha256": hashlib.sha256(response.encode()).hexdigest(),
    }


def business_request(item, market, code, frequency):
    usable = lease_time(item["lease_expires_at"]) - time.time() - 2
    if usable < 2:
        return {"success": False, "reason": "lease_too_short", "attempted": False}
    timeout = min(12, usable)
    params = {
        "secid": str(market) + "." + code, "klt": frequency, "fqt": "0",
        "beg": "20261026" if frequency == "101" else "20261105", "end": "20261105",
        "fields1": "f1,f2,f3,f4,f5,f6",
        "fields2": "f51,f52,f53,f54,f55,f56,f57,f58,f59,f60,f61",
    }
    uri = item["proxy_uri"]
    if uri.startswith("socks5://"):
        uri = "socks5h://" + uri[len("socks5://"):]
    settings = 'proxy = "' + config_quote(uri) + '"\n'
    connect_options = []
    if PINNED_TARGET:
        connect_options = ["--connect-to", "push2his.eastmoney.com:443:" + PINNED_TARGET + ":443"]
    metrics_format = (
        '\n__METRICS__:{"http_status":%{http_code},"connect_status":%{http_connect},'
        '"connect_seconds":%{time_connect},"tls_seconds":%{time_appconnect},'
        '"first_byte_seconds":%{time_starttransfer},"total_seconds":%{time_total},'
        '"download_bytes":%{size_download},"tls_verify_result":%{ssl_verify_result}}'
    )
    reply = subprocess.run(
        [
            "curl", "-q", "--noproxy", "", "--http1.1", "--connect-timeout", "6",
            "--max-time", str(round(timeout, 3)), "--max-filesize", "1048576",
            "--compressed", "-sS", "-K", "-", "-H", "Accept: application/json",
            "-A", "Mozilla/5.0 ShadowFlow/0.1", "-e", "https://quote.eastmoney.com/",
            "-w", metrics_format,
        ] + connect_options + [
            "https://push2his.eastmoney.com/api/qt/stock/kline/get?" + urllib.parse.urlencode(params),
        ],
        input=settings, capture_output=True, text=True, timeout=timeout + 3,
    )
    response, _, raw_metrics = reply.stdout.rpartition("\n__METRICS__:")
    try:
        metrics = json.loads(raw_metrics.replace('":000', '":0'))
    except ValueError:
        metrics = {}
    result = {"attempted": True, "curl_exit": reply.returncode, **metrics}
    if reply.returncode:
        reason = {
            5: "proxy_dns_failure", 6: "target_dns_failure", 7: "connect_failure",
            28: "timeout", 35: "tls_handshake_failure", 52: "empty_reply",
            56: "receive_or_connect_tunnel_failure", 60: "certificate_verification_failure",
            63: "response_too_large", 97: "proxy_handshake_failure",
        }.get(reply.returncode, "curl_transport_failure")
        if metrics.get("connect_status") not in {None, 0, 200}:
            reason = "proxy_connect_http_" + str(metrics["connect_status"])
        result.update(success=False, reason=reason)
    elif metrics.get("http_status") != 200:
        result.update(success=False, reason="target_http_" + str(metrics.get("http_status")))
    else:
        result.update(validate_bars(response, market, code, frequency))
    return result


class Experiment:
    def __init__(self, pool, recorder, args):
        self.pool, self.recorder, self.args = pool, recorder, args
        self.current = None
        self.current_label = None
        self.current_successes = 0
        self.acquire_count = 0
        self.labels = set()
        self.logical_results = []
        self.business_results = []
        self.started = time.monotonic()
        self.successes_by_proxy = collections.Counter()
        self.session_id = "eastmoney-probe-" + uuid.uuid4().hex[:16] if getattr(args, "sticky", False) else None
        self.binding_known = False

    def drop_binding(self, reason):
        if not self.session_id or not self.binding_known:
            return
        response = self.pool.call("/api/v1/sessions/" + self.session_id, method="DELETE")
        self.recorder.emit("binding_released", session_id=self.session_id, reason=reason, response=response)
        self.binding_known = False

    def retire(self, reason):
        if self.current is None:
            if reason == "finished":
                self.drop_binding(reason)
            return
        lease_id = self.current["lease_id"]
        outcome = "failure" if reason not in {"finished", "lease_expiring"} else "success"
        if outcome == "failure" or self.current_successes:
            self.pool.call("/api/v1/leases/" + lease_id + "/feedback", {
                "outcome": outcome, "stage": "eastmoney_kline_http",
                "note": reason,
            })
        response = self.pool.call("/api/v1/leases/" + lease_id + "/release", {})
        self.recorder.emit("lease_released", proxy=self.current_label, reason=reason, prior_successes=self.current_successes, response=response)
        self.current = None
        self.current_successes = 0
        if reason != "lease_expiring":
            self.drop_binding(reason)

    def acquire(self):
        if self.acquire_count >= self.args.max_acquires:
            return False
        self.acquire_count += 1
        started = time.monotonic()
        request = {
            "count": 1, "timeout": 30, "cd": 300,
            "source_ids": self.args.sources.split(","),
            "validation": {"mode": "tls", "target": TARGET},
        }
        if self.session_id:
            request.update(mode="sticky", session_id=self.session_id)
        response = self.pool.call("/api/v1/proxies/acquire", request, timeout=38)
        if not isinstance(response, dict) or not response.get("items"):
            self.recorder.emit("supply_unavailable", acquire=self.acquire_count, response=response, duration=round(time.monotonic() - started, 3))
            return False
        item = response["items"][0]
        connection = item["connection"]
        label = hashlib.sha256(json.dumps(connection, sort_keys=True).encode()).hexdigest()[:12]
        self.current, self.current_label = item, label
        self.current_successes = self.successes_by_proxy[label]
        self.binding_known = self.session_id is not None
        self.labels.add(label)
        self.recorder.emit(
            "acquired", acquire=self.acquire_count, proxy=label, source=item["source_id"],
            transport=connection["transport"], validation=item.get("validation"),
            lease_expires_at=item["lease_expires_at"], provider_expires_at=item.get("provider_expires_at"),
            cooldown_seconds=response.get("cooldown_seconds"), duration=round(time.monotonic() - started, 3),
            session_changed=response.get("session_changed"), change_reason=response.get("change_reason"),
            binding_expires_at=response.get("binding_expires_at"),
        )
        return True

    def run(self):
        no_supply_streak = 0
        stop_reason = "completed"
        tasks = [(round_number, market, code, frequency) for round_number in range(1, self.args.rounds + 1) for market, code in STOCKS for frequency in ["101", "5"]]
        try:
            for task_index, (round_number, market, code, frequency) in enumerate(tasks, 1):
                if time.monotonic() - self.started >= self.args.max_runtime:
                    stop_reason = "runtime_budget"
                    break
                attempts = []
                used = set()
                for attempt in range(1, self.args.max_attempts + 1):
                    if self.current and lease_time(self.current["lease_expires_at"]) - time.time() < 14:
                        self.retire("lease_expiring")
                    if self.current is None and not self.acquire():
                        no_supply_streak += 1
                        if no_supply_streak >= 3 or self.acquire_count >= self.args.max_acquires:
                            stop_reason = "supply_or_acquire_budget_exhausted"
                            break
                        time.sleep(3)
                        continue
                    no_supply_streak = 0
                    if self.current_label in used:
                        self.retire("repeated_proxy_for_same_request")
                        continue
                    used.add(self.current_label)
                    result = business_request(self.current, market, code, frequency)
                    event = {
                        "task": task_index, "round": round_number, "code": code,
                        "frequency": "daily" if frequency == "101" else "5m", "attempt": attempt,
                        "proxy": self.current_label, "source": self.current["source_id"],
                        "proxy_prior_successes": self.current_successes, **result,
                    }
                    self.recorder.emit("business_attempt", **event)
                    attempts.append(event)
                    self.business_results.append(event)
                    if result["success"]:
                        self.current_successes += 1
                        self.successes_by_proxy[self.current_label] += 1
                        break
                    self.retire(result["reason"])
                success = bool(attempts and attempts[-1]["success"])
                logical = {
                    "task": task_index, "round": round_number, "code": code,
                    "frequency": "daily" if frequency == "101" else "5m", "success": success,
                    "business_attempts": len(attempts),
                    "recovered_by_switch": success and any(not item["success"] for item in attempts[:-1]),
                }
                self.logical_results.append(logical)
                self.recorder.emit("logical_request", **logical)
                if stop_reason != "completed":
                    break
                if len(self.labels) >= self.args.max_proxies and not any(item["success"] for item in self.business_results):
                    stop_reason = "no_business_success_within_proxy_budget"
                    break
                time.sleep(3)
        except BaseException as error:
            stop_reason = "interrupted" if isinstance(error, KeyboardInterrupt) else "experiment_error"
            self.recorder.emit("experiment_error", error_type=type(error).__name__)
            raise
        finally:
            self.retire("finished")
            summary = {
                "stop_reason": stop_reason, "planned_logical_requests": len(tasks),
                "completed_logical_requests": len(self.logical_results),
                "logical_successes": sum(item["success"] for item in self.logical_results),
                "business_attempts": len(self.business_results),
                "business_successes": sum(item["success"] for item in self.business_results),
                "recovered_requests": sum(item["recovered_by_switch"] for item in self.logical_results),
                "distinct_proxy_connections": len(self.labels), "acquire_calls": self.acquire_count,
                "failure_reasons": dict(collections.Counter(item["reason"] for item in self.business_results if not item["success"])),
                "elapsed_seconds": round(time.monotonic() - self.started, 3),
            }
            self.recorder.emit("summary", **summary)


def main():
    global PINNED_TARGET
    parser = argparse.ArgumentParser()
    parser.add_argument("action", choices=["inspect", "prewarm", "run"])
    parser.add_argument("--ssh-env", required=True)
    parser.add_argument("--remote-config", default="/opt/proxy-pool/config.json")
    parser.add_argument("--pool-url", default="https://proxy-pool.522401.xyz")
    parser.add_argument("--control-ssh", action="store_true")
    parser.add_argument("--pin-target", action="store_true")
    parser.add_argument("--sticky", action="store_true")
    parser.add_argument("--sources", default="spys-http,proxyscrape-elite")
    parser.add_argument("--warm-source", default="spys-http")
    parser.add_argument("--warm-count", type=int, default=1)
    parser.add_argument("--warm-timeout", type=int, default=45)
    parser.add_argument("--warm-before-run", action="store_true")
    parser.add_argument("--rounds", type=int, default=3)
    parser.add_argument("--max-attempts", type=int, default=3)
    parser.add_argument("--max-proxies", type=int, default=12)
    parser.add_argument("--max-acquires", type=int, default=18)
    parser.add_argument("--max-runtime", type=int, default=720)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    recorder = Recorder(args.output)
    if args.pin_target:
        candidates = socket.getaddrinfo("push2his.eastmoney.com", 443, socket.AF_INET, socket.SOCK_STREAM)
        PINNED_TARGET = next(address[4][0] for address in candidates if ipaddress.ip_address(address[4][0]).is_global)
    token = load_token(args.ssh_env, args.remote_config)
    tunnel = None
    try:
        base = args.pool_url
        if args.control_ssh:
            tunnel, base = open_control_tunnel(args.ssh_env)
        pool = Pool(base, token, recorder)
        recorder.emit("run_configuration", action=args.action, target=TARGET, sources=args.sources.split(","), login_cookies=False, direct_fallback=False, tls_verification=True, control_transport="ssh" if args.control_ssh else "public_https", connect_target="resolved_public_ipv4" if PINNED_TARGET else "hostname", pinned_target=PINNED_TARGET, acquisition_mode="sticky" if args.sticky else "ordinary")
        if args.action == "inspect":
            for path in ["/api/v1/admin/system", "/api/v1/admin/projects/default", "/api/v1/admin/jobs?limit=5"]:
                recorder.emit("inspection", path=path, response=pool.call(path))
        elif args.action == "prewarm":
            pool.prewarm(args.warm_source, args.warm_count, args.warm_timeout)
        else:
            if args.warm_before_run:
                pool.prewarm(args.warm_source, args.warm_count, args.warm_timeout)
            Experiment(pool, recorder, args).run()
    finally:
        if tunnel is not None:
            tunnel.terminate()
            tunnel.wait(timeout=5)


if __name__ == "__main__":
    main()
