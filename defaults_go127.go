//go:build go1.27

// Keep the shipping toolchain's security defaults even though go.mod retains
// Go 1.24 source compatibility for ContainerBin's built-in Go shim. Update this
// baseline deliberately alongside a future release-toolchain series upgrade.
//go:debug default=go1.27

package main
