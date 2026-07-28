// Package server is the HTTP(S) server. It routes the OpenAI-compatible surface, serves the embedded
// dashboard, and enforces safe-by-default startup (no non-loopback bind without TLS; master key must
// exist). Ported from server.js. The dashboard is embedded (public.Dashboard), so it needs no files
// on disk — consistent with the single-static-binary goal.
package server

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/jackofall1232/l00prite/cli-os/internal/config"
	"github.com/jackofall1232/l00prite/cli-os/internal/engine"
	"github.com/jackofall1232/l00prite/cli-os/internal/gateway"
	pep "github.com/jackofall1232/l00prite/cli-os/internal/policy"
	"github.com/jackofall1232/l00prite/cli-os/internal/state"
	"github.com/jackofall1232/l00prite/cli-os/public"
)

// inlineScriptHashes returns one CSP hash-source per inline <script> block in body. The browser
// hashes each block's exact text content individually, so every block needs its own entry in
// script-src — a single hash over the first-to-last-script span (the previous behavior) matches
// no block and silently blocks ALL inline JavaScript on the page.
func inlineScriptHashes(body []byte) []string {
	s := string(body)
	var out []string
	for {
		i := strings.Index(s, "<script")
		if i < 0 {
			return out
		}
		s = s[i+len("<script"):]
		gt := strings.Index(s, ">")
		if gt < 0 {
			return out
		}
		s = s[gt+1:]
		end := strings.Index(s, "</script>")
		if end < 0 {
			return out
		}
		sum := sha256.Sum256([]byte(s[:end]))
		out = append(out, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
		s = s[end+len("</script>"):]
	}
}

var pageScriptHashes = append(inlineScriptHashes(public.Dashboard), inlineScriptHashes(public.Setup)...)

func serveHTML(w http.ResponseWriter, body []byte, fallback string) {
	if len(body) == 0 {
		w.Header().Set("content-type", "text/plain")
		w.WriteHeader(200)
		w.Write([]byte(fallback))
		return
	}
	w.Header().Set("content-type", "text/html; charset=utf-8")
	w.Header().Set("content-length", fmt.Sprint(len(body)))
	w.WriteHeader(200)
	w.Write(body)
}

func setSecurityHeaders(w http.ResponseWriter, p string) {
	w.Header().Set("x-content-type-options", "nosniff")
	w.Header().Set("referrer-policy", "no-referrer")
	w.Header().Set("permissions-policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
	w.Header().Set("x-frame-options", "DENY")
	w.Header().Set("content-security-policy", "default-src 'self'; script-src 'self' "+strings.Join(pageScriptHashes, " ")+"; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; font-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	if p == "/" || p == "/dashboard" || p == "/setup" || strings.HasPrefix(p, "/v1/") {
		w.Header().Set("cache-control", "no-store")
		w.Header().Set("pragma", "no-cache")
	}
}

func serveDashboard(w http.ResponseWriter) {
	serveHTML(w, public.Dashboard, "l00prite CLI-OS is running. Dashboard asset not found.")
}

func serveSetup(w http.ResponseWriter) {
	serveHTML(w, public.Setup, "l00prite CLI-OS first-run setup. Setup asset not found.")
}

// serveAsset serves an embedded static file at /assets/... — content-type by extension, a long
// client cache (the files are fixed at build time and change only with the binary), and 404 for
// anything not in the embedded FS. embed.FS + fs.ValidPath make traversal impossible: ".." is
// not a valid embed path, and the FS contains only what was compiled in.
func serveAsset(w http.ResponseWriter, urlPath string) {
	rel := strings.TrimPrefix(urlPath, "/")
	if !strings.HasPrefix(rel, "assets/") || !fs.ValidPath(rel) {
		notFound(w)
		return
	}
	data, err := public.Assets.ReadFile(rel)
	if err != nil {
		notFound(w)
		return
	}
	ctype := ""
	switch strings.ToLower(path.Ext(rel)) {
	case ".png":
		ctype = "image/png"
	case ".jpg", ".jpeg":
		ctype = "image/jpeg"
	case ".webp":
		ctype = "image/webp"
	case ".svg":
		ctype = "image/svg+xml"
	case ".md":
		ctype = "text/plain; charset=utf-8"
	default:
		ctype = "application/octet-stream"
	}
	w.Header().Set("content-type", ctype)
	w.Header().Set("cache-control", "public, max-age=86400")
	w.Header().Set("content-length", fmt.Sprint(len(data)))
	w.WriteHeader(200)
	w.Write(data)
}

func notFound(w http.ResponseWriter) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(404)
	w.Write([]byte(`{"error":{"message":"Not found","type":"invalid_request_error"}}`))
}

func requiredScope(method, p string) string {
	switch {
	case method == http.MethodGet && p == "/v1/providers/catalog":
		return ""
	case method == http.MethodPost && p == "/v1/chat/completions":
		return "chat:invoke"
	case method == http.MethodGet && p == "/v1/dashboard/summary":
		return "audit:read"
	case method == http.MethodPost && p == "/v1/ui/session":
		return "audit:read"
	case p == "/v1/budget" || p == "/v1/auto-pr" || p == "/v1/chat-limits":
		return "budget:manage"
	case strings.HasPrefix(p, "/v1/providers/") || p == "/v1/providers":
		return "provider:manage"
	case strings.HasPrefix(p, "/v1/github/"):
		return "credential:manage"
	case p == "/v1/repos" || p == "/v1/repos/remove" || p == "/v1/repos/clone" || p == "/v1/repos/scaffold-branch":
		return "admin"
	case p == "/v1/runs" || p == "/v1/runs/preflight" || p == "/v1/runs/start":
		return "run:create"
	case p == "/v1/runs/approve" || p == "/v1/runs/stop":
		return "run:approve"
	case p == "/v1/runs/list" || p == "/v1/runs/get" || p == "/v1/runs/events":
		return "repo:read"
	}
	return ""
}

// Handler builds the request router with a top-level recover (any panic becomes a 500).
func Handler(app *gateway.App) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w, r.URL.Path)
		defer func() {
			if rec := recover(); rec != nil {
				// Best-effort 500 if nothing was written yet.
				defer func() { _ = recover() }()
				w.Header().Set("content-type", "application/json")
				w.WriteHeader(500)
				w.Write([]byte(`{"error":{"message":"Internal error","type":"api_error"}}`))
			}
		}()
		p := r.URL.Path
		if scope := requiredScope(r.Method, p); scope != "" && !app.AuthorizeScope(w, r, scope) {
			return
		}
		switch {
		case r.Method == http.MethodGet && p == "/":
			// First-run: an unconfigured system serves the setup wizard at / until setup completes,
			// after which / is permanently the real-data dashboard.
			if app.SetupComplete() {
				serveDashboard(w)
			} else {
				serveSetup(w)
			}
		case r.Method == http.MethodGet && p == "/dashboard":
			serveDashboard(w)
		case r.Method == http.MethodGet && p == "/setup":
			serveSetup(w)
		case r.Method == http.MethodGet && p == "/healthz":
			app.HandleHealth(w, r)
		case r.Method == http.MethodGet && strings.HasPrefix(p, "/assets/"):
			serveAsset(w, p)
		case r.Method == http.MethodGet && p == "/v1/models":
			app.HandleModels(w, r)
		case r.Method == http.MethodGet && p == "/v1/dashboard/summary":
			app.HandleDashboardSummary(w, r)
		case r.Method == http.MethodPost && p == "/v1/ui/session":
			app.HandleUISession(w, r)
		case r.Method == http.MethodPost && p == "/v1/ui/logout":
			app.HandleUILogout(w, r)
		case r.Method == http.MethodGet && p == "/v1/setup/status":
			app.HandleSetupStatus(w, r)
		// Unauthenticated read-only provider presets for the Add-provider UI (static embedded
		// manifest facts only -- same non-secret class as /v1/setup/status).
		case r.Method == http.MethodGet && p == "/v1/providers/catalog":
			app.HandleProviderPresets(w, r)
		case r.Method == http.MethodPost && p == "/v1/setup/vault":
			app.HandleSetupVault(w, r)
		case r.Method == http.MethodPost && p == "/v1/setup/session":
			app.HandleSetupSession(w, r)
		case r.Method == http.MethodPost && p == "/v1/setup/provider/test":
			app.HandleSetupProviderTest(w, r)
		case r.Method == http.MethodPost && p == "/v1/setup/provider":
			app.HandleSetupProvider(w, r)
		case r.Method == http.MethodPost && p == "/v1/setup/token":
			app.HandleSetupToken(w, r)
		// Authenticated per-project daily budget (dashboard "Set budget" modal).
		case r.Method == http.MethodGet && p == "/v1/budget":
			app.HandleBudgetGet(w, r)
		case r.Method == http.MethodPost && p == "/v1/budget":
			app.HandleBudgetSet(w, r)
		// Authenticated per-project Auto-PR toggle (dashboard "Auto-PR" modal, mirrors budget).
		case r.Method == http.MethodGet && p == "/v1/auto-pr":
			app.HandleAutoPRGet(w, r)
		case r.Method == http.MethodPost && p == "/v1/auto-pr":
			app.HandleAutoPRSet(w, r)
		// Authenticated per-project Playground tool-call budget (dashboard "Tool budget" modal,
		// mirrors auto-pr). Raises the chat read-only-tool round/call caps up to a compile-time
		// ceiling; the loop can never raise its own (see gateway/chatlimits.go).
		case r.Method == http.MethodGet && p == "/v1/chat-limits":
			app.HandleChatLimitsGet(w, r)
		case r.Method == http.MethodPost && p == "/v1/chat-limits":
			app.HandleChatLimitsSet(w, r)
		// Authenticated "Connect GitHub" credential (dashboard GitHub card). The token is verified
		// live, vault-sealed, and never returned; a model can never reach these (no HTTP client in
		// the engine/chat toolboxes).
		case r.Method == http.MethodGet && p == "/v1/github/status":
			app.HandleGitHubStatus(w, r)
		case r.Method == http.MethodPost && p == "/v1/github/connect":
			app.HandleGitHubConnect(w, r)
		case r.Method == http.MethodPost && p == "/v1/github/disconnect":
			app.HandleGitHubDisconnect(w, r)
		// Authenticated provider lifecycle management (Part E) — flat POST actions, name in the body.
		case r.Method == http.MethodPost && p == "/v1/providers":
			app.HandleProviderAdd(w, r)
		case r.Method == http.MethodPost && p == "/v1/providers/test":
			app.HandleProviderTest(w, r)
		case r.Method == http.MethodPost && p == "/v1/providers/rotate":
			app.HandleProviderRotate(w, r)
		case r.Method == http.MethodPost && p == "/v1/providers/remove":
			app.HandleProviderRemove(w, r)
		case r.Method == http.MethodPost && p == "/v1/providers/update":
			app.HandleProviderUpdate(w, r)
		case r.Method == http.MethodPost && p == "/v1/providers/models":
			app.HandleProviderModels(w, r)
		// Authenticated repo registration — the dashboard's "connect a repository" path (same
		// primitive as CLI `repo register`).
		case r.Method == http.MethodPost && p == "/v1/repos":
			app.HandleRepoRegister(w, r)
		case r.Method == http.MethodPost && p == "/v1/repos/remove":
			app.HandleRepoRemove(w, r)
		case r.Method == http.MethodPost && p == "/v1/repos/clone":
			app.HandleRepoClone(w, r)
		// Consent-gated "add l00prite" action — branch + commit the full protocol (AGENTS.md,
		// loop prompts, vendor adapters), reused by both the Register-repo checkbox and a
		// standalone per-repo dashboard action.
		case r.Method == http.MethodPost && p == "/v1/repos/scaffold-branch":
			app.HandleRepoScaffoldBranch(w, r)
		// L00prite OS run engine — the "enter a prompt, press Start" autonomous surface.
		case r.Method == http.MethodPost && p == "/v1/runs":
			app.HandleRunCreate(w, r)
		case r.Method == http.MethodGet && p == "/v1/runs/list":
			app.HandleRunList(w, r)
		case r.Method == http.MethodGet && p == "/v1/runs/get":
			app.HandleRunGet(w, r)
		case r.Method == http.MethodPost && p == "/v1/runs/preflight":
			app.HandleRunPreflight(w, r)
		case r.Method == http.MethodPost && p == "/v1/runs/start":
			app.HandleRunStart(w, r)
		case r.Method == http.MethodGet && p == "/v1/runs/events":
			app.HandleRunEvents(w, r)
		case r.Method == http.MethodPost && p == "/v1/runs/approve":
			app.HandleRunApprove(w, r)
		case r.Method == http.MethodPost && p == "/v1/runs/stop":
			app.HandleRunStop(w, r)
		case r.Method == http.MethodPost && p == "/v1/chat/completions":
			app.HandleChatCompletion(w, r)
		default:
			notFound(w)
		}
	})
}

