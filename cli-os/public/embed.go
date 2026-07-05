// Package public embeds the static control-plane assets so the single binary serves them with no
// external files. The canonical sources remain public/dashboard.html (the real-data operator
// dashboard) and public/setup.html (the zero-config first-run wizard).
package public

import _ "embed"

//go:embed dashboard.html
var Dashboard []byte

//go:embed setup.html
var Setup []byte
