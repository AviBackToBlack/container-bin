#!/usr/bin/env bash
# Contract tests for the release gate; these fake payloads are NOT vuln evidence.
set -euo pipefail
gate="$(realpath "$(dirname "$0")/check-release-binaries.sh")"
scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT
cd "$scratch"
mkdir -p tools bundle payload
printf 'module test\n\ngo 1.24\n' > go.mod
printf '1.27.2\n' > .go-version
printf 'fixture executable\n' > payload/cb.exe
cp payload/cb.exe bundle/cb.exe
(cd payload; zip -q ../bundle/container-bin-v0.0.0-test-windows-amd64.zip cb.exe)
cp bundle/container-bin-v0.0.0-test-windows-amd64.zip bundle/container-bin-v0.0.0-test-windows-arm64.zip
cp payload/cb.exe payload/cb
tar -czf bundle/container-bin-v0.0.0-test-linux-amd64.tar.gz -C payload cb
(
  cd bundle
  sha256sum cb.exe ./*.zip > SHA256SUMS
  sha256sum ./*.tar.gz > SHA256SUMS-WSL
)
cat > tools/go <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
if [[ "$1 $2" == 'env GOVERSION' ]]; then echo go1.27.2; exit; fi
if [[ "$3" == */govulncheck ]]; then
  printf '\tmod\tgolang.org/x/vuln\t%s\th1:fixture\n' "${SCANNER_VERSION:-v1.8.0}"
else
  target="${3##*/}"
  target="${target%.exe}"
  printf '%s: %s\n\tbuild\tGOOS=%s\n\tbuild\tGOARCH=%s\n\tbuild\tCGO_ENABLED=%s\n' \
    "$3" "${EMBEDDED_GO:-go1.27.2}" "${target%-*}" "${TARGET_ARCH:-${target#*-}}" "${CGO_FLAG:-0}"
  if [[ -n "${DEBUG_DEFAULTS:-}" ]]; then printf '\tbuild\tDefaultGODEBUG=%s\n' "$DEBUG_DEFAULTS"; fi
fi
MOCK
cat > tools/govulncheck <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
if [[ "$1" == -mode=extract ]]; then echo '{"fixture":true}'; exit; fi
binary="${!#}"
echo "scanned ${binary##*/}" >> "$SCAN_LOG"
echo 'fixture scanner output'
exit "${SCAN_EXIT:-0}"
MOCK
chmod +x tools/*
export PATH="$scratch/tools:$PATH" SCAN_LOG="$scratch/scans.txt"
bash "$gate" bundle v0.0.0-test reports > pass.log 2>&1
test "$(wc -l < "$SCAN_LOG")" -eq 3
expect_failure() {
  if "$@" > failure.log 2>&1; then
    echo "gate unexpectedly accepted: $*" >&2
    exit 1
  fi
}
expect_failure env SCAN_EXIT=3 bash "$gate" bundle v0.0.0-test reports
test "$(wc -l < "$SCAN_LOG")" -eq 6 # all targets scanned despite findings
expect_failure env SCANNER_VERSION=v0.0.0 bash "$gate" bundle v0.0.0-test reports
expect_failure env EMBEDDED_GO=go1.23.12 bash "$gate" bundle v0.0.0-test reports
expect_failure env TARGET_ARCH=386 bash "$gate" bundle v0.0.0-test reports
expect_failure env CGO_FLAG=1 bash "$gate" bundle v0.0.0-test reports
expect_failure env DEBUG_DEFAULTS=tlssha1=1 bash "$gate" bundle v0.0.0-test reports
expect_failure bash "$gate" bundle invalid reports
printf 'corruption\n' >> bundle/cb.exe
expect_failure bash "$gate" bundle v0.0.0-test reports
echo 'release gate contract tests passed (success, findings, scanner/toolchain/target/CGO/defaults mismatch, invalid version, corruption)'
