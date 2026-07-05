// Post-setup provider lifecycle management (Part E). Once first-run setup is complete the wizard
// endpoints are permanently locked, so these AUTHENTICATED endpoints are the ongoing way to add,
// rotate, remove, toggle, and re-select models for providers entirely from the dashboard — no CLI.
//
// SECURITY:
//   - Every endpoint authenticates with the SAME gateway Bearer token as /v1/chat/completions and
//     /v1/dashboard/summary — there is no unauthenticated management or "emergency revoke" path.
//     (NOTE for reviewers: per the Part E spec this is same-auth, so ANY valid token can manage
//     providers. A dedicated management-scoped token is a deliberate, documented follow-up — see
//     docs/dashboard-and-setup.md §Part E — not a silent omission.)
//   - No endpoint EVER returns a stored key value (not even masked): a saved key can only be replaced,
//     never read back. Responses carry has_key / verified booleans only.
//   - Every add/rotate/remove/model/toggle action is written to the audit log with the acting token id
//     as the actor and a real timestamp, so the audit trail attributes each change to a session.
//
// The add path is the SAME code (storeProvider) the first-run wizard uses, so the two can never
// diverge. Rotation and removal are deliberately NOT the add path: rotate is a targeted UPDATE that
// preserves the provider's identity (default flag, enabled state, created_at) while overwriting only
// the key; removal is a transactional DELETE that also clears the provider's model-selection rows.
package gateway

import (
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/jackofall1232/l00prite/cli-os/internal/apierr"
	"github.com/jackofall1232/l00prite/cli-os/internal/config"
	"github.com/jackofall1232/l00prite/cli-os/internal/gateway/adapters"
	"github.com/jackofall1232/l00prite/cli-os/internal/security"
	"github.com/jackofall1232/l00prite/cli-os/internal/state"
	"github.com/jackofall1232/l00prite/cli-os/internal/util"
)

// validProviderName is the charset the router can actually address (the same as router.go's targetRE),
// enforced at store time so a provider name can never be un-routable or smuggle characters into a URL,
// path, or log line.
var validProviderName = regexp.MustCompile(`^[a-z0-9_-]+$`)

// providerParams is the normalized input to the shared provider-store core.
type providerParams struct {
	Name           string
	Adapter        string
	BaseURL        string
	APIKey         string
	Model          string // optional model to probe during validation
	Default        bool
	SkipValidation bool
}

// providerStoreResult reports what was persisted — never the key.
type providerStoreResult struct {
	Name           string
	Adapter        string
	BaseURL        string
	HasKey         bool
	Validated      bool
	ValidatedModel string
	IsDefault      bool
}

