#!/usr/bin/env bash
# Scan the actual archive payloads, not a substitute build or scanner toolchain.
set -euo pipefail

if [[ $# -ne 3 || ! "$2" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo 'usage: check-release-binaries.sh BUNDLE VERSION REPORT_DIRECTORY' >&2
  exit 1
fi
bundle="$(realpath "$1")"
version="$2"
mkdir -p "$3"
reports="$(realpath "$3")"
date -u +'%Y-%m-%dT%H:%M:%SZ' > "$reports/started-at.txt"
expected="$(tr -d '\r\n' < .go-version)"
[[ "$expected" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]
test "$(go env GOVERSION)" = "go${expected}"
go version -m "$(command -v govulncheck)" > "$reports/scanner-buildinfo.txt"
grep -Eq '^[[:space:]]+mod[[:space:]]+golang.org/x/vuln[[:space:]]+v1\.8\.0[[:space:]]' "$reports/scanner-buildinfo.txt"
(
  cd "$bundle"
  sha256sum -c SHA256SUMS
  sha256sum -c SHA256SUMS-WSL
) | tee "$reports/checksums.txt"

scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT
unzip -p "$bundle/container-bin-${version}-windows-amd64.zip" cb.exe > "$scratch/windows-amd64.exe"
unzip -p "$bundle/container-bin-${version}-windows-arm64.zip" cb.exe > "$scratch/windows-arm64.exe"
tar -xOzf "$bundle/container-bin-${version}-linux-amd64.tar.gz" cb > "$scratch/linux-amd64"
cmp "$bundle/cb.exe" "$scratch/windows-amd64.exe"

failed=0
for target in windows-amd64 windows-arm64 linux-amd64; do
  binary="$scratch/$target"
  if [[ "$target" == windows-* ]]; then binary+='.exe'; fi
  info="$reports/buildinfo-$target.txt"
  go version -m "$binary" > "$info"
  test "$(head -n 1 "$info" | awk '{print $NF}')" = "go${expected}"
  grep -Fxq $'\tbuild\tGOOS='"${target%-*}" "$info"
  grep -Fxq $'\tbuild\tGOARCH='"${target#*-}" "$info"
  grep -Fxq $'\tbuild\tCGO_ENABLED=0' "$info"
  # A lower language minimum must not select legacy shipping GODEBUG defaults.
  if grep -q 'DefaultGODEBUG=' "$info"; then
    echo "unexpected compatibility defaults in $target" >&2
    exit 1
  fi
  sha256sum "$binary" | sed "s|$scratch/||" > "$reports/sha256-$target.txt"
  # Extract metadata records whether the scanner has symbols. Stripped release
  # payloads may get conservative module-level results; never label these as
  # proven reachable symbols. Keep source-mode scans as a separate CI gate.
  govulncheck -mode=extract "$binary" > "$reports/extract-$target.json"
  # JSON/SARIF modes exit 0 even on findings. Text mode plus pipefail is the gate.
  if govulncheck -mode=binary -show version "$binary" 2>&1 | tee "$reports/govulncheck-$target.txt"; then
    result=0
  else
    result=$?
    failed=1
  fi
  printf '%s\n' "$result" > "$reports/exit-$target.txt"
done
date -u +'%Y-%m-%dT%H:%M:%SZ' > "$reports/finished-at.txt"
exit "$failed"
