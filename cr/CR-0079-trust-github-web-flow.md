# CR-0079: trust verified GitHub Web Flow integration signatures

Base: main
Head or Range: fix/main-ugs-signed
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: fix(ugs): validate the existing GitHub Web Flow integration signature
Revision: 4
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: b248ac2986adf356a8effdc51bb289f655d6c843
Head OID: b248ac2986adf356a8effdc51bb289f655d6c843
Integrated Result: pending

## Summary

Teach the commit-signature validator to recognize the already integrated GitHub
Web Flow OpenPGP signature and add the corresponding pinned public key.
SSH-signed maintainer commits continue to use the SSH signer registry; the new
path accepts only the Web Flow key fingerprint
968479A1AFF927E37D1A566BB5690EEEBB952194.

## Motivation

The current main branch contains a GitHub-verified Web Flow integration commit
whose OpenPGP signature is valid but was rejected because the validator assumed
every gpgsig field used SSH. This made the post-merge UGS check fail after the
protected branch accepted the platform integration commit.

## Test Evidence

The validator accepts the existing 4b80c304 commit with the pinned key,
continues to accept the maintainer SSH-signed range, and rejects an OpenPGP
commit signed by an unrelated key. Repository policy, quality, supply-chain,
action-pinning, repository-shape, build-contract, CR, and git diff --check
validators pass. GitHub ugs-validate must pass on the pull request and on the
post-merge main event.

## Risk

The trust exception is limited to one public key embedded in the signed
repository history and a hard-coded fingerprint. It does not add the key to
the SSH signer registry or permit arbitrary OpenPGP signatures. A future GitHub
signer rotation will fail closed until reviewed and pinned through a new CR.

## Rollback

Revert this CR and restore the previous SSH-only signature validator. If GitHub
rotates the Web Flow key, remove the old key and update the fingerprint through
a reviewed CR.

## Breaking Change

None to application protocols or release artifacts. UGS signature validation
now supports the explicitly pinned hosting-platform integration signature already
present on main.

## Backport Target

none
