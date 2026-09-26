#!/usr/bin/env python3
"""Resolve the release channel and version for one GitHub Actions run."""

from __future__ import annotations

import argparse
import json
import re


SHA_RE = re.compile(r"^[0-9a-fA-F]{40}$")
TAG_RE = re.compile(r"^refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$")


def resolve(event: str, ref: str, input_channel: str, run_number: int, sha: str) -> dict[str, str]:
    if run_number <= 0:
        raise ValueError("run number must be positive")
    if not SHA_RE.fullmatch(sha):
        raise ValueError("commit must be a full SHA-1")
    short_sha = sha[:12].lower()

    if TAG_RE.fullmatch(ref):
        return {"channel": "stable", "version": ref.removeprefix("refs/tags/")}
    if event == "schedule":
        if ref != "refs/heads/main":
            raise ValueError("scheduled nightly builds must run from main")
        return {"channel": "nightly", "version": f"nightly-{run_number}-{short_sha}"}
    if event == "workflow_dispatch":
        if input_channel == "test":
            return {"channel": "test", "version": f"test-{run_number}-{short_sha}"}
        if input_channel == "nightly":
            if ref != "refs/heads/main":
                raise ValueError("manual nightly builds must run from main")
            return {"channel": "nightly", "version": f"nightly-{run_number}-{short_sha}"}
        raise ValueError(f"unsupported manual channel: {input_channel}")
    return {"channel": "ci", "version": f"dev-{short_sha}"}


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--event", required=True)
    parser.add_argument("--ref", required=True)
    parser.add_argument("--input-channel", default="")
    parser.add_argument("--run-number", required=True, type=int)
    parser.add_argument("--sha", required=True)
    args = parser.parse_args()
    print(json.dumps(resolve(args.event, args.ref, args.input_channel, args.run_number, args.sha), sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