// storeProvider is the ONE code path that validates (a real upstream round-trip, unless explicitly
// skipped), encrypts, and inserts a provider row — shared by the first-run setup wizard (Part B) and
// the authenticated dashboard "add provider" endpoint (Part E), so the two can never diverge. It always
// writes verified=0: a freshly stored key is unverified until a real routed request succeeds against it.
// It performs NO audit and NO HTTP I/O — the caller attributes the audit (setup vs. token id) and
// renders the result — and it returns a typed *apierr.Error the caller maps to a response.
func (app *App) storeProvider(p providerParams) (*providerStoreResult, *apierr.Error) {
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return nil, apierr.New(400, "provider name is required", "invalid_request_error")
	}
	if !validProviderName.MatchString(name) {
		return nil, apierr.New(400, `provider name must match [a-z0-9_-]+ (lowercase letters, digits, "-" or "_")`, "invalid_request_error")
	}
	adapterKind := resolveAdapterKind(name, p.Adapter)
	baseURL := p.BaseURL
	if strings.TrimSpace(baseURL) == "" {
		baseURL = adapters.DefaultBaseURL(name)
	}

	needsKey := adapterKind != "mock"
	if needsKey && !config.MasterKeyPresent(app.Cfg) {
		e := apierr.New(400, "The vault is not initialized, so a keyed provider can't be encrypted.", "invalid_request_error")
		e.Code = "vault_required"
		return nil, e
	}

	// Real key validation before we store anything — unless explicitly skipped (keyless/offline add).
	validatedModel := ""
	if !p.SkipValidation {
		res := TestProviderKey(app.Cfg, adapterKind, name, baseURL, p.APIKey, p.Model)
		if !res.OK {
			e := apierr.New(400, "Provider validation failed: "+res.Error, "invalid_request_error")
			e.Code = "provider_validation_failed"
			e.Decision = map[string]any{"detail": res.Error} // carry the raw reason for the caller's body
			return nil, e
		}
		validatedModel = res.ModelUsed
	}

	// Encrypt the key (keyed adapters only) and persist — identical row shape to CLI `provider add`.
	var encVal any
	if needsKey && strings.TrimSpace(p.APIKey) != "" {
		enc, err := security.EncryptSecret(app.Cfg.MasterKeyPath, p.APIKey)
		if err != nil {
			return nil, apierr.New(500, "Failed to encrypt provider key: "+err.Error(), "configuration_error")
		}
		encVal = enc
	}
	isDef := 0
	if p.Default {
		isDef = 1
	}
	var baseVal any
	if baseURL != "" {
		baseVal = baseURL
	}
	// Insert the row AND (when it's the new default) clear every other provider's default flag as ONE
	// atomic unit, so a concurrent reader or a crash between the two statements can never observe/persist
	// two default providers. The network validation + encryption above deliberately stay OUTSIDE the
	// transaction — a BEGIN IMMEDIATE lock must not be held across an upstream round-trip.
	if _, err := state.Tx(app.DB, func(q state.Querier) (any, error) {
		if _, err := q.ExecContext(state.Ctx(),
			`INSERT OR REPLACE INTO providers(name,adapter,base_url,enc_key,enabled,is_default,verified,created_at) VALUES(?,?,?,?,1,?,0,?)`,
			name, adapterKind, baseVal, encVal, isDef, util.NowISO()); err != nil {
			return nil, err
		}
		if p.Default {
			if _, err := q.ExecContext(state.Ctx(), `UPDATE providers SET is_default = CASE WHEN name = ? THEN 1 ELSE 0 END`, name); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}); err != nil {
		return nil, apierr.New(500, "Failed to store provider: "+err.Error(), "configuration_error")
	}
	return &providerStoreResult{
		Name: name, Adapter: adapterKind, BaseURL: baseURL, HasKey: encVal != nil,
		Validated: !p.SkipValidation, ValidatedModel: validatedModel, IsDefault: p.Default,
	}, nil
}

// ---- auth + audit helpers ----

// requireToken authenticates a management request and writes a 401 (returning nil) if it fails, so the
// management endpoints share the exact auth of every other data endpoint.
func (app *App) requireToken(w http.ResponseWriter, r *http.Request) *security.Principal {
	principal := principalFrom(app, r)
	if principal == nil {
		oaiError(w, 401, "Missing or invalid l00prite token", "authentication_error", "")
		return nil
	}
	return principal
}

// auditAs records an operator action attributed to the acting token (its id becomes the audit actor),
// with the provider name as the detail — never any key material.
func (app *App) auditAs(principal *security.Principal, action, detail string) {
	actor := "dashboard"
	if principal != nil && principal.TokenID != "" {
		actor = principal.TokenID
	}
	var d any
	if detail != "" {
		d = detail
	}
	_, _ = app.DB.ExecContext(state.Ctx(), `INSERT INTO audit(id,ts,actor,action,detail) VALUES(?,?,?,?,?)`,
		util.RID("aud"), util.NowISO(), actor, action, d)
}

// writeProviderErr maps a storeProvider typed error to a response, preserving the wizard's richer
// validation-failure body ({ok:false, detail}) so the UI can show the raw upstream reason.
func writeProviderErr(w http.ResponseWriter, e *apierr.Error) {
	if e.Code == "provider_validation_failed" {
		detail := ""
		if e.Decision != nil {
			detail, _ = e.Decision["detail"].(string)
		}
		sendJSON(w, e.Status, map[string]any{
			"error": map[string]any{"message": e.Message, "type": e.Type, "code": e.Code},
			"ok":    false, "detail": detail,
		})
		return
	}
	oaiError(w, e.Status, e.Message, e.Type, e.Code)
}

