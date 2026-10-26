import argparse
import json
import os
from pathlib import Path
import shlex
import sys
import urllib.error
import urllib.parse
import urllib.request
import uuid


parser = argparse.ArgumentParser(description="Manage the local development Linux desktop")
parser.add_argument("action", choices=["create", "status", "pause", "resume", "exec", "logs", "command-status", "download", "screenshot"])
parser.add_argument("value", nargs="?")
parser.add_argument("--sandbox-id")
parser.add_argument("--background", action="store_true")
parser.add_argument("--timeout", type=int, default=60, help="Foreground command timeout in seconds")
parser.add_argument("--output", type=Path)
args = parser.parse_intermixed_args()
if args.timeout < 1:
    parser.error("--timeout must be positive")

api_key = os.environ.get("SANDBOX_API_KEY")
if not api_key:
    parser.error("SANDBOX_API_KEY is required; load infra/sandbox/.env first")
base_url = os.environ.get("SANDBOX_URL", "http://127.0.0.1:" + os.environ.get("SANDBOX_PORT", "18090")).rstrip("/") + "/v1"
state_file = Path(__file__).parent / ".local" / "sandbox.json"


def request(path, method="GET", body=None, timeout=30):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(base_url + path, data=data, method=method, headers={
        "OPEN-SANDBOX-API-KEY": api_key,
        "Content-Type": "application/json",
    })
    return urllib.request.urlopen(req, timeout=timeout)


def execute(command, background=False):
    with request(sandbox_path + "/proxy/44772/command", "POST", {
        "command": command,
        "background": background,
        "timeout": 0 if background else args.timeout * 1000,
    }, timeout=args.timeout + 15) as response:
        for line in response:
            if not line.strip():
                continue
            event = json.loads(line)
            if event["type"] == "init":
                print("command_id=" + event["text"], file=sys.stderr, flush=True)
            elif event["type"] in ["stdout", "stderr"]:
                print(event.get("text", ""), flush=True)
            elif event["type"] == "error":
                raise RuntimeError(json.dumps(event, ensure_ascii=False))


try:
    if args.action == "create":
        if state_file.exists():
            parser.error("A local desktop is already recorded; use status or --sandbox-id for another sandbox")
        with request("/sandboxes", "POST", {
            "image": {"uri": os.environ.get("SANDBOX_IMAGE", "aether-personal-desktop:dev")},
            "timeout": None,
            "resourceLimits": {"cpu": "2", "memory": "2Gi"},
            "entrypoint": ["/usr/local/bin/start-desktop"],
            "metadata": {"project": "aether-personal", "purpose": "local-development"},
        }, timeout=300) as response:
            sandbox = json.load(response)
        state_file.parent.mkdir(parents=True, exist_ok=True)
        state_file.write_text(json.dumps({"id": sandbox["id"]}) + "\n")
        print(json.dumps(sandbox, ensure_ascii=False, indent=2))
        sys.exit(0)

    sandbox_id = args.sandbox_id
    if not sandbox_id and state_file.exists():
        sandbox_id = json.loads(state_file.read_text())["id"]
    if not sandbox_id:
        parser.error("Create a desktop first or specify --sandbox-id")
    sandbox_path = "/sandboxes/" + str(uuid.UUID(sandbox_id))

    if args.action == "status":
        with request(sandbox_path) as response:
            print(json.dumps(json.load(response), ensure_ascii=False, indent=2))
    elif args.action in ["pause", "resume"]:
        with request(sandbox_path + "/" + args.action, "POST", {}) as response:
            print(args.action + " accepted; use status to check completion")
    elif args.action == "exec":
        if not args.value:
            parser.error("exec requires a command")
        execute(args.value, args.background)
    elif args.action in ["logs", "command-status"]:
        if not args.value:
            parser.error("Specify the command ID returned by exec")
        command_id = urllib.parse.quote(args.value, safe="")
        suffix = "/command/" + command_id + "/logs" if args.action == "logs" else "/command/status/" + command_id
        with request(sandbox_path + "/proxy/44772" + suffix) as response:
            print(response.read().decode())
    else:
        if not args.output:
            parser.error("Specify --output for the downloaded file")
        if args.output.exists():
            parser.error("The output file already exists; choose another path")
        remote_path = args.value
        if args.action == "screenshot":
            remote_path = "/tmp/aether-screen-" + uuid.uuid4().hex + ".png"
            execute("DISPLAY=:0 scrot " + shlex.quote(remote_path))
        if not remote_path:
            parser.error("Specify the remote file path")
        path = sandbox_path + "/proxy/44772/files/download?" + urllib.parse.urlencode({"path": remote_path})
        with request(path) as response:
            args.output.parent.mkdir(parents=True, exist_ok=True)
            with args.output.open("xb") as output:
                while chunk := response.read(64 * 1024):
                    output.write(chunk)
        print(str(args.output.resolve()))
except urllib.error.HTTPError as error:
    print("Sandbox API returned HTTP " + str(error.code) + ": " + error.read(2000).decode(errors="replace"), file=sys.stderr)
    sys.exit(1)
except (OSError, ValueError, RuntimeError) as error:
    print(str(error) + "; a submitted operation may still be running. Check its status before retrying.", file=sys.stderr)
    sys.exit(1)
