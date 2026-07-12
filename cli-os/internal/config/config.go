// Package config loads runtime configuration (defaults -> $LOOPRITE_HOME/config.json -> env, later
// wins) and enforces "no insecure defaults" at startup — ported from config.js. Routing "opinions"
// (profiles, quality ranks, bridge switches) deep-merge so a config.json tweak overrides one field
// without dropping the built-ins.
package config

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type TLS struct {
	CertPath string
	KeyPath  string
}

type Retry struct {
	MaxAttempts int
	BaseMs      int
	MaxMs       int
}

type Memory struct {
	LatencyMs     int
	ContextTokens int
	MaxFileBytes  int
}

type Profile struct {
	Preference string   `json:"preference"`
	Require    []string `json:"require"`
	RankMap    string   `json:"rankMap,omitempty"`   // named roleRanks map to rank by (falls back to qualityRanks)
	Providers  []string `json:"providers,omitempty"` // non-empty: restrict candidates to these providers
}

type Bridge struct {
	Enabled bool `json:"enabled"`
	MaxHops int  `json:"maxHops"`
}

type Routing struct {
	AutoDefaultProfile string                    `json:"autoDefaultProfile"`
	Profiles           map[string]Profile        `json:"profiles"`
	QualityRanks       map[string]int            `json:"qualityRanks"`
	RoleRanks          map[string]map[string]int `json:"roleRanks"` // role-map-name -> "provider/model" -> 0-100 rank
	Bridge             Bridge                    `json:"bridge"`
}

type Config struct {
	Host               string
	Port               int
	TLS                *TLS
	DefaultDailyCapUsd float64
	DefaultMaxTokens   int
	Retry              Retry
	Memory             Memory
	RequestTimeoutMs   int
	Routing            Routing
	Aliases            map[string]string

	Home          string
	DBPath        string
	MasterKeyPath string
	LedgerPath    string
}

