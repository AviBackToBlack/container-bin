# Release toolchain and vulnerability gates

## Deterministic security baseline

`go.mod` pins **Go 1.27.2** for contributor builds, native CI, platform-specific
source vulnerability analysis, release builds and independent reproduction.
`check-latest: false` and `GOTOOLCHAIN=local` prevent resolution to a different
patch or a downloaded toolchain. Updating the pin is an explicit reviewed
maintenance change, not a floating build input. The standard-library-only
runtime dependency policy is unchanged.

Go supports its latest two release series. As checked on 2026-10-11, 1.27.2 is
the current patched release and 1.23.12 is unsupported. No documented consumer
requirement for retaining Go 1.23 source-build compatibility was identified;
ContainerBin's supported contract is its CLI/shims, not a public Go library.
This intentionally raises the contributor toolchain requirement, without
changing CLI compatibility. A future older language minimum needs its own
justification and compatibility tests, separate from the shipping toolchain.

## Evidence must match what ships

CI runs pinned `govulncheck v1.8.0` source analysis with the shipping toolchain
and `CGO_ENABLED=0` for Windows amd64, Windows ARM64 and Linux amd64. Native
Windows/ARM64 tests and Linux race tests remain separate requirements.

Before uploading a release bundle, `scripts/check-release-binaries.sh`:

- checks the bundle's two checksum manifests;
- extracts the executables from all three actual archives, and compares the
  Windows amd64 payload to the standalone `cb.exe`;
- verifies embedded Go version, target architecture/OS and disabled CGO;
- runs pinned binary-mode vulnerability analysis on each payload and preserves
  build metadata, extraction precision, hashes, scan output and exit codes.

The scanner's text-mode exit status is a blocking gate. JSON/SARIF output is
not used as an exit-status gate because those formats exit successfully even
when findings exist. The vulnerability database stays live; scan time and its
database revision must accompany retrospective evidence. Deterministic builds
do not mean freezing security intelligence.

Released binaries remain stripped. govulncheck v1.8.0 cannot recover their
symbols and falls back to **module-level precision**; even output headed
"symbols found" is not proof of linked or reachable code in that case. Source
analysis is complementary, not a replacement for scanning actual binaries.
Interface calls are conservative, runtime platform guards need manual review,
and neither scan proves exploitability or the absence of every vulnerability.

Independent-runner byte comparison, checksums, tag/source/workflow provenance,
draft-only publication and refusal to replace existing releases are preserved.
The release evidence artifact is additional CI evidence, not a new public
release asset or a substitute for authentication.

## v2.0.0 retrospective and v2.0.1 recommendation

The authenticated v2.0.0 executables for all three targets embed Go 1.23.12
(source commit `3cfc259f96c78a27dd417afd8dd8c64206c752ca`). At release time,
source govulncheck used a newer Go version than the build/reproduction jobs;
therefore that green source check did not establish the safety of the shipped
standard library. The 2026-10-11 investigation found 58 advisory version-range
candidates. They are **not 58 confirmed project vulnerabilities**. Stripped
binary scans yielded 57 candidates on each platform, with platform-dependent
sets and module-level precision; conservative source analysis matched 35
advisories on Windows and 36 on Linux before runtime/precondition review.

A **v2.0.1 security/maintenance release is recommended** after normal review
and fresh qualification. Do not replace v2.0.0 assets or retag its commit.
Review and qualification must cover the actual newly built candidate, including
native Windows amd64/ARM64 CI, independent reproduction, authenticated assets,
the full Windows shell/Docker Desktop release matrix, native WSL2 integration,
backup restore and self-update. ARM64 compilation/native CI does not imply
real ARM64 Docker Desktop qualification. Previous v2.0.0 evidence is historical,
not qualification of a different compiler's outputs.

PR #136's fuzzing work is separate and was not part of v2.0.0 qualification.
This maintenance change does not merge that PR, alter repository security
rules, make personal OpenSSF attestations, or accept assessment-data licenses.

References: [Go releases/support](https://go.dev/doc/devel/release),
[official downloads](https://go.dev/dl/),
[govulncheck configuration, limitations and exit codes](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck),
[pinned scanner stripped-binary fallback](https://github.com/golang/vuln/blob/v1.8.0/internal/vulncheck/binary.go#L98-L105),
[release qualification matrix](release-matrix.md).
