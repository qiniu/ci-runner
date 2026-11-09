#!/usr/bin/env python3
"""Build a resumable regional batch using the existing per-template helpers."""

import asyncio
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import tempfile
import time
import tomllib

ROOT = Path(__file__).resolve().parents[1]
QSHELL = os.environ.get("QSHELL", "qshell")
ID = r"[A-Za-z0-9][A-Za-z0-9_.-]*"
SUCCESS = {"ready", "uploaded"}
FAILED = {"failed", "error"}


def configs(include_windows):
    directories = [ROOT / "templates" / f"github-runner-ubuntu-{os_name}"
                   for os_name in ("slim", "22.04", "24.04", "26.04")]
    paths = [directory / filename for filename in ("qshell.sandbox.toml", "qshell.sandbox.large.toml")
             for directory in directories]
    if include_windows:
        paths.append(ROOT / "templates/github-runner-windows-11/qshell.sandbox.toml")
    result = {}
    for path in paths:
        name = tomllib.loads(path.read_text())["name"]
        if not re.fullmatch(ID, name) or name in result:
            raise ValueError(f"invalid or duplicate template name in {path}")
        result[name] = path
    return result


def fingerprint(config, version):
    # Hash working files, including uncommitted setup scripts and symlink targets.
    # Generated Linux chunks are independently verified against actions-runner.env.
    paths = {config, ROOT / "scripts/run-runner-template-operation.sh",
             ROOT / "scripts/lib/runner-template-disk.sh", Path(__file__).resolve()}
    templates = ROOT / "templates"
    directories = [config.parent]
    if config.parent.name == "github-runner-windows-11":
        paths.update([templates / "common/actions-runner.env", ROOT / "scripts/build-windows-template.sh"])
    else:
        directories.append(templates / "common")
        paths.add(ROOT / "scripts/prepare-runner-archive.sh")
    if (templates / ".dockerignore").exists():
        paths.add(templates / ".dockerignore")
    for directory in directories:
        for base, dirs, files in os.walk(directory):
            dirs[:] = [d for d in dirs if d != ".build"]
            for file in files:
                if file.startswith((".qshell-sandbox.", "qshell.sandbox")) or file.endswith(".md"):
                    continue
                paths.add(Path(base) / file)
    digest = hashlib.sha256(version.encode())
    for path in sorted(paths):
        for value in (str(path.relative_to(ROOT)), str(path.stat().st_mode),
                      os.readlink(path) if path.is_symlink() else "",
                      hashlib.sha256(path.read_bytes()).hexdigest()):
            digest.update(value.encode() + b"\0")
    return digest.hexdigest()


def save(path, record):
    with tempfile.NamedTemporaryFile("w", dir=path.parent, delete=False) as tmp:
        json.dump(record, tmp)
        tmp.flush()
        os.fsync(tmp.fileno())
    os.replace(tmp.name, path)
    directory = os.open(path.parent, os.O_RDONLY)
    try:
        os.fsync(directory)
    finally:
        os.close(directory)


def submit(config):
    # Called from the build helper after local preflight and archive preparation.
    # Keep the pending record even if interrupted before qshell returns its IDs.
    path = Path(os.environ["TEMPLATE_BATCH_RECEIPT"])
    record = {"batch": os.environ["BUILD_BATCH"], "inputs": os.environ["TEMPLATE_BATCH_INPUTS"],
              "status": "submitting"}
    save(path, record)
    template = None
    with subprocess.Popen([QSHELL, "sandbox", "template", "build", "--config", config],
                          stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                          text=True, errors="replace") as proc:
        for line in proc.stdout:
            line = line.rstrip()
            print(f"[{path.stem}] {line}", flush=True)
            created = re.fullmatch(rf"Template ({ID}) created \(build ID: ({ID})\)", line)
            rebuilding = re.fullmatch(rf"Requesting new build for template ({ID})\.\.\.", line)
            rebuilt = re.fullmatch(rf"New build requested \(build ID: ({ID})\)", line)
            started = re.fullmatch(rf"Build started\. Use 'qshell sandbox template builds ({ID}) ({ID})' to check status\.", line)
            if rebuilding:
                template = rebuilding[1]
            if created or started:
                template, build = (created or started).groups()
            elif rebuilt and template:
                build = rebuilt[1]
            else:
                continue
            if "build" in record and (template, build) != (record["template"], record["build"]):
                raise RuntimeError(f"build IDs changed during submission; inspect {path}")
            record.update(template=template, build=build, status="pending" if started else "requested")
            save(path, record)
    if record["status"] != "pending":
        raise RuntimeError(f"build start not confirmed; inspect {path} and the provider before retrying")
    return proc.returncode


