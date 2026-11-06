"""Run two runnerd processes against an isolated schema in a PostgreSQL *_test DB."""
import argparse
import base64
from concurrent.futures import ThreadPoolExecutor
import contextlib
import hashlib
import hmac
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import tempfile
import time
import uuid
from urllib.request import Request, urlopen

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
def hold_lock(env, statement):
    with subprocess.Popen(["psql", "-XqAt", "-v", "ON_ERROR_STOP=1", "-d", DSN],
                          stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                          env=env, text=True) as psql:
        psql.stdin.write(f"BEGIN; SET LOCAL lock_timeout='10s'; {statement}; SELECT 'locked';\n")
        psql.stdin.flush()
        if psql.stdout.readline().strip() != "locked":
            raise RuntimeError("could not acquire fixture lock")
        try:
            yield
        finally:
            psql.communicate("COMMIT;\n", timeout=10)


@contextlib.contextmanager
def node(binary, directory, name, env, worker=None):
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
        "worker": {"lease_ttl_seconds": 300, **(worker or {})},
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
        with hold_lock(env, "LOCK runner_profiles IN ACCESS EXCLUSIVE MODE"):
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
            with hold_lock(env, "LOCK runner_profiles IN ACCESS EXCLUSIVE MODE"):
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


def exercise_capacity(binary, directory, schema):
    # A listening proxy with no upstream holds the admitted attempt at GitHub's
    # job check. All observations below are persisted by real runnerd workers.
    with socket.socket() as proxy:
        proxy.bind(("127.0.0.1", 0))
        proxy.listen()
        address = f"http://127.0.0.1:{proxy.getsockname()[1]}"
        env = dict(os.environ, PGOPTIONS=f"-c search_path={schema}", HTTP_PROXY=address,
                   HTTPS_PROXY=address, NO_PROXY="127.0.0.1,localhost")
        worker = {"max_concurrent_runners": 1}
        with node(binary, directory, schema + "_a", env, worker), node(binary, directory, schema + "_b", env, worker):
            sql(env, """INSERT INTO runner_profiles
                (name,labels_json,template_id,max_concurrency,enabled,default_available,created_at,updated_at)
                VALUES ('shared','["self-hosted"]','fixture',1,true,false,now(),now());
                CREATE TABLE admissions (id text, worker text);
                CREATE FUNCTION record_admission() RETURNS trigger AS $$
                BEGIN
                  IF OLD.status='queued' AND NEW.status='creating' THEN
                    PERFORM pg_sleep(0.5);
                    INSERT INTO admissions VALUES (NEW.id, NEW.lease_owner);
                  END IF;
                  RETURN NEW;
                END; $$ LANGUAGE plpgsql;
                CREATE TRIGGER observed BEFORE UPDATE ON runner_requests
                FOR EACH ROW EXECUTE FUNCTION record_admission()""")
            with hold_lock(env, "LOCK runner_profiles IN ACCESS EXCLUSIVE MODE"):
                sql(env, """INSERT INTO runner_requests
                    (id,source,workflow_job_id,repository_full_name,labels_json,requested_labels_json,
                     runner_name,status,queued_at,updated_at,profile_name)
                    SELECT 'capacity-' || i,'fixture',i,'fixture/ci','["self-hosted"]','["self-hosted"]',
                           'capacity-' || i,'queued',now(),now(),'shared'
                    FROM generate_series(1,2) i""")
                wait_for("two different workers to claim", lambda: sql(env,
                    "SELECT count(DISTINCT lease_owner) FROM runner_requests WHERE lease_owner <> ''") == "2")
            wait_for("losing worker to defer", lambda: sql(env, """SELECT count(*) FROM runner_requests
                WHERE status='queued' AND lease_owner='' AND last_error_code='runner_capacity_global'""") == "1")
            assert sql(env, "SELECT count(*) FROM admissions") == "1"
            assert sql(env, "SELECT count(*) FROM runner_requests WHERE status='creating'") == "1"
            # Model terminal persistence; normal queue claim must reuse the slot.
            sql(env, """UPDATE runner_requests SET status='completed',version=version+1 WHERE status='creating';
                UPDATE runner_requests SET next_retry_at=clock_timestamp() WHERE status='queued'""")
            wait_for("next job to use the released slot", lambda: sql(env,
                "SELECT count(DISTINCT id) FROM admissions") == "2")


