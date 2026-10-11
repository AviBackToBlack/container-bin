// Package pythonenv shares the Python state bootstrap between both frontends.
package pythonenv

import (
	_ "embed"
	"strings"
)

//go:embed bootstrap.py
var script string

// Bootstrap execs the image interpreter, then the venv interpreter. Neither
// frontend introduces an extra process between Docker and the user's command.
var Bootstrap = "exec python -c '" + strings.ReplaceAll(script, "'", "'\"'\"'") + "' \"$@\""