func providerPublic(res *providerStoreResult) map[string]any {
	return map[string]any{
		"name": res.Name, "adapter": res.Adapter, "base_url": nilIfEmpty(res.BaseURL),
		"is_default": res.IsDefault, "has_key": res.HasKey, "verified": false,
		"validated": res.Validated, "validated_model": nilIfEmpty(res.ValidatedModel),
	}
}

// ---- handlers ----

// HandleProviderTest is POST /v1/providers/test — authenticated key validation with a REAL upstream
// call. Stores nothing (mirrors the setup wizard's provider/test), so the add/rotate modal can prove a
// key before committing it.
func (app *App) HandleProviderTest(w http.ResponseWriter, r *http.Request) {
	if app.requireToken(w, r) == nil {
		return
	}
	var body providerTestReq
	if err := decodeSetupBody(r, &body); err != nil {
		oaiError(w, 400, "Invalid JSON body", "invalid_request_error", "")
		return
	}
	adapterKind := resolveAdapterKind(body.Name, body.Adapter)
	res := TestProviderKey(app.Cfg, adapterKind, body.Name, body.BaseURL, body.APIKey, body.Model)
	sendJSON(w, 200, map[string]any{"ok": res.OK, "error": nilIfEmpty(res.Error), "model_used": nilIfEmpty(res.ModelUsed)})
}

// HandleProviderAdd is POST /v1/providers — add a provider post-setup via the SAME core the wizard uses.
// A name that already exists is rejected (409) so an add can never silently clobber a configured
// provider's key/default/enabled state; use rotate to change a key.
func (app *App) HandleProviderAdd(w http.ResponseWriter, r *http.Request) {
	principal := app.requireToken(w, r)
	if principal == nil {
		return
	}
	var body providerReq
	if err := decodeSetupBody(r, &body); err != nil {
		oaiError(w, 400, "Invalid JSON body", "invalid_request_error", "")
		return
	}
	name := strings.TrimSpace(body.Name)
	if name != "" && getProvider(app.DB, name) != nil {
		oaiError(w, 409, `A provider named "`+name+`" already exists. Rotate its key or remove it first.`, "invalid_request_error", "provider_exists")
		return
	}
	res, e := app.storeProvider(providerParams{
		Name: body.Name, Adapter: body.Adapter, BaseURL: body.BaseURL, APIKey: body.APIKey,
		Model: body.Model, Default: body.Default, SkipValidation: body.SkipValidation,
	})
	if e != nil {
		writeProviderErr(w, e)
		return
	}
	app.auditAs(principal, "provider.add", res.Name)
	sendJSON(w, 200, map[string]any{"provider": providerPublic(res)})
}

type rotateReq struct {
	Name           string `json:"name"`
	APIKey         string `json:"api_key"`
	Model          string `json:"model"`
	SkipValidation bool   `json:"skip_validation"`
}