def exercise_admission_crash(binary, directory, schema):
    env = dict(os.environ, PGOPTIONS=f"-c search_path={schema}", HTTP_PROXY="http://127.0.0.1:1",
               HTTPS_PROXY="http://127.0.0.1:1")
    a_name, b_name = schema + "_a", schema + "_b"
    with node(binary, directory, a_name, env) as a, node(binary, directory, b_name, env) as b:
        sql(env, """INSERT INTO runner_profiles
            (name,labels_json,template_id,max_concurrency,enabled,default_available,created_at,updated_at)
            VALUES ('shared','["self-hosted"]','fixture',1,true,false,now(),now());
            CREATE TABLE admissions(worker text);
            CREATE FUNCTION pause_creating() RETURNS trigger AS $$
            BEGIN
              IF OLD.status='queued' AND NEW.status='creating' THEN
                INSERT INTO admissions VALUES (NEW.lease_owner);
                LOCK admissions IN ACCESS EXCLUSIVE MODE;
              END IF;
              RETURN NEW;
            END; $$ LANGUAGE plpgsql;
            CREATE TRIGGER pause_creating BEFORE UPDATE ON runner_requests
            FOR EACH ROW EXECUTE FUNCTION pause_creating()""")
        os.kill(b.pid, signal.SIGSTOP)
        # ROW SHARE permits A's trigger INSERT but holds its subsequent lock upgrade.
        with hold_lock(env, "LOCK admissions IN ROW SHARE MODE"):
            sql(env, """INSERT INTO runner_requests
                (id,source,runner_name,status,labels_json,requested_labels_json,queued_at,updated_at,profile_name)
                VALUES ('crash-admit','fixture','crash-admit','queued','["self-hosted"]','["self-hosted"]',
                        now(),now(),'shared')""")
            wait_for("A to reach the creating write", lambda: sql(env, f"""SELECT EXISTS (
                SELECT FROM pg_stat_activity WHERE application_name='{a_name}' AND wait_event_type='Lock'
                  AND query ILIKE '%UPDATE%runner_requests%')""") == "t")
            a.kill()
            a.wait(timeout=10)
            # PostgreSQL may finish a submitted statement after the client dies.
            # Wait for connection cleanup or the bounded server-side timeout.
            wait_for("A to lose its open transaction", lambda: sql(env, f"""SELECT count(*) FROM pg_stat_activity
                WHERE application_name='{a_name}' AND xact_start IS NOT NULL""") == "0")
        assert sql(env, "SELECT count(*) FROM admissions") == "0"
        assert sql(env, "SELECT status FROM runner_requests WHERE id='crash-admit'") == "queued"
        sql(env, "UPDATE runner_requests SET lease_expires_at=clock_timestamp() WHERE id='crash-admit'")
        os.kill(b.pid, signal.SIGCONT)
        wait_for("B to reclaim and commit the rolled-back slot", lambda: sql(env,
            "SELECT worker FROM admissions").endswith(f"-{b.pid}"))
        assert sql(env, "SELECT count(*) FROM admissions") == "1"


