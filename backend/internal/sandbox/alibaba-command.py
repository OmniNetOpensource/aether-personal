import base64
import json
import os
import pathlib
import signal
import subprocess
import sys
import threading

args = json.loads(base64.b64decode(sys.argv[1]))
folder = pathlib.Path('/var/lib/aether/commands') / args['id']
# Never overwrite an earlier receipt, even when a caller has an uncertain result.
folder.mkdir(parents=True, exist_ok=False)
status_path = folder / 'status.json'

def save(status):
    temporary = folder / 'status.tmp'
    temporary.write_text(json.dumps(status))
    temporary.replace(status_path)

def start_time(pid):
    return pathlib.Path('/proc/' + str(pid) + '/stat').read_text().rsplit(')', 1)[1].split()[19]

with (folder / 'output.log').open('wb', buffering=0) as output:
    try:
        child = subprocess.Popen(['/bin/bash', '-lc', args['command']], cwd=args['directory'],
                                 stdout=subprocess.PIPE, stderr=subprocess.STDOUT, start_new_session=True)
        status = {'id': args['id'], 'running': True, 'exit_code': None,
                  'pid': child.pid, 'child_start_time': start_time(child.pid),
                  'supervisor': os.getpid(), 'start_time': start_time(os.getpid())}
        save(status)
        def expire():
            try:
                os.killpg(child.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
        timer = threading.Timer(args['timeout'], expire)
        timer.start()
        kept = 0
        marker = b'\n[output truncated]'
        limit = 32768 - len(marker)
        truncated = False
        while True:
            data = os.read(child.stdout.fileno(), 8192)
            if not data:
                break
            take = min(len(data), max(0, limit - kept))
            output.write(data[:take])
            kept += take
            if take < len(data) and not truncated:
                output.write(marker)
                truncated = True
        code = child.wait()
        # Remaining descendants belong to this bounded command, not untracked work.
        expire()
        timer.cancel()
        status.update(running=False, exit_code=code if code >= 0 else 128 - code)
        save(status)
    except Exception as error:
        output.write(str(error).encode()[:1024])
        if 'timer' in locals():
            timer.cancel()
        if 'child' in locals():
            expire()
        save({'id': args['id'], 'running': False, 'exit_code': 125})
