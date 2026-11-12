"""Run in a disposable Linux container; never against a production Sandbox."""
import base64
import concurrent.futures
import json
import os
import select
import shutil
import signal
import socket
import subprocess
import tempfile
import time
import unittest
import uuid
from pathlib import Path


class HelperTest(unittest.TestCase):
    def setUp(self):
        self.root = Path(tempfile.mkdtemp(prefix='terminal-test-'))
        shutil.copy(Path(__file__).with_name('helper.py'), self.root / 'helper.py')
        self.children = []

    def tearDown(self):
        for p in self.children:
            if p.poll() is None:
                p.kill()
            p.wait()
            if p.stdout: p.stdout.close()
        shutil.rmtree(self.root)

    def serve(self, deadline=None):
        p = subprocess.Popen(['python3', str(self.root / 'helper.py'), 'serve', '28', '100', str(deadline or time.time()+10)], stdout=subprocess.PIPE, bufsize=0)
        self.children.append(p)
        return p

    def ready(self, p):
        self.assertTrue(select.select([p.stdout], [], [], 5)[0], 'no readiness')
        frame = self.decode(p.stdout.readline())
        self.assertGreater(frame['ready'], 0)
        return frame['ready']

    def decode(self, line):
        prefix = b'\x1eRUNNERD_TERMINAL:'
        self.assertTrue(line.startswith(prefix), line)
        return json.loads(line[len(prefix):])

    def call(self, op, **args):
        name = uuid.uuid4().hex
        (self.root / name).write_text(json.dumps({'op': op, 'expires': time.time()+5, **args}))
        return subprocess.run(['python3', str(self.root / 'helper.py'), 'call', name], capture_output=True, timeout=8)

    def output(self, p, marker):
        data = b''
        deadline = time.monotonic()+5
        while time.monotonic() < deadline:
            if select.select([p.stdout], [], [], .2)[0]:
                line = p.stdout.readline()
                if not line:
                    break
                data += base64.b64decode(self.decode(line).get('data', ''))
                if marker in data:
                    return data
        self.fail(f'missing {marker!r}: {data!r}')

    def test_interaction_duplicate_start_and_close(self):
        p = self.serve(); self.ready(p)
        duplicate = self.serve(); self.assertNotEqual(duplicate.wait(timeout=5), 0)
        self.assertEqual(self.call('resize', rows=41, cols=121).returncode, 0)
        command = b"stty size; export TEST_VALUE=kept; printf '__VALUE=%s__\\n' \"$TEST_VALUE\"\n"
        self.assertEqual(self.call('input', data=base64.b64encode(command).decode()).returncode, 0)
        out = self.output(p, b'__VALUE=kept__')
        self.assertIn(b'41 121', out)
        self.assertEqual(self.call('close').returncode, 0)
        self.assertEqual(p.wait(timeout=5), 0)
        self.assertTrue((self.root/'finished').exists())
        self.assertEqual(self.call('close').returncode, 0)
        late = self.serve(); self.assertEqual(late.wait(timeout=5), 0)
        self.assertEqual(late.stdout.read(), b'')

    def test_close_before_unknown_creation(self):
        # No readiness was observed; close leaves a tombstone even with no socket.
        self.assertNotEqual(self.call('close').returncode, 0)
        p = self.serve(); self.assertEqual(p.wait(timeout=5), 0)
        self.assertEqual(p.stdout.read(), b'')
        self.assertFalse((self.root/'used').exists())

    def test_concurrent_create_and_close(self):
        with concurrent.futures.ThreadPoolExecutor() as pool:
            starts = [pool.submit(self.serve) for _ in range(8)]
            close = pool.submit(self.call, 'close')
            processes = [f.result() for f in starts]
            close.result()
        # If create won, close controls that one supervisor. Otherwise the
        # tombstone prevents all late creates. No ID is restarted.
        self.call('close')
        for p in processes:
            p.wait(timeout=7)
        self.assertLessEqual(sum(b'"ready"' in p.stdout.read() for p in processes), 1)

    def test_output_is_bounded_even_when_client_keeps_reading(self):
        p = self.serve(); self.ready(p)
        command = b"python3 -c 'import os; os.write(1, b\"x\"*4000000)'\n"
        self.assertEqual(self.call('input', data=base64.b64encode(command).decode()).returncode, 0)
        total = 0
        while True:
            self.assertTrue(select.select([p.stdout], [], [], 5)[0])
            data = os.read(p.stdout.fileno(), 65536)
            if not data: break
            total += len(data)
        self.assertLessEqual(total, 4*1024*1024+8192)
        self.assertEqual(p.wait(timeout=5), 0)

    def final_output_with_backpressure(self):
        p = self.serve(); self.ready(p)
        # Keep encoded output below the helper's 128 KiB pending budget but
        # above a normal pipe capacity. No literal Z occurs in the echo.
        command = b"python3 -c 'import os; os.write(1, bytes([90])*80000)'; exit\n"
        self.assertEqual(self.call('input', data=base64.b64encode(command).decode()).returncode, 0)
        return p

    def test_final_output_drains_after_short_backpressure(self):
        p = self.final_output_with_backpressure()
        time.sleep(.3)  # Let the shell exit while stdout cannot accept its tail.
        output, _ = p.communicate(timeout=5)
        data = b''.join(base64.b64decode(self.decode(line).get('data', ''))
                        for line in output.split(b'\n') if line)
        self.assertEqual(data.count(b'Z'), 80000)
        self.assertEqual(p.returncode, 0)
        self.assertTrue((self.root/'finished').exists())

    def test_final_output_drain_has_deadline(self):
        p = self.final_output_with_backpressure()
        started = time.monotonic()
        # Do not read the pipe at all. Cleanup must not wait for its consumer.
        self.assertNotEqual(p.wait(timeout=5), 0)
        self.assertLess(time.monotonic()-started, 4)
        self.assertTrue((self.root/'finished').exists())

    def test_close_can_interrupt_final_output_drain(self):
        p = self.final_output_with_backpressure()
        time.sleep(.3)
        self.assertEqual(self.call('close').returncode, 0)
        self.assertEqual(p.wait(timeout=3), 0)
        self.assertTrue((self.root/'finished').exists())

    def test_partial_control_cannot_extend_output_drain(self):
        p = self.final_output_with_backpressure()
        deadline = time.monotonic()+3
        time.sleep(.3)
        with socket.socket(socket.AF_UNIX) as control:
            control.settimeout(.5)
            control.connect(str(self.root/'control.sock'))
            control.sendall(b'{')
            while p.poll() is None and time.monotonic() < deadline:
                time.sleep(.1)
                try:
                    control.sendall(b' ')  # Never finish the request.
                except OSError:
                    break
        self.assertNotEqual(p.wait(timeout=1), 0)
        self.assertLess(time.monotonic(), deadline)
        self.assertTrue((self.root/'finished').exists())

    def test_unknown_creation_watchdog_and_expired_dispatch(self):
        p = self.serve(); self.ready(p)  # Drop control, as if runnerd crashed.
        self.assertEqual(p.wait(timeout=35), 0)
        self.assertTrue((self.root/'finished').exists())
        root = self.root / 'late'; root.mkdir(); shutil.copy(self.root/'helper.py', root/'helper.py')
        result = subprocess.run(['python3',str(root/'helper.py'),'serve','28','100',str(time.time()-1)], capture_output=True)
        self.assertEqual(result.stdout,b'');self.assertFalse((root/'used').exists())

    def stopped_supervisor_child(self):
        p = self.serve(); child = self.ready(p)
        p.kill(); p.wait(timeout=5)
        until = time.monotonic()+5
        while time.monotonic()<until:
            try:
                status = Path(f'/proc/{child}/stat').read_text().split()[2]
                if status == 'Z': break
            except FileNotFoundError: break
            time.sleep(.02)
        else: self.fail('direct shell survived supervisor death')
        # Reap orphan via the container init before forcing a real reused PID.
        until = time.monotonic()+5
        while Path(f'/proc/{child}').exists() and time.monotonic()<until: time.sleep(.02)
        self.assertFalse(Path(f'/proc/{child}').exists(), 'run container with --init')
        return child

    def test_supervisor_death(self):
        self.stopped_supervisor_child()

    @unittest.skipUnless(os.environ.get('RUNNERD_TERMINAL_PID_REUSE_TEST') == '1', 'forced reuse requires an explicit disposable PID namespace')
    def test_pid_reuse(self):
        child = self.stopped_supervisor_child()
        Path('/proc/sys/kernel/ns_last_pid').write_text(str(child-1))
        replacement = subprocess.Popen(['/bin/sleep','30']);self.children.append(replacement)
        self.assertEqual(replacement.pid,child,'PID reuse fixture failed')
        self.assertNotEqual(self.call('input',data=base64.b64encode(b'exit\n').decode()).returncode,0)
        self.assertNotEqual(self.call('resize',rows=40,cols=80).returncode,0)
        self.assertNotEqual(self.call('close').returncode,0)
        self.assertIsNone(replacement.poll(),'stale operations killed reused PID')

if __name__ == '__main__':
    unittest.main(verbosity=2)
