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
var Bootstrap = `image_python=$(PATH=/usr/local/bin:/usr/local/sbin:/usr/sbin:/usr/bin:/sbin:/bin command -v python) || exit $?; exec "$image_python" -I -c '` + strings.ReplaceAll(script, "'", "'\"'\"'") + "' \"$@\""
