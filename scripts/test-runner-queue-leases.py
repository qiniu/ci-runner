"""Run two runnerd processes against an isolated schema in a PostgreSQL *_test DB."""
import argparse
import contextlib
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import tempfile
import time
import uuid

DSN = os.environ["RUNNERD_POSTGRES_TEST_DSN"]


def wait_for(description, condition, timeout=20, interval=0.05):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if condition():
            return
        time.sleep(interval)
    raise RuntimeError(f"timed out waiting for {description}")


def sql(env, query):
    proc = subprocess.run(["psql", "-XqAt", "-v", "ON_ERROR_STOP=1", "-d", DSN],
                          input=query, env=env, text=True, stdout=subprocess.PIPE)
    if proc.returncode:
        raise RuntimeError("fixture SQL failed")
    return proc.stdout.strip()


@contextlib.contextmanager
def block_profile_lookup(env):
    # Keep a worker between the committed queue claim and its admission write.
    with subprocess.Popen(["psql", "-XqAt", "-v", "ON_ERROR_STOP=1", "-d", DSN],
                          stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                          env=env, text=True) as psql:
        psql.stdin.write("BEGIN; SET LOCAL lock_timeout='10s'; LOCK runner_profiles IN ACCESS EXCLUSIVE MODE; SELECT 'locked';\n")
        psql.stdin.flush()
        if psql.stdout.readline().strip() != "locked":
            raise RuntimeError("could not hold the profile table")
        try:
            yield
        finally:
            psql.communicate("COMMIT;\n", timeout=10)


@contextlib.contextmanager
def node(binary, directory, name, env):
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    config = directory / (name + ".json")
    config.write_text(json.dumps({
        "server": {"http_addr": f"127.0.0.1:{port}"},
        "database": {"backend": "postgres", "dsn": DSN},
        "auth": {"session_secret": "fixture-session", "encryption_key": "fixture-key"},
        "sandbox": {"regions": [{"id": "local", "label": "Test", "sandbox_api_url": "http://127.0.0.1:1"}]},
        "github": {"token": "fixture", "webhook_secret": "fixture",
                   "oauth": {"client_id": "fixture", "client_secret": "fixture"}},
        "worker": {"lease_ttl_seconds": 300},
        "deploy": {"work_root": str(directory / "deploy")},
    }))
    log = directory / (name + ".log")
    with log.open("w") as out:
        proc = subprocess.Popen([binary, "--config", config], stdout=out, stderr=out,
                                cwd=directory, env=dict(env, PGAPPNAME=name))
        try:
            def started():
                if proc.poll() is not None:
                    raise RuntimeError(f"{name} exited during startup (code {proc.returncode})")
                return "starting background loops" in log.read_text()
            wait_for(f"{name} startup recovery", started)
            yield proc
        finally:
            if proc.poll() is None:
                proc.kill()  # also cleans up a deliberately SIGSTOPed worker
            proc.wait(timeout=10)


def exercise(binary, directory, schema, crash, full_ttl):
    env = dict(os.environ, PGOPTIONS=f"-c search_path={schema}",
               HTTP_PROXY="http://127.0.0.1:1", HTTPS_PROXY="http://127.0.0.1:1")
    def row():
        return sql(env, "SELECT status,lease_owner,version FROM runner_requests WHERE id='lease-test'")
    def awaiting_profile(name):
        return sql(env, f"""SELECT EXISTS (SELECT FROM pg_stat_activity
            WHERE application_name='{name}' AND wait_event_type='Lock'
              AND query LIKE '%runner_profiles%' AND query LIKE '%WHERE%')""") == "t"
    a_name, b_name = schema + "_a", schema + "_b"
    with node(binary, directory, a_name, env) as a:
        with block_profile_lookup(env):
            sql(env, """INSERT INTO runner_requests
                (id,source,labels_json,runner_name,status,queued_at,updated_at,profile_name)
                VALUES ('lease-test','fixture','[]','lease-test','queued',now(),now(),'missing')""")
            wait_for("A to claim and enter profile lookup", lambda: awaiting_profile(a_name))
            old = row()
            assert old.startswith("queued|") and old.endswith(f"-{a.pid}|1"), old
            claimed_at = int(sql(env, """SELECT floor(extract(epoch FROM last_attempt_at)*1000)::bigint
                FROM runner_requests WHERE id='lease-test'"""))
            os.kill(a.pid, signal.SIGSTOP)
        with node(binary, directory, b_name, env) as b:
            assert row() == old, "B startup changed A's valid lease"
            if crash:
                a.kill()
                a.wait(timeout=10)
            with block_profile_lookup(env):
                if full_ttl:
                    wait_for("B to take over the default lease", lambda: awaiting_profile(b_name),
                             timeout=320, interval=1)
                else:
                    # Shorten the wait in CI and exercise a later retry deadline.
                    sql(env, """UPDATE runner_requests SET lease_expires_at=clock_timestamp(),
                        next_retry_at=clock_timestamp()+interval '1 second' WHERE id='lease-test'""")
                    wait_for("B to take over after eligibility", lambda: awaiting_profile(b_name))
                owned = row()
                assert owned.endswith(f"-{b.pid}|2"), owned
                if full_ttl:
                    taken_at = int(sql(env, """SELECT floor(extract(epoch FROM last_attempt_at)*1000)::bigint
                        FROM runner_requests WHERE id='lease-test'"""))
                    elapsed = (taken_at - claimed_at) / 1000
                    assert elapsed >= 299.999, elapsed
                    print(f"Default 300s lease: {elapsed:.3f}s between claims", flush=True)
                else:
                    assert sql(env, """SELECT last_attempt_at >= next_retry_at FROM runner_requests
                        WHERE id='lease-test'""") == "t"
                if not crash:
                    os.kill(b.pid, signal.SIGSTOP)
            if not crash:
                os.kill(a.pid, signal.SIGCONT)
                wait_for("A to finish its stale failure callback", lambda: sql(env,
                    "SELECT count(*) FROM runner_events WHERE request_id='lease-test' AND stage='runner_timing'") != "0")
                assert row() == owned, "stale callback changed B's queued claim"
                os.kill(b.pid, signal.SIGCONT)
            # Deliberately missing profile produces a definitive admission failure
            # without any GitHub or Sandbox provider call.
            wait_for("B to finish admission", lambda: row().startswith("failed|"))
            assert row() == "failed||3", row()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    parser.add_argument("--full-ttl", action="store_true", help="wait the full 300s in the crash scenario")
    args = parser.parse_args()
    if not sql(os.environ, "SELECT current_database()").endswith("_test"):
        raise RuntimeError("a dedicated database ending in _test is required")
    for crash in [False, True]:
        schema = "queue_" + uuid.uuid4().hex
        sql(os.environ, f"CREATE SCHEMA {schema}")
        try:
            with tempfile.TemporaryDirectory() as tmp:
                exercise(args.binary.resolve(), Path(tmp), schema, crash, args.full_ttl and crash)
            print("PASS:", "crashed owner" if crash else "paused and resumed owner", flush=True)
        finally:
            sql(os.environ, f"DROP SCHEMA {schema} CASCADE")


if __name__ == "__main__":
    main()
