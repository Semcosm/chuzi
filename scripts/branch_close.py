#!/usr/bin/env python3
"""Safely close an integrated topic branch or archive abandoned work."""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from pathlib import Path


ZERO_OID = "0" * 40


class GitError(RuntimeError):
    pass


def run_git(root: Path, *args: str, check: bool = True) -> str:
    result = subprocess.run(
        ["git", "-C", str(root), *args],
        text=True,
        capture_output=True,
    )
    if check and result.returncode != 0:
        detail = (result.stderr or result.stdout).strip()
        raise GitError(detail or "git command failed: " + " ".join(args))
    return result.stdout.strip()


def find_repo() -> Path:
    try:
        return Path(run_git(Path.cwd(), "rev-parse", "--show-toplevel"))
    except GitError as exc:
        raise RuntimeError("current directory is not a Git repository") from exc


def normalize_branch(value: str) -> str:
    branch = value.removeprefix("refs/heads/")
    if not branch or branch.startswith("-") or "\x00" in branch:
        raise ValueError("invalid branch name")
    return branch


def normalize_ref_name(value: str) -> str:
    return value.removeprefix("refs/heads/")


def resolve_commit(root: Path, ref: str) -> str | None:
    value = run_git(root, "rev-parse", "--verify", "--quiet", ref + "^{commit}", check=False)
    return value or None


def policy(root: Path) -> tuple[set[str], list[str]]:
    path = root / ".ugs" / "policy.json"
    try:
        data = json.loads(path.read_text())
    except (OSError, json.JSONDecodeError) as exc:
        raise RuntimeError(f"cannot read policy manifest: {path}") from exc
    branching = data.get("branching")
    if not isinstance(branching, dict):
        raise RuntimeError("policy manifest has no branching declaration")
    protected = {
        normalize_ref_name(item)
        for item in branching.get("protected_refs", [])
        if isinstance(item, str)
    }
    prefixes = [item for item in branching.get("topic_prefixes", []) if isinstance(item, str)]
    if not protected or not prefixes:
        raise RuntimeError("policy manifest has incomplete branch declarations")
    return protected, prefixes


def parse_cr_fallback(path: Path) -> dict[str, str]:
    values: dict[str, str] = {}
    for line in path.read_text().splitlines():
        match = re.match(r"^(Base|Head OID|Status|Integrated Result|Integration Target):\s*(.*)$", line)
        if match:
            values[match.group(1)] = match.group(2).strip()
    return values


def find_integrated_cr(root: Path, branch: str, tip: str, target: str) -> str | None:
    parser = root / "scripts" / "cr_model.py"
    for path in sorted((root / "cr").glob("CR-*.md")):
        model = None
        if parser.is_file():
            result = subprocess.run(
                ["python3", str(parser), "--json", str(path)],
                text=True,
                capture_output=True,
            )
            if result.returncode == 0:
                try:
                    model = json.loads(result.stdout)
                except json.JSONDecodeError:
                    model = None
        if model:
            source = model.get("source", {})
            integration = model.get("integration", {})
            source_head = source.get("head_oid")
            head_range = source.get("head_or_range", "")
            head_matches = source_head == tip
            if not head_matches and branch in head_range and source_head:
                head_matches = subprocess.run(
                    ["git", "-C", str(root), "merge-base", "--is-ancestor", source_head, tip],
                    capture_output=True,
                ).returncode == 0
            if (
                model.get("status") == "integrated"
                and head_matches
                and integration.get("target_ref") == target
            ):
                return model.get("id") or path.stem
            continue
        fallback = parse_cr_fallback(path)
        integrated = fallback.get("Integrated Result", "")
        if (
            fallback.get("Status") == "integrated"
            and fallback.get("Head OID") == tip
            and (
                fallback.get("Integration Target") == target
                or integrated.startswith(target + "@")
            )
        ):
            return path.stem
    return None


def worktree_conflict(root: Path, branch: str) -> bool:
    expected = "refs/heads/" + branch
    output = run_git(root, "worktree", "list", "--porcelain", check=False)
    return any(line.strip() == "branch " + expected for line in output.splitlines())


def remote_tip(root: Path, remote: str, branch: str) -> str | None:
    output = run_git(root, "ls-remote", "--heads", remote, "refs/heads/" + branch, check=False)
    if not output:
        return None
    first = output.splitlines()[0].split()
    return first[0] if first and re.fullmatch(r"[0-9a-f]{40}", first[0]) else None


def emit(report: dict, fmt: str, error: bool = False) -> None:
    if fmt == "json":
        print(json.dumps(report, indent=2, sort_keys=True))
    else:
        stream = sys.stderr if error else sys.stdout
        status = report.get("status", "unknown")
        message = report.get("reason") or "completed"
        print(f"branch close: {status}: {report['branch']} ({message})", file=stream)
        if report.get("matched_cr"):
            print("matched CR: " + report["matched_cr"], file=stream)
        if report.get("archive_ref"):
            print("archive ref: " + report["archive_ref"], file=stream)


def refusal(report: dict, fmt: str, code: str, reason: str) -> int:
    report.update({"status": "refused", "code": code, "reason": reason})
    emit(report, fmt, error=True)
    return 1


def parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="ugs branch close")
    parser.add_argument("branch")
    parser.add_argument("--target", default="main")
    parser.add_argument("--remote")
    parser.add_argument("--dry-run", action="store_true")
    parser.add_argument("--format", choices=("text", "json"), default="text")
    parser.add_argument("--archive", action="store_true")
    parser.add_argument("--reason")
    return parser


