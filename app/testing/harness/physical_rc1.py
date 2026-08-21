"""RC1 physical acceptance harness with bounded, sanitized diagnostics."""

import argparse
import base64
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import random
import re
import shlex
import sqlite3
import subprocess
import sys
import time


MAX_DIAGNOSTIC_TAIL = 512
MANAGEMENT_PROBE_TIMEOUT_SECONDS = 15
SSH_OPTIONS = [
    "-T",
    "-o", "BatchMode=yes",
    "-o", "ConnectTimeout=8",
    "-o", "ServerAliveInterval=30",
    "-o", "ServerAliveCountMax=3",
    "-o", "StrictHostKeyChecking=accept-new",
]

MANAGEMENT_OPERATIONS = {
    "health": {"expected": 200, "payload": False, "resource": False},
    "create_enrollment": {"expected": 201, "payload": True, "resource": False},
    "list_nodes": {"expected": 200, "payload": False, "resource": False},
    "get_node": {"expected": 200, "payload": False, "resource": True},
    "patch_node": {"expected": 200, "payload": True, "resource": True},
    "list_check_definitions": {"expected": 200, "payload": False, "resource": False},
    "list_check_bundles": {"expected": 200, "payload": False, "resource": False},
    "list_check_policies": {"expected": 200, "payload": False, "resource": False},
    "list_operation_definitions": {"expected": 200, "payload": False, "resource": False},
    "list_operation_runs": {"expected": 200, "payload": False, "resource": False},
    "create_check_run": {"expected": 201, "payload": True, "resource": False},
    "get_check_run": {"expected": 200, "payload": False, "resource": True},
}

REMOTE_MANAGEMENT_PROBE = r'''import base64
import json
import re
import sys
try:
    from urllib import error as urllib_error
    from urllib import request as urllib_request
except ImportError:
    import urllib2 as urllib_request
    urllib_error = urllib_request


def fullmatch(pattern, value):
    return re.match("^(?:" + pattern + ")$", value) is not None

operation, port_text, resource_arg, payload_arg = sys.argv[1:5]
if not resource_arg.startswith("r:") or not payload_arg.startswith("p:"):
    raise SystemExit("argument_encoding_invalid")
resource_id = resource_arg[2:]
payload_text = payload_arg[2:]
operations = {
    "health": ("GET", "/healthz", 200, False, False),
    "create_enrollment": ("POST", "/api/v1/enrollment-tokens", 201, True, False),
    "list_nodes": ("GET", "/api/v1/nodes", 200, False, False),
    "get_node": ("GET", "/api/v1/nodes/{resource}", 200, False, True),
    "patch_node": ("PATCH", "/api/v1/nodes/{resource}", 200, True, True),
    "list_check_definitions": ("GET", "/api/v1/check-definitions", 200, False, False),
    "list_check_bundles": ("GET", "/api/v1/check-bundles", 200, False, False),
    "list_check_policies": ("GET", "/api/v1/check-policies", 200, False, False),
    "list_operation_definitions": ("GET", "/api/v1/operation-definitions", 200, False, False),
    "list_operation_runs": ("GET", "/api/v1/operation-runs?limit=200&offset=0", 200, False, False),
    "create_check_run": ("POST", "/api/v1/check-runs", 201, True, False),
    "get_check_run": ("GET", "/api/v1/check-runs/{resource}", 200, False, True),
}
if operation not in operations:
    raise SystemExit("operation_not_allowed")
if not fullmatch(r"[0-9]{2,5}", port_text) or not 1 <= int(port_text) <= 65535:
    raise SystemExit("port_invalid")
method, path_template, expected, accepts_payload, needs_resource = operations[operation]
if needs_resource:
    if not fullmatch(r"[A-Za-z0-9][A-Za-z0-9._:-]{0,127}", resource_id):
        raise SystemExit("resource_id_invalid")
    path = path_template.format(resource=resource_id)
elif resource_id:
    raise SystemExit("resource_id_not_allowed")
else:
    path = path_template
if accepts_payload:
    if not payload_text:
        raise SystemExit("payload_required")
    body = base64.b64decode(payload_text)
    json.loads(body.decode("utf-8"))
else:
    if payload_text:
        raise SystemExit("payload_not_allowed")
    body = None
headers = {"Content-Type": "application/json"} if body is not None else {}
request = urllib_request.Request(
    "http://127.0.0.1:{0}{1}".format(port_text, path),
    data=body,
    headers=headers,
)
request.get_method = lambda: method
opener = urllib_request.build_opener(urllib_request.ProxyHandler({}))
try:
    response = opener.open(request, timeout=8)
    status = response.getcode()
    contents = response.read()
    response.close()
except urllib_error.HTTPError as response:
    status = response.code
    contents = response.read()
except (urllib_error.URLError, IOError) as error:
    print(json.dumps({"transport_error": type(error).__name__}, separators=(",", ":")))
    raise SystemExit(0)
encoded_contents = base64.b64encode(contents)
if not isinstance(encoded_contents, str):
    encoded_contents = encoded_contents.decode("ascii")
print(json.dumps({
    "status": status,
    "expected": status == expected,
    "body": encoded_contents,
}, separators=(",", ":")))
'''


class HarnessFailure(Exception):
    def __init__(self, code, *, diagnostics=None):
        super().__init__(code)
        self.code = code
        self.diagnostics = diagnostics


def require(condition, code):
    if not condition:
        raise HarnessFailure(code)


def sha256_file(path):
    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def sanitize_diagnostic_tail(value):
    """Return a bounded diagnostic tail without endpoints, accounts, or secrets."""
    if value is None:
        return ""
    if isinstance(value, bytes):
        value = value.decode("utf-8", errors="replace")
    lines = []
    diagnostic = re.compile(
        r"(?i)(error|fail|refus|timed?\s*out|timeout|closed|resolve|bind|forward|listen|permission|denied|connection|network|host\s*key|address\s+already)"
    )
    for line in str(value).splitlines():
        if diagnostic.search(line):
            lines.append(line.strip())
    text = " | ".join(lines[-4:])
    text = re.sub(r"(?i)https?://[^\s]+", "[ENDPOINT]", text)
    text = re.sub(r"\b[A-Za-z_][A-Za-z0-9_.-]*@", "[ACCOUNT]@", text)
    text = re.sub(r"(?<!\d)(?:\d{1,3}\.){3}\d{1,3}(?!\d)", "[ADDRESS]", text)
    text = re.sub(
        r"(?i)\b(authorization|credential|password|token|private[_ -]?key)\b\s*[:=]\s*\S+",
        r"\1=[REDACTED]",
        text,
    )
    text = re.sub(r"(?i)\bsp_(?:enroll|agent)_[A-Za-z0-9._-]+", "[REDACTED]", text)
    return text[-MAX_DIAGNOSTIC_TAIL:]


