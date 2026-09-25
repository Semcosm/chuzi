# Release catalog and notes

The release catalog branch has three independent paths: `test/<target>/`,
`nightly/<target>/`, and `stable/<target>/`. Their source Actions artifacts are
named `chuzi-test-<target>`, `chuzi-nightly-<target>`, and
`chuzi-stable-<target>` respectively. Each path contains versioned bundles and
one per-target JSON catalog. Test and nightly entries set `prerelease: true`;
stable entries set `prerelease: false`.

Stable releases use a manually reviewed, signed annotated
`v<major>.<minor>.<patch>` tag and a matching `releases/v<version>.md` notes
file. Test and nightly builds do not create tags or GitHub Releases. Keep
version-specific notes out of this file; it records only the publication
contract used by CI.
