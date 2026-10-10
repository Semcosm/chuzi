#!/usr/bin/env python3
"""Offline mode protocol fixture; owns no deployment or Windows resources."""
import json
from pathlib import Path
import sys

args = sys.argv[1:]
root = Path(args[args.index("-root") + 1])
command = args[args.index("-command") + 1]
with (root / "calls.jsonl").open("a") as calls:
    calls.write(json.dumps(dict(command=command, args=args)) + "\n")
scenario = (root / "scenario").read_text()
if command == "core-status":
    print(json.dumps(dict(installed=True, running=scenario == "running",
                         ready=False, configured_pool_mode="logical")))
elif command == "core-pool-mode-save":
    if scenario == "pool_cleanup_required":
        print("chuzi launcher: pool_cleanup_required", file=sys.stderr)
        sys.exit(1)
    mode = args[args.index("-pool-mode") + 1]
    print(json.dumps(dict(configured_pool_mode=mode,
                         status="unexpected" if scenario == "malformed" else "restart_required")))
else:
    print("unexpected fixture command", file=sys.stderr)
    sys.exit(1)