def management_probe_diagnostics():
    return {
        "stage": "start_management_probe",
        "substage": "remote_loopback_http",
        "exception_type": "",
        "stable_error_code": "",
        "last_observation_code": "",
        "timeout": False,
        "attempt_count": 0,
        "remote_exec": True,
        "elapsed_ms": 0,
        "endpoint_class": "loopback_management",
        "stderr_tail": "",
        "cleanup_result": "not_required",
    }


def _management_probe_failure(code, diagnostics, started_at, clock, exc=None):
    diagnostics["stable_error_code"] = code
    diagnostics["elapsed_ms"] = max(0, int((clock() - started_at) * 1000))
    if exc is not None:
        diagnostics["exception_type"] = type(exc).__name__
        diagnostics["stderr_tail"] = sanitize_diagnostic_tail(str(exc))
    raise HarnessFailure(code, diagnostics=diagnostics) from exc


def probe_management_readiness(
    health_probe,
    *,
    clock=time.monotonic,
    sleeper=time.sleep,
    timeout_seconds=MANAGEMENT_PROBE_TIMEOUT_SECONDS,
):
    """Prove the fixed Management health operation over remote loopback."""
    diagnostics = management_probe_diagnostics()
    started_at = clock()
    deadline = started_at + timeout_seconds
    probe_failed = False
    while clock() < deadline:
        diagnostics["attempt_count"] += 1
        try:
            if health_probe():
                diagnostics["stable_error_code"] = ""
                diagnostics["elapsed_ms"] = max(0, int((clock() - started_at) * 1000))
                return diagnostics
        except HarnessFailure as exc:
            probe_failed = True
            diagnostics["exception_type"] = type(exc).__name__
            diagnostics["stderr_tail"] = sanitize_diagnostic_tail(str(exc))
        except Exception as exc:
            _management_probe_failure("unexpected_harness_exception", diagnostics, started_at, clock, exc)
        probe_failed = True
        sleeper(0.1)

    diagnostics["timeout"] = True
    if probe_failed:
        _management_probe_failure("management_probe_failed", diagnostics, started_at, clock)
    _management_probe_failure("management_probe_timeout", diagnostics, started_at, clock)


def final_verdict(*, succeeded, executed, cleaned):
    execution = "EXECUTED" if executed else "NOT_EXECUTED"
    if succeeded and executed and cleaned:
        return "PASS / EXECUTED / CLEANED"
    cleanup = "CLEANED" if cleaned else "CLEANUP_INCOMPLETE"
    return "FAIL / {0} / {1}".format(execution, cleanup)


def failure_domain_for(summary):
    if summary.get("product_run_created", False):
        return "product_acceptance"
    return "physical_harness"


def run_command(args, *, input_bytes=None, timeout=60, cwd=None, ok=(0,)):
    try:
        completed = subprocess.run(
            args,
            input=input_bytes,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=timeout,
            cwd=cwd,
            check=False,
        )
    except subprocess.TimeoutExpired as exc:
        raise HarnessFailure("command_timeout") from exc
    if completed.returncode not in ok:
        raise HarnessFailure("command_failed")
    return completed.stdout


def classify_gitleaks_findings(findings, raw_dir, run_id):
    run_path = (Path(raw_dir) / "run-final.json").resolve()
    expected_key = "rc1-" + run_id.lower()
    try:
        run_resource = json.loads(run_path.read_text(encoding="utf-8"))
        actual_key = run_resource["metadata"]["idempotency_key"]
    except (OSError, KeyError, TypeError, json.JSONDecodeError):
        actual_key = None

    accepted = 0
    unresolved = []
    for finding in findings:
        try:
            finding_path = Path(finding.get("File", "")).resolve()
        except (OSError, RuntimeError):
            finding_path = None
        known_idempotency_key = (
            finding.get("RuleID") == "generic-api-key"
            and finding_path == run_path
            and finding.get("StartLine") == 1
            and finding.get("EndLine") == 1
            and finding.get("Match") == 'idempotency_key\":\"REDACTED\"'
            and finding.get("Secret") == "REDACTED"
            and actual_key == expected_key
        )
        if known_idempotency_key:
            accepted += 1
        else:
            unresolved.append(finding)
    return unresolved, accepted


