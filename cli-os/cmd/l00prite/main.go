// Command l00prite is the CLI-OS control plane: it manages provider keys, gateway tokens, repos,
// cost caps, inspects routing/ledger, and starts the server. Ported from bin/cli.js + cli-main.js.
// (The Node launcher's re-exec to suppress a node:sqlite warning has no Go equivalent — the pure-Go
// SQLite driver emits no such warning.)
package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/jackofall1232/l00prite/cli-os/internal/config"
	"github.com/jackofall1232/l00prite/cli-os/internal/gateway"
	"github.com/jackofall1232/l00prite/cli-os/internal/gateway/adapters"
	"github.com/jackofall1232/l00prite/cli-os/internal/ledger"
	pep "github.com/jackofall1232/l00prite/cli-os/internal/policy"
	"github.com/jackofall1232/l00prite/cli-os/internal/security"
	"github.com/jackofall1232/l00prite/cli-os/internal/server"
	"github.com/jackofall1232/l00prite/cli-os/internal/state"
	"github.com/jackofall1232/l00prite/cli-os/internal/util"
)

const help = `l00prite CLI-OS — control plane

  l00prite init                                 Initialize data dir, database, master key
  l00prite serve [--host H] [--port N]          Start the gateway + dashboard
  l00prite health                               Print provider + spend status
  l00prite version                              Print version + platform (os/arch)

  l00prite provider add <name> [--key K] [--adapter native-messages|openai-compat|mock]
                              [--base URL] [--default]
  l00prite provider list
  l00prite provider test <name> [--key K] [--adapter A] [--base URL] [--model M]
  l00prite provider default <name>
  l00prite provider enable|disable|remove <name>

  l00prite token mint --project P [--repo ID] [--expires DAYS]
  l00prite token list
  l00prite token revoke <id>

  l00prite repo register <id> --root PATH [--project P]
  l00prite repo list

  l00prite cap set --project P --daily USD
  l00prite cap list

  l00prite route explain <request-id>
  l00prite route plan <model|auto|auto:profile> [--task "..."] [--vision] [--tools] [--route P/M]
  l00prite route profiles
  l00prite bridge status
  l00prite ledger [--limit N]

  Auto-routing: send model "auto" or "auto:<profile>" (cheap|quality|balanced), or
    header  x-l00prite-route: auto:<profile>  — best/most-efficient provider per task.
  Bridging (off by default): header  x-l00prite-bridge: on  lets a model delegate a
    sub-task to another provider via the l00prite_bridge tool (hop cap: routing.bridge.maxHops).
`

func parseArgs(argv []string) ([]string, map[string]string) {
	var pos []string
	flags := map[string]string{}
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if strings.HasPrefix(a, "--") {
			k := a[2:]
			if i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "--") {
				flags[k] = argv[i+1]
				i++
			} else {
				flags[k] = "true"
			}
		} else {
			pos = append(pos, a)
		}
	}
	return pos, flags
}

func flagStr(flags map[string]string, key string) (string, bool) {
	v, ok := flags[key]
	return v, ok
}

func audit(db *sql.DB, action, detail string) {
	var d any
	if detail != "" {
		d = detail
	}
	_, _ = db.ExecContext(state.Ctx(), `INSERT INTO audit(id,ts,actor,action,detail) VALUES(?,?,?,?,?)`,
		util.RID("aud"), util.NowISO(), "cli", action, d)
}

func withDB(cfg config.Config) *sql.DB {
	_ = config.EnsureHome(cfg)
	db, err := state.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "failed to open database: "+err.Error())
		os.Exit(1)
	}
	return db
}

func at(pos []string, i int) string {
	if i < len(pos) {
		return pos[i]
	}
	return ""
}

