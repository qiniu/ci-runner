"""Unix PTY bridge for the opt-in terminalfault browser fixture, no provider I/O."""

import os
import pty
import select
import signal
import sys

child, master = pty.fork()
if child == 0:
    os.execl("/bin/sh", "sh", "-i")

cleaned = False


def cleanup():
    global cleaned
    if cleaned:
        return
    cleaned = True
    # The PTY child owns its process group. Reap the shell and stop any foreground
    # sleep as well, including teardown while the browser cannot send DELETE.
    try:
        os.killpg(child, signal.SIGKILL)
    except ProcessLookupError:
        pass
    try:
        os.waitpid(child, 0)
    except ChildProcessError:
        pass


def stop(*_):
    cleanup()
    sys.exit(0)


signal.signal(signal.SIGTERM, stop)
print(f"PTY_PID:{child}", flush=True)
try:
    while True:
        readable, _, _ = select.select([0, master], [], [])
        for fd in readable:
            data = os.read(fd, 65536)
            if not data:
                stop()
            destination = master if fd == 0 else 1
            while data:
                data = data[os.write(destination, data):]
except OSError:
    # Linux reports EIO when the PTY slave closes; macOS can report EOF.
    pass
finally:
    cleanup()
