"""Exercise batch admission and recovery against a local qshell fixture."""
import asyncio
import importlib.util
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
FAKE = r'''#!/usr/bin/env python3
import fcntl, json, os, sys, time, tomllib
from pathlib import Path
path = Path(os.environ["FIXTURE"])
args = sys.argv[1:]
if args == ["version"]:
    print("v2.21.0")
    sys.exit()
with (path / "lock").open("a") as lock:
    fcntl.flock(lock, fcntl.LOCK_EX)
    db = json.loads((path / "remote").read_text())
    mode = (path / "mode").read_text()
    if args[:3] == ["sandbox", "template", "build"]:
        assert "--wait" not in args
        name = tomllib.loads(Path(args[-1]).read_text())["name"]
        rows = db["builds"]
        assert all(r["name"] != name or r["terminal"] for r in rows)
        build = str(len(rows) + 1)
        rows.append(dict(name=name, id=build, terminal=False, waiting=mode == "upload-error"))
        db["peak"] = max(db["peak"], sum(not r["terminal"] for r in rows))
        if db["peak"] == 4:
            (path / "four-slots").touch()
        if mode != "lost-receipt":
            print(f"Template {name} ready for build (build ID: {build})")
            if mode == "upload-error":
                print("Error: COPY upload failed", file=sys.stderr)
            else:
                print(f"Build started. Use 'qshell sandbox template builds {name} {build}' to check status.")
    elif args[:3] == ["sandbox", "template", "builds"]:
        name, build = args[3:]
        row = next(r for r in db["builds"] if r["id"] == build and r["name"] == name)
        with (path / "queries").open("a") as queries:
            queries.write(build + "\n")
        if mode == "wrong-id":
            build = "unrelated"
        status = row.get("status", "ready")
        if mode == "query-error":
            print("Error: get build status failed: fixture unavailable", file=sys.stderr)
            sys.exit(0)
        if mode == "query-error-once" and not (path / "query-retried").exists():
            (path / "query-retried").touch()
            print("Error: get build status failed: temporary outage", file=sys.stderr)
            sys.exit(0)
        if mode == "reject-failed" and row.get("status") == "error":
            print("Error: build no longer exists", file=sys.stderr)
            sys.exit(0)
        if row.get("waiting"):
            status = "waiting"
        elif mode == "hold":
            status = "building"
        elif mode == "fail-slim" and name.endswith("-slim"):
            status = "error"
        if status in ("ready", "error"):
            row.update(terminal=True, status=status)
        print(f"Template ID:  {name}\nBuild ID:     {build}\nStatus:       {status}")
    elif args[:3] == ["sandbox", "template", "list"]:
        latest = {r["name"]:r for r in db["builds"]}
        print(json.dumps([dict(Aliases=[n], TemplateID=n, BuildID="other" if mode == "replaced" else r["id"]) for n,r in latest.items()]))
    else:
        sys.exit(2)
    (path / "remote").write_text(json.dumps(db))
if args[:3] == ["sandbox", "template", "build"]:
    if mode == "fail-slim":
        deadline = time.monotonic() + 8
        while not (path / "four-slots").exists():
            assert time.monotonic() < deadline
            time.sleep(.05)
    else:
        time.sleep(.25)
    if mode == "upload-error":
        sys.exit(201)
'''