def main(argv: list[str] | None = None) -> int:
    args = parser().parse_args(argv)
    report: dict = {
        "format": "ugs-branch-close/v1",
        "branch": args.branch,
        "target": args.target,
        "remote": args.remote,
        "archive": bool(args.archive),
        "dry_run": bool(args.dry_run),
        "status": "pending",
        "tip_oid": None,
        "matched_cr": None,
        "local_result": "pending",
        "remote_result": "not_requested" if not args.remote else "pending",
    }
    try:
        root = find_repo()
        branch = normalize_branch(args.branch)
        target = normalize_branch(args.target)
        report["branch"] = branch
        report["target"] = target
        if args.reason is not None and not args.archive:
            return refusal(report, args.format, "UGS-BRANCH-002", "--reason requires --archive")
        if args.archive and not (args.reason or "").strip():
            return refusal(report, args.format, "UGS-BRANCH-003", "--archive requires an explicit --reason")
        try:
            run_git(root, "check-ref-format", "refs/heads/" + branch)
        except GitError:
            return refusal(report, args.format, "UGS-BRANCH-004", "branch name is not a valid Git ref")
        protected, prefixes = policy(root)
        if branch in protected:
            return refusal(report, args.format, "UGS-BRANCH-005", f"protected branch cannot be closed: {branch}")
        if not any(branch.startswith(prefix) for prefix in prefixes):
            return refusal(report, args.format, "UGS-BRANCH-006", f"branch is not a declared topic branch: {branch}")

        local_ref = "refs/heads/" + branch
        archive_ref = "refs/ugs/archive/" + branch
        closed_ref = "refs/ugs/closed/" + branch
        tip = resolve_commit(root, local_ref)
        archived_tip = resolve_commit(root, archive_ref)
        closed_tip = resolve_commit(root, closed_ref)
        if tip is None and (archived_tip or closed_tip):
            source_oid = archived_tip or closed_tip
            report.update(
                {
                    "status": "already_closed",
                    "tip_oid": source_oid,
                    "source_oid": source_oid,
                    "local_result": "already_closed",
                    "remote_result": "unchanged" if args.remote else "not_requested",
                }
            )
            if archived_tip:
                report.update({"archive_ref": archive_ref, "reason": args.reason or "already archived"})
            else:
                report["reason"] = "branch was already closed"
            emit(report, args.format)
            return 0
        if tip is None:
            return refusal(report, args.format, "UGS-BRANCH-007", f"branch does not exist locally: {branch}")
        report["tip_oid"] = tip
        if archived_tip:
            return refusal(report, args.format, "UGS-BRANCH-008", f"archive ref already exists: {archive_ref}")
        if closed_tip and closed_tip != tip:
            return refusal(report, args.format, "UGS-BRANCH-009", "branch has a prior close marker with a different tip OID")
        if worktree_conflict(root, branch):
            return refusal(report, args.format, "UGS-BRANCH-010", f"branch is checked out in a worktree: {branch}")
        if not args.archive:
            target_ref = "refs/heads/" + target
            target_oid = resolve_commit(root, target_ref)
            if target_oid is None:
                return refusal(report, args.format, "UGS-BRANCH-011", f"target branch does not exist locally: {target}")
            result = subprocess.run(
                ["git", "-C", str(root), "merge-base", "--is-ancestor", tip, target_oid],
                capture_output=True,
            )
            if result.returncode != 0:
                return refusal(report, args.format, "UGS-BRANCH-012", f"branch tip is not merged into target {target}")
            matched_cr = find_integrated_cr(root, branch, tip, target)
            if not matched_cr:
                return refusal(report, args.format, "UGS-BRANCH-013", "no integrated CR matches the branch tip and target")
            report["matched_cr"] = matched_cr

        remote_oid = None
        if args.remote:
            remote_oid = remote_tip(root, args.remote, branch)
            if remote_oid and remote_oid != tip:
                return refusal(report, args.format, "UGS-BRANCH-014", f"remote OID differs from local tip: {remote_oid}")
            report["remote_oid"] = remote_oid

        report["local_result"] = "archive" if args.archive else "delete"
        if args.archive:
            report.update({"reason": args.reason, "source_oid": tip, "archive_ref": archive_ref})
        if args.dry_run:
            report["status"] = "dry_run"
            report["remote_result"] = "would_delete" if remote_oid else ("already_absent" if args.remote else "not_requested")
            emit(report, args.format)
            return 0

        if args.remote and remote_oid:
            fresh_remote_oid = remote_tip(root, args.remote, branch)
            if fresh_remote_oid != remote_oid:
                return refusal(report, args.format, "UGS-BRANCH-015", f"remote OID changed before deletion: {fresh_remote_oid or 'absent'}")
            lease = f"refs/heads/{branch}:{remote_oid}"
            result = subprocess.run(
                ["git", "-C", str(root), "push", "--force-with-lease=" + lease, args.remote, ":refs/heads/" + branch],
                text=True,
                capture_output=True,
            )
            if result.returncode != 0:
                return refusal(report, args.format, "UGS-BRANCH-016", (result.stderr or result.stdout).strip() or "remote deletion refused")
            report["remote_result"] = "deleted"
        elif args.remote:
            report["remote_result"] = "already_absent"

        if args.archive:
            run_git(root, "update-ref", archive_ref, tip, ZERO_OID)
            run_git(root, "update-ref", "-d", local_ref, tip)
        else:
            run_git(root, "update-ref", closed_ref, tip, ZERO_OID)
            run_git(root, "update-ref", "-d", local_ref, tip)
        report["status"] = "closed"
        emit(report, args.format)
        return 0
    except (RuntimeError, GitError, OSError, ValueError) as exc:
        return refusal(report, args.format, "UGS-BRANCH-999", str(exc))


if __name__ == "__main__":
    sys.exit(main())
