# CR-0095: document future Genshin BetterGI adapter boundary

Base: main
Head or Range: 1ed863a7d311a095beb3117ac035f689080ad272..17ede74
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: docs(adapter): record future Genshin BetterGI session design
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 1ed863a7d311a095beb3117ac035f689080ad272
Head OID: 17ede74
Integrated Result: pending

## Summary

Add a research and design record for a future Windows Genshin adapter under
plugins/bettergi. The record covers native and cloud-game startup, BetterGI
preset execution, per-slot isolation, session state, protocol capabilities,
RDP observation and operator takeover, recovery behavior, and implementation
phases. It does not implement an adapter or claim production BetterGI/RDP
support.

## Motivation

The existing BetterGI directory only defined a communication-plugin placeholder.
Future work needs a durable boundary that matches the existing automation,
Windows slot, credential, and RDP workspace contracts. Recording the researched
constraints now prevents private BetterGI internals, arbitrary commands, shared
configuration roots, or a second RDP session from becoming accidental design
assumptions.

## Test Evidence

Documentation review against plugins/bettergi/README.md, docs/architecture.md,
docs/ui/rdp-workspace.md, docs/job-user-pool.md, internal/automation, and the
existing Core RDP capability contract. Run git diff --check after editing.
No runtime implementation or live-account test is included in this change.

## Risk

The design depends on version-sensitive BetterGI behavior and a future public
integration interface. BetterGI packaging also requires GPLv3 review. The
record explicitly treats these as blockers, keeps RDP authorizer use
fail-closed, and requires version, digest, configuration-isolation, and
Windows-session evidence before implementation.

## Rollback

Remove plugins/bettergi/design.md, the README design-record link, and this CR.
No runtime data, credentials, generated artifacts, or deployment state changes.

## Breaking Change

None. This is documentation only and does not change the chuzi adapter protocol,
Core API, scheduler, or platform support.

## Backport Target

None.
