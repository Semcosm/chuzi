#!/usr/bin/env python3
"""Local protocol fixture; creates no native sessions and uses no credentials."""
import json
from pathlib import Path
import sys

args = sys.argv[1:]
root = Path(args[args.index("-root") + 1])
command = args[args.index("-command") + 1]
if command == "core-status":
    print(json.dumps(dict(installed=True, running=True, ready=True, configured_pool_mode="windows")))
    sys.exit(0)
method = args[args.index("-core-method") + 1]
params = json.loads(args[args.index("-core-params-json") + 1])
with (root / "calls.jsonl").open("a") as calls:
    calls.write(json.dumps(dict(method=method, params=params)) + "\n")
scenario = (root / "scenario").read_text()
if method == "list_job_pools":
    status = dict.fromkeys(
        ["desired", "ready", "leased", "quarantined", "draining",
         "provisioning", "retiring", "effective_capacity"], 0
    )
    status["config_revision"] = 7
    status["execution_mode"] = "windows"
    print(json.dumps(dict(job_pools=[dict(config=dict(pool_id="pool-a"), status=status)])))
elif method == "list_environments":
    print(json.dumps(dict(environments=[])))
elif method in ("start_slot_session", "get_slot_session_operation"):
    if method == "start_slot_session" and scenario in ("stale_revision", "slot_unavailable"):
        print("chuzi core: " + ("unavailable" if scenario == "slot_unavailable" else scenario), file=sys.stderr)
        sys.exit(1)
    counter = root / (scenario + "-" + method + "-count")
    count = int(counter.read_text()) + 1 if counter.exists() else 1
    counter.write_text(str(count))
    state = "requested" if method == "start_slot_session" else (
        "provisioning" if scenario == "ready" and count == 1 else scenario
    )
    slot_state = "quarantined" if state == "failed" else state
    print(json.dumps(dict(operation=dict(
        operation_id="slotop-" + scenario, ordinal=1, state=state,
        failure_code="slot_quarantined" if state == "failed" else "",
        environment_generation=9,
        status=dict(status=slot_state, session_state=slot_state, agent_ready=state == "ready",
                    execution_mode="windows", environment_generation=9),
        idempotent=method == "start_slot_session" and count > 1,
    ))))
else:
    print("unexpected fixture method", file=sys.stderr)
    sys.exit(1)
