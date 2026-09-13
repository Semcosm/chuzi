# Stable release notes

Stable releases require a signed annotated `v<major>.<minor>.<patch>` tag and
a matching `releases/v<version>.md` notes file. The tag workflow signs every
target artifact with the configured UGS attestation key, verifies the artifact
digest and trusted signer, then publishes the release.