func defaults() Config {
	return Config{
		Host:               "127.0.0.1",
		Port:               8787,
		DefaultDailyCapUsd: 10,
		DefaultMaxTokens:   4096,
		Retry:              Retry{MaxAttempts: 3, BaseMs: 250, MaxMs: 4000},
		Memory:             Memory{LatencyMs: 150, ContextTokens: 8000, MaxFileBytes: 262144},
		RequestTimeoutMs:   120000,
		Routing: Routing{
			AutoDefaultProfile: "balanced",
			Profiles: map[string]Profile{
				"cheap":     {Preference: "cost"},
				"quality":   {Preference: "quality"},
				"balanced":  {Preference: "balanced"},
				"plan": {Preference: "quality", RankMap: "plan"},
				// code/writer are "quality" (not "balanced") on purpose: the maintainer's role policy
				// is that Sonnet-class models do the bulk writing, and a balanced cost-blend would
				// silently hand the writing role to whatever cheap tools-capable model a provider
				// catalog offers (observed with Venice's first-party pricing loaded). Cost control
				// belongs to the PEP daily caps and the cheap/balanced profiles, not to the role
				// policy; operators who want cost-blended writing can override these in config.json.
				"code":      {Preference: "quality", Require: []string{"tools"}, RankMap: "code"},
				"review":    {Preference: "quality", RankMap: "review"},
				"summarize": {Preference: "cost"},
				// Role profiles (brief: "Fable 5 architects, Sonnet 5 writes") — same profile+roleRanks
				// machinery as plan/code/review above, under names that map onto the role model directly.
				"architect": {Preference: "quality", RankMap: "architect"},
				"writer":    {Preference: "quality", Require: []string{"tools"}, RankMap: "writer"}, // same rationale as "code"
				"reviewer":  {Preference: "quality", RankMap: "reviewer"},
				"advisor":   {Preference: "balanced", RankMap: "advisor"},
			},
			QualityRanks: map[string]int{
				"anthropic/claude-opus-4-8":  96,
				"anthropic/claude-fable-5":   93,
				"anthropic/claude-sonnet-5":  88,
				"anthropic/claude-haiku-4-5": 74,
				"zhipu/glm-5.2":              82,
				"zhipu/glm-5.1":              78,
				"zhipu/glm-5v-turbo":         70,
				// Venice + Gemini flagships, so bare/unranked quality routing sees them once enabled.
				"venice/claude-fable-5":                       90,
				"venice/claude-sonnet-5":                      85,
				"venice/qwen3-coder-480b-a35b-instruct-turbo": 78,
				"venice/deepseek-v4-pro":                      76,
				"venice/openai-gpt-52":                        80,
				"gemini/gemini-3.1-pro-preview":               84,
				"gemini/gemini-3.5-flash":                     74,
				"gemini/gemini-3.1-flash-lite":                68,
			},
			// roleRanks: seeded operator opinions (same nature as qualityRanks — opinions, not
			// benchmarks) encoding the maintainer's role policy: Fable 5 architects/plans/reviews,
			// Sonnet 5 does the bulk writing. Every map here is fully overridable per deployment via
			// config.json routing.roleRanks (mergeRouting replaces a named role's map wholesale — see
			// mergeRouting below — so an operator can swap in their own policy for one role without
			// disturbing the others). The engine's internal plan/code/review roles are seeded with the
			// SAME values as architect/writer/reviewer (written out as separate map literals, not
			// aliased to the same map instance, so a future in-place mutation of one can never leak into
			// the other) so on-device Execution Mode inherits the identical policy.
			RoleRanks: map[string]map[string]int{
				"architect": {
					"anthropic/claude-fable-5":  98,
					"anthropic/claude-opus-4-8": 95,
					"venice/claude-fable-5":     90,
					"zhipu/glm-5.2":             80,
					"venice/zai-org-glm-5":      72,
				},
				// Sonnet is 97 (not 96) in writer/code: a candidate ABSENT from a role map falls back
				// to its qualityRanks value during the merge, and opus's qualityRanks entry is 96 —
				// an exact tie would hand the writing role to opus on the alphabetical tiebreak. 97
				// keeps the stated policy ("Sonnet 5 writes") true under quality-preference ranking.
				"writer": {
					"anthropic/claude-sonnet-5":                   97,
					"anthropic/claude-fable-5":                    90,
					"venice/claude-sonnet-5":                      88,
					"venice/qwen3-coder-480b-a35b-instruct-turbo": 80,
					"venice/openai-gpt-52-codex":                  78,
					"zhipu/glm-5.2":                               76,
				},
				"reviewer": {
					"anthropic/claude-opus-4-8": 96,
					"anthropic/claude-fable-5":  94,
					"venice/claude-fable-5":     86,
					"zhipu/glm-5.2":             78,
					"venice/deepseek-v4-pro":    72,
				},
				"advisor": {
					"anthropic/claude-fable-5":  92,
					"anthropic/claude-sonnet-5": 88,
					"venice/claude-sonnet-5":    82,
					"venice/minimax-m27":        70,
					"zhipu/glm-5.1":             68,
				},
				// engine-internal roles — same policy as architect/writer/reviewer, written out (not
				// aliased) per the note above.
				"plan": {
					"anthropic/claude-fable-5":  98,
					"anthropic/claude-opus-4-8": 95,
					"venice/claude-fable-5":     90,
					"zhipu/glm-5.2":             80,
					"venice/zai-org-glm-5":      72,
				},
				"code": { // mirrors "writer", incl. the 97-not-96 fallback-tie note above
					"anthropic/claude-sonnet-5":                   97,
					"anthropic/claude-fable-5":                    90,
					"venice/claude-sonnet-5":                      88,
					"venice/qwen3-coder-480b-a35b-instruct-turbo": 80,
					"venice/openai-gpt-52-codex":                  78,
					"zhipu/glm-5.2":                               76,
				},
				"review": {
					"anthropic/claude-opus-4-8": 96,
					"anthropic/claude-fable-5":  94,
					"venice/claude-fable-5":     86,
					"zhipu/glm-5.2":             78,
					"venice/deepseek-v4-pro":    72,
				},
			},
			Bridge: Bridge{Enabled: false, MaxHops: 3},
		},
	}
}

