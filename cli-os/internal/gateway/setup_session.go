package gateway

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"os"
	"time"

	"github.com/jackofall1232/l00prite/cli-os/internal/util"
)

// Loopback is intentionally plain HTTP, so this cannot use the __Host- cookie prefix (which
// requires Secure and would be rejected by WebView's cookie store).
const setupSessionCookie = "l00prite_setup"
const setupSessionTTL = 15 * time.Minute

// HandleSetupSession exchanges the Android-only install secret for an HttpOnly loopback session.
// Only one unexpired session may be minted at a time, so replaying the install secret while the
// legitimate setup session is active fails closed. Desktop installs without an install secret do
// not need this endpoint.
func (app *App) HandleSetupSession(w http.ResponseWriter, r *http.Request) {
	if app.setupGate(w) {
		return
	}
	secret := os.Getenv("LOOPRITE_SETUP_SECRET")
	got := r.Header.Get("x-l00prite-setup-secret")
	if secret == "" || got == "" || !util.TimingSafeEqual(got, secret) {
		sendJSON(w, 403, map[string]any{"error": map[string]any{
			"message": "Missing or invalid setup exchange secret.", "type": "authentication_error", "code": "setup_secret_required"}})
		return
	}

	now := time.Now()
	app.setupMu.Lock()
	if app.setupSessions == nil {
		app.setupSessions = map[string]time.Time{}
	}
	for hash, expiry := range app.setupSessions {
		if now.Before(expiry) {
			app.setupMu.Unlock()
			sendJSON(w, 409, map[string]any{"error": map[string]any{
				"message": "A setup session is already active.", "type": "invalid_request_error", "code": "setup_session_active"}})
			return
		}
		delete(app.setupSessions, hash)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		app.setupMu.Unlock()
		oaiError(w, 500, "Could not create setup session", "api_error", "")
		return
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	app.setupSessions[util.SHA256Hex(token)] = now.Add(setupSessionTTL)
	app.setupMu.Unlock()

	http.SetCookie(w, &http.Cookie{Name: setupSessionCookie, Value: token, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: int(setupSessionTTL.Seconds())})
	setupAudit(app, "setup.session", "issued")
	sendJSON(w, 200, map[string]any{"ok": true, "expires_in_seconds": int(setupSessionTTL.Seconds())})
}

func (app *App) validSetupSession(r *http.Request) bool {
	c, err := r.Cookie(setupSessionCookie)
	if err != nil || c.Value == "" {
		return false
	}
	hash := util.SHA256Hex(c.Value)
	app.setupMu.Lock()
	defer app.setupMu.Unlock()
	expiry, ok := app.setupSessions[hash]
	if !ok || !time.Now().Before(expiry) {
		delete(app.setupSessions, hash)
		return false
	}
	return true
}