class NodeRun:
    def __init__(self, args):
        self.args = args
        self.host_target = args.ssh_user + "@" + args.host
        self.remote_dir = "/var/tmp/setpoint-m2-rc1-" + args.run_id
        self.remote_mgmt_port = random.SystemRandom().randint(42000, 47999)
        self.remote_agent_port = random.SystemRandom().randint(48000, 54999)
        self.raw_dir = Path(args.work_root) / "raw" / args.run_id
        self.summary_dir = Path(args.work_root) / "summaries"
        self.summary_path = self.summary_dir / (args.run_id + ".json")
        self.gitleaks_report = self.summary_dir / (args.run_id + "-gitleaks-redacted.json")
        self.server_pid = None
        self.agent_pid = None
        self.node_id = None
        self.server_started = False
        self.agent_started = False
        self.remote_dir_created = False
        self.token_created = False
        self.raw_files = []
        self.stage = "initialize"
        self.error_code = ""
        self.failure_stage = ""
        self.summary = {
            "run_id": args.run_id,
            "node_alias": args.alias_name,
            "execution_head": args.expected_head,
            "verdict": "FAIL",
            "executed": False,
            "execution_phase": "pre_product_task",
            "failure_domain": "",
            "product_run_created": False,
            "product_tasks_created": 0,
            "cleaned": False,
            "catalog": {},
            "task_contract": {},
            "sqlite": {},
            "privacy": {},
            "cleanup": {},
            "system_state_unchanged": False,
            "task_replay": None,
            "system_writes": None,
            "management_probe": {},
        }
        self.remote_paths = {
            "server_bin": self.remote_dir + "/setpoint-server",
            "agent_bin": self.remote_dir + "/setpoint-agent",
            "server_config": self.remote_dir + "/server.json",
            "agent_config": self.remote_dir + "/agent.json",
            "server_log": self.remote_dir + "/server.log",
            "agent_log": self.remote_dir + "/agent.log",
            "database": self.remote_dir + "/setpoint.db",
            "identity": self.remote_dir + "/agent-id",
            "credential": self.remote_dir + "/agent-credential.json",
            "journal": self.remote_dir + "/task-journal.json",
            "token": self.remote_dir + "/enrollment.token",
        }

    def ssh_args(self):
        return ["ssh"] + SSH_OPTIONS + [self.host_target]

    def ssh_command(self, command, *, input_bytes=None, timeout=60, ok=(0,)):
        return run_command(self.ssh_args() + [command], input_bytes=input_bytes, timeout=timeout, ok=ok)

    def remote_shell(self, script, args=(), timeout=60):
        command = self.ssh_args() + ["sh", "-s", "--"] + list(args)
        return run_command(command, input_bytes=script.encode("utf-8"), timeout=timeout)

    def remote_python(self, code, args=(), timeout=60):
        command = self.ssh_args() + [self.remote_python_name, "-"] + list(args)
        output = run_command(command, input_bytes=code.encode("utf-8"), timeout=timeout)
        return json.loads(output.decode("utf-8"))

    def scp_to(self, local_path, remote_path):
        run_command(
            ["scp", "-q", "-o", "BatchMode=yes", "-o", "ConnectTimeout=8", "-o", "StrictHostKeyChecking=accept-new",
             str(local_path), self.host_target + ":" + remote_path],
            timeout=120,
        )

    def scp_from(self, remote_path, local_path):
        run_command(
            ["scp", "-q", "-o", "BatchMode=yes", "-o", "ConnectTimeout=8", "-o", "StrictHostKeyChecking=accept-new",
             self.host_target + ":" + remote_path, str(local_path)],
            timeout=120,
        )

    def remote_write(self, path, contents, mode="600"):
        command = "umask 077; cat > {0} && chmod {1} {0}".format(shlex.quote(path), mode)
        self.ssh_command(command, input_bytes=contents, timeout=30)

    def management_request(self, operation, *, resource_id="", payload=None):
        contract = MANAGEMENT_OPERATIONS.get(operation)
        require(contract is not None, "management_operation_not_allowed")
        require(bool(resource_id) == contract["resource"], "management_resource_contract_invalid")
        if resource_id:
            require(
                re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._:-]{0,127}", resource_id) is not None,
                "management_resource_id_invalid",
            )
        require((payload is not None) == contract["payload"], "management_payload_contract_invalid")
        encoded_payload = ""
        if payload is not None:
            encoded_payload = base64.b64encode(
                json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
            ).decode("ascii")
        response = self.remote_python(
            REMOTE_MANAGEMENT_PROBE,
            [operation, str(self.remote_mgmt_port), "r:" + resource_id, "p:" + encoded_payload],
            timeout=15,
        )
        require("transport_error" not in response, "management_transport_failed")
        require(response.get("expected") is True, "http_status_unexpected")
        contents = base64.b64decode(response.get("body", ""), validate=True)
        return json.loads(contents.decode("utf-8")) if contents else None

    def preflight(self):
        self.stage = "remote_preflight"
        script = r'''set -eu
dir=$1
mgmt=$2
agent=$3
uid_value=$(id -u)
arch_value=$(uname -m)
os_id=unknown
os_version=unknown
if [ -r /etc/os-release ]; then . /etc/os-release; os_id=${ID:-unknown}; os_version=${VERSION_ID:-unknown}; fi
python_name=
if command -v python3 >/dev/null 2>&1; then python_name=python3; elif command -v python >/dev/null 2>&1; then python_name=python; fi
setpoint_count=0
for proc in /proc/[0-9]*; do
  [ -r "$proc/comm" ] || continue
  name=$(cat "$proc/comm" 2>/dev/null || true)
  case "$name" in setpoint-server|setpoint-agent) setpoint_count=$((setpoint_count+1));; esac
done
port_count=0
while IFS= read -r local_address; do
  case "$local_address" in *:$mgmt|*:$agent) port_count=$((port_count+1));; esac
done <<EOF
$(ss -H -lnt 2>/dev/null | awk '{print $4}')
EOF
dir_exists=0
[ -e "$dir" ] && dir_exists=1
printf 'uid=%s\narch=%s\nos_id=%s\nos_version=%s\npython=%s\nsetpoint_processes=%s\nplanned_ports=%s\ndir_exists=%s\nhostname_digest=%s\n' \
  "$uid_value" "$arch_value" "$os_id" "$os_version" "$python_name" "$setpoint_count" "$port_count" "$dir_exists" \
  "$(hostname | sha256sum | awk '{print $1}')"
'''
        output = self.remote_shell(
            script,
            [self.remote_dir, str(self.remote_mgmt_port), str(self.remote_agent_port)],
        ).decode("utf-8")
        values = {}
        for line in output.splitlines():
            if "=" in line:
                key, value = line.split("=", 1)
                values[key.strip()] = value.strip()
        require(values.get("uid") == "0", "identity_not_root")
        require(values.get("arch") == "x86_64", "architecture_mismatch")
        require(self.args.expected_os.lower() in values.get("os_id", "").lower(), "os_mismatch")
        require(values.get("python") in ("python3", "python"), "remote_python_unavailable")
        require(values.get("setpoint_processes") == "0", "setpoint_residue_detected")
        require(values.get("planned_ports") == "0", "planned_port_conflict")
        require(values.get("dir_exists") == "0", "remote_task_directory_exists")
        self.remote_python_name = values["python"]
        self.summary["platform"] = {
            "os_id": values.get("os_id"),
            "os_version": values.get("os_version"),
            "arch": values.get("arch"),
            "root": True,
            "hostname_verified_by_digest": bool(values.get("hostname_digest")),
        }

    def create_remote_dir(self):
        self.stage = "create_remote_dir"
        script = r'''set -eu
d=$1
[ ! -e "$d" ]
mkdir -- "$d"
chmod 700 -- "$d"
'''
        self.remote_shell(script, [self.remote_dir])
        self.remote_dir_created = True

    def state_fingerprint(self):
        script = r'''set -eu
nginx_mode=$1
file_digest=$({
  for f in /etc/login.defs /etc/security/pwquality.conf /etc/profile /etc/motd /etc/passwd /etc/group /etc/shadow /etc/gshadow /etc/ssh/sshd_config /etc/sysctl.conf; do
    if [ -e "$f" ] || [ -L "$f" ]; then
      stat -Lc '%n|%F|%a|%u|%g|%s|%Y' -- "$f" 2>/dev/null || true
      if [ -f "$f" ]; then sha256sum -- "$f" 2>/dev/null || true; fi
    else
      printf '%s|missing\n' "$f"
    fi
  done
  for d in /etc/sysctl.d /run/sysctl.d /usr/local/lib/sysctl.d /usr/lib/sysctl.d /lib/sysctl.d; do
    if [ -d "$d" ]; then
      find "$d" -maxdepth 1 -type f -name '*.conf' -print0 2>/dev/null | sort -z | xargs -0 -r sha256sum 2>/dev/null || true
    fi
  done
} | sha256sum | awk '{print $1}')
sysctl_digest=$({
  for key in net.ipv4.conf.all.accept_source_route net.ipv4.conf.default.accept_source_route net.ipv4.conf.all.accept_redirects net.ipv4.conf.default.accept_redirects net.ipv4.conf.all.send_redirects net.ipv4.conf.default.send_redirects; do
    printf '%s=' "$key"; sysctl -n "$key" 2>/dev/null || printf 'unavailable'; printf '\n'
  done
} | sha256sum | awk '{print $1}')
sshd_digest=$((command -v sshd >/dev/null 2>&1 && sshd -T 2>/dev/null || true) | sha256sum | awk '{print $1}')
service_digest=$({ for unit in sshd ssh nginx auditd rsyslog; do printf '%s=' "$unit"; systemctl is-active "$unit" 2>/dev/null || true; done; } | sha256sum | awk '{print $1}')
nginx_digest=not_applicable
if [ "$nginx_mode" = yes ] && [ -x /usr/bin/nginx ]; then
  nginx_digest=$(/usr/bin/nginx -T 2>&1 | sha256sum | awk '{print $1}')
fi
printf 'file_digest=%s\nsysctl_digest=%s\nsshd_digest=%s\nservice_digest=%s\nnginx_digest=%s\n' \
  "$file_digest" "$sysctl_digest" "$sshd_digest" "$service_digest" "$nginx_digest"
'''
        output = self.remote_shell(script, ["yes" if self.args.expected_root else "no"], timeout=120).decode("utf-8")
        return dict(line.split("=", 1) for line in output.splitlines() if "=" in line)

    def deploy(self):
        self.stage = "deploy"
        self.scp_to(self.args.server_bin, self.remote_paths["server_bin"])
        self.scp_to(self.args.agent_bin, self.remote_paths["agent_bin"])
        script = r'''set -eu
server=$1
agent=$2
chmod 700 -- "$server" "$agent"
printf 'server_sha=%s\nagent_sha=%s\nserver_mode=%s\nagent_mode=%s\n' \
  "$(sha256sum "$server" | awk '{print $1}')" "$(sha256sum "$agent" | awk '{print $1}')" \
  "$(stat -c %a "$server")" "$(stat -c %a "$agent")"
'''
        output = self.remote_shell(
            script, [self.remote_paths["server_bin"], self.remote_paths["agent_bin"]]
        ).decode("utf-8")
        values = dict(line.split("=", 1) for line in output.splitlines() if "=" in line)
        require(values.get("server_sha") == self.args.server_sha, "remote_server_sha_mismatch")
        require(values.get("agent_sha") == self.args.agent_sha, "remote_agent_sha_mismatch")
        require(values.get("server_mode") == "700" and values.get("agent_mode") == "700", "remote_binary_mode_invalid")
        self.summary["binaries"] = {
            "server_sha256": self.args.server_sha,
            "agent_sha256": self.args.agent_sha,
            "remote_sha_verified": True,
            "vcs_revision": self.args.expected_head,
            "vcs_modified": False,
        }

    def write_configs(self):
        server_config = {
            "management_listen_address": "127.0.0.1:{0}".format(self.remote_mgmt_port),
            "agent_listen_address": "127.0.0.1:{0}".format(self.remote_agent_port),
            "database_path": self.remote_paths["database"],
            "offline_after": "15s",
            "shutdown_timeout": "10s",
            "read_header_timeout": "5s",
            "idle_timeout": "60s",
        }
        agent_config = {
            "server_url": "http://127.0.0.1:{0}".format(self.remote_agent_port),
            "identity_path": self.remote_paths["identity"],
            "credential_path": self.remote_paths["credential"],
            "task_journal_path": self.remote_paths["journal"],
            "heartbeat_interval": "2s",
            "task_poll_interval": "1s",
            "command_timeout": "30s",
            "request_timeout": "5s",
            "retry_max_attempts": 3,
            "retry_initial_delay": "500ms",
            "retry_max_delay": "3s",
            "reconnect_initial_delay": "1s",
            "reconnect_max_delay": "5s",
        }
        self.remote_write(self.remote_paths["server_config"], json.dumps(server_config, separators=(",", ":")).encode("utf-8"))
        self.remote_write(self.remote_paths["agent_config"], json.dumps(agent_config, separators=(",", ":")).encode("utf-8"))

    def launch_remote_process(self, binary_key, config_key, log_key):
        binary = self.remote_paths[binary_key]
        config = self.remote_paths[config_key]
        log = self.remote_paths[log_key]
        command = (
            "umask 077; nohup {binary} -config {config} >> {log} 2>&1 < /dev/null & printf '%s\\n' $!"
        ).format(binary=shlex.quote(binary), config=shlex.quote(config), log=shlex.quote(log))
        output = self.ssh_command(command, timeout=30).decode("utf-8").strip()
        require(output.isdigit(), "remote_pid_missing")
        return int(output)

    def remote_port_count(self, *ports):
        script = r'''set -eu
count=0
while IFS= read -r local_address; do
  for port in "$@"; do case "$local_address" in *:$port) count=$((count+1));; esac; done
done <<EOF
$(ss -H -lnt 2>/dev/null | awk '{print $4}')
EOF
printf '%s\n' "$count"
'''
        output = self.remote_shell(script, [str(value) for value in ports]).decode("utf-8").strip()
        return int(output)

    def start_server(self):
        self.stage = "start_server"
        self.server_pid = self.launch_remote_process("server_bin", "server_config", "server_log")
        self.server_started = True
        deadline = time.time() + 20
        while time.time() < deadline:
            if self.remote_port_count(self.remote_mgmt_port, self.remote_agent_port) == 2:
                return
            time.sleep(0.5)
        raise HarnessFailure("server_listeners_unavailable")

    def start_management_probe(self):
        self.stage = "start_management_probe"
        try:
            diagnostics = probe_management_readiness(
                lambda: self.management_request("health").get("status") == "ok",
            )
            self.summary["management_probe"] = diagnostics
        except HarnessFailure as exc:
            self.summary["management_probe"] = exc.diagnostics or management_probe_diagnostics()
            raise

    def enroll_agent(self):
        self.stage = "enroll_agent"
        enrollment = self.management_request(
            "create_enrollment",
            payload={"api_version": "setpoint.io/v1", "kind": "EnrollmentToken", "spec": {"expires_in": "30m", "max_uses": 1}},
        )
        token = enrollment.get("secret", "")
        require(token.startswith("spe.") and len(token) > 70, "enrollment_token_invalid")
        self.token_created = True
        self.remote_write(self.remote_paths["token"], token.encode("utf-8"))
        command = (
            "umask 077; SETPOINT_AGENT_ENROLLMENT_TOKEN=$(cat {token}); export SETPOINT_AGENT_ENROLLMENT_TOKEN; "
            "nohup {agent} -config {config} > {log} 2>&1 < /dev/null & printf '%s\\n' $!"
        ).format(
            token=shlex.quote(self.remote_paths["token"]),
            agent=shlex.quote(self.remote_paths["agent_bin"]),
            config=shlex.quote(self.remote_paths["agent_config"]),
            log=shlex.quote(self.remote_paths["agent_log"]),
        )
        output = self.ssh_command(command, timeout=30).decode("utf-8").strip()
        require(output.isdigit(), "agent_pid_missing")
        self.agent_pid = int(output)
        self.agent_started = True
        token = None

        deadline = time.time() + 60
        node = None
        while time.time() < deadline:
            response = self.management_request("list_nodes")
            nodes = response.get("nodes", [])
            if len(nodes) == 1 and nodes[0].get("status") == "online":
                node = nodes[0]
                break
            time.sleep(1)
        require(node is not None, "agent_did_not_become_online")
        require(node.get("arch") == "amd64", "agent_arch_mismatch")
        self.node_id = node.get("id")
        require(bool(self.node_id), "node_id_missing")

        privacy = self.remote_secret_scan(expect_token_file=True)
        require(privacy["token_hits_outside_token_file"] == 0, "enrollment_token_leak")
        require(privacy["credential_hits_outside_credential_file"] == 0, "agent_credential_leak")
        require(not privacy["enrollment_environment_present"], "enrollment_environment_residue")
        require(privacy["credential_mode"] == "600", "credential_mode_invalid")
        self.remote_shell(r'''set -eu
path=$1
[ -f "$path" ]
unlink -- "$path"
[ ! -e "$path" ]
''', [self.remote_paths["token"]])

    def remote_secret_scan(self, expect_token_file=False):
        code = r'''import json, os, re, sys
token_path, credential_path, agent_log, server_log, agent_config, server_config, journal, pid_text = sys.argv[1:]
token = b''
if os.path.exists(token_path):
    token = open(token_path, 'rb').read().strip()
credential = json.load(open(credential_path, 'r'))
credential_secret = credential['secret'].encode('utf-8')
paths = [agent_log, server_log, agent_config, server_config, journal]
contents = []
for path in paths:
    if os.path.exists(path):
        contents.append(open(path, 'rb').read())
combined = b'\n'.join(contents)
environment = b''
environment_path = '/proc/%s/environ' % pid_text
if os.path.exists(environment_path):
    environment = open(environment_path, 'rb').read()
token_pattern = re.compile(rb'(?:spe|spc)\.[0-9a-f]{32}\.[A-Za-z0-9_-]{40,}')
result = {
    'token_hits_outside_token_file': sum(data.count(token) for data in contents) if token else 0,
    'credential_hits_outside_credential_file': sum(data.count(credential_secret) for data in contents),
    'generic_token_hits': len(token_pattern.findall(combined)),
    'authorization_header_hits': len(re.findall(rb'Authorization\s*:\s*(?:Bearer|Basic)\s+\S+', combined, re.I)),
    'private_key_hits': combined.count(b'PRIVATE KEY-----'),
    'enrollment_environment_present': b'SETPOINT_AGENT_ENROLLMENT_TOKEN=' in environment,
    'credential_environment_present': credential_secret in environment,
    'credential_mode': format(os.stat(credential_path).st_mode & 0o777, 'o'),
}
print(json.dumps(result, sort_keys=True))
'''
        result = self.remote_python(
            code,
            [self.remote_paths["token"], self.remote_paths["credential"], self.remote_paths["agent_log"],
             self.remote_paths["server_log"], self.remote_paths["agent_config"], self.remote_paths["server_config"],
             self.remote_paths["journal"], str(self.agent_pid)],
        )
        if expect_token_file:
            require(self.token_created, "token_scan_state_invalid")
        require(result["generic_token_hits"] == 0, "generic_token_leak")
        require(result["authorization_header_hits"] == 0, "authorization_header_leak")
        require(result["private_key_hits"] == 0, "private_key_leak")
        require(not result["credential_environment_present"], "credential_environment_residue")
        return result

    def configure_trusted_root(self):
        self.stage = "trusted_root_configuration"
        node = self.management_request("get_node", resource_id=self.node_id)
        if not self.args.expected_root:
            require(node.get("trusted_executable_roots", []) == [], "unexpected_trusted_root_on_208")
            self.summary["trusted_root"] = {"configured": False, "frozen_count": 0}
            return
        payload = {"trusted_executable_roots": [self.args.expected_root]}
        node = self.management_request("patch_node", resource_id=self.node_id, payload=payload)
        roots = node.get("trusted_executable_roots", [])
        require(len(roots) == 1, "trusted_root_api_count_invalid")
        root = roots[0]
        require(root.get("path") == self.args.expected_root, "trusted_root_api_path_invalid")
        require(root.get("scope") == "node", "trusted_root_api_scope_invalid")
        require(root.get("source") == "node:" + self.node_id, "trusted_root_api_source_invalid")
        require(root.get("validation_status") == "pending_agent_validation", "trusted_root_api_status_invalid")
        self.validate_nginx_root()
        self.summary["trusted_root"] = {
            "configured": True,
            "via_management_api": True,
            "scope": "node",
            "pending_agent_validation": True,
            "runtime_preflight": True,
        }

    def validate_nginx_root(self):
        script = r'''set -eu
root=$1
candidate=/usr/bin/nginx
[ -d "$root" ]
[ -L "$candidate" ]
target=$(readlink -f "$candidate")
case "$target" in "$root"/*) ;; *) exit 20;; esac
current=
old_ifs=$IFS
IFS=/
for component in ${root#/}; do
  [ -n "$component" ] || continue
  current=$current/$component
  [ "$(stat -c %u "$current")" = 0 ]
  [ -z "$(find "$current" -maxdepth 0 -perm /022 -print 2>/dev/null)" ]
done
IFS=$old_ifs
[ -f "$target" ]
[ -x "$target" ]
[ "$(stat -c %u "$target")" = 0 ]
[ -z "$(find "$target" -maxdepth 0 -perm /022 -print 2>/dev/null)" ]
/usr/bin/nginx -v >/dev/null 2>&1
/usr/bin/nginx -t >/dev/null 2>&1
digest=$(/usr/bin/nginx -T 2>&1 | sha256sum | awk '{print $1}')
printf 'chain_ok=1\nconfig_digest_present=%s\n' "$([ -n "$digest" ] && printf 1 || printf 0)"
'''
        output = self.remote_shell(script, [self.args.expected_root], timeout=120).decode("utf-8")
        values = dict(line.split("=", 1) for line in output.splitlines() if "=" in line)
        require(values.get("chain_ok") == "1" and values.get("config_digest_present") == "1", "nginx_root_preflight_failed")

    def load_catalog(self):
        self.stage = "catalog_audit"
        definitions = self.management_request("list_check_definitions")
        bundles = self.management_request("list_check_bundles")
        policies = self.management_request("list_check_policies")
        operations = self.management_request("list_operation_definitions")
        operation_runs = self.management_request("list_operation_runs")
        require(len(definitions.get("definitions", [])) == 71, "catalog_check_count_invalid")
        require(len(bundles.get("bundles", [])) == 8, "catalog_bundle_count_invalid")
        require(len(policies.get("policies", [])) == 7, "catalog_policy_count_invalid")
        require(len(operations.get("definitions", [])) == 0, "catalog_operation_count_invalid")
        require(len(operation_runs.get("runs", [])) == 0, "operation_runs_not_empty")
        ids = sorted(item["id"] for item in definitions["definitions"])
        require(len(set(ids)) == 71, "catalog_check_ids_not_unique")
        require(all(item.get("source_refs") for item in definitions["definitions"]), "catalog_source_ref_missing")
        self.definitions = definitions
        self.check_ids = ids
        self.summary["catalog"] = {"checks": 71, "bundles": 8, "policies": 7, "operations": 0}

    def create_and_wait_run(self):
        self.stage = "create_check_run"
        payload = {
            "api_version": "setpoint.io/v1",
            "kind": "ReadOnlyCheckRun",
            "metadata": {"idempotency_key": "rc1-" + self.args.run_id.lower(), "name": "M2 RC1 physical " + self.args.alias_name},
            "spec": {
                "node_ids": [self.node_id],
                "check_ids": self.check_ids,
                "parameters": {"linux.network.source_route": {"host_role": "unknown"}},
            },
        }
        created = self.management_request("create_check_run", payload=payload)
        self.summary["product_run_created"] = True
        self.summary["execution_phase"] = "product_run"
        require(isinstance(created, dict), "created_run_response_invalid")
        tasks = created.get("tasks", [])
        require(isinstance(tasks, list), "created_task_list_invalid")
        self.summary["product_tasks_created"] = len(tasks)
        if tasks:
            self.summary["executed"] = True
            self.summary["execution_phase"] = "product_task"
        require(len(tasks) == 8, "created_task_count_invalid")
        run_id = created["metadata"]["id"]
        deadline = time.time() + self.args.task_timeout
        final = None
        while time.time() < deadline:
            current = self.management_request("get_check_run", resource_id=run_id)
            if current.get("status", {}).get("phase") in ("completed", "partial_failed", "canceled"):
                final = current
                break
            time.sleep(2)
        require(final is not None, "check_run_timeout")
        self.product_run_id = run_id
        self.final_run = final
        require(final["status"]["phase"] == "completed", "check_run_not_completed")

    def verify_run(self):
        self.stage = "verify_contract"
        self.raw_dir.mkdir(parents=True, exist_ok=False)
        run_path = self.raw_dir / "run-final.json"
        definitions_path = self.raw_dir / "definitions.json"
        run_path.write_text(json.dumps(self.final_run, ensure_ascii=False, separators=(",", ":")), encoding="utf-8")
        definitions_path.write_text(json.dumps(self.definitions, ensure_ascii=False, separators=(",", ":")), encoding="utf-8")
        self.raw_files.extend([run_path, definitions_path])
        verifier_args = [
            "go", "run", "./testing/harness/physicalverifier",
            "--run", str(run_path),
            "--definitions", str(definitions_path),
        ]
        if self.args.expected_root:
            verifier_args += ["--expected-root", self.args.expected_root]
        output = run_command(verifier_args, timeout=180, cwd=self.args.verifier_cwd)
        contract_summary = json.loads(output.decode("utf-8"))
        status_total = sum(contract_summary["statuses"].values())
        require(status_total == 71, "five_state_total_invalid")
        require(contract_summary["statuses"].get("error", 0) == 0, "error_result_present")
        self.summary["task_contract"] = contract_summary
        self.independent_runtime_probe()

    def independent_runtime_probe(self):
        items = {}
        for task_resource in self.final_run["tasks"]:
            for item in task_resource.get("result", {}).get("items", []):
                items[item["id"]] = item
        runtime_ids = sorted(
            item_id for item_id in items
            if item_id.startswith("net.ipv4.") and not item_id.endswith(".persisted")
        )
        script = r'''set -eu
for key in "$@"; do printf '%s=%s\n' "$key" "$(sysctl -n "$key")"; done
'''
        output = self.remote_shell(script, runtime_ids).decode("utf-8")
        values = dict(line.split("=", 1) for line in output.splitlines() if "=" in line)
        require(len(values) == len(runtime_ids), "independent_sysctl_probe_incomplete")
        for item_id, value in values.items():
            require(items[item_id].get("current_value", "").strip() == value.strip(), "independent_sysctl_mismatch")
        self.summary["independent_validation"] = {
            "runtime_sysctl_checks": len(runtime_ids),
            "runtime_values_match": True,
            "fixed_probe_only": True,
        }

    def sqlite_summary(self):
        code = r'''import json, sqlite3, sys
db = sqlite3.connect(sys.argv[1])
def scalar(sql): return db.execute(sql).fetchone()[0]
result = {
  'integrity_check': scalar('PRAGMA integrity_check'),
  'check_runs': scalar('SELECT COUNT(*) FROM check_runs'),
  'check_run_tasks': scalar('SELECT COUNT(*) FROM check_run_tasks'),
  'tasks': scalar('SELECT COUNT(*) FROM tasks'),
  'task_results': scalar('SELECT COUNT(*) FROM task_results'),
  'max_attempt': scalar('SELECT COALESCE(MAX(attempt),0) FROM tasks'),
  'operation_runs': scalar('SELECT COUNT(*) FROM operation_runs'),
  'operation_checkpoints': scalar('SELECT COUNT(*) FROM operation_checkpoints'),
}
db.close()
print(json.dumps(result, sort_keys=True))
'''
        return self.remote_python(code, [self.remote_paths["database"]])

    def verify_restart(self):
        self.stage = "sqlite_before_restart"
        before_sqlite = self.sqlite_summary()
        require(before_sqlite == {
            "integrity_check": "ok", "check_runs": 1, "check_run_tasks": 8, "tasks": 8,
            "task_results": 8, "max_attempt": 1, "operation_runs": 0, "operation_checkpoints": 0,
        }, "sqlite_before_restart_invalid")
        before_run_digest = hashlib.sha256(
            json.dumps(self.final_run, sort_keys=True, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
        ).hexdigest()
        node_before = self.management_request("get_node", resource_id=self.node_id)
        last_seen_before = node_before.get("last_seen_at", "")

        self.stage = "server_restart"
        self.stop_remote_process(self.server_pid, self.remote_paths["server_bin"])
        self.server_started = False
        self.server_pid = self.launch_remote_process("server_bin", "server_config", "server_log")
        self.server_started = True
        deadline = time.time() + 30
        while time.time() < deadline:
            try:
                if self.management_request("health").get("status") == "ok":
                    break
            except HarnessFailure:
                pass
            time.sleep(0.5)
        else:
            raise HarnessFailure("server_restart_health_failed")

        deadline = time.time() + 40
        reconnected = False
        while time.time() < deadline:
            node = self.management_request("get_node", resource_id=self.node_id)
            if node.get("status") == "online" and node.get("last_seen_at", "") > last_seen_before:
                reconnected = True
                break
            time.sleep(1)
        require(reconnected, "agent_reconnect_not_observed")

        after_run = self.management_request("get_check_run", resource_id=self.product_run_id)
        after_run_digest = hashlib.sha256(
            json.dumps(after_run, sort_keys=True, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
        ).hexdigest()
        after_sqlite = self.sqlite_summary()
        require(after_run_digest == before_run_digest, "task_state_changed_after_restart")
        require(after_sqlite == before_sqlite, "sqlite_changed_after_restart")
        self.final_run = after_run
        self.summary["sqlite"] = after_sqlite
        self.summary["task_replay"] = 0

    def final_privacy_scan(self):
        self.stage = "remote_privacy_scan"
        remote_privacy = self.remote_secret_scan(expect_token_file=False)
        require(remote_privacy["token_hits_outside_token_file"] == 0, "final_token_leak")
        require(remote_privacy["credential_hits_outside_credential_file"] == 0, "final_credential_leak")
        require(remote_privacy["generic_token_hits"] == 0, "final_generic_token_leak")
        require(remote_privacy["authorization_header_hits"] == 0, "final_authorization_leak")
        require(remote_privacy["private_key_hits"] == 0, "final_private_key_leak")
        self.summary["privacy"].update({
            "remote_exact_secret_hits": 0,
            "remote_authorization_hits": 0,
            "remote_private_key_hits": 0,
            "credential_mode": remote_privacy["credential_mode"],
            "enrollment_environment_present": remote_privacy["enrollment_environment_present"],
        })

    def stop_remote_process(self, pid, expected_binary):
        if not pid:
            return
        script = r'''set -eu
pid=$1
expected=$2
if [ ! -d "/proc/$pid" ]; then exit 0; fi
actual=$(readlink -f "/proc/$pid/exe" 2>/dev/null || true)
[ "$actual" = "$expected" ]
kill -TERM "$pid"
i=0
while kill -0 "$pid" 2>/dev/null && [ "$i" -lt 100 ]; do i=$((i+1)); sleep 0.1; done
if kill -0 "$pid" 2>/dev/null; then kill -KILL "$pid"; fi
i=0
while kill -0 "$pid" 2>/dev/null && [ "$i" -lt 50 ]; do i=$((i+1)); sleep 0.1; done
! kill -0 "$pid" 2>/dev/null
'''
        self.remote_shell(script, [str(pid), expected_binary], timeout=30)

    def remote_exists(self, path):
        output = self.remote_shell(r'''if [ -e "$1" ] || [ -L "$1" ]; then printf 1; else printf 0; fi
''', [path]).decode("utf-8").strip()
        return output == "1"

    def collect_and_scan_raw(self):
        self.stage = "collect_transient_scan_data"
        if not self.raw_dir.exists():
            self.raw_dir.mkdir(parents=True, exist_ok=False)
        for key, local_name in (("server_log", "server.log"), ("agent_log", "agent.log"), ("database", "setpoint.db")):
            remote_path = self.remote_paths[key]
            if self.remote_exists(remote_path):
                local_path = self.raw_dir / local_name
                self.scp_from(remote_path, local_path)
                self.raw_files.append(local_path)

        self.stage = "local_privacy_scan"
        self.summary_dir.mkdir(parents=True, exist_ok=True)
        command = [
            "gitleaks", "dir", "--redact=100", "--no-banner", "--report-format", "json",
            "--report-path", str(self.gitleaks_report), str(self.raw_dir),
        ]
        completed = subprocess.run(command, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=120, check=False)
        require(completed.returncode in (0, 1), "gitleaks_execution_failed")
        findings = []
        if self.gitleaks_report.exists() and self.gitleaks_report.stat().st_size:
            findings = json.loads(self.gitleaks_report.read_text(encoding="utf-8"))
        unresolved_findings, audited_non_secret_findings = classify_gitleaks_findings(
            findings, self.raw_dir, self.args.run_id
        )

        patterns = {
            "token": re.compile(rb'(?:spe|spc)\.[0-9a-f]{32}\.[A-Za-z0-9_-]{40,}'),
            "authorization": re.compile(rb'Authorization\s*:\s*(?:Bearer|Basic)\s+\S+', re.I),
            "private_key": re.compile(rb'BEGIN (?:RSA |OPENSSH |EC )?PRIVATE KEY'),
            "private_address": re.compile(rb'(?<![0-9])(?:10(?:\.[0-9]{1,3}){3}|192\.168(?:\.[0-9]{1,3}){2}|172\.(?:1[6-9]|2[0-9]|3[01])(?:\.[0-9]{1,3}){2})(?![0-9])'),
            "origin_url": re.compile(rb'https?://(?!127\.0\.0\.1)[^\s"\\]+', re.I),
            "account_mapping": re.compile(rb'(?:root:x:0:0:|root:\$[0-9A-Za-z]+\$)'),
            "full_nginx_config": re.compile(rb'# configuration file .*:(?:\s*\n|\\n)'),
            "full_ssh_config": re.compile(rb'(?:\n|\\n)(?:PermitRootLogin|PasswordAuthentication|Port)\s+'),
        }
        targeted = {name: 0 for name in patterns}
        for path in self.raw_files:
            if not path.exists():
                continue
            contents = path.read_bytes()
            for name, pattern in patterns.items():
                for match in pattern.finditer(contents):
                    if name == "private_address" and match.group(0).startswith(b"127."):
                        continue
                    targeted[name] += 1
        self.summary["privacy"].update({
            "gitleaks_findings": len(unresolved_findings),
            "gitleaks_audited_non_secret_findings": audited_non_secret_findings,
            "targeted_secret_hits": targeted["token"] + targeted["authorization"] + targeted["private_key"],
            "private_address_hits": targeted["private_address"],
            "full_nginx_config_hits": targeted["full_nginx_config"],
        })
        require(len(unresolved_findings) == 0, "gitleaks_finding_present")
        require(sum(targeted.values()) == 0, "targeted_privacy_finding_present")

    def cleanup_remote_files(self):
        file_paths = [
            self.remote_paths["token"],
            self.remote_paths["journal"],
            self.remote_paths["credential"] + ".previous",
            self.remote_paths["credential"],
            self.remote_paths["identity"],
            self.remote_paths["agent_config"],
            self.remote_paths["server_config"],
            self.remote_paths["agent_log"],
            self.remote_paths["server_log"],
            self.remote_paths["database"] + "-shm",
            self.remote_paths["database"] + "-wal",
            self.remote_paths["database"],
            self.remote_paths["agent_bin"],
            self.remote_paths["server_bin"],
        ]
        script = r'''set -eu
d=$1
shift
for path in "$@"; do
  if [ -e "$path" ] || [ -L "$path" ]; then unlink -- "$path"; fi
done
rmdir -- "$d"
[ ! -e "$d" ]
'''
        self.remote_shell(script, [self.remote_dir] + file_paths, timeout=60)

    def cleanup_local_raw(self):
        for path in list(dict.fromkeys(self.raw_files)):
            if path.exists():
                path.unlink()
        if self.raw_dir.exists():
            remaining = list(self.raw_dir.iterdir())
            require(not remaining, "local_raw_cleanup_not_empty")
            self.raw_dir.rmdir()
        raw_parent = Path(self.args.work_root) / "raw"
        if raw_parent.exists() and not list(raw_parent.iterdir()):
            raw_parent.rmdir()

    def cleanup(self):
        cleanup_ok = True
        process_cleanup_ok = True
        cleanup_error = ""
        try:
            if self.agent_started:
                self.stop_remote_process(self.agent_pid, self.remote_paths["agent_bin"])
                self.agent_started = False
            if self.server_started:
                self.stop_remote_process(self.server_pid, self.remote_paths["server_bin"])
                self.server_started = False
        except Exception:
            cleanup_ok = False
            process_cleanup_ok = False
            cleanup_error = "process_cleanup_failed"

        try:
            if self.remote_dir_created:
                self.collect_and_scan_raw()
        except HarnessFailure as exc:
            if not self.error_code:
                self.error_code = exc.code
                self.failure_stage = self.stage

        try:
            if self.remote_dir_created and process_cleanup_ok:
                self.cleanup_remote_files()
                self.remote_dir_created = False
        except Exception:
            cleanup_ok = False
            if not cleanup_error:
                cleanup_error = "remote_file_cleanup_failed"

        try:
            self.cleanup_local_raw()
        except Exception:
            cleanup_ok = False
            if not cleanup_error:
                cleanup_error = "local_raw_cleanup_failed"

        try:
            process_count = self.remote_shell(r'''set -eu
count=0
for proc in /proc/[0-9]*; do
  [ -r "$proc/comm" ] || continue
  name=$(cat "$proc/comm" 2>/dev/null || true)
  case "$name" in setpoint-server|setpoint-agent) count=$((count+1));; esac
done
printf '%s\n' "$count"
''').decode("utf-8").strip()
            remote_ports = self.remote_port_count(self.remote_mgmt_port, self.remote_agent_port)
            remote_dir_absent = not self.remote_exists(self.remote_dir)
            require(process_count == "0", "remote_setpoint_process_residue")
            require(remote_ports == 0, "remote_listener_residue")
            require(remote_dir_absent, "remote_directory_residue")
        except Exception:
            cleanup_ok = False
            if not cleanup_error:
                cleanup_error = "zero_residue_verification_failed"

        self.summary["cleanup"] = {
            "local_setpoint_processes": 0,
            "remote_setpoint_processes": 0 if cleanup_ok else None,
            "planned_listeners": 0 if cleanup_ok else None,
            "remote_task_directory_absent": cleanup_ok,
            "temporary_credential_absent": cleanup_ok,
            "enrollment_token_absent": cleanup_ok,
            "raw_local_data_absent": cleanup_ok,
            "error": cleanup_error,
        }
        self.summary["cleaned"] = cleanup_ok
        return cleanup_ok

    def write_summary(self):
        self.summary_dir.mkdir(parents=True, exist_ok=True)
        self.summary_path.write_text(
            json.dumps(self.summary, ensure_ascii=False, sort_keys=True, indent=2) + "\n",
            encoding="utf-8",
        )

    def execute(self):
        try:
            self.preflight()
            self.create_remote_dir()
            before = self.state_fingerprint()
            self.deploy()
            self.write_configs()
            self.start_server()
            self.start_management_probe()
            self.enroll_agent()
            self.configure_trusted_root()
            self.load_catalog()
            self.create_and_wait_run()
            self.verify_run()
            self.verify_restart()
            after = self.state_fingerprint()
            require(before == after, "system_state_changed")
            self.summary["system_state_unchanged"] = True
            self.summary["system_writes"] = 0
            self.final_privacy_scan()
        except HarnessFailure as exc:
            self.error_code = exc.code
            self.failure_stage = self.stage
            if exc.diagnostics is not None:
                self.summary["management_probe"] = exc.diagnostics
        except Exception as exc:
            self.error_code = "unexpected_harness_exception"
            self.failure_stage = self.stage
            if self.stage == "start_management_probe":
                diagnostics = self.summary.get("management_probe") or management_probe_diagnostics()
                diagnostics.update({
                    "substage": diagnostics.get("substage") or "unexpected_exception",
                    "exception_type": type(exc).__name__,
                    "stable_error_code": "unexpected_harness_exception",
                    "stderr_tail": sanitize_diagnostic_tail(str(exc)),
                })
                self.summary["management_probe"] = diagnostics
        finally:
            cleanup_ok = self.cleanup()

        succeeded = not self.error_code
        self.summary["verdict"] = final_verdict(
            succeeded=succeeded,
            executed=self.summary["executed"],
            cleaned=cleanup_ok,
        )
        if succeeded and cleanup_ok:
            self.summary["failure_domain"] = ""
        else:
            self.summary["failure_domain"] = failure_domain_for(self.summary)
            self.summary["failure_stage"] = self.failure_stage or self.stage
            self.summary["failure_code"] = self.error_code or self.summary["cleanup"].get("error") or "cleanup_incomplete"
        self.write_summary()
        print(json.dumps(self.summary, ensure_ascii=False, sort_keys=True, separators=(",", ":")))
        return 0 if self.summary["verdict"] == "PASS / EXECUTED / CLEANED" else 1


def parse_args():
    parser = argparse.ArgumentParser()
    parser.add_argument("--alias", dest="alias_name", required=True)
    parser.add_argument("--host", required=True)
    parser.add_argument("--ssh-user", required=True)
    parser.add_argument("--expected-os", required=True)
    parser.add_argument("--expected-head", required=True)
    parser.add_argument("--run-id", required=True)
    parser.add_argument("--server-bin", type=Path, required=True)
    parser.add_argument("--agent-bin", type=Path, required=True)
    parser.add_argument("--server-sha", required=True)
    parser.add_argument("--agent-sha", required=True)
    parser.add_argument("--work-root", type=Path, required=True)
    parser.add_argument("--verifier-cwd", type=Path, required=True)
    parser.add_argument("--expected-root", default="")
    parser.add_argument("--task-timeout", type=int, default=900)
    args = parser.parse_args()
    require(args.server_bin.is_file() and args.agent_bin.is_file(), "local_binary_missing")
    require(sha256_file(args.server_bin) == args.server_sha, "local_server_sha_mismatch")
    require(sha256_file(args.agent_bin) == args.agent_sha, "local_agent_sha_mismatch")
    require(re.fullmatch(r"[0-9]{8}T[0-9]{6}Z-[a-z0-9]+", args.run_id) is not None, "run_id_invalid")
    require(re.fullmatch(r"[0-9a-f]{40}", args.expected_head) is not None, "expected_head_invalid")
    return args


if __name__ == "__main__":
    try:
        arguments = parse_args()
        sys.exit(NodeRun(arguments).execute())
    except HarnessFailure as failure:
        print(json.dumps({
            "verdict": "FAIL / NOT_EXECUTED / CLEANUP_NOT_REQUIRED",
            "executed": False,
            "execution_phase": "pre_product_task",
            "failure_domain": "physical_harness",
            "failure_code": failure.code,
            "product_run_created": False,
            "product_tasks_created": 0,
        }, sort_keys=True))
        sys.exit(1)
