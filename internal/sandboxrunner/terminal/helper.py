# runnerd-owned Linux PTY supervisor. Only stdlib; no provider changes.
import base64
import ctypes
import fcntl
import json
import os
import pty
import select
import signal
import socket
import struct
import subprocess
import sys
import termios
import time

ROOT = os.path.dirname(os.path.abspath(__file__))
SOCKET = os.path.join(ROOT, 'control.sock')


def locked():
    fd = os.open(os.path.join(ROOT, 'gate'), os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    fcntl.flock(fd, fcntl.LOCK_EX)
    return fd


def mark(name):
    fd = os.open(os.path.join(ROOT, name), os.O_CREAT | os.O_EXCL | os.O_WRONLY | os.O_NOFOLLOW, 0o600)
    os.close(fd)


def emit(value):
    print(json.dumps(value, separators=(',', ':')), flush=True)


def resize(fd, rows, cols):
    if not (type(rows) is int and type(cols) is int and 1 <= rows <= 500 and 1 <= cols <= 500):
        raise ValueError('size')
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack('HHHH', rows, cols, 0, 0))


def serve(rows, cols, deadline):
    # A delayed provider dispatch cannot create a shell after its start budget.
    if time.time() > deadline:
        return
    gate = locked()
    try:
        if os.path.exists(os.path.join(ROOT, 'closed')):
            return
        mark('used')  # Never reuse an ID, including a crashed creation.
        listener = socket.socket(socket.AF_UNIX)
        listener.bind(SOCKET)
        os.chmod(SOCKET, 0o600)
        listener.listen(4)
        listener.settimeout(1)
        master, slave = pty.openpty()
        resize(master, rows, cols)
        parent = os.getpid()

        def child_setup():
            os.setsid()
            fcntl.ioctl(slave, termios.TIOCSCTTY, 0)
            # Linux only. Direct child dies when this supervisor dies. We do
            # not claim to terminate intentionally detached descendants.
            if ctypes.CDLL(None).prctl(1, signal.SIGKILL) != 0 or os.getppid() != parent:
                os._exit(1)

        child = subprocess.Popen(['/bin/bash', '-l'], stdin=slave, stdout=slave, stderr=slave,
                                 preexec_fn=child_setup, env={**os.environ, 'TERM': 'xterm-256color'})
        os.close(slave)
    finally:
        os.close(gate)
    # Keep the child unreaped until cleanup; its PID cannot be reused while
    # kill is possible. Never poll/wait then kill that numerical PID.
    last_ping = time.monotonic()
    os.set_blocking(master, False)
    os.set_blocking(sys.stdout.fileno(), False)
    pending = (json.dumps({'ready': child.pid}) + '\n').encode()
    emitted = 0
    try:
        running = True
        while running and time.monotonic() - last_ping < 30:
            readable, writable, _ = select.select([listener, master], [sys.stdout] if pending else [], [], 1)
            if sys.stdout in writable:
                try:
                    written = os.write(sys.stdout.fileno(), pending)
                    emitted += written
                    pending = pending[written:]
                    # The existing SDK retains command stdout. Bound total wire
                    # output as well as live queues; do not change the SDK.
                    if emitted >= 4 * 1024 * 1024:
                        break
                except BlockingIOError:
                    pass
            if master in readable:
                try:
                    data = os.read(master, 4096)
                except OSError:
                    break
                if not data:
                    break
                pending += (json.dumps({'data': base64.b64encode(data).decode()}) + '\n').encode()
                if len(pending) > 131072:
                    break  # Backpressure closes instead of silently losing output.
            if listener in readable:
                conn, _ = listener.accept()
                with conn:
                    conn.settimeout(2)
                    raw = b''
                    while b'\n' not in raw and len(raw) <= 16384:
                        part = conn.recv(16384 - len(raw) + 1)
                        if not part:
                            break
                        raw += part
                    try:
                        q = json.loads(raw)
                        if not time.time() < q['expires'] <= time.time() + 15:
                            raise ValueError('expired')
                        op = q['op']
                        if op == 'input':
                            data = base64.b64decode(q['data'], validate=True)
                            if len(data) > 4096:
                                raise ValueError('input')
                            # Partial writes are errors, never replayed.
                            if os.write(master, data) != len(data):
                                raise ValueError('short write')
                        elif op == 'resize':
                            resize(master, q['rows'], q['cols'])
                        elif op == 'ping':
                            last_ping = time.monotonic()
                        elif op == 'close':
                            running = False
                        else:
                            raise ValueError('operation')
                        last_ping = time.monotonic()
                        conn.sendall(b'ok\n')
                    except (ValueError, KeyError, OSError):
                        running = False
                        try:
                            conn.sendall(b'error\n')
                        except OSError:
                            pass
    finally:
        listener.close()
        os.close(master)
        # No wait/poll/reaper thread ran before this kill.
        try:
            os.kill(child.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        child.wait()
        gate = locked()
        try:
            if not os.path.exists(os.path.join(ROOT, 'closed')):
                mark('closed')
        finally:
            os.close(gate)
        os.unlink(SOCKET)
        mark('finished')


def call(name):
    # Payload never enters command arguments or provider stdout/stderr.
    if len(name) != 32 or any(c not in '0123456789abcdef' for c in name):
        raise ValueError('name')
    path = os.path.join(ROOT, name)
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    os.unlink(path)  # Consume before dispatch: an unknown result is not retried.
    with os.fdopen(fd, 'rb') as f:
        raw = f.read(16385)
    if len(raw) > 16384:
        raise ValueError('size')
    q = json.loads(raw)
    if q.get('op') == 'close':
        gate = locked()
        try:
            if not os.path.exists(os.path.join(ROOT, 'closed')):
                mark('closed')  # Also prevents a delayed serve from starting.
        finally:
            os.close(gate)
    if q.get('op') == 'close' and os.path.exists(os.path.join(ROOT, 'finished')):
        print('ok')
        return
    with socket.socket(socket.AF_UNIX) as conn:
        conn.settimeout(3)
        conn.connect(SOCKET)
        conn.sendall(raw + b'\n')
        if conn.recv(16) != b'ok\n':
            raise ValueError('operation')
    if q.get('op') == 'close':
        # A close request is not proof of completed cleanup. Socket removal
        # occurs only after PTY close and child wait.
        end = time.monotonic() + 3
        while os.path.exists(SOCKET) and time.monotonic() < end:
            time.sleep(.02)
        if os.path.exists(SOCKET):
            raise ValueError('close pending')
    print('ok')


if __name__ == '__main__':
    try:
        if sys.argv[1] == 'serve':
            serve(int(sys.argv[2]), int(sys.argv[3]), float(sys.argv[4]))
        elif sys.argv[1] == 'call':
            call(sys.argv[2])
        else:
            raise ValueError('mode')
    except Exception:
        # Do not print input, provider diagnostics or environment.
        sys.exit(1)
