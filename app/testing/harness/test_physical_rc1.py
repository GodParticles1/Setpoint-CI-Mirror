import base64
import http.server
import io
import inspect
import json
import subprocess
import sys
import tempfile
import threading
import unittest
from contextlib import redirect_stdout
from pathlib import Path
from types import SimpleNamespace

import physical_rc1 as harness


class StepClock:
    def __init__(self):
        self.value = 0.0

    def __call__(self):
        return self.value

    def sleep(self, seconds):
        self.value += max(seconds, 0.1)


def run_probe(health_probe, timeout=0.3):
    clock = StepClock()
    return harness.probe_management_readiness(
        health_probe=health_probe,
        clock=clock,
        sleeper=clock.sleep,
        timeout_seconds=timeout,
    )


def new_node_run(directory):
    args = SimpleNamespace(
        alias_name="fixture-node",
        host="fixture.invalid",
        ssh_user="fixture-user",
        run_id="20260811T000000Z-fixture",
        work_root=Path(directory),
        expected_head="0" * 40,
        expected_root="",
        task_timeout=1,
    )
    node_run = harness.NodeRun(args)
    node_run.node_id = "fixture-node-id"
    node_run.check_ids = ["fixture-check-{0}".format(index) for index in range(71)]
    node_run.create_remote_dir = lambda: None
    node_run.state_fingerprint = lambda: {"state": "unchanged"}
    node_run.deploy = lambda: None
    node_run.write_configs = lambda: None
    node_run.start_server = lambda: None
    node_run.start_management_probe = lambda: None
    node_run.enroll_agent = lambda: None
    node_run.configure_trusted_root = lambda: None
    node_run.load_catalog = lambda: None
    node_run.verify_run = lambda: None
    node_run.verify_restart = lambda: None
    node_run.final_privacy_scan = lambda: None
    def cleanup():
        node_run.summary["cleaned"] = True
        node_run.summary["cleanup"] = {"error": ""}
        return True

    node_run.cleanup = cleanup
    node_run.write_summary = lambda: None
    return node_run


def execute_silently(node_run):
    with redirect_stdout(io.StringIO()):
        return node_run.execute()


def fail_at(node_run, stage, code):
    node_run.stage = stage
    raise harness.HarnessFailure(code)


def completed_run_api(task_count):
    def request(operation, *, resource_id="", payload=None):
        del payload
        if operation == "create_check_run":
            return {"metadata": {"id": "fixture-run"}, "tasks": [{} for _ in range(task_count)]}
        if operation == "get_check_run" and resource_id == "fixture-run":
            return {"status": {"phase": "completed"}}
        raise AssertionError("unexpected fixture request")

    return request