// Overrides let `serve --host/--port` override config at boot.
type Overrides struct {
	Host string
	Port int
}

func staleAfterMs(cfg config.Config) int64 {
	v := int64(cfg.Retry.MaxAttempts*cfg.RequestTimeoutMs) + 60_000
	if v < 10*60_000 {
		v = 10 * 60_000
	}
	return v
}

// Start boots the gateway: load config, validate (refuse insecure), open the db, reap stale
// reservations (once + periodically), and listen. Blocks. Exits the process on a fatal config error.
func Start(ov Overrides) {
	cfg := config.Load()
	if ov.Host != "" {
		cfg.Host = ov.Host
	}
	if ov.Port != 0 {
		cfg.Port = ov.Port
	}
	// Bind-safety problems are ALWAYS fatal (no non-loopback bind without TLS; a configured cert pair
	// must exist). A missing master key is NOT fatal: the server boots into first-run setup mode and
	// the browser wizard initializes the vault. This is what makes zero-config first run possible.
	problems := config.BindProblems(cfg)
	if config.EnvMasterKeyInvalid() {
		// An invalid LOOPRITE_MASTER_KEY makes the vault unusable (the loader prefers the env var and
		// errors on it), so booting "as configured" would only fail on the first encrypt/decrypt. Fatal.
		problems = append(problems, "LOOPRITE_MASTER_KEY is set but is not valid base64 of 32 bytes — unset it or fix it.")
	}
	if len(problems) > 0 {
		fmt.Fprintln(os.Stderr, "Refusing to start — fix these first:")
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, "  • "+p)
		}
		os.Exit(1)
	}
	// A first-run `serve` may have no data dir yet (the user never ran `init`); create it so the DB
	// and the vault the wizard writes have a home.
	if err := config.EnsureHome(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "Failed to create data dir: "+err.Error())
		os.Exit(1)
	}
	firstRun := !config.MasterKeyPresent(cfg)
	db, err := state.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Failed to open database: "+err.Error())
		os.Exit(1)
	}
	stale := staleAfterMs(cfg)
	if n := pep.ReapStaleReservations(db, stale); n > 0 {
		fmt.Printf("  • reaped %d stale reservation(s)\n", n)
	}
	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for range t.C {
			pep.ReapStaleReservations(db, stale)
		}
	}()

	app := &gateway.App{DB: db, Cfg: cfg, Aliases: cfg.Aliases, StartedAt: time.Now()}
	// Wire the L00prite OS run engine to this App (over the same runTurn/router primitives), and
	// reconcile any run left "running" by a crash: the engine store marks it interrupted, and the
	// next pre-flight for that repo performs repo-side stale-run recovery per execute-loop.md.
	eng := engine.New(&engine.Store{DB: db}, gateway.NewEngineCaller(app))
	// The push_branch tool's credential source: decrypt the project's stored GitHub token at call
	// time. Injected here so the engine package never imports the vault (security/state).
	eng.PushCred = app.GitHubAuthFor
	if n, _ := eng.Store.ReconcileOrphans(); n > 0 {
		fmt.Printf("  • reconciled %d interrupted run(s) from a previous boot\n", n)
	}
	app.Engine = eng
	// Latch setup-complete at boot if the install is already configured (e.g. provisioned entirely via
	// the CLI). This makes the lockdown durable: a later `token revoke` / `provider remove` can never
	// re-open the unauthenticated setup endpoints.
	_ = app.SetupComplete()
	srv := &http.Server{Addr: fmt.Sprintf("%s:%d", cfg.Host, cfg.Port), Handler: Handler(app)}

	scheme := "http"
	if cfg.TLS != nil {
		scheme = "https"
	}
	fmt.Printf("l00prite CLI-OS listening on %s://%s:%d\n", scheme, cfg.Host, cfg.Port)
	fmt.Printf("  • OpenAI endpoint : %s://%s:%d/v1/chat/completions\n", scheme, cfg.Host, cfg.Port)
	fmt.Printf("  • Dashboard       : %s://%s:%d/\n", scheme, cfg.Host, cfg.Port)
	if firstRun || !app.SetupComplete() {
		fmt.Printf("  • First-run setup : open %s://%s:%d/ in a browser to configure (no terminal needed)\n", scheme, cfg.Host, cfg.Port)
	}

	if cfg.TLS != nil {
		err = srv.ListenAndServeTLS(cfg.TLS.CertPath, cfg.TLS.KeyPath)
	} else {
		err = srv.ListenAndServe()
	}
	if err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, "server error: "+err.Error())
		os.Exit(1)
	}
}