// HandleProviderRotate is POST /v1/providers/rotate — replace a provider's key WITHOUT touching its
// identity (default flag, enabled state, created_at, adapter, base URL, request history all preserved).
// The new key is validated (unless skipped), then the OLD ciphertext is overwritten in place (a real
// overwrite — there is no soft-delete or second copy), verified resets to 0, and the circuit breaker is
// cleared so a key that fixes a failing provider isn't left behind a still-open breaker.
func (app *App) HandleProviderRotate(w http.ResponseWriter, r *http.Request) {
	principal := app.requireToken(w, r)
	if principal == nil {
		return
	}
	var body rotateReq
	if err := decodeSetupBody(r, &body); err != nil {
		oaiError(w, 400, "Invalid JSON body", "invalid_request_error", "")
		return
	}
	name := strings.TrimSpace(body.Name)
	prov := getProvider(app.DB, name)
	if prov == nil {
		oaiError(w, 404, `No provider named "`+name+`".`, "invalid_request_error", "provider_not_found")
		return
	}
	if prov.Adapter == "mock" {
		oaiError(w, 400, "The mock provider has no key to rotate.", "invalid_request_error", "no_key")
		return
	}
	if strings.TrimSpace(body.APIKey) == "" {
		oaiError(w, 400, "A new API key is required to rotate. To remove the provider entirely, use remove.", "invalid_request_error", "")
		return
	}
	if !config.MasterKeyPresent(app.Cfg) {
		oaiError(w, 400, "The vault is not initialized.", "invalid_request_error", "vault_required")
		return
	}
	validatedModel := ""
	if !body.SkipValidation {
		res := TestProviderKey(app.Cfg, prov.Adapter, name, prov.BaseURL, body.APIKey, body.Model)
		if !res.OK {
			sendJSON(w, 400, map[string]any{
				"error": map[string]any{"message": "Provider validation failed: " + res.Error, "type": "invalid_request_error", "code": "provider_validation_failed"},
				"ok":    false, "detail": res.Error,
			})
			return
		}
		validatedModel = res.ModelUsed
	}
	enc, err := security.EncryptSecret(app.Cfg.MasterKeyPath, body.APIKey)
	if err != nil {
		oaiError(w, 500, "Failed to encrypt provider key: "+err.Error(), "configuration_error", "")
		return
	}
	// Targeted overwrite — NOT the add path's INSERT OR REPLACE, which would reset enabled/is_default/
	// created_at. Only enc_key and verified change; verified resets so the new key must prove itself.
	if _, err := app.DB.ExecContext(state.Ctx(), `UPDATE providers SET enc_key = ?, verified = 0 WHERE name = ?`, enc, name); err != nil {
		oaiError(w, 500, "Failed to store rotated key: "+err.Error(), "configuration_error", "")
		return
	}
	MarkSuccess(name) // clear any breaker the old (failing) key tripped, so the fresh key gets a clean try
	app.auditAs(principal, "provider.rotate", name)
	sendJSON(w, 200, map[string]any{"provider": map[string]any{
		"name": name, "adapter": prov.Adapter, "has_key": true, "verified": false,
		"is_default": prov.IsDefault, "enabled": prov.Enabled,
		"validated": !body.SkipValidation, "validated_model": nilIfEmpty(validatedModel),
	}})
}

type removeReq struct {
	Name    string `json:"name"`
	Confirm string `json:"confirm"`
}

// HandleProviderRemove is POST /v1/providers/remove — fully delete a provider's key and config. It is
// a SERVER-SIDE type-to-confirm action: the request must echo the exact provider name in `confirm`, so
// a raw curl can't bypass the UI's confirmation dialog. On a confirm mismatch it returns 409 with the
// freshly computed removal impact (so the client can render the specific warning). On success the
// provider row AND its model-selection rows are deleted in one transaction; the encrypted key is gone
// from the vault (secure_delete zeroes the freed page), and — because ListProviders reads the DB on
// every request — the provider stops being routable immediately, with no cache to expire.
func (app *App) HandleProviderRemove(w http.ResponseWriter, r *http.Request) {
	principal := app.requireToken(w, r)
	if principal == nil {
		return
	}
	var body removeReq
	if err := decodeSetupBody(r, &body); err != nil {
		oaiError(w, 400, "Invalid JSON body", "invalid_request_error", "")
		return
	}
	name := strings.TrimSpace(body.Name)
	if getProvider(app.DB, name) == nil {
		oaiError(w, 404, `No provider named "`+name+`".`, "invalid_request_error", "provider_not_found")
		return
	}
	impact := app.removalImpact(name, ListProviders(app.DB))
	if strings.TrimSpace(body.Confirm) != name {
		sendJSON(w, 409, map[string]any{
			"error":  map[string]any{"message": `Type the provider name "` + name + `" to confirm removal.`, "type": "invalid_request_error", "code": "confirmation_required"},
			"impact": impact,
		})
		return
	}
	if _, err := state.Tx(app.DB, func(q state.Querier) (any, error) {
		if _, err := q.ExecContext(state.Ctx(), `DELETE FROM provider_models WHERE provider = ?`, name); err != nil {
			return nil, err
		}
		if _, err := q.ExecContext(state.Ctx(), `DELETE FROM providers WHERE name = ?`, name); err != nil {
			return nil, err
		}
		return nil, nil
	}); err != nil {
		oaiError(w, 500, "Failed to remove provider: "+err.Error(), "configuration_error", "")
		return
	}
	app.auditAs(principal, "provider.remove", name)
	sendJSON(w, 200, map[string]any{"removed": true, "name": name, "impact": impact})
}

