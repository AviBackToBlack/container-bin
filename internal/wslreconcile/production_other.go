//go:build !linux

package wslreconcile

// Non-Linux builds retain the command surface for cross-compilation, while
// incomplete dependencies make any accidental execution fail closed.
func productionDependencies() dependencies { return dependencies{} }
