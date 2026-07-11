package server_test

import (
	"testing"
)

// TestBudgetAuthRequired: /v1/budget has no unauthenticated path, same as every other management
// endpoint.
func TestBudgetAuthRequired(t *testing.T) {
	srv, _, _, _, _ := configured(t)
	base := srv.URL
	if resp, _ := doJSON(t, "GET", base+"/v1/budget", "", nil); resp.StatusCode != 401 {
		t.Fatalf("GET /v1/budget without token must be 401, got %d", resp.StatusCode)
	}
	if resp, _ := doJSON(t, "POST", base+"/v1/budget", "", map[string]any{"daily_usd": 5.0}); resp.StatusCode != 401 {
		t.Fatalf("POST /v1/budget without token must be 401, got %d", resp.StatusCode)
	}
}

// TestBudgetGetDefaultsWhenNoCapsRow: a project with no caps row reads the server default and reports
// explicit:false, so the dashboard can distinguish "never configured" from "configured to the same
// number as the default".
func TestBudgetGetDefaultsWhenNoCapsRow(t *testing.T) {
	srv, cfg, _, _, token := configured(t)
	resp, body := doJSON(t, "GET", srv.URL+"/v1/budget", token, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET /v1/budget want 200 got %d: %v", resp.StatusCode, body)
	}
	if body["explicit"] != false {
		t.Fatalf("no caps row must report explicit:false, got %v", body)
	}
	if toFloat(body["daily_usd"]) != cfg.DefaultDailyCapUsd {
		t.Fatalf("daily_usd want the server default %v got %v", cfg.DefaultDailyCapUsd, body["daily_usd"])
	}
	if body["unlimited"] != false {
		t.Fatalf("default must not be unlimited: %v", body)
	}
}

// TestBudgetSetStrictRoundTrips: POST a plain daily cap (no overage, not unlimited) and read it back
// through the same budgetView GET renders.
func TestBudgetSetStrictRoundTrips(t *testing.T) {
	srv, _, _, _, token := configured(t)
	resp, body := doJSON(t, "POST", srv.URL+"/v1/budget", token, map[string]any{"daily_usd": 5.0})
	if resp.StatusCode != 200 {
		t.Fatalf("POST /v1/budget want 200 got %d: %v", resp.StatusCode, body)
	}
	if toFloat(body["daily_usd"]) != 5.0 || body["explicit"] != true {
		t.Fatalf("expected daily_usd=5 explicit=true, got %v", body)
	}
	if toFloat(body["effective_cap_usd"]) != 5.0 {
		t.Fatalf("with no overage the effective cap equals the base cap, got %v", body["effective_cap_usd"])
	}
	_, get := doJSON(t, "GET", srv.URL+"/v1/budget", token, nil)
	if toFloat(get["daily_usd"]) != 5.0 {
		t.Fatalf("GET after POST should read back the stored budget, got %v", get)
	}
}

// TestBudgetSetOverageRoundTrips: overage_pct extends the effective ceiling but is reported alongside
// the unmodified base daily_usd.
func TestBudgetSetOverageRoundTrips(t *testing.T) {
	srv, _, _, _, token := configured(t)
	resp, body := doJSON(t, "POST", srv.URL+"/v1/budget", token, map[string]any{"daily_usd": 2.0, "overage_pct": 50.0})
	if resp.StatusCode != 200 {
		t.Fatalf("POST /v1/budget want 200 got %d: %v", resp.StatusCode, body)
	}
	if toFloat(body["daily_usd"]) != 2.0 {
		t.Fatalf("base daily_usd must stay 2.0, got %v", body["daily_usd"])
	}
	if toFloat(body["overage_pct"]) != 50.0 {
		t.Fatalf("overage_pct want 50 got %v", body["overage_pct"])
	}
	if toFloat(body["effective_cap_usd"]) != 3.0 {
		t.Fatalf("effective cap want 2*(1+.5)=3.0 got %v", body["effective_cap_usd"])
	}
}

// TestBudgetSetUnlimitedRoundTrips: unlimited:true must marshal effective_cap_usd as JSON null (never
// +Inf, which json.Marshal would fail on) and zero out overage_pct.
func TestBudgetSetUnlimitedRoundTrips(t *testing.T) {
	srv, _, _, _, token := configured(t)
	resp, body := doJSON(t, "POST", srv.URL+"/v1/budget", token, map[string]any{"daily_usd": 10.0, "unlimited": true})
	if resp.StatusCode != 200 {
		t.Fatalf("POST /v1/budget want 200 got %d: %v", resp.StatusCode, body)
	}
	if body["unlimited"] != true {
		t.Fatalf("unlimited must round-trip true, got %v", body)
	}
	if body["effective_cap_usd"] != nil {
		t.Fatalf("effective_cap_usd must be JSON null when unlimited, got %v", body["effective_cap_usd"])
	}
	if toFloat(body["overage_pct"]) != 0 {
		t.Fatalf("unlimited must zero out overage_pct, got %v", body["overage_pct"])
	}
}

// TestBudgetSetRejectsInvalidAmounts: NaN/Inf/negative/out-of-range values must be refused (fail closed),
// never silently clamped or stored.
func TestBudgetSetRejectsInvalidAmounts(t *testing.T) {
	srv, _, _, _, token := configured(t)
	cases := []map[string]any{
		{"daily_usd": -1.0},
		{"daily_usd": 0.0},
		{"daily_usd": 5.0, "overage_pct": -1.0},
		{"daily_usd": 5.0, "overage_pct": 1001.0},
		{}, // no daily_usd and not unlimited
	}
	for i, c := range cases {
		resp, body := doJSON(t, "POST", srv.URL+"/v1/budget", token, c)
		if resp.StatusCode != 400 {
			t.Fatalf("case %d (%v): want 400 got %d: %v", i, c, resp.StatusCode, body)
		}
	}
}

func toFloat(v any) float64 {
	f, _ := v.(float64)
	return f
}