type updateReq struct {
	Name    string `json:"name"`
	Enabled *bool  `json:"enabled"`
	Default *bool  `json:"default"`
}

// HandleProviderUpdate is POST /v1/providers/update — flip enabled and/or set-as-default. Re-enabling
// clears the circuit breaker (the same "give the fixed provider a clean try" rule as rotate). Only
// default:true sets the default (there is intentionally no way to leave the gateway with no default via
// this path — remove is the destructive action, and it has its own warnings).
func (app *App) HandleProviderUpdate(w http.ResponseWriter, r *http.Request) {
	principal := app.requireToken(w, r)
	if principal == nil {
		return
	}
	var body updateReq
	if err := decodeSetupBody(r, &body); err != nil {
		oaiError(w, 400, "Invalid JSON body", "invalid_request_error", "")
		return
	}
	name := strings.TrimSpace(body.Name)
	if getProvider(app.DB, name) == nil {
		oaiError(w, 404, `No provider named "`+name+`".`, "invalid_request_error", "provider_not_found")
		return
	}
	if body.Enabled != nil {
		v := 0
		action := "provider.disable"
		if *body.Enabled {
			v = 1
			action = "provider.enable"
		}
		if _, err := app.DB.ExecContext(state.Ctx(), `UPDATE providers SET enabled = ? WHERE name = ?`, v, name); err != nil {
			oaiError(w, 500, "Failed to update provider: "+err.Error(), "configuration_error", "")
			return
		}
		if *body.Enabled {
			MarkSuccess(name)
		}
		app.auditAs(principal, action, name)
	}
	if body.Default != nil && *body.Default {
		if _, err := app.DB.ExecContext(state.Ctx(), `UPDATE providers SET is_default = CASE WHEN name = ? THEN 1 ELSE 0 END`, name); err != nil {
			oaiError(w, 500, "Failed to set default: "+err.Error(), "configuration_error", "")
			return
		}
		app.auditAs(principal, "provider.default", name)
	}
	prov := getProvider(app.DB, name)
	sendJSON(w, 200, map[string]any{"provider": map[string]any{
		"name": prov.Name, "enabled": prov.Enabled, "is_default": prov.IsDefault,
	}})
}

type modelsReq struct {
	Name           string   `json:"name"`
	DisabledModels []string `json:"disabled_models"`
}

