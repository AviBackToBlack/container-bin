# WSL2 frontend boundary

ContainerBin's selected WSL model is a native Linux `cb` binary and native
Linux shims inside one WSL2 distribution, using Docker Desktop's supported WSL
integration. A Windows `cb.exe` launched through WSL interoperability is not the
WSL frontend, and standalone Linux remains a separate, demand-gated product.

This first implementation slice establishes the runtime boundary only. It does
not publish a Linux artifact or enable WSL execution yet. Until the remaining
layout, namespace and Docker qualification slices land, non-bootstrap commands
fail closed on every host except native Windows.

## Runtime classification

- Native Windows is the currently supported frontend.
- A Windows process with `WSL_INTEROP` or `WSL_DISTRO_NAME` is classified as
  Windows-through-WSL interoperability and rejected. The diagnostic names the
  inherited marker so a stray variable in an otherwise native Windows process
  can be found and removed.
- A native Linux process is classified as WSL2 only when
  `/proc/sys/kernel/osrelease` contains both the Microsoft and WSL2 markers.
  `WSL_DISTRO_NAME` is then required so later state identity cannot silently
  collapse multiple distributions together.
- A Microsoft kernel without the WSL2 marker is rejected. Classic Microsoft
  kernels are classified as WSL1; `microsoft-standard` kernels are reported as
  ambiguous because early WSL2 releases used that form before the WSL2 suffix
  became consistent. Other Linux kernels are standalone Linux and rejected.
- `cb version`, `cb help` and `cb config` remain bootstrap-safe for diagnosis;
  they perform no Docker or registry mutation and return before host enforcement.

Environment variables alone never promote an ordinary Linux kernel to WSL2.
Custom kernels that remove the Microsoft WSL2 identity markers fail closed;
Docker Desktop also documents custom WSL kernels as unsupported.

## Required before WSL execution can be enabled

Later reviewable slices must still implement and qualify all of the following:

1. native config, lock, binary and symlink locations with Linux ownership and
   permission checks;
2. distribution-scoped shared/project volume identities, with no implicit
   equivalence to Windows paths or state;
3. native Linux path, symlink, case, stdin/TTY and signal semantics;
4. Docker Desktop WSL-integration detection without accepting a separate local
   Docker Engine by accident;
5. Windows-filesystem and WSL-filesystem project tests plus mixed-invocation
   rejection; and
6. real WSL2 + Docker Desktop end-to-end qualification before any support claim.

Docker's setup contract is documented in its
[WSL2 backend guide](https://docs.docker.com/desktop/features/wsl/): WSL2
integration must be enabled for the selected distribution, and Docker recommends
keeping bind-mounted project files in the Linux filesystem where practical.
