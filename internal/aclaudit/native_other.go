//go:build !windows

package aclaudit

import "context"

func Inspect(_ context.Context, _ Request) Report {
	var r Report
	r.missing("native Windows audit unavailable on this platform")
	return r
}