def exercise_disable(binary, directory, schema):
    env = dict(os.environ, PGOPTIONS=f"-c search_path={schema}", HTTP_PROXY="http://127.0.0.1:1",
               HTTPS_PROXY="http://127.0.0.1:1")
    a_name, b_name = schema + "_a", schema + "_b"
    with node(binary, directory, a_name, env) as a, node(binary, directory, b_name, env) as b:
        sql(env, """INSERT INTO runner_profiles
            (name,labels_json,template_source,template_id,max_concurrency,enabled,default_available,created_at,updated_at)
            VALUES ('shared','["self-hosted"]','private','fixture',1,true,false,now(),now());
            INSERT INTO accounts(id,role,created_at,updated_at) VALUES (444,'admin',now(),now());
            INSERT INTO oauth_identities(account_id,oauth_provider,oauth_subject,oauth_login,created_at,updated_at)
            VALUES (444,'github','fixture','fixture',now(),now())""")
        def encode(value):
            return base64.urlsafe_b64encode(value).rstrip(b"=")
        payload = encode(json.dumps({"provider":"github","subject":"fixture","login":"fixture","role":"admin",
                                     "expires_at":int(time.time())+60}).encode())
        sig = encode(hmac.new(b"fixture-session", payload, hashlib.sha256).digest())
        address = json.loads((directory / (b_name + ".json")).read_text())["server"]["http_addr"]
        request = Request(f"http://{address}/runner_specs/shared", data=b'{"enabled":false}', method="PATCH",
                          headers={"Cookie": "runnerd_admin_session=" + (payload + b"." + sig).decode(),
                                   "Content-Type":"application/json"})
        def disable():
            with urlopen(request, timeout=10) as response:
                return response.status
        with ThreadPoolExecutor(max_workers=1) as pool:
            with hold_lock(env, "LOCK runner_profiles IN SHARE MODE"):
                result = pool.submit(disable)
                def editing():
                    if result.done():
                        raise RuntimeError(f"PATCH ended before the guarded update: {result.result()}")
                    return sql(env, f"""SELECT EXISTS (SELECT FROM pg_stat_activity
                        WHERE application_name='{b_name}' AND wait_event_type='Lock'
                          AND query ILIKE '%UPDATE%runner_profiles%')""") == "t"
                wait_for("B to hold the guard while disabling the spec", editing)
                # B has submitted its UPDATE. Let A be the worker to claim the new row.
                os.kill(b.pid, signal.SIGSTOP)
                sql(env, """INSERT INTO runner_requests(id,source,runner_name,labels_json,requested_labels_json,
                    status,queued_at,updated_at,profile_name) VALUES
                    ('spec-race','fixture','spec-race','["self-hosted"]','["self-hosted"]','queued',now(),now(),'shared')""")
                wait_for("A to claim while B is changing the spec", lambda: sql(env,
                    "SELECT lease_owner FROM runner_requests WHERE id='spec-race'").endswith(f"-{a.pid}"))
                os.kill(b.pid, signal.SIGCONT)
            assert result.result() == 200
        wait_for("A to reject the disabled spec", lambda: sql(env, """SELECT status || '|' || failure_stage
            FROM runner_requests WHERE id='spec-race'""") == "failed|profile_validation")
        assert sql(env, "SELECT creating_at IS NULL FROM runner_requests WHERE id='spec-race'") == "t"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    parser.add_argument("--full-ttl", action="store_true", help="wait the full 300s in the crash scenario")
    args = parser.parse_args()
    if not sql(os.environ, "SELECT current_database()").endswith("_test"):
        raise RuntimeError("a dedicated database ending in _test is required")
    for scenario in ["paused", "crashed", "cluster capacity", "disable spec", "admission crash"]:
        schema = "queue_" + uuid.uuid4().hex
        sql(os.environ, f"CREATE SCHEMA {schema}")
        try:
            with tempfile.TemporaryDirectory() as tmp:
                if scenario == "admission crash":
                    exercise_admission_crash(args.binary.resolve(), Path(tmp), schema)
                elif scenario == "disable spec":
                    exercise_disable(args.binary.resolve(), Path(tmp), schema)
                elif scenario == "cluster capacity":
                    exercise_capacity(args.binary.resolve(), Path(tmp), schema)
                else:
                    crash = scenario == "crashed"
                    exercise(args.binary.resolve(), Path(tmp), schema, crash, args.full_ttl and crash)
            print("PASS:", scenario, flush=True)
        finally:
            sql(os.environ, f"DROP SCHEMA {schema} CASCADE")


if __name__ == "__main__":
    main()
