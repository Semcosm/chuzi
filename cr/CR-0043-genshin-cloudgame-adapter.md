# CR-0043: switch the first cloud-game adapter to Genshin Cloud

Base: main
Head or Range: 1a0df921685acb5a10da36799589f770742f1454
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(adapter): switch first cloud-game adapter to Genshin Cloud
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 1a0df921685acb5a10da36799589f770742f1454
Head OID: 1a0df921685acb5a10da36799589f770742f1454
Integrated Result: pending

## Summary

Set the first real headless-CDP business adapter to the authorized-session
probe for Genshin Cloud at
`https://ys.mihoyo.com/cloud/#/`. The adapter advertises the stable
`genshin-cloudgame@1` capability and `genshin.cloudgame.session_probe`
operation while preserving the existing loopback CDP, isolated Profile, and
redacted result boundaries.

## Motivation

The first supported cloud-game service is Genshin Cloud. Keeping the endpoint,
capability, operation, page title validation, service selection, and release
manifest aligned prevents the service from launching an unrelated platform.

## Test Evidence

Passed `npm test` and `npm run build` in `browser-worker`, `go test ./...`,
`go vet ./...`, `./scripts/test_runtime.sh`, `./scripts/test_build_contract.sh`,
all repository policy/profile/action/shape validators, and `git diff --check`.
The adapter tests verify the exact Genshin Cloud URL, authenticated facts,
authentication failure, and fail-closed page-shell behavior using a fake CDP
browser. The authenticated fixture uses Chromium's direct
`Runtime.evaluate` result shape; the local-page fixture retains coverage for
the historical nested shape, and the cloud fixture also covers the initial
`about:blank` navigation response.

An additional smoke run used the locally installed Chromium 153.0.8010.36 and
a disposable empty Profile. It opened the fixed URL and returned only
`{"platform":"genshin-cloudgame","page":"recognized","shell":"present","session":"unknown"}`
(plus the fixed flow marker), so an empty Profile reached the real page shell
but did not claim authentication.

## Risk

The adapter only checks an existing authorized browser Profile. It does not
accept caller URLs or credentials, perform login, handle CAPTCHA/risk controls,
or infer business success from endpoint discovery. A changed Cloud Genshin page
title or shell fails closed with a stable business error.

## Rollback

Revert the adapter, service option, fixture/test, manifest, documentation, and
this CR together. The default deferred backend and existing browser protocol
remain usable without migration or runtime-data cleanup.

## Breaking Change

The previously exposed cloud-game adapter capability and operation are removed;
callers must select `genshin-cloudgame` and invoke
`genshin.cloudgame.session_probe`. No storage, credential, account-state,
queue, Matrix, or browser-worker lifecycle protocol changes are introduced.

## Backport Target

none
