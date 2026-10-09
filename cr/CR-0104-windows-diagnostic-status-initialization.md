# CR-0104: initialize Windows diagnostic Core status fields

Base: main
Head or Range: df0ddfd116992f10784195d15c5a80be58b9b120
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: fix(windows): initialize diagnostic Core status fields
Revision: 3
Status: integrated
Decision: accepted
Policy Version: v0.3
Base OID: f17e06435ee79d958f55829bc70458b35682e4e5
Head OID: df0ddfd116992f10784195d15c5a80be58b9b120
Integrated Result: main@2d6879fa36233a390f7df9e66b89b4794119326b

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