// fileConfig mirrors the recognized keys of config.json. The Node loader shallow-merged config.json
// over the defaults, so operator overrides for retry/memory/requestTimeoutMs/tls must be honored here
// too (they were previously dropped, silently reverting to compiled defaults).
type fileConfig struct {
	Host               *string           `json:"host"`
	Port               *int              `json:"port"`
	DefaultDailyCapUsd *json.RawMessage  `json:"defaultDailyCapUsd"`
	DefaultMaxTokens   *json.RawMessage  `json:"defaultMaxTokens"`
	RequestTimeoutMs   *int              `json:"requestTimeoutMs"`
	Retry              *retryOverride    `json:"retry"`
	Memory             *memoryOverride   `json:"memory"`
	TLS                *tlsOverride      `json:"tls"`
	Aliases            map[string]string `json:"aliases"`
	Routing            *routingOverride  `json:"routing"`
}

type retryOverride struct {
	MaxAttempts *int `json:"maxAttempts"`
	BaseMs      *int `json:"baseMs"`
	MaxMs       *int `json:"maxMs"`
}

type memoryOverride struct {
	LatencyMs     *int `json:"latencyMs"`
	ContextTokens *int `json:"contextTokens"`
	MaxFileBytes  *int `json:"maxFileBytes"`
}

type tlsOverride struct {
	CertPath *string `json:"certPath"`
	KeyPath  *string `json:"keyPath"`
}

type routingOverride struct {
	AutoDefaultProfile *string                   `json:"autoDefaultProfile"`
	Profiles           map[string]Profile        `json:"profiles"`
	QualityRanks       map[string]int            `json:"qualityRanks"`
	RoleRanks          map[string]map[string]int `json:"roleRanks"`
	Bridge             *bridgeOverride           `json:"bridge"`
}

// bridgeOverride uses pointer fields so a config.json bridge override deep-merges (a field absent
// from JSON keeps the default) rather than wholesale-replacing the struct.
type bridgeOverride struct {
	Enabled *bool `json:"enabled"`
	MaxHops *int  `json:"maxHops"`
}

// jsonScalarString unwraps a config.json scalar that may be a JSON number (20) or a JSON string
// ("20") into its plain string form, so string-typed numeric settings parse (matching JS Number()).
func jsonScalarString(raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return string(raw)
	}
}

func homeDir() string {
	if h := os.Getenv("LOOPRITE_HOME"); h != "" {
		return h
	}
	uh, err := os.UserHomeDir()
	if err != nil {
		uh = "."
	}
	return filepath.Join(uh, ".l00prite-cli-os")
}

// finiteOr parses raw (a string or JSON number) into a non-negative float, else returns fallback.
func finiteOr(raw string, fallback float64) float64 {
	n, err := strconv.ParseFloat(raw, 64)
	if err != nil || n < 0 || isNaNorInf(n) {
		return fallback
	}
	return n
}

func isNaNorInf(f float64) bool { return f != f || f > 1e308 || f < -1e308 }