// HandleProviderModels is POST /v1/providers/models — set which of a provider's models are enabled,
// editable at any time post-setup (Part C's selection, now mutable from the dashboard). The body's
// disabled_models list is intersected with the provider's real manifest catalog (unknown ids are
// dropped, so a stale UI can't persist phantom models), then the provider's selection rows are replaced
// atomically. Absence of a row means enabled, so an empty list re-enables every model.
func (app *App) HandleProviderModels(w http.ResponseWriter, r *http.Request) {
	principal := app.requireToken(w, r)
	if principal == nil {
		return
	}
	var body modelsReq
	if err := decodeSetupBody(r, &body); err != nil {
		oaiError(w, 400, "Invalid JSON body", "invalid_request_error", "")
		return
	}
	name := strings.TrimSpace(body.Name)
	if getProvider(app.DB, name) == nil {
		oaiError(w, 404, `No provider named "`+name+`".`, "invalid_request_error", "provider_not_found")
		return
	}
	manifest := adapters.ModelsFor(name)
	known := map[string]bool{}
	for _, m := range manifest {
		known[m] = true
	}
	disabledSet := map[string]bool{}
	for _, m := range body.DisabledModels {
		if known[m] {
			disabledSet[m] = true
		}
	}
	if _, err := state.Tx(app.DB, func(q state.Querier) (any, error) {
		if _, err := q.ExecContext(state.Ctx(), `DELETE FROM provider_models WHERE provider = ?`, name); err != nil {
			return nil, err
		}
		for m := range disabledSet {
			if _, err := q.ExecContext(state.Ctx(), `INSERT INTO provider_models(provider,model,enabled) VALUES(?,?,0)`, name, m); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}); err != nil {
		oaiError(w, 500, "Failed to update model selection: "+err.Error(), "configuration_error", "")
		return
	}
	app.auditAs(principal, "provider.models", name)
	var enabledModels, disabledModels []any
	for _, m := range manifest {
		if disabledSet[m] {
			disabledModels = append(disabledModels, m)
		} else {
			enabledModels = append(enabledModels, m)
		}
	}
	sendJSON(w, 200, map[string]any{
		"name": name, "models": strSliceAny(manifest),
		"enabled_models": orEmpty(enabledModels), "disabled_models": orEmpty(disabledModels),
		"all_disabled": len(manifest) > 0 && len(disabledSet) == len(manifest),
	})
}

// ---- removal impact ----

// removalImpact computes the SPECIFIC consequences of removing a provider — enumerated worst-first —
// from real state: whether it's the only provider, whether it's the default, and which config-file
// routing rules (aliases, auto-routing quality ranks) point at it. The same function feeds both the
// dashboard's pre-removal warning (via the summary) and the server-side confirm gate, so the warning a
// user sees and the one the server enforces are identical. `providers` is passed in so callers reuse a
// single ListProviders result rather than re-querying per provider.
func (app *App) removalImpact(name string, providers []ProviderRow) map[string]any {
	var target *ProviderRow
	for i := range providers {
		if providers[i].Name == name {
			target = &providers[i]
		}
	}
	only := len(providers) == 1 && target != nil
	isDefault := target != nil && target.IsDefault

	var aliasRefs []string
	for alias, tgt := range app.Cfg.Aliases {
		if p, _, ok := splitTarget(tgt); ok && p == name {
			aliasRefs = append(aliasRefs, alias)
		}
	}
	sort.Strings(aliasRefs)
	var rankRefs []string
	for k := range app.Cfg.Routing.QualityRanks {
		if p, m, ok := splitTarget(k); ok && p == name {
			rankRefs = append(rankRefs, p+"/"+m)
		}
	}
	sort.Strings(rankRefs)

	var warnings []any
	add := func(level, msg string) { warnings = append(warnings, map[string]any{"level": level, "message": msg}) }
	if only {
		add("error", "This is your only configured provider. Removing it will leave the gateway unable to route any requests until you add a new one.")
	} else if isDefault {
		add("warn", "This is your default provider. Requests that don't name a specific model will fall back to another provider, or fail if none can serve them.")
	}
	if len(aliasRefs) > 0 {
		add("warn", fmt.Sprintf("%d model alias%s point at this provider (%s). Requests using %s will fail until you update your config.",
			len(aliasRefs), plural(len(aliasRefs), "", "es"), strings.Join(aliasRefs, ", "), plural(len(aliasRefs), "it", "them")))
	}
	if len(rankRefs) > 0 {
		add("warn", fmt.Sprintf("This provider appears in your auto-routing quality ranks (%s). Auto-routed requests will lose these candidates.", strings.Join(rankRefs, ", ")))
	}
	return map[string]any{
		"provider": name, "is_only": only, "is_default": isDefault,
		"alias_refs": strSliceAny(aliasRefs), "quality_rank_refs": strSliceAny(rankRefs),
		"warnings": orEmpty(warnings),
	}
}

// ---- small helpers ----

func strSliceAny(ss []string) []any {
	out := make([]any, 0, len(ss))
	for _, s := range ss {
		out = append(out, s)
	}
	return out
}

// sortedDisabled returns a provider's disabled models as a stable, sorted slice for display.
func sortedDisabled(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for m := range set {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
