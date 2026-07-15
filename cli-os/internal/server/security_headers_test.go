package server_test

import (
	"net/http"
	"strings"
	"testing"

	publicassets "github.com/jackofall1232/l00prite/cli-os/public"
)

func TestSecurityHeadersOnDashboardAndAPI(t *testing.T) {
	srv, _, _, _, token := configured(t)
	for _, tc := range []struct {
		path string
		auth bool
	}{
		{path: "/", auth: false},
		{path: "/v1/dashboard/summary", auth: true},
	} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+tc.path, nil)
		if tc.auth {
			req.Header.Set("authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.Header.Get("x-content-type-options") != "nosniff" {
			t.Fatalf("%s missing nosniff", tc.path)
		}
		if resp.Header.Get("x-frame-options") != "DENY" {
			t.Fatalf("%s missing frame deny", tc.path)
		}
		if !strings.Contains(resp.Header.Get("content-security-policy"), "frame-ancestors 'none'") {
			t.Fatalf("%s missing CSP frame protection", tc.path)
		}
		if strings.Contains(resp.Header.Get("content-security-policy"), "script-src 'self' 'unsafe-inline'") {
			t.Fatalf("%s permits inline scripts without a hash", tc.path)
		}
		if resp.Header.Get("cache-control") != "no-store" {
			t.Fatalf("%s should be no-store", tc.path)
		}
	}
}

func TestDashboardDoesNotPersistBearerInLocalStorage(t *testing.T) {
	html := string(publicassets.Dashboard)
	if strings.Contains(html, "localStorage.setItem(TOKEN_KEY") || strings.Contains(html, `const TOKEN_KEY=`) {
		t.Fatal("dashboard still persists the long-lived bearer in localStorage")
	}
	if !strings.Contains(html, "/v1/ui/session") || !strings.Contains(html, "/v1/ui/logout") {
		t.Fatal("dashboard does not use the HttpOnly UI-session exchange")
	}
}

func TestSetupPageNeverReadsSecretFromURL(t *testing.T) {
	html := string(publicassets.Setup)
	if strings.Contains(html, `qp.get("ss")`) || strings.Contains(html, "x-l00prite-setup-secret") {
		t.Fatal("setup page still receives the native install secret")
	}
	if strings.Contains(html, `localStorage.setItem("l00prite_token"`) {
		t.Fatal("setup page still persists the minted bearer")
	}
}