class PhysicalHarnessTests(unittest.TestCase):
    def test_01_remote_loopback_probe_succeeds(self):
        diagnostics = run_probe(lambda: True)
        self.assertTrue(diagnostics["remote_exec"])
        self.assertEqual(diagnostics["endpoint_class"], "loopback_management")
        self.assertEqual(diagnostics["attempt_count"], 1)

    def test_02_remote_loopback_probe_is_bounded(self):
        with self.assertRaises(harness.HarnessFailure) as raised:
            run_probe(lambda: False)
        self.assertEqual(raised.exception.code, "management_probe_failed")
        self.assertTrue(raised.exception.diagnostics["timeout"])
        self.assertGreater(raised.exception.diagnostics["attempt_count"], 0)

    def test_03_remote_loopback_probe_retries_typed_failure(self):
        outcomes = [harness.HarnessFailure("management_transport_failed"), True]

        def health():
            outcome = outcomes.pop(0)
            if isinstance(outcome, Exception):
                raise outcome
            return outcome

        diagnostics = run_probe(health, timeout=0.5)
        self.assertEqual(diagnostics["attempt_count"], 2)
        self.assertEqual(diagnostics["stable_error_code"], "")

    def test_04_remote_loopback_probe_fails_on_programming_error(self):
        with self.assertRaises(harness.HarnessFailure) as raised:
            run_probe(lambda: (_ for _ in ()).throw(ValueError("fixture error")))
        self.assertEqual(raised.exception.code, "unexpected_harness_exception")
        self.assertEqual(raised.exception.diagnostics["exception_type"], "ValueError")

    def test_05_arbitrary_management_operation_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            node_run = new_node_run(directory)
            node_run.remote_python = lambda *_args, **_kwargs: self.fail("remote exec must not run")
            with self.assertRaises(harness.HarnessFailure) as raised:
                node_run.management_request("arbitrary_path")
        self.assertEqual(raised.exception.code, "management_operation_not_allowed")

    def test_06_dynamic_resource_id_is_strictly_validated(self):
        with tempfile.TemporaryDirectory() as directory:
            node_run = new_node_run(directory)
            node_run.remote_python = lambda *_args, **_kwargs: self.fail("remote exec must not run")
            with self.assertRaises(harness.HarnessFailure) as raised:
                node_run.management_request("get_node", resource_id="../arbitrary")
        self.assertEqual(raised.exception.code, "management_resource_id_invalid")

    def test_07_remote_helper_only_targets_loopback_and_fixed_paths(self):
        self.assertIn('"http://127.0.0.1:{0}{1}"', harness.REMOTE_MANAGEMENT_PROBE)
        self.assertIn("ProxyHandler({})", harness.REMOTE_MANAGEMENT_PROBE)
        self.assertNotIn("http://0.0.0.0", harness.REMOTE_MANAGEMENT_PROBE)
        self.assertNotIn("sys.argv[5]", harness.REMOTE_MANAGEMENT_PROBE)
        self.assertEqual(set(harness.MANAGEMENT_OPERATIONS), {
            "health", "create_enrollment", "list_nodes", "get_node", "patch_node",
            "list_check_definitions", "list_check_bundles", "list_check_policies",
            "list_operation_definitions", "list_operation_runs", "create_check_run", "get_check_run",
        })

    def test_08_management_request_uses_fixed_operation_contract(self):
        calls = []
        with tempfile.TemporaryDirectory() as directory:
            node_run = new_node_run(directory)

            def remote_python(code, args=(), timeout=60):
                calls.append((code, list(args), timeout))
                return {"status": 200, "expected": True, "body": base64.b64encode(b'{"status":"ok"}').decode()}

            node_run.remote_python = remote_python
            response = node_run.management_request("health")
        self.assertEqual(response, {"status": "ok"})
        self.assertEqual(calls[0][1], ["health", str(node_run.remote_mgmt_port), "r:", "p:"])

        class HealthHandler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(b'{"status":"ok"}')

            def log_message(self, *_args):
                pass

        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), HealthHandler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            completed = subprocess.run(
                [sys.executable, "-", "health", str(server.server_port), "r:", "p:"],
                input=harness.REMOTE_MANAGEMENT_PROBE.encode("utf-8"),
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                timeout=10,
                check=False,
            )
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=5)
        self.assertEqual(completed.returncode, 0, completed.stderr.decode("utf-8", "replace"))
        helper_response = json.loads(completed.stdout.decode("utf-8"))
        self.assertTrue(helper_response["expected"])
        self.assertEqual(json.loads(base64.b64decode(helper_response["body"])), {"status": "ok"})

    def test_09_server_management_and_agent_listeners_are_loopback_only(self):
        writes = {}
        with tempfile.TemporaryDirectory() as directory:
            node_run = new_node_run(directory)
            node_run.remote_write = lambda path, contents, mode="600": writes.update({path: json.loads(contents)})
            harness.NodeRun.write_configs(node_run)
        server = writes[node_run.remote_paths["server_config"]]
        agent = writes[node_run.remote_paths["agent_config"]]
        self.assertEqual(server["management_listen_address"], "127.0.0.1:{0}".format(node_run.remote_mgmt_port))
        self.assertEqual(server["agent_listen_address"], "127.0.0.1:{0}".format(node_run.remote_agent_port))
        self.assertEqual(agent["server_url"], "http://127.0.0.1:{0}".format(node_run.remote_agent_port))

    def test_10_verdict_not_executed_and_cleaned(self):
        verdict = harness.final_verdict(succeeded=False, executed=False, cleaned=True)
        self.assertEqual(verdict, "FAIL / NOT_EXECUTED / CLEANED")

    def test_11_verdict_executed_and_cleaned(self):
        verdict = harness.final_verdict(succeeded=False, executed=True, cleaned=True)
        self.assertEqual(verdict, "FAIL / EXECUTED / CLEANED")

    def test_12_verdict_success(self):
        verdict = harness.final_verdict(succeeded=True, executed=True, cleaned=True)
        self.assertEqual(verdict, "PASS / EXECUTED / CLEANED")

    def test_13_verdict_cleanup_incomplete(self):
        verdict = harness.final_verdict(succeeded=False, executed=False, cleaned=False)
        self.assertEqual(verdict, "FAIL / NOT_EXECUTED / CLEANUP_INCOMPLETE")

    def test_14_diagnostic_schema_is_complete(self):
        diagnostics = harness.management_probe_diagnostics()
        required = {
            "stage", "substage", "exception_type", "stable_error_code", "timeout", "attempt_count",
            "remote_exec", "elapsed_ms", "cleanup_result", "stderr_tail", "endpoint_class",
        }
        self.assertTrue(required.issubset(diagnostics))
        self.assertNotIn("arguments", diagnostics)
        self.assertNotIn("command", diagnostics)

    def test_15_product_task_count_starts_at_zero(self):
        with tempfile.TemporaryDirectory() as directory:
            node_run = new_node_run(directory)
        self.assertFalse(node_run.summary["executed"])
        self.assertFalse(node_run.summary["product_run_created"])
        self.assertEqual(node_run.summary["product_tasks_created"], 0)
        self.assertEqual(node_run.summary["execution_phase"], "pre_product_task")

    def test_16_sanitizes_credential(self):
        text = harness.sanitize_diagnostic_tail("connection failed credential=secret-value")
        self.assertNotIn("secret-value", text)
        self.assertIn("[REDACTED]", text)

    def test_17_sanitizes_authorization(self):
        text = harness.sanitize_diagnostic_tail("network error Authorization: Bearer-secret")
        self.assertNotIn("Bearer-secret", text)
        self.assertIn("[REDACTED]", text)

    def test_18_sanitizes_address_and_account(self):
        text = harness.sanitize_diagnostic_tail("ssh connection to fixture-user@192.0.2.10 failed")
        self.assertNotIn("fixture-user", text)
        self.assertNotIn("192.0.2.10", text)
        self.assertIn("[ACCOUNT]@[ADDRESS]", text)

    def test_19_sanitized_tail_is_bounded(self):
        text = harness.sanitize_diagnostic_tail("error " + "x" * 5000)
        self.assertLessEqual(len(text), harness.MAX_DIAGNOSTIC_TAIL)

    def test_20_management_probe_tail_is_sanitized(self):
        tail = harness.sanitize_diagnostic_tail(
            "ssh connection to fixture-user@192.0.2.10 failed token=sp_enroll_secret"
        )
        self.assertNotIn("fixture-user", tail)
        self.assertNotIn("192.0.2.10", tail)
        self.assertNotIn("sp_enroll_secret", tail)

    def test_21_pre_product_failure_is_physical_harness(self):
        with tempfile.TemporaryDirectory() as directory:
            node_run = new_node_run(directory)
            node_run.preflight = lambda: fail_at(node_run, "remote_preflight", "preflight_failed")
            self.assertEqual(execute_silently(node_run), 1)
        self.assertEqual(node_run.summary["failure_domain"], "physical_harness")
        self.assertEqual(node_run.summary["verdict"], "FAIL / NOT_EXECUTED / CLEANED")
        self.assertFalse(node_run.summary["product_run_created"])

    def test_22_seven_created_tasks_are_product_acceptance(self):
        with tempfile.TemporaryDirectory() as directory:
            node_run = new_node_run(directory)
            node_run.preflight = lambda: None
            node_run.management_request = completed_run_api(7)
            self.assertEqual(execute_silently(node_run), 1)
        self.assertTrue(node_run.summary["product_run_created"])
        self.assertEqual(node_run.summary["product_tasks_created"], 7)
        self.assertTrue(node_run.summary["executed"])
        self.assertEqual(node_run.summary["execution_phase"], "product_task")
        self.assertEqual(node_run.summary["failure_domain"], "product_acceptance")
        self.assertEqual(node_run.summary["verdict"], "FAIL / EXECUTED / CLEANED")

    def test_23_zero_created_tasks_still_record_product_run(self):
        with tempfile.TemporaryDirectory() as directory:
            node_run = new_node_run(directory)
            node_run.preflight = lambda: None
            node_run.management_request = completed_run_api(0)
            self.assertEqual(execute_silently(node_run), 1)
        self.assertTrue(node_run.summary["product_run_created"])
        self.assertEqual(node_run.summary["product_tasks_created"], 0)
        self.assertFalse(node_run.summary["executed"])
        self.assertEqual(node_run.summary["execution_phase"], "product_run")
        self.assertEqual(node_run.summary["failure_domain"], "product_acceptance")

    def test_24_post_run_result_failure_is_product_acceptance(self):
        with tempfile.TemporaryDirectory() as directory:
            node_run = new_node_run(directory)
            node_run.preflight = lambda: None
            node_run.management_request = completed_run_api(8)
            node_run.verify_run = lambda: fail_at(node_run, "verify_contract", "error_result_present")
            self.assertEqual(execute_silently(node_run), 1)
        self.assertTrue(node_run.summary["product_run_created"])
        self.assertEqual(node_run.summary["product_tasks_created"], 8)
        self.assertEqual(node_run.summary["failure_domain"], "product_acceptance")
        self.assertEqual(node_run.summary["failure_code"], "error_result_present")

    def test_25_sqlite_and_replay_failures_are_product_acceptance(self):
        for code in ("sqlite_invariant_failure", "task_replay_detected"):
            with self.subTest(code=code), tempfile.TemporaryDirectory() as directory:
                node_run = new_node_run(directory)
                node_run.preflight = lambda: None
                node_run.management_request = completed_run_api(8)
                node_run.verify_restart = lambda code=code: fail_at(node_run, "sqlite_before_restart", code)
                self.assertEqual(execute_silently(node_run), 1)
                self.assertEqual(node_run.summary["failure_domain"], "product_acceptance")
                self.assertEqual(node_run.summary["failure_code"], code)

    def test_26_pre_product_management_failure_semantics_are_unchanged(self):
        with tempfile.TemporaryDirectory() as directory:
            node_run = new_node_run(directory)
            node_run.preflight = lambda: None
            node_run.start_management_probe = lambda: fail_at(
                node_run, "start_management_probe", "management_probe_failed"
            )
            self.assertEqual(execute_silently(node_run), 1)
        self.assertFalse(node_run.summary["product_run_created"])
        self.assertEqual(node_run.summary["product_tasks_created"], 0)
        self.assertFalse(node_run.summary["executed"])
        self.assertEqual(node_run.summary["failure_domain"], "physical_harness")
        self.assertEqual(node_run.summary["verdict"], "FAIL / NOT_EXECUTED / CLEANED")

    def test_27_harness_has_no_ssh_forward_dependency(self):
        source = inspect.getsource(harness)
        self.assertNotIn('"-L"', source)
        self.assertNotIn("ExitOnForwardFailure", source)
        self.assertNotIn("local_forward_port", source)
        self.assertNotIn("start_management_forward", source)
        self.assertNotIn("AllowTcpForwarding", source)

    def test_28_known_idempotency_key_gitleaks_finding_is_audited(self):
        with tempfile.TemporaryDirectory() as directory:
            raw_dir = Path(directory)
            run_id = "20260812T000000Z-fixture"
            run_path = raw_dir / "run-final.json"
            run_path.write_text(json.dumps({
                "metadata": {"idempotency_key": "rc1-" + run_id.lower()},
            }), encoding="utf-8")
            finding = {
                "RuleID": "generic-api-key",
                "File": str(run_path),
                "StartLine": 1,
                "EndLine": 1,
                "Match": 'idempotency_key\":\"REDACTED\"',
                "Secret": "REDACTED",
            }
            unresolved, accepted = harness.classify_gitleaks_findings([finding], raw_dir, run_id)
        self.assertEqual(unresolved, [])
        self.assertEqual(accepted, 1)

    def test_29_gitleaks_audit_fails_closed_on_any_mismatch(self):
        with tempfile.TemporaryDirectory() as directory:
            raw_dir = Path(directory)
            run_id = "20260812T000000Z-fixture"
            run_path = raw_dir / "run-final.json"
            run_path.write_text(json.dumps({
                "metadata": {"idempotency_key": "unexpected-value"},
            }), encoding="utf-8")
            finding = {
                "RuleID": "generic-api-key",
                "File": str(run_path),
                "StartLine": 1,
                "EndLine": 1,
                "Match": 'idempotency_key\":\"REDACTED\"',
                "Secret": "REDACTED",
            }
            unresolved, accepted = harness.classify_gitleaks_findings([finding], raw_dir, run_id)
        self.assertEqual(unresolved, [finding])
        self.assertEqual(accepted, 0)


if __name__ == "__main__":
    unittest.main()
