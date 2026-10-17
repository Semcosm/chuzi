#!/usr/bin/env python3
"""Offline slot-session wire fixture; it never creates native sessions."""
import json
from pathlib import Path
import sys

args = sys.argv[1:]
root = Path(args[args.index("-root") + 1])
command = args[args.index("-command") + 1]
scenario = (root / "scenario").read_text().strip()

def count(name):
    path = root / (scenario + "-" + name + "-count")
    value = int(path.read_text()) + 1 if path.exists() else 1
    path.write_text(str(value))
    return value

def state_value(name, default=""):
    path = root / (scenario + "-" + name + "-state")
    if path.exists():
        return path.read_text()
    return default

def set_state(name, value):
    (root / (scenario + "-" + name + "-state")).write_text(value)

def record(method, params):
    with (root / "calls.jsonl").open("a") as calls:
        calls.write(json.dumps(dict(method=method, params=params), sort_keys=True) + "\n")

def operation(operation_id, state, *, attempt=1, revision=7, generation=9,
              phase="environment", desired="running", failure="",
              idempotent=False, execution="windows", status=None):
    if status is None:
        status = state
    return dict(operation=dict(
        operation_id=operation_id, pool_id="pool-a", slot_id="pool-a-001", ordinal=1,
        state=state, phase=phase, failure_code=failure,
        environment_generation=generation, generation=generation, attempt=attempt,
        revision=revision, desired_state=desired,
        deadline="2026-10-17T00:10:00Z", retry_budget=2,
        status=dict(status=status, session_state=status, execution_mode=execution,
                    environment_generation=generation, agent_ready=state == "ready",
                    worker_ready=state == "ready", observed_at="2026-10-17T00:00:00Z",
                    fresh_until="2026-10-17T00:05:00Z"),
        idempotent=idempotent)))

if command == "core-status":
    print(json.dumps(dict(installed=True, running=True, ready=True,
                          configured_pool_mode="windows")))
    sys.exit(0)

method = args[args.index("-core-method") + 1]
params = json.loads(args[args.index("-core-params-json") + 1])
record(method, params)

if method == "list_job_pools":
    status = dict.fromkeys(
        ["desired", "ready", "leased", "quarantined", "draining",
         "provisioning", "retiring", "effective_capacity"], 0)
    status.update(config_revision=7, execution_mode="windows",
                  environment_ready=True, environment_readiness="ready")
    print(json.dumps(dict(job_pools=[dict(config=dict(pool_id="pool-a"), status=status)])))
    sys.exit(0)
if method == "list_environments":
    print(json.dumps(dict(environments=[])))
    sys.exit(0)

if scenario == "transport_retry" and method == "start_slot_session" \
        and count("transport") == 1:
    print("connection reset by peer", file=sys.stderr)
    sys.exit(1)

if method == "start_slot_session":
    if scenario in ("stale_revision", "slot_unavailable"):
        print("chuzi core: " + ("unavailable" if scenario == "slot_unavailable"
                               else scenario), file=sys.stderr)
        sys.exit(1)
    if scenario == "actions":
        set_state("current", "start")
        print(json.dumps(operation("slotop-actions-start", "requested",
                                   phase="environment")))
        sys.exit(0)
    n = count("start_slot_session")
    state = "requested" if scenario == "ready" and n == 1 else scenario
    slot_state = "quarantined" if state == "failed" else state
    print(json.dumps(operation("slotop-" + scenario, state,
                               failure="slot_quarantined" if state == "failed" else "",
                               idempotent=n > 1, status=slot_state)))
    sys.exit(0)

if scenario == "actions" and method == "get_slot_session_operation":
    current = state_value("current", "start")
    n = count("get_slot_session_operation")
    if current == "start":
        print(json.dumps(operation(
            "slotop-actions-start", "provisioning" if n == 1 else "ready",
            phase="profile" if n == 1 else "worker")))
    elif current == "cancel":
        print(json.dumps(operation("slotop-actions-start", "cancelled",
                                   phase="stopped", desired="stopped", status="stopped")))
    elif current == "stop":
        print(json.dumps(operation("slotop-actions-start", "stopped",
                                   phase="stopped", desired="stopped", status="stopped")))
    else:
        print(json.dumps(operation("slotop-actions-retry", "ready", attempt=2,
                                   phase="worker")))
    sys.exit(0)

if scenario == "resume" and method == "get_slot_session_operation":
    print(json.dumps(operation("slotop-resume", "ready", phase="worker")))
    sys.exit(0)

if scenario == "actions" and method in ("cancel_slot_session", "stop_slot_session",
                                         "retry_slot_session"):
    action = method.removesuffix("_slot_session")
    set_state("current", action)
    if action == "retry":
        print(json.dumps(operation("slotop-actions-retry", "retrying", attempt=2,
                                   phase="environment")))
    elif action == "cancel":
        print(json.dumps(operation("slotop-actions-start", "cancelling",
                                   desired="stopped", phase="stopping")))
    else:
        print(json.dumps(operation("slotop-actions-start", "stopping",
                                   desired="stopped", phase="stopping")))
    sys.exit(0)

if method == "get_slot_session_operation":
    counter = count("get_slot_session_operation")
    state = "provisioning" if scenario == "ready" and counter == 1 else scenario
    slot_state = "quarantined" if state == "failed" else state
    print(json.dumps(operation("slotop-" + scenario, state,
                               failure="slot_quarantined" if state == "failed" else "",
                               status=slot_state)))
    sys.exit(0)

print("unexpected fixture method", file=sys.stderr)
sys.exit(1)
