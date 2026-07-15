package gateway

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"net/http"
	"time"

	"github.com/jackofall1232/l00prite/cli-os/internal/security"
	"github.com/jackofall1232/l00prite/cli-os/internal/state"
	"github.com/jackofall1232/l00prite/cli-os/internal/util"
)

const uiSessionCookie = "l00prite_ui"
const uiSessionTTL = 8 * time.Hour

type uiSession struct {
	Principal security.Principal
	ExpiresAt time.Time
}

func (app *App) issueUISession(principal *security.Principal) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	app.uiMu.Lock()
	if app.uiSessions == nil {
		app.uiSessions = map[string]uiSession{}
	}
	app.uiSessions[util.SHA256Hex(token)] = uiSession{Principal: *principal, ExpiresAt: time.Now().Add(uiSessionTTL)}
	app.uiMu.Unlock()
	return token, nil
}

func (app *App) uiPrincipal(r *http.Request) *security.Principal {
	c, err := r.Cookie(uiSessionCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	hash := util.SHA256Hex(c.Value)
	app.uiMu.Lock()
	s, ok := app.uiSessions[hash]
	if !ok || !time.Now().Before(s.ExpiresAt) {
		delete(app.uiSessions, hash)
		app.uiMu.Unlock()
		return nil
	}
	app.uiMu.Unlock()
	p := s.Principal
	var revoked int
	var expires sql.NullString
	if err := app.DB.QueryRowContext(state.Ctx(), `SELECT revoked,expires_at FROM tokens WHERE id=?`, p.TokenID).Scan(&revoked, &expires); err != nil || revoked != 0 {
		return nil
	}
	if expires.Valid {
		if when, err := time.Parse(time.RFC3339Nano, expires.String); err == nil && !time.Now().Before(when) {
			return nil
		}
	}
	return &p
}

func (app *App) HandleUISession(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(app, r)
	if p == nil || !p.HasScope(security.ScopeAuditRead) {
		oaiError(w, 403, "A dashboard session requires audit:read scope", "permission_error", "insufficient_scope")
		return
	}
	token, err := app.issueUISession(p)
	if err != nil {
		oaiError(w, 500, "Could not create UI session", "api_error", "")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: uiSessionCookie, Value: token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, MaxAge: int(uiSessionTTL.Seconds())})
	app.auditAs(p, "ui.session", "issued")
	sendJSON(w, 200, map[string]any{"ok": true, "expires_in_seconds": int(uiSessionTTL.Seconds())})
}

func (app *App) HandleUILogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(uiSessionCookie); err == nil {
		app.uiMu.Lock()
		delete(app.uiSessions, util.SHA256Hex(c.Value))
		app.uiMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: uiSessionCookie, Value: "", Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, MaxAge: -1})
	sendJSON(w, 200, map[string]any{"ok": true})
}