// Load builds the effective config from defaults, config.json, and environment overrides.
func Load() Config {
	cfg := defaults()
	home := homeDir()

	var fc fileConfig
	if raw, err := os.ReadFile(filepath.Join(home, "config.json")); err == nil {
		_ = json.Unmarshal(raw, &fc) // malformed config.json is ignored, like readFileJSON's catch
	}

	if fc.Host != nil && *fc.Host != "" { // falsy host in config.json falls back to the default (127.0.0.1)
		cfg.Host = *fc.Host
	}
	if fc.Port != nil && *fc.Port != 0 { // falsy port in config.json falls back to the default (8787)
		cfg.Port = *fc.Port
	}
	if fc.Aliases != nil {
		cfg.Aliases = fc.Aliases
	}

	// Host / port env overrides.
	if h := os.Getenv("LOOPRITE_HOST"); h != "" {
		cfg.Host = h
	}
	if p := os.Getenv("LOOPRITE_PORT"); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n != 0 {
			cfg.Port = n
		}
	}

	// Default daily cap: env ?? file, must fail SAFE to the default (never NaN).
	capRaw := os.Getenv("LOOPRITE_DEFAULT_DAILY_CAP")
	if capRaw == "" && fc.DefaultDailyCapUsd != nil {
		capRaw = jsonScalarString(*fc.DefaultDailyCapUsd)
	}
	if capRaw != "" {
		cfg.DefaultDailyCapUsd = finiteOr(capRaw, defaults().DefaultDailyCapUsd)
	}

	// Default max tokens: env ?? file, finite>=0, and 0 -> default (so reservations always bound output).
	mtRaw := os.Getenv("LOOPRITE_DEFAULT_MAX_TOKENS")
	if mtRaw == "" && fc.DefaultMaxTokens != nil {
		mtRaw = jsonScalarString(*fc.DefaultMaxTokens)
	}
	if mtRaw != "" {
		v := finiteOr(mtRaw, float64(defaults().DefaultMaxTokens))
		if v == 0 {
			v = float64(defaults().DefaultMaxTokens)
		}
		cfg.DefaultMaxTokens = int(v)
	}

	// Runtime overrides from config.json (deep-merged per field so a partial block keeps the defaults).
	if fc.RequestTimeoutMs != nil && *fc.RequestTimeoutMs > 0 {
		cfg.RequestTimeoutMs = *fc.RequestTimeoutMs
	}
	if fc.Retry != nil {
		if fc.Retry.MaxAttempts != nil {
			cfg.Retry.MaxAttempts = *fc.Retry.MaxAttempts
		}
		if fc.Retry.BaseMs != nil {
			cfg.Retry.BaseMs = *fc.Retry.BaseMs
		}
		if fc.Retry.MaxMs != nil {
			cfg.Retry.MaxMs = *fc.Retry.MaxMs
		}
	}
	if fc.Memory != nil {
		if fc.Memory.LatencyMs != nil {
			cfg.Memory.LatencyMs = *fc.Memory.LatencyMs
		}
		if fc.Memory.ContextTokens != nil {
			cfg.Memory.ContextTokens = *fc.Memory.ContextTokens
		}
		if fc.Memory.MaxFileBytes != nil {
			cfg.Memory.MaxFileBytes = *fc.Memory.MaxFileBytes
		}
	}
	if fc.TLS != nil && fc.TLS.CertPath != nil && fc.TLS.KeyPath != nil && *fc.TLS.CertPath != "" && *fc.TLS.KeyPath != "" {
		cfg.TLS = &TLS{CertPath: *fc.TLS.CertPath, KeyPath: *fc.TLS.KeyPath}
	}

	// TLS from env (both required) — env wins over a config.json tls block.
	if cert, key := os.Getenv("LOOPRITE_TLS_CERT"), os.Getenv("LOOPRITE_TLS_KEY"); cert != "" && key != "" {
		cfg.TLS = &TLS{CertPath: cert, KeyPath: key}
	}

	// Deep-merge routing opinions from config.json.
	if fc.Routing != nil {
		mergeRouting(&cfg.Routing, fc.Routing)
	}
	// Operational bridge env switches.
	if v := os.Getenv("LOOPRITE_BRIDGE_ENABLED"); v != "" {
		cfg.Routing.Bridge.Enabled = v == "1"
	}
	if v := os.Getenv("LOOPRITE_BRIDGE_MAX_HOPS"); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil && n >= 0 {
			cfg.Routing.Bridge.MaxHops = int(n)
		}
	}

	cfg.Home = home
	cfg.DBPath = filepath.Join(home, "cli-os.db")
	cfg.MasterKeyPath = filepath.Join(home, "master.key")
	cfg.LedgerPath = filepath.Join(home, "ledger.jsonl")
	if cfg.Aliases == nil {
		cfg.Aliases = map[string]string{}
	}
	return cfg
}

func mergeRouting(base *Routing, o *routingOverride) {
	if o.AutoDefaultProfile != nil && *o.AutoDefaultProfile != "" {
		base.AutoDefaultProfile = *o.AutoDefaultProfile
	}
	for k, v := range o.Profiles {
		base.Profiles[k] = v
	}
	for k, v := range o.QualityRanks {
		base.QualityRanks[k] = v
	}
	if base.RoleRanks == nil {
		base.RoleRanks = map[string]map[string]int{}
	}
	for k, v := range o.RoleRanks { // per role key: an override role map REPLACES that role's map wholesale
		base.RoleRanks[k] = v
	}
	if o.Bridge != nil { // deep-merge: only fields present in the override replace the defaults
		if o.Bridge.Enabled != nil {
			base.Bridge.Enabled = *o.Bridge.Enabled
		}
		if o.Bridge.MaxHops != nil {
			base.Bridge.MaxHops = *o.Bridge.MaxHops
		}
	}
}