class BatchTest(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.repo = Path(temp.name)
        self.fake = self.repo / "fake-qshell"
        self.fake.write_text(FAKE)
        self.fake.chmod(0o755)
        self.mode = self.repo / "mode"
        self.mode.write_text("ready")
        self.remote = self.repo / "remote"
        self.remote.write_text('{"peak":0,"builds":[]}')
        dirs = shutil.ignore_patterns(".build", ".qshell-sandbox.*")
        shutil.copytree(ROOT / "templates", self.repo / "templates", symlinks=True, ignore=dirs)
        (self.repo / "scripts/lib").mkdir(parents=True)
        for file in ["build-template-batch.py", "build-windows-template.sh",
                     "run-runner-template-operation.sh", "lib/runner-template-disk.sh"]:
            shutil.copy2(ROOT / "scripts" / file, self.repo / "scripts" / file)
        (self.repo / "scripts/prepare-runner-archive.sh").write_text("exit 0\n")
        subprocess.run(["git", "init", "-q"], cwd=self.repo, check=True)
        self.env = dict(os.environ, QSHELL=str(self.fake), QINIU_API_KEY="fixture",
                        QINIU_SANDBOX_API_URL="https://fixture.invalid", FIXTURE=temp.name,
                        BUILD_BATCH="default", INCLUDE_WINDOWS="0", BUILD_WAIT_SECONDS="2")
        self.env.pop("BUILD_CONCURRENCY", None)

    def launch(self, **env):
        return subprocess.Popen([sys.executable, "scripts/build-template-batch.py"], cwd=self.repo,
                                env={**self.env, **env}, stdout=subprocess.PIPE,
                                stderr=subprocess.STDOUT, text=True)

    def batch(self, ok=True, **env):
        proc = self.launch(**env)
        output, _ = proc.communicate(timeout=60)
        self.assertEqual(proc.returncode == 0, ok, output)
        return output

    def count(self):
        return len(json.loads(self.remote.read_text())["builds"])

    def test_retry_and_selection(self):
        self.mode.write_text("fail-slim")
        self.batch(ok=False)
        self.assertEqual(self.count(), 8)
        self.assertEqual(json.loads(self.remote.read_text())["peak"], 4)
        failed_id = next(r["id"] for r in json.loads(self.remote.read_text())["builds"]
                         if r.get("status") == "error")
        (self.repo / "queries").write_text("")
        self.mode.write_text("reject-failed")
        self.batch(INCLUDE_WINDOWS="1")
        self.assertNotIn(failed_id, (self.repo / "queries").read_text().splitlines())
        self.assertEqual(self.count(), 10)  # one retry plus Windows
        changed = self.repo / "templates/github-runner-ubuntu-slim/qshell.sandbox.large.toml"
        with changed.open("a") as file:
            file.write("# changed input\n")
        self.batch(INCLUDE_WINDOWS="1")
        self.assertEqual(self.count(), 11)
        self.batch(BUILD_BATCH="refresh")
        self.assertEqual(self.count(), 19)
        self.batch(QINIU_SANDBOX_API_URL="https://other.invalid")
        self.assertEqual(self.count(), 27)

    def test_interruption_and_competing_batch(self):
        self.mode.write_text("hold")
        proc = self.launch(BUILD_CONCURRENCY="1", BUILD_WAIT_SECONDS="60")
        try:
            deadline = time.monotonic() + 10
            while not any('"status": "pending"' in path.read_text()
                          for path in (self.repo / ".git").glob("template-build-batches/*/*.json")):
                self.assertLess(time.monotonic(), deadline)
                time.sleep(.05)
            self.assertIn("another template batch", self.batch(ok=False, BUILD_BATCH="other"))
            proc.send_signal(signal.SIGINT)
        finally:
            proc.communicate(timeout=10)
        self.mode.write_text("query-error")
        self.assertIn("fixture unavailable", self.batch(ok=False))
        self.assertEqual(self.count(), 1)
        self.mode.write_text("ready")
        self.batch()
        self.assertEqual(self.count(), 8)

    def test_skip_requires_exact_build_and_current_catalog(self):
        self.batch()
        self.mode.write_text("wrong-id")
        self.assertIn("unconfirmed response", self.batch(ok=False))
        self.mode.write_text("replaced")
        self.assertIn("catalog changed", self.batch(ok=False))
        self.assertEqual(self.count(), 8)

    def test_transient_query_error_keeps_queued_builds(self):
        self.mode.write_text("query-error-once")
        self.batch(BUILD_CONCURRENCY="1", BUILD_WAIT_SECONDS="30")
        self.assertEqual(self.count(), 8)
        self.assertTrue((self.repo / "query-retried").exists())

    def test_missing_receipt_blocks_even_new_batch(self):
        self.mode.write_text("lost-receipt")
        self.batch(ok=False, BUILD_CONCURRENCY="1")
        self.assertEqual(self.count(), 1)
        self.assertIn("submission outcome unknown", self.batch(ok=False, BUILD_BATCH="another"))
        self.assertEqual(self.count(), 1)

    def test_unconfirmed_start_never_polls_waiting_build(self):
        for rebuild in (False, True):
            with self.subTest(rebuild=rebuild):
                if rebuild:
                    self.mode.write_text("ready")
                    self.batch()
                self.mode.write_text("upload-error")
                queries = self.repo / "queries"
                queries.write_text("")
                batch = "rebuild" if rebuild else "default"
                output = self.batch(ok=False, BUILD_CONCURRENCY="1", BUILD_BATCH=batch,
                                    BUILD_WAIT_SECONDS="3600")
                self.assertIn("build start not confirmed", output)
                count = self.count()
                self.assertEqual(queries.read_text(), "")
                self.assertIn("start not confirmed", self.batch(ok=False, BUILD_BATCH="another",
                                                                BUILD_WAIT_SECONDS="3600"))
                self.assertEqual(self.count(), count)
                self.assertEqual(queries.read_text(), "")
                receipts = list((self.repo / ".git").glob("template-build-batches/*/*.json"))
                receipt = next(p for p in receipts if json.loads(p.read_text())["status"] == "requested")
                saved = json.loads(receipt.read_text())
                last_build = json.loads(self.remote.read_text())["builds"][-1]
                self.assertEqual((saved["template"], saved["build"]),
                                 (last_build["name"], last_build["id"]))
                # Operator confirms the orphaned waiting build is inactive, then
                # moves its receipt outside the directory to permit another try.
                receipt.rename(self.repo / "orphaned-receipt.json")
                db = json.loads(self.remote.read_text())
                db["builds"][-1].update(terminal=True, status="error")
                self.remote.write_text(json.dumps(db))


class StatusTest(unittest.IsolatedAsyncioTestCase):
    async def test_query_failures_retry_but_mismatched_ids_stop(self):
        spec = importlib.util.spec_from_file_location("batch", ROOT / "scripts/build-template-batch.py")
        batch = importlib.util.module_from_spec(spec)
        with mock.patch.object(sys, "dont_write_bytecode", True):
            spec.loader.exec_module(batch)
        record = {"template": "template", "build": "build"}
        ready = "Template ID: template\nBuild ID: build\nStatus: ready\n"
        failures = ["", "Error: get build status failed: network", RuntimeError("qshell exited 1"),
                    RuntimeError("qshell timed out after 30s")]
        with mock.patch.object(batch.asyncio, "sleep", new_callable=mock.AsyncMock):
            for failure in failures:
                with self.subTest(failure=failure), mock.patch.object(
                        batch, "command", side_effect=[failure, ready]) as command:
                    self.assertEqual(await batch.terminal("template", record), "ready")
                    self.assertEqual(command.call_count, 2)
            with mock.patch.object(batch, "command", return_value=ready.replace("ID: build", "ID: other")) as command:
                with self.assertRaisesRegex(RuntimeError, "unconfirmed response"):
                    await batch.terminal("template", record)
                self.assertEqual(command.call_count, 1)

        with mock.patch.object(batch, "command", return_value="Error: fixture unavailable"), \
                mock.patch.dict(os.environ, BUILD_WAIT_SECONDS="1"), \
                mock.patch.object(batch.time, "monotonic", side_effect=[0, 0, 2]):
            with self.assertRaisesRegex(RuntimeError, "could not query build build: Error: fixture unavailable"):
                await batch.terminal("template", record)


if __name__ == "__main__":
    unittest.main()
