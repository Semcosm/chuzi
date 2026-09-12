# BetterGI communication plugin

This directory reserves the first chuzi automation plugin: a bridge that
speaks `chuzi.adapter/v1` to the control service and the official/public
integration interface exposed by a user-installed BetterGI instance.

The bridge is not the BetterGI automation engine and does not contain a
BetterGI binary. It must not scrape the BetterGI UI, inject into its process,
or bypass a service's access controls. The concrete BetterGI transport (for
example, an official local IPC, HTTP, or WebSocket API) is intentionally not
assumed until its supported interface and version policy are verified.

## Planned contract

- The host starts this bridge through the generic plugin process boundary.
- Windows uses a native process; macOS and Linux may use the Wine process
  backend when a verified Wine runtime and prefix are available.
- The bridge advertises capabilities before accepting an operation.
- Operations carry service-derived session/request identifiers and an opaque
  session handle; credentials are never placed in JSONL payloads.
- Results contain stable error classes/codes and redacted runtime facts only.

The first implementation CR must record the BetterGI public protocol, supported
BetterGI versions, required Wine/GUI prerequisites, and target-specific test
evidence. Until then this is a communication-plugin boundary, not a claim that
BetterGI runs on every chuzi target.
