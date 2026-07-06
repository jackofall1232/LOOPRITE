package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMalformedDefaultCapFallsBackSafe(t *testing.T) {
	t.Setenv("LOOPRITE_HOME", t.TempDir())
	t.Setenv("LOOPRITE_DEFAULT_DAILY_CAP", "not-a-number")
	cfg := Load()
	if cfg.DefaultDailyCapUsd != 10 {
		t.Fatalf("malformed cap must fall back to 10, got %v", cfg.DefaultDailyCapUsd)
	}
}

func writeConfig(t *testing.T, json string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("LOOPRITE_HOME", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(json), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBridgeConfigDeepMerge(t *testing.T) {
	// A minimal bridge override must not wipe the default maxHops (the common "just enable it" config).
	writeConfig(t, `{"routing":{"bridge":{"enabled":true}}}`)
	cfg := Load()
	if !cfg.Routing.Bridge.Enabled {
		t.Fatalf("bridge should be enabled")
	}
	if cfg.Routing.Bridge.MaxHops != 3 {
		t.Fatalf("maxHops must keep the default 3, got %d", cfg.Routing.Bridge.MaxHops)
	}
}

func TestStringTypedNumericConfig(t *testing.T) {
	// Numeric settings written as JSON strings ("20") must parse, like JS Number("20").
	writeConfig(t, `{"defaultDailyCapUsd":"20","defaultMaxTokens":"8000"}`)
	cfg := Load()
	if cfg.DefaultDailyCapUsd != 20 {
		t.Fatalf("string cap want 20 got %v", cfg.DefaultDailyCapUsd)
	}
	if cfg.DefaultMaxTokens != 8000 {
		t.Fatalf("string maxTokens want 8000 got %d", cfg.DefaultMaxTokens)
	}
}

func TestRuntimeOverridesHonored(t *testing.T) {
	// retry / memory / requestTimeoutMs from config.json must apply (were previously dropped).
	writeConfig(t, `{"requestTimeoutMs":300000,"retry":{"maxAttempts":5},"memory":{"contextTokens":16000}}`)
	cfg := Load()
	if cfg.RequestTimeoutMs != 300000 {
		t.Fatalf("requestTimeoutMs want 300000 got %d", cfg.RequestTimeoutMs)
	}
	if cfg.Retry.MaxAttempts != 5 {
		t.Fatalf("retry.maxAttempts want 5 got %d", cfg.Retry.MaxAttempts)
	}
	if cfg.Retry.BaseMs != 250 {
		t.Fatalf("retry.baseMs must keep default 250 (deep-merge), got %d", cfg.Retry.BaseMs)
	}
	if cfg.Memory.ContextTokens != 16000 {
		t.Fatalf("memory.contextTokens want 16000 got %d", cfg.Memory.ContextTokens)
	}
}

func TestFalsyPortHostFallBack(t *testing.T) {
	writeConfig(t, `{"host":"","port":0}`)
	cfg := Load()
	if cfg.Host != "127.0.0.1" || cfg.Port != 8787 {
		t.Fatalf("falsy host/port must fall back to defaults, got %s:%d", cfg.Host, cfg.Port)
	}
}

func TestMasterKeyPresence(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LOOPRITE_HOME", dir)
	t.Setenv("LOOPRITE_MASTER_KEY", "")
	cfg := Load()
	// No env key, no key file -> absent (first run). ValidateForServe must flag it, but this is NOT a
	// bind problem, so the server may still boot into setup mode.
	if MasterKeyPresent(cfg) {
		t.Fatalf("master key must be absent on a fresh dir")
	}
	if len(BindProblems(cfg)) != 0 {
		t.Fatalf("a loopback bind without a key must have no BIND problems (setup mode is allowed): %v", BindProblems(cfg))
	}
	if len(ValidateForServe(cfg)) == 0 {
		t.Fatalf("ValidateForServe must still flag the missing master key")
	}
	// Env key of 32 bytes -> present.
	t.Setenv("LOOPRITE_MASTER_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if !MasterKeyPresent(cfg) {
		t.Fatalf("a valid 32-byte env key must count as present")
	}
	// A key file also counts as present.
	t.Setenv("LOOPRITE_MASTER_KEY", "")
	if err := os.WriteFile(cfg.MasterKeyPath, []byte("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="), 0o600); err != nil {
		t.Fatal(err)
	}
	if !MasterKeyPresent(cfg) {
		t.Fatalf("a key file must count as present")
	}
}

func TestBindSafetyStaysFatal(t *testing.T) {
	t.Setenv("LOOPRITE_HOME", t.TempDir())
	t.Setenv("LOOPRITE_ALLOW_INSECURE_BIND", "")
	// Non-loopback host without TLS is a fatal bind problem — setup mode does NOT relax this.
	cfg := Load()
	cfg.Host = "0.0.0.0"
	if len(BindProblems(cfg)) == 0 {
		t.Fatalf("binding 0.0.0.0 without TLS must be a bind problem")
	}
	// Opting in clears it.
	t.Setenv("LOOPRITE_ALLOW_INSECURE_BIND", "1")
	if len(BindProblems(cfg)) != 0 {
		t.Fatalf("LOOPRITE_ALLOW_INSECURE_BIND=1 must clear the non-loopback bind problem")
	}
}

func TestDefaultRoleProfiles(t *testing.T) {
	// The built-in role profiles ship alongside cheap/quality/balanced, including the newer
	// architect/writer/reviewer/advisor role-model profiles (brief: "Fable 5 architects, Sonnet 5
	// writes"). roleRanks now ships seeded (no longer empty) — see TestSeededRoleRanks.
	profiles := defaults().Routing.Profiles
	cases := []struct {
		name       string
		preference string
		rankMap    string
		require    []string
	}{
		{"plan", "quality", "plan", nil},
		// code/writer are "quality", not "balanced": the role policy ("Sonnet 5 writes") must
		// actually route, not be silently out-blended by a cheaper tools-capable catalog entry —
		// see the profile comment in defaults() and gateway's TestAutoWriterQualityPicksSonnet.
		{"code", "quality", "code", []string{"tools"}},
		{"review", "quality", "review", nil},
		{"summarize", "cost", "", nil},
		{"architect", "quality", "architect", nil},
		{"writer", "quality", "writer", []string{"tools"}},
		{"reviewer", "quality", "reviewer", nil},
		{"advisor", "balanced", "advisor", nil},
	}
	for _, c := range cases {
		p, ok := profiles[c.name]
		if !ok {
			t.Fatalf("default profile %q missing", c.name)
		}
		if p.Preference != c.preference {
			t.Fatalf("%s preference want %q got %q", c.name, c.preference, p.Preference)
		}
		if p.RankMap != c.rankMap {
			t.Fatalf("%s rankMap want %q got %q", c.name, c.rankMap, p.RankMap)
		}
		if len(p.Require) != len(c.require) {
			t.Fatalf("%s require want %v got %v", c.name, c.require, p.Require)
		}
		for i := range c.require {
			if p.Require[i] != c.require[i] {
				t.Fatalf("%s require[%d] want %q got %q", c.name, i, c.require[i], p.Require[i])
			}
		}
	}
}

func TestSeededRoleRanks(t *testing.T) {
	// RoleRanks no longer ships empty: architect/writer/reviewer/advisor plus the engine-internal
	// plan/code/review roles are seeded so on-device Execution Mode inherits the same role policy
	// (Fable 5 architects, Sonnet 5 writes). review/summarize is intentionally absent (falls back to
	// qualityRanks, unchanged).
	rr := defaults().Routing.RoleRanks
	wantKeys := []string{"architect", "writer", "reviewer", "advisor", "plan", "code", "review"}
	if len(rr) != len(wantKeys) {
		t.Fatalf("RoleRanks want %d role maps got %d: %v", len(wantKeys), len(rr), rr)
	}
	for _, k := range wantKeys {
		if _, ok := rr[k]; !ok {
			t.Fatalf("RoleRanks missing role map %q", k)
		}
	}
	// architect and plan must encode the identical policy (Fable 5 first), and be independent map
	// instances (mutating one must never be observable through the other).
	if rr["architect"]["anthropic/claude-fable-5"] != 98 {
		t.Fatalf(`architect["anthropic/claude-fable-5"] want 98 got %d`, rr["architect"]["anthropic/claude-fable-5"])
	}
	for k, v := range rr["architect"] {
		if rr["plan"][k] != v {
			t.Fatalf("plan role map must mirror architect's policy; plan[%q]=%d architect[%q]=%d", k, rr["plan"][k], k, v)
		}
	}
	rr["architect"]["probe/model"] = 1
	if _, leaked := rr["plan"]["probe/model"]; leaked {
		t.Fatalf("architect and plan must be independent map instances, not aliased")
	}
	// writer/code must prefer Sonnet 5 over Fable 5 (bulk-writer policy), and mirror each other.
	if rr["writer"]["anthropic/claude-sonnet-5"] <= rr["writer"]["anthropic/claude-fable-5"] {
		t.Fatalf("writer must rank claude-sonnet-5 above claude-fable-5, got sonnet=%d fable=%d",
			rr["writer"]["anthropic/claude-sonnet-5"], rr["writer"]["anthropic/claude-fable-5"])
	}
	for k, v := range rr["writer"] {
		if rr["code"][k] != v {
			t.Fatalf("code role map must mirror writer's policy; code[%q]=%d writer[%q]=%d", k, rr["code"][k], k, v)
		}
	}
	// reviewer/review must mirror each other.
	for k, v := range rr["reviewer"] {
		if rr["review"][k] != v {
			t.Fatalf("review role map must mirror reviewer's policy; review[%q]=%d reviewer[%q]=%d", k, rr["review"][k], k, v)
		}
	}
	// advisor has no engine-internal counterpart; just check it is seeded and sane.
	if rr["advisor"]["anthropic/claude-fable-5"] == 0 {
		t.Fatalf("advisor role map must be seeded, got %v", rr["advisor"])
	}
}

func TestQualityRanksIncludeVeniceAndGemini(t *testing.T) {
	// New providers' flagships must be visible to bare (non-role) quality routing.
	qr := defaults().Routing.QualityRanks
	want := map[string]int{
		"venice/claude-fable-5":                       90,
		"venice/claude-sonnet-5":                      85,
		"venice/qwen3-coder-480b-a35b-instruct-turbo": 78,
		"venice/deepseek-v4-pro":                      76,
		"venice/openai-gpt-52":                        80,
		"gemini/gemini-2.5-pro":                       84,
		"gemini/gemini-2.5-flash":                     72,
	}
	for k, v := range want {
		if qr[k] != v {
			t.Fatalf("qualityRanks[%q] want %d got %d", k, v, qr[k])
		}
	}
}

func TestRoleRanksOverrideMerge(t *testing.T) {
	// (config.json path) a roleRanks override lands without disturbing other routing defaults.
	writeConfig(t, `{"routing":{"roleRanks":{"code":{"zhipu/glm-5.2":90}}}}`)
	cfg := Load()
	if cfg.Routing.RoleRanks["code"]["zhipu/glm-5.2"] != 90 {
		t.Fatalf("roleRanks.code override must land, got %v", cfg.Routing.RoleRanks["code"])
	}
	if cfg.Routing.QualityRanks["anthropic/claude-opus-4-8"] != 96 {
		t.Fatalf("qualityRanks defaults must stay intact alongside a roleRanks override")
	}
	if _, ok := cfg.Routing.Profiles["code"]; !ok {
		t.Fatalf("default profiles must stay intact alongside a roleRanks override")
	}

	// (merge semantics) an override role map REPLACES that role's map wholesale; sibling roles keep theirs.
	base := defaults().Routing
	base.RoleRanks = map[string]map[string]int{
		"plan": {"anthropic/claude-opus-4-8": 91},
		"code": {"anthropic/claude-sonnet-5": 40, "zhipu/glm-5.2": 55},
	}
	mergeRouting(&base, &routingOverride{RoleRanks: map[string]map[string]int{"code": {"x/y": 90}}})
	if got := base.RoleRanks["code"]; len(got) != 1 || got["x/y"] != 90 {
		t.Fatalf("code role map must be replaced wholesale, got %v", got)
	}
	if base.RoleRanks["plan"]["anthropic/claude-opus-4-8"] != 91 {
		t.Fatalf("untouched role map (plan) must keep its entries, got %v", base.RoleRanks["plan"])
	}
}

func TestProfileProvidersRankMapRoundTrip(t *testing.T) {
	// A config.json profile carrying providers + rankMap round-trips whole (the per-key profile merge
	// replaces the value, so the new fields survive), and default profiles remain.
	writeConfig(t, `{"routing":{"profiles":{"privacy":{"preference":"quality","providers":["local","zhipu"],"rankMap":"review"}}}}`)
	cfg := Load()
	p, ok := cfg.Routing.Profiles["privacy"]
	if !ok {
		t.Fatalf("override profile must be present")
	}
	if p.Preference != "quality" {
		t.Fatalf("preference want quality got %q", p.Preference)
	}
	if p.RankMap != "review" {
		t.Fatalf("rankMap want review got %q", p.RankMap)
	}
	if len(p.Providers) != 2 || p.Providers[0] != "local" || p.Providers[1] != "zhipu" {
		t.Fatalf("providers want [local zhipu] got %v", p.Providers)
	}
	if _, ok := cfg.Routing.Profiles["balanced"]; !ok {
		t.Fatalf("default profiles must remain after an override adds a new profile")
	}
}
