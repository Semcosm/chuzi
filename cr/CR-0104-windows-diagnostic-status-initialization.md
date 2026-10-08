# CR-0104: initialize Windows diagnostic Core status fields

Base: main
Head or Range: 037a2ad89d1e6a46f2726d833c4e609eb9256e1b
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: fix(windows): initialize diagnostic Core status fields
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 5ffc66c7259577725283b2f8521350edde28e90f
Head OID: 037a2ad89d1e6a46f2726d833c4e609eb9256e1b
Integrated Result: pending

## Summary

Initialize the Windows UI diagnostic Core status protocol and capability fields
when building diagnostic context. This keeps the Slint Windows target compiling
after the Core capability compatibility metadata was added.

## Motivation

The main branch installer repair was blocked by a Rust compiler error because a
diagnostic status initializer omitted the new protocol and capability fields.

## Test Evidence

~~~text
cargo fmt --manifest-path ui/windows/Cargo.toml --all -- --check
go test ./...
go vet ./...
./scripts/test_build_contract.sh
git diff --check
~~~

## Risk

The change only copies already available Core status metadata into diagnostics.
It does not alter runtime behavior or user data.

## Rollback

Revert the single Rust initialization change.

## Breaking Change

None.

## Backport Target

None.