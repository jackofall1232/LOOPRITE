// Package public embeds the static control-plane assets so the single binary serves them with no
// external files. The canonical sources remain public/dashboard.html (the real-data operator
// dashboard) and public/setup.html (the zero-config first-run wizard).
package public

import "embed" // NOTE: was `import _ "embed"` — embed.FS needs the real import

//go:embed dashboard.html
var Dashboard []byte

//go:embed setup.html
var Setup []byte

// Assets holds build-time-fixed static files (the Help section's screenshots). Served read-only
// at /assets/ by internal/server. go:embed refuses an empty match, so assets/ always ships at
// least assets/screenshots/README.md — screenshots land beside it in a later capture pass and
// are picked up with no further code change.
//
//go:embed assets
var Assets embed.FS
