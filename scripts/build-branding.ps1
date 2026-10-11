[CmdletBinding()]
param([switch]$Check)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
# Fixed build images. No workstation runtimes, fonts or packages are installed.
$pythonImage = 'python@sha256:bf44cdfcb76cd3b41e879bc058fc37ec5872002ccfde7fcb765e218cde0cd79c'
$nodeImage = 'node@sha256:d6aa754f16b3197301076f047b5def2f02ea1dbbc2ca920407d46d7ec7f87b20'
$checkArg = if ($Check) { '--check' } else { '' }

Push-Location -LiteralPath $root
try {
    & docker run --rm --mount "type=bind,source=$root,target=/src" -w /src $pythonImage `
        sh -eu -c "python -m pip install --disable-pip-version-check --no-cache-dir -r assets/branding/source/requirements.txt >/dev/null && python scripts/build-branding.py $checkArg"
    if ($LASTEXITCODE -ne 0) { throw 'Native SVG generation/validation failed' }
    # Install the lockfile into the disposable container, never the checkout.
    & docker run --rm --mount "type=bind,source=$root,target=/src" -w /src `
        -e NODE_PATH=/tmp/branding/node_modules $nodeImage sh -eu -c `
        "mkdir -p /tmp/branding && cp assets/branding/source/package*.json /tmp/branding/ && npm ci --prefix /tmp/branding --ignore-scripts --no-audit --no-fund >/dev/null && node scripts/export-branding.cjs $checkArg"
    if ($LASTEXITCODE -ne 0) { throw 'Raster generation/validation failed' }
}
finally {
    Pop-Location
}
