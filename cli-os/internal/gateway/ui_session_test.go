package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackofall1232/l00prite/cli-os/internal/security"
	"github.com/jackofall1232/l00prite/cli-os/internal/state"
)

func TestUIPrincipalRejectsMalformedTokenExpiry(t *testing.T) {
	db, err := state.Open(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id, raw, err := security.MintTokenWithRole(db, "test", nil, nil, security.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	principal := security.VerifyToken(db, raw)
	app := &App{DB: db}
	session, err := app.issueUISession(principal)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: uiSessionCookie, Value: session})
	if app.uiPrincipal(req) == nil {
		t.Fatal("valid UI session was rejected")
	}
	if _, err := db.Exec(`UPDATE tokens SET expires_at='not-a-timestamp' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if app.uiPrincipal(req) != nil {
		t.Fatal("UI session backed by a token with malformed expiry must fail closed")
	}
}