func main() {
	// Must run before anything (config.Load included) might perform a DNS lookup — see
	// docs/android-architecture.md §4 G1. A no-op on desktop (LOOPRITE_DNS unset, /etc/resolv.conf
	// present), so this costs nothing there.
	util.InitResolver()
	cfg := config.Load()
	pos, flags := parseArgs(os.Args[1:])
	cmd, sub, arg := at(pos, 0), at(pos, 1), at(pos, 2)

	if _, helpFlag := flags["help"]; cmd == "" || cmd == "help" || helpFlag {
		fmt.Print(help)
		return
	}

	if cmd == "init" {
		_ = config.EnsureHome(cfg)
		if err := security.EnsureMasterKey(cfg.MasterKeyPath); err != nil {
			fmt.Fprintln(os.Stderr, "failed to create master key: "+err.Error())
			os.Exit(1)
		}
		db := withDB(cfg)
		audit(db, "init", cfg.Home)
		fmt.Printf("Initialized l00prite CLI-OS at %s\n", cfg.Home)
		fmt.Println("Next (easiest): start the server and finish setup in your browser —")
		fmt.Println("  l00prite serve        # then open http://127.0.0.1:8787/")
		fmt.Println("Or do the same from the CLI:")
		fmt.Println("  l00prite provider add anthropic --key sk-ant-... --default")
		fmt.Println("  l00prite token mint --project default")
		fmt.Println("  l00prite serve")
		return
	}

	if cmd == "serve" {
		ov := server.Overrides{}
		if h, ok := flagStr(flags, "host"); ok {
			ov.Host = h
		}
		if p, ok := flagStr(flags, "port"); ok {
			if n, err := strconv.Atoi(p); err == nil {
				ov.Port = n
			}
		}
		server.Start(ov)
		return
	}

	if cmd == "version" {
		// Normalize to a single leading "v": the in-source default is "1.0.0", while a release
		// build stamps git-describe output (often already "vX.Y.Z"), so trim any leading "v"
		// before re-adding one — no "vv2.0.0".
		fmt.Printf("l00prite v%s (%s/%s)\n", strings.TrimPrefix(gateway.Version, "v"), runtime.GOOS, runtime.GOARCH)
		return
	}

	db := withDB(cfg)

	switch cmd {
	case "health":
		rows, _ := db.QueryContext(state.Ctx(), `SELECT name,adapter,enabled,is_default FROM providers ORDER BY is_default DESC`)
		fmt.Println("Providers:")
		if rows != nil {
			defer rows.Close()
			for rows.Next() {
				var name, adapter string
				var enabled, isDef int
				if rows.Scan(&name, &adapter, &enabled, &isDef) == nil {
					star := " "
					if isDef != 0 {
						star = "★"
					}
					st := "disabled"
					if enabled != 0 {
						st = "enabled"
					}
					fmt.Printf("  %s %-14s %-16s %s\n", star, name, adapter, st)
				}
			}
		}
	case "provider":
		providerCmd(db, cfg, sub, arg, flags)
	case "token":
		tokenCmd(db, sub, arg, flags)
	case "repo":
		repoCmd(db, sub, arg, flags)
	case "cap":
		capCmd(db, cfg, sub, flags)
	case "route":
		routeCmd(db, cfg, sub, arg, flags)
	case "bridge":
		if sub == "status" {
			b := cfg.Routing.Bridge
			en := "disabled"
			if b.Enabled {
				en = "ENABLED"
			}
			fmt.Printf("Bridging: %s by default  (per-request: header x-l00prite-bridge: on|off)\n", en)
			fmt.Printf("  max hops : %d  (header x-l00prite-bridge-max-hops may only LOWER this)\n", b.MaxHops)
			fmt.Println("  tool     : l00prite_bridge  (injected into the primary request when armed)")
			fmt.Println("  targets  : provider name, provider/model, or auto / auto:<profile>")
		} else {
			fmt.Fprintln(os.Stderr, "usage: bridge status")
		}
	case "ledger":
		limit := 20
		if v, ok := flagStr(flags, "limit"); ok {
			if n, err := strconv.Atoi(v); err == nil {
				limit = n
			}
		}
		for _, r := range ledger.Recent(db, limit) {
			cost := 0.0
			if r.CostUSD.Valid {
				cost = r.CostUSD.Float64
			}
			fmt.Printf("%s  %s/%s  $%.5f  %s\n", r.TS, orQ(r.Provider), orQ(r.Model), cost, r.Outcome)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		fmt.Print(help)
	}
}

func orQ(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

func providerCmd(db *sql.DB, cfg config.Config, sub, arg string, flags map[string]string) {
	switch sub {
	case "add":
		name := arg
		if name == "" {
			fmt.Fprintln(os.Stderr, "usage: provider add <name> [--key K] ...")
			return
		}
		var existingKey sql.NullString
		var existingDef int
		existing := db.QueryRowContext(state.Ctx(), `SELECT enc_key, is_default FROM providers WHERE name = ?`, name).Scan(&existingKey, &existingDef) == nil
		adapter, ok := flagStr(flags, "adapter")
		if !ok {
			adapter = adapters.DefaultAdapterKind(name)
		}
		base, ok := flagStr(flags, "base")
		if !ok || base == "" {
			base = adapters.DefaultBaseURL(name)
		}
		var encVal any
		if key, hasKey := flagStr(flags, "key"); hasKey && adapter != "mock" {
			enc, err := security.EncryptSecret(cfg.MasterKeyPath, key)
			if err != nil {
				fmt.Fprintln(os.Stderr, "failed to encrypt key: "+err.Error())
				return
			}
			encVal = enc
		} else if existing && existingKey.Valid {
			encVal = existingKey.String
		}
		if adapter != "mock" && encVal == nil {
			fmt.Printf("  ! no --key given; provider %q will fail real calls until a key is added.\n", name)
		}
		isDef := 0
		if _, d := flagStr(flags, "default"); d {
			isDef = 1
		} else if existing {
			isDef = existingDef
		}
		var baseVal any
		if base != "" {
			baseVal = base
		}
		_, _ = db.ExecContext(state.Ctx(),
			`INSERT OR REPLACE INTO providers(name,adapter,base_url,enc_key,enabled,is_default,created_at) VALUES(?,?,?,?,1,?,?)`,
			name, adapter, baseVal, encVal, isDef, util.NowISO())
		if _, d := flagStr(flags, "default"); d {
			_, _ = db.ExecContext(state.Ctx(), `UPDATE providers SET is_default = CASE WHEN name = ? THEN 1 ELSE 0 END`, name)
		}
		audit(db, "provider.add", name)
		suffix := ""
		if base != "" {
			suffix = ", base=" + base
		}
		def := ""
		if _, d := flagStr(flags, "default"); d {
			def = " [default]"
		}
		fmt.Printf("Added provider %q (adapter=%s%s)%s\n", name, adapter, suffix, def)
	case "list":
		rows, _ := db.QueryContext(state.Ctx(), `SELECT name,adapter,base_url,enabled,is_default,enc_key FROM providers`)
		if rows != nil {
			defer rows.Close()
			for rows.Next() {
				var name, adapter string
				var baseURL, encKey sql.NullString
				var enabled, isDef int
				if rows.Scan(&name, &adapter, &baseURL, &enabled, &isDef, &encKey) == nil {
					star := " "
					if isDef != 0 {
						star = "★"
					}
					keyYes := "no "
					if encKey.Valid && encKey.String != "" {
						keyYes = "yes"
					}
					st := "disabled"
					if enabled != 0 {
						st = "enabled"
					}
					fmt.Printf("%s %-14s %-16s key:%s %s %s\n", star, name, adapter, keyYes, st, baseURL.String)
				}
			}
		}
	case "test":
		name := arg
		if name == "" {
			fmt.Fprintln(os.Stderr, "usage: provider test <name> [--key K] [--adapter A] [--base URL] [--model M]")
			return
		}
		// Load the stored provider (if any) as the baseline, then apply flag overrides. This validates
		// the SAME way the setup wizard does — one shared primitive, gateway.TestProviderKey.
		var adapter string
		var baseURL, encKey sql.NullString
		_ = db.QueryRowContext(state.Ctx(), `SELECT adapter, base_url, enc_key FROM providers WHERE name = ?`, name).Scan(&adapter, &baseURL, &encKey)
		if a, ok := flagStr(flags, "adapter"); ok {
			adapter = a
		}
		if adapter == "" {
			adapter = adapters.DefaultAdapterKind(name)
		} else if adapter == "openai-native" {
			adapter = "openai-compat"
		}
		base := baseURL.String
		if b, ok := flagStr(flags, "base"); ok {
			base = b
		}
		key := ""
		if k, ok := flagStr(flags, "key"); ok {
			key = k
		} else if encKey.Valid && encKey.String != "" {
			if k, err := security.DecryptSecret(cfg.MasterKeyPath, encKey.String); err == nil {
				key = k
			}
		}
		model, _ := flagStr(flags, "model")
		res := gateway.TestProviderKey(cfg, adapter, name, base, key, model)
		if res.OK {
			suffix := ""
			if res.ModelUsed != "" {
				suffix = " (model " + res.ModelUsed + ")"
			}
			fmt.Printf("✓ provider %q validated%s\n", name, suffix)
		} else {
			fmt.Fprintf(os.Stderr, "✗ provider %q failed validation: %s\n", name, res.Error)
			os.Exit(1)
		}
	case "default":
		_, _ = db.ExecContext(state.Ctx(), `UPDATE providers SET is_default = CASE WHEN name = ? THEN 1 ELSE 0 END`, arg)
		fmt.Printf("Default provider: %s\n", arg)
	case "enable", "disable":
		v := 0
		if sub == "enable" {
			v = 1
		}
		_, _ = db.ExecContext(state.Ctx(), `UPDATE providers SET enabled = ? WHERE name = ?`, v, arg)
		fmt.Printf("%s: %sd\n", arg, sub)
	case "remove":
		// Delete the provider AND its model-selection rows in ONE transaction, matching the dashboard's
		// remove endpoint: neither surface leaves ghost provider_models rows behind for a later re-add to
		// resurrect, and a mid-delete failure is surfaced instead of printing a false "Removed".
		if _, err := state.Tx(db, func(q state.Querier) (any, error) {
			if _, err := q.ExecContext(state.Ctx(), `DELETE FROM provider_models WHERE provider = ?`, arg); err != nil {
				return nil, err
			}
			if _, err := q.ExecContext(state.Ctx(), `DELETE FROM providers WHERE name = ?`, arg); err != nil {
				return nil, err
			}
			return nil, nil
		}); err != nil {
			fmt.Fprintln(os.Stderr, "failed to remove provider: "+err.Error())
			return
		}
		fmt.Printf("Removed %s\n", arg)
	default:
		fmt.Fprintln(os.Stderr, "unknown provider subcommand")
	}
}

func tokenCmd(db *sql.DB, sub, arg string, flags map[string]string) {
	switch sub {
	case "mint":
		project, ok := flagStr(flags, "project")
		if !ok {
			fmt.Fprintln(os.Stderr, "usage: token mint --project P [--repo ID] [--expires DAYS]")
			return
		}
		var repo *string
		if r, ok := flagStr(flags, "repo"); ok {
			repo = &r
		}
		var expires *int
		if e, ok := flagStr(flags, "expires"); ok {
			if n, err := strconv.Atoi(e); err == nil {
				expires = &n
			}
		}
		id, token, err := security.MintToken(db, project, repo, expires)
		if err != nil {
			fmt.Fprintln(os.Stderr, "failed to mint token: "+err.Error())
			return
		}
		audit(db, "token.mint", id)
		fmt.Println("Token minted (shown once — store it now):")
		fmt.Println()
		fmt.Println("  " + token)
		fmt.Println()
		repoNote := ""
		if repo != nil {
			repoNote = " repo=" + *repo
		}
		fmt.Printf("  id=%s project=%s%s\n", id, project, repoNote)
	case "list":
		for _, t := range security.ListTokens(db) {
			repo := "-"
			if t.Repo != "" {
				repo = t.Repo
			}
			st := "active"
			if t.Revoked {
				st = "REVOKED"
			}
			exp := ""
			if t.ExpiresAt != "" {
				exp = "  exp=" + t.ExpiresAt
			}
			fmt.Printf("%s  project=%s  repo=%s  %s%s\n", t.ID, t.Project, repo, st, exp)
		}
	case "revoke":
		if security.RevokeToken(db, arg) {
			fmt.Printf("Revoked %s\n", arg)
		} else {
			fmt.Printf("No such token %s\n", arg)
		}
		audit(db, "token.revoke", arg)
	default:
		fmt.Fprintln(os.Stderr, "unknown token subcommand")
	}
}

func repoCmd(db *sql.DB, sub, arg string, flags map[string]string) {
	switch sub {
	case "register":
		id := arg
		root, hasRoot := flagStr(flags, "root")
		if id == "" || !hasRoot {
			fmt.Fprintln(os.Stderr, "usage: repo register <id> --root PATH [--project P]")
			return
		}
		absRoot, _ := filepath.Abs(root)
		project := "default"
		if p, ok := flagStr(flags, "project"); ok {
			project = p
		}
		_, _ = db.ExecContext(state.Ctx(), `INSERT OR REPLACE INTO repos(id,root,project,created_at) VALUES(?,?,?,?)`,
			id, absRoot, project, util.NowISO())
		audit(db, "repo.register", id)
		fmt.Printf("Registered repo %q -> %s (project=%s)\n", id, absRoot, project)
	case "list":
		rows, _ := db.QueryContext(state.Ctx(), `SELECT id,root,project FROM repos`)
		if rows != nil {
			defer rows.Close()
			for rows.Next() {
				var id, root, project string
				if rows.Scan(&id, &root, &project) == nil {
					fmt.Printf("%-20s project=%-12s %s\n", id, project, root)
				}
			}
		}
	default:
		fmt.Fprintln(os.Stderr, "unknown repo subcommand")
	}
}

func capCmd(db *sql.DB, cfg config.Config, sub string, flags map[string]string) {
	switch sub {
	case "set":
		project, okP := flagStr(flags, "project")
		daily, okD := flagStr(flags, "daily")
		if !okP || !okD {
			fmt.Fprintln(os.Stderr, "usage: cap set --project P --daily USD")
			return
		}
		d, err := strconv.ParseFloat(daily, 64)
		if err != nil {
			fmt.Fprintln(os.Stderr, "invalid --daily amount")
			return
		}
		_, _ = db.ExecContext(state.Ctx(), `INSERT OR REPLACE INTO caps(project,window,limit_usd) VALUES(?,'daily',?)`, project, d)
		audit(db, "cap.set", fmt.Sprintf("%s=%s", project, daily))
		fmt.Printf("Daily cap for %q set to $%.2f\n", project, d)
	case "list":
		// Drain the cap rows into memory BEFORE calling GetSpend: the pool is capped at one connection,
		// and GetSpend opens its own transaction, so querying while these rows are still open deadlocks.
		type capRow struct {
			project string
			limit   float64
		}
		var caps []capRow
		if rows, err := db.QueryContext(state.Ctx(), `SELECT project,limit_usd FROM caps`); err == nil {
			for rows.Next() {
				var c capRow
				if rows.Scan(&c.project, &c.limit) == nil {
					caps = append(caps, c)
				}
			}
			rows.Close()
		}
		for _, c := range caps {
			s := pep.GetSpend(db, c.project, cfg.DefaultDailyCapUsd)
			fmt.Printf("%-16s daily $%.2f  spent today $%.4f\n", c.project, c.limit, s.Reserved+s.Committed)
		}
	default:
		fmt.Fprintln(os.Stderr, "unknown cap subcommand")
	}
}

func routeCmd(db *sql.DB, cfg config.Config, sub, arg string, flags map[string]string) {
	switch sub {
	case "explain":
		rows := ledger.Explain(db, arg)
		if len(rows) == 0 {
			fmt.Printf("No ledger rows for %q\n", arg)
			return
		}
		for _, r := range rows {
			fmt.Printf("\nrequest %s @ %s\n", r.RequestID, r.TS)
			fmt.Printf("  route   %s/%s  (rule: %s)\n", r.Provider, r.Model, r.RuleID)
			if r.Decision != "" {
				var d map[string]any
				if json.Unmarshal([]byte(r.Decision), &d) == nil {
					if reason, ok := d["reason"].(string); ok {
						fmt.Printf("  reason  %s\n", reason)
					}
					if kind, ok := d["kind"].(string); ok {
						extra := ""
						if hop, ok := d["hop"]; ok {
							extra += fmt.Sprintf(" #%v", hop)
						}
						if turn, ok := d["turn"]; ok {
							extra += fmt.Sprintf(" turn %v", turn)
						}
						fmt.Printf("  hop     %s%s\n", kind, extra)
					}
				}
			}
			fmt.Printf("  tokens  in=%s out=%s cache_read=%s\n", nullIntStr(r.PromptTokens), nullIntStr(r.CompletionTokens), nullIntStr(r.CacheReadTokens))
			cost := 0.0
			if r.CostUSD.Valid {
				cost = r.CostUSD.Float64
			}
			estNote := ""
			if r.CostEstimated {
				estNote = " (estimated/unconfirmed price)"
			}
			fmt.Printf("  cost    $%.6f%s\n", cost, estNote)
			fmt.Printf("  memory  %s   outcome %s\n", r.MemoryStatus, r.Outcome)
		}
	case "profiles":
		fmt.Println("Auto profiles (model \"auto:<name>\" or header x-l00prite-route: auto:<name>):")
		for name, p := range cfg.Routing.Profiles {
			pref := p.Preference
			if pref == "" {
				pref = "balanced"
			}
			req := ""
			if len(p.Require) > 0 {
				req = "  require=" + strings.Join(p.Require, "+")
			}
			def := ""
			if name == cfg.Routing.AutoDefaultProfile {
				def = "   (default for bare \"auto\")"
			}
			fmt.Printf("  auto:%-10s preference=%s%s%s\n", name, pref, req, def)
		}
		fmt.Println("\nOperator quality ranks (provider/model, 0-100; edit routing.qualityRanks in config.json):")
		for k, v := range cfg.Routing.QualityRanks {
			fmt.Printf("  %-34s %d\n", k, v)
		}
	case "plan":
		model := arg
		if model == "" {
			fmt.Fprintln(os.Stderr, "usage: route plan <model|auto|auto:profile> [--task \"...\"] [--vision] [--tools] [--route P/M]")
			return
		}
		var providers []gateway.ProviderInfo
		rows, _ := db.QueryContext(state.Ctx(), `SELECT name, enabled, is_default FROM providers`)
		if rows != nil {
			for rows.Next() {
				var name string
				var enabled, isDef int
				if rows.Scan(&name, &enabled, &isDef) == nil {
					providers = append(providers, gateway.ProviderInfo{Name: name, Enabled: enabled != 0, IsDefault: isDef != 0})
				}
			}
			rows.Close()
		}
		task := "do the task"
		if t, ok := flagStr(flags, "task"); ok {
			task = t
		}
		var content any = task
		if _, vision := flagStr(flags, "vision"); vision {
			content = []any{
				map[string]any{"type": "text", "text": task},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AA"}},
			}
		}
		req := map[string]any{"model": model, "messages": []any{map[string]any{"role": "user", "content": content}}}
		if _, tools := flagStr(flags, "tools"); tools {
			req["tools"] = []any{map[string]any{"type": "function", "function": map[string]any{"name": "demo", "parameters": map[string]any{"type": "object", "properties": map[string]any{}}}}}
		}
		routeHeader, _ := flagStr(flags, "route")
		// route plan uses gateway.Pick against ProviderInfo directly.
		r, err := gatewayPickFromCLI(providers, cfg, req, routeHeader)
		if err != nil {
			fmt.Fprintf(os.Stderr, "No route: [%d] %s\n", statusOfErr(err), err.Error())
			os.Exit(1)
		}
		fmt.Printf("Would route -> %s/%s   (rule: %s)\n", r.Provider, r.Model, r.Decision["rule_id"])
		if reason, ok := r.Decision["reason"].(string); ok {
			fmt.Printf("  reason: %s\n", reason)
		}
		if cands, ok := r.Decision["candidates"].([]any); ok {
			fmt.Println("  candidates (best first):")
			for _, cRaw := range cands {
				c, _ := cRaw.(map[string]any)
				est := "n/a"
				if c["est_usd"] != nil {
					est = fmt.Sprintf("$%v", c["est_usd"])
				}
				ctx := ""
				if c["context_unverified"] == true {
					ctx = " (ctx unverified)"
				}
				fmt.Printf("    %-30s tier=%v quality=%v est=%s%s\n", c["target"], c["price_tier"], c["quality"], est, ctx)
			}
		}
		if rej, ok := r.Decision["rejected"].([]any); ok && len(rej) > 0 {
			fmt.Println("  rejected:")
			for _, xRaw := range rej {
				x, _ := xRaw.(map[string]any)
				var reasons []string
				for _, rr := range asAnyArr(x["reasons"]) {
					reasons = append(reasons, fmt.Sprint(rr))
				}
				fmt.Printf("    %v: %s\n", x["target"], strings.Join(reasons, "; "))
			}
		}
	default:
		fmt.Fprintln(os.Stderr, "unknown route subcommand (explain|plan|profiles)")
	}
}

func gatewayPickFromCLI(providers []gateway.ProviderInfo, cfg config.Config, req map[string]any, routeHeader string) (gateway.RouteResult, error) {
	return gateway.Pick(providers, cfg.Aliases, req, routeHeader, cfg)
}

func statusOfErr(err error) int {
	if s := gateway.HTTPStatusOf(err); s != 0 {
		return s
	}
	return 500
}

func nullIntStr(n sql.NullInt64) string {
	if !n.Valid {
		return "<nil>"
	}
	return strconv.FormatInt(n.Int64, 10)
}

func asAnyArr(v any) []any {
	if a, ok := v.([]any); ok {
		return a
	}
	return nil
}