var loopback = map[string]bool{"127.0.0.1": true, "::1": true, "localhost": true}

// validBase64Key32 reports whether s decodes to 32 bytes under any common base64 variant.
func validBase64Key32(s string) bool {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil && len(b) == 32 {
			return true
		}
	}
	return false
}

// IsLoopbackHost reports whether host is a loopback address the server may bind without TLS.
func IsLoopbackHost(host string) bool { return loopback[host] }

// AllowInsecureBind reports whether the operator opted into binding a non-loopback host without TLS.
func AllowInsecureBind() bool { return os.Getenv("LOOPRITE_ALLOW_INSECURE_BIND") == "1" }

// BindProblems returns the bind-safety problems that must ALWAYS block startup (no non-loopback bind
// without TLS; a configured TLS pair must exist on disk). These are independent of whether the vault
// is initialized — an unconfigured first run may still boot the setup wizard, but only on a safe bind.
func BindProblems(cfg Config) []string {
	var problems []string
	if !loopback[cfg.Host] && cfg.TLS == nil && !AllowInsecureBind() {
		problems = append(problems, `Refusing to bind non-loopback host "`+cfg.Host+`" without TLS. Either set `+
			`LOOPRITE_TLS_CERT + LOOPRITE_TLS_KEY, bind to 127.0.0.1, or (only behind a trusted `+
			`reverse proxy / private network) set LOOPRITE_ALLOW_INSECURE_BIND=1.`)
	}
	if cfg.TLS != nil {
		for _, pair := range [][2]string{{"certificate", cfg.TLS.CertPath}, {"key", cfg.TLS.KeyPath}} {
			if _, err := os.Stat(pair[1]); err != nil {
				problems = append(problems, "TLS "+pair[0]+" not found at "+pair[1])
			}
		}
	}
	return problems
}

// MasterKeyPresent reports whether a USABLE vault master key is available, mirroring the precedence of
// security.loadMasterKey exactly: if LOOPRITE_MASTER_KEY is set, it is the source of truth and is
// usable only if it decodes to 32 bytes (the loader will NOT fall back to the key file when the env var
// is set-but-invalid); otherwise a key file on disk counts. When false, the system is genuinely
// unconfigured and boots into first-run setup instead of refusing to start. Single source of truth for
// "is the vault initialized". (An env var that is set-but-invalid is caught as a fatal boot error via
// EnvMasterKeyInvalid, so it never reaches here as a silent "configured" state.)
func MasterKeyPresent(cfg Config) bool {
	if k := os.Getenv("LOOPRITE_MASTER_KEY"); k != "" {
		return validBase64Key32(k) // env set: usable ONLY if valid; do not fall back to the file
	}
	_, err := os.Stat(cfg.MasterKeyPath)
	return err == nil
}

// EnvMasterKeyInvalid reports whether LOOPRITE_MASTER_KEY is set but not valid base64 of 32 bytes.
// Such a value makes the vault unusable (the loader prefers the env var and errors on it), so startup
// treats it as fatal rather than booting "as configured" and failing on the first encrypt/decrypt.
func EnvMasterKeyInvalid() bool {
	k := os.Getenv("LOOPRITE_MASTER_KEY")
	return k != "" && !validBase64Key32(k)
}

// ValidateForServe returns human-readable problems that must block startup, else nil. It combines the
// always-fatal bind-safety checks with the master-key check. Callers that support a first-run setup
// mode (the server) use BindProblems + MasterKeyPresent directly so a missing key opens the wizard
// rather than aborting; ValidateForServe is retained for callers that require a fully-configured host.
func ValidateForServe(cfg Config) []string {
	problems := BindProblems(cfg)
	if !MasterKeyPresent(cfg) {
		problems = append(problems, `Master key missing. Set LOOPRITE_MASTER_KEY (base64 of 32 bytes) or run "l00prite init" first.`)
	}
	return problems
}

// EnsureHome creates the data dir with 0700 perms.
func EnsureHome(cfg Config) error {
	return os.MkdirAll(cfg.Home, 0o700)
}