async def command(*args, capture=False, timeout=None, **kwargs):
    proc = await asyncio.create_subprocess_exec(*args, cwd=ROOT, start_new_session=True,
                                                stdout=subprocess.PIPE if capture else None, **kwargs)
    try:
        output, _ = await asyncio.wait_for(proc.communicate(), timeout)
    except (TimeoutError, asyncio.CancelledError) as error:
        if proc.returncode is None:
            try:
                os.killpg(proc.pid, signal.SIGTERM)
                try:
                    await asyncio.wait_for(proc.wait(), 5)
                except TimeoutError:
                    os.killpg(proc.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            await proc.wait()
        if isinstance(error, TimeoutError):
            raise RuntimeError(f"{Path(args[0]).name} timed out after {timeout}s") from error
        raise
    if proc.returncode:
        detail = output.decode(errors="replace").strip()[-1000:] if output else ""
        raise RuntimeError(f"{Path(args[0]).name} exited {proc.returncode}: {detail}")
    return output.decode(errors="replace") if capture else ""


async def terminal(name, record):
    deadline = time.monotonic() + int(os.environ.get("BUILD_WAIT_SECONDS", "3600"))
    status = "unavailable"
    last_error = ""
    while True:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            break
        match = None
        last_error = ""
        try:
            output = await command(QSHELL, "sandbox", "template", "builds", record["template"],
                                   record["build"], capture=True, stderr=subprocess.STDOUT,
                                   timeout=min(30, remaining))
            match = re.match(rf"Template ID:\s*({ID})\s+Build ID:\s*({ID})\s+Status:\s*(\w+)\b", output)
            if not match:
                last_error = output.strip()[-1000:] or "empty status response"
        except (OSError, RuntimeError) as error:
            last_error = str(error)
        if match and match.groups()[:2] != (record["template"], record["build"]):
            raise RuntimeError(f"unconfirmed response for build {record['build']}")
        status = match[3].lower() if match else "unavailable"
        if status in SUCCESS | FAILED:
            return status
        if time.monotonic() >= deadline:
            break
        print(f"[{name}] {record['build']} {status}", flush=True)
        await asyncio.sleep(min(15, max(0, deadline - time.monotonic())))
    if last_error:
        raise RuntimeError(f"could not query build {record['build']}: {last_error}; rerun to resume")
    raise RuntimeError(f"build {record['build']} is still {status}; rerun to resume")


async def run_batch(state, lock_fd, jobs, batch, limit):
    version = await command(QSHELL, "version", capture=True, timeout=30)
    inputs = {name: fingerprint(path, version) for name, path in jobs.items()}
    semaphore = asyncio.Semaphore(limit)
    skip = {}

    async def reconcile(path):
        async with semaphore:
            name = path.stem
            record = json.loads(path.read_text())
            same = name in inputs and record["batch"] == batch and record["inputs"] == inputs[name]
            if record["status"] in FAILED or (record["status"] in SUCCESS and not same):
                return
            if "build" not in record:
                raise RuntimeError(f"{path}: submission outcome unknown; inspect the provider before clearing this receipt")
            if record["status"] == "requested":
                raise RuntimeError(f"{path}: build {record['build']} start not confirmed; inspect the provider before clearing this receipt")
            status = await terminal(name, record)
            record["status"] = status
            save(path, record)
            if same and status in SUCCESS:
                skip[name] = record

    # Resolve outstanding attempts before admitting any new work, even if the
    # next invocation selected a different batch, concurrency or Windows setting.
    results = await asyncio.gather(*(reconcile(path) for path in sorted(state.glob("*.json"))),
                                   return_exceptions=True)
    for result in results:
        if isinstance(result, BaseException):
            raise result
    # Reconciliation can take a while. Use the current checkout when deciding
    # whether to skip or submit, including edits made while waiting.
    skip = {name: record for name, record in skip.items()
            if record["inputs"] == fingerprint(jobs[name], version)}
    if skip:
        rows = json.loads(await command(QSHELL, "sandbox", "template", "list", "--format",
                                        "json", capture=True, timeout=30))
        for name, record in skip.items():
            matches = [row for row in rows if name in (row.get("Aliases") or [])
                       or any(n.rsplit("/", 1)[-1] == name for n in row.get("Names") or [])]
            if len(matches) != 1 or (matches[0]["TemplateID"], matches[0]["BuildID"]) != (
                    record["template"], record["build"]):
                raise RuntimeError(f"{name}: catalog changed; verify it before starting a new BUILD_BATCH")
            print(f"[{name}] skipped verified build {record['build']}", flush=True)

    failures = []
    uncertain = False

    async def build(name, config):
        nonlocal uncertain
        async with semaphore:
            if uncertain:
                return
            path = state / f"{name}.json"
            helper = ["bash", "scripts/run-runner-template-operation.sh", "build", str(config)]
            if config.parent.name == "github-runner-windows-11":
                helper = ["bash", "scripts/build-windows-template.sh", str(config.parent)]
            try:
                digest = fingerprint(config, version)
                env = dict(os.environ, BUILD_BATCH=batch, TEMPLATE_BATCH_INPUTS=digest,
                           TEMPLATE_BATCH_RECEIPT=str(path))
                await command(*helper, env=env, pass_fds=(lock_fd,))
                record = json.loads(path.read_text())
                if "build" not in record or record["batch"] != batch or record["inputs"] != digest:
                    raise RuntimeError(f"submission not confirmed; inspect {path}")
                status = await terminal(name, record)
                record["status"] = status
                if fingerprint(config, version) != digest:
                    record["inputs"] = None
                    failures.append(f"{name}: inputs changed during build; rerun this batch")
                save(path, record)
                print(f"[{name}] {record['template']} {record['build']} {status}", flush=True)
                if status in FAILED:
                    failures.append(name)
            except (OSError, ValueError, KeyError, RuntimeError, TimeoutError) as error:
                # A remote build may still occupy this slot.
                uncertain = True
                failures.append(f"{name}: {error}")

    await asyncio.gather(*(build(name, config) for name, config in jobs.items() if name not in skip))
    if failures:
        raise RuntimeError("batch incomplete: " + "; ".join(failures))


async def main():
    asyncio.get_running_loop().add_signal_handler(signal.SIGTERM, asyncio.current_task().cancel)
    endpoint = os.environ["QINIU_SANDBOX_API_URL"].rstrip("/")
    if not endpoint or not os.environ.get("QINIU_API_KEY"):
        raise ValueError("QINIU_SANDBOX_API_URL and QINIU_API_KEY are required")
    limit = int(os.getenv("BUILD_CONCURRENCY", "4"))
    batch = os.getenv("BUILD_BATCH", "default")
    windows = os.getenv("INCLUDE_WINDOWS", "0")
    if not 1 <= limit <= 9 or windows not in {"0", "1"} or not re.fullmatch(ID, batch):
        raise ValueError("expected BUILD_CONCURRENCY=1..9, INCLUDE_WINDOWS=0 or 1 and a simple BUILD_BATCH label")
    if int(os.getenv("BUILD_WAIT_SECONDS", "3600")) < 1:
        raise ValueError("BUILD_WAIT_SECONDS must be positive")
    git_dir = subprocess.check_output(["git", "rev-parse", "--git-common-dir"], cwd=ROOT, text=True).strip()
    state = ROOT / git_dir / "template-build-batches" / hashlib.sha256(endpoint.encode()).hexdigest()
    state.mkdir(parents=True, exist_ok=True)
    with (state / "lock").open("a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise RuntimeError("another template batch is running for this endpoint") from None
        print(f"Batch {batch}; concurrency {limit}; receipts {state.resolve()}", flush=True)
        await run_batch(state.resolve(), lock.fileno(), configs(windows == "1"), batch, limit)


if __name__ == "__main__":
    try:
        if len(sys.argv) == 3 and sys.argv[1] == "--submit":
            sys.exit(submit(sys.argv[2]))
        if len(sys.argv) != 1:
            sys.exit("usage: task template-build-all [BUILD_BATCH=name] [BUILD_CONCURRENCY=1..9] [INCLUDE_WINDOWS=1]")
        asyncio.run(main())
    except (KeyboardInterrupt, asyncio.CancelledError):
        sys.exit("template batch interrupted; rerun to resume")
    except (OSError, KeyError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
        sys.exit(f"template batch: {error}")
