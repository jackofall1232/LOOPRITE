package server_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/jackofall1232/l00prite/cli-os/internal/security"
)

func TestScopedTokenCanUseOnlyAuthorizedEndpoints(t *testing.T) {
	srv, _, db, _, _ := configured(t)
	_, token, err := security.MintTokenWithScopes(db, "ops", nil, nil,
		[]string{security.ScopeChatInvoke}, false)
	if err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/providers",
		strings.NewReader(`{"name":"mock","adapter":"mock","skip_validation":true}`))
	req.Header.Set("authorization", "Bearer "+token)
	req.Header.Set("content-type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "insufficient_scope") {
		t.Fatalf("want scoped 403, got %d %s", resp.StatusCode, body)
	}
}

func TestUISessionUsesHttpOnlyCookieAndHonorsRevocation(t *testing.T) {
	srv, _, db, tokenID, token := configured(t)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/ui/session", nil)
	req.Header.Set("authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	cookie := resp.Header.Get("Set-Cookie")
	if resp.StatusCode != 200 || !strings.Contains(cookie, "HttpOnly") || !strings.Contains(cookie, "SameSite=Strict") {
		t.Fatalf("unsafe UI session response: status=%d cookie=%q", resp.StatusCode, cookie)
	}

	requestSummary := func() int {
		r, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/dashboard/summary", nil)
		r.Header.Set("Cookie", cookie)
		got, e := http.DefaultClient.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		got.Body.Close()
		return got.StatusCode
	}
	if got := requestSummary(); got != 200 {
		t.Fatalf("session status=%d", got)
	}
	if !security.RevokeToken(db, tokenID) {
		t.Fatal("revoke failed")
	}
	if got := requestSummary(); got != 401 {
		t.Fatalf("revoked session status=%d", got)
	}
}
