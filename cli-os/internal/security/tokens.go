// Opaque gateway tokens. Format: l00p_<id>_<secret>. We store the id (public) and a sha-256 of the
// secret half (never the secret). Lookup is by id; the secret is compared in CONSTANT TIME
// (util.TimingSafeEqual over crypto/subtle) — a leaked token is revocable without touching provider
// keys. Ported from tokens.js.
package security

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackofall1232/l00prite/cli-os/internal/state"
	"github.com/jackofall1232/l00prite/cli-os/internal/util"
)

// Principal is what a valid token resolves to.
type Principal struct {
	TokenID  string
	Project  string
	Repo     string // "" when the token is not repo-scoped
	Scopes   []string
	ScopeSet map[string]bool
	Legacy   bool
	Role     string
}

// TokenRow is a row for `token list`.
type TokenRow struct {
	ID        string
	Project   string
	Repo      string
	Revoked   bool
	ExpiresAt string
	CreatedAt string
	Scopes    []string
	Legacy    bool
	Role      string
}

var tokenRE = regexp.MustCompile(`^l00p_([A-Za-z0-9]+)_(.+)$`)

// MintToken creates a token, storing only the sha-256 of its secret half. Returns (id, fullToken).
func MintToken(q state.Querier, project string, repo *string, expiresDays *int) (string, string, error) {
	return mintToken(q, project, repo, expiresDays, LegacyAdminScopes, "legacy_admin", true)
}

func MintTokenWithScopes(q state.Querier, project string, repo *string, expiresDays *int, scopes []string, legacy bool) (string, string, error) {
	return mintToken(q, project, repo, expiresDays, scopes, RoleCustom, legacy)
}

func MintTokenWithRole(q state.Querier, project string, repo *string, expiresDays *int, role string) (string, string, error) {
	scopes, ok := ScopesForRole(role)
	if !ok {
		return "", "", errors.New("unknown token role")
	}
	return mintToken(q, project, repo, expiresDays, scopes, strings.ToLower(strings.TrimSpace(role)), false)
}

func mintToken(q state.Querier, project string, repo *string, expiresDays *int, scopes []string, role string, legacy bool) (string, string, error) {
	encoded, ok := EncodeScopes(scopes)
	if !ok {
		return "", "", errors.New("token scopes must contain at least one known scope")
	}
	id := strings.TrimPrefix(util.RID("tok"), "tok_")
	secBytes := make([]byte, 24)
	if _, err := rand.Read(secBytes); err != nil {
		return "", "", err
	}
	secret := base64.RawURLEncoding.EncodeToString(secBytes)
	hash := util.SHA256Hex(secret)
	var expiresAt any
	if expiresDays != nil && *expiresDays != 0 { // 0 (like nil) means "never expires", matching Node's falsy check
		expiresAt = util.ISOFromTime(time.Now().Add(time.Duration(*expiresDays) * 24 * time.Hour))
	}
	var repoVal any
	if repo != nil {
		repoVal = *repo
	}
	if _, err := q.ExecContext(state.Ctx(),
		`INSERT INTO tokens(id,hash,project,repo,revoked,expires_at,created_at,scopes,legacy,role) VALUES(?,?,?,?,0,?,?,?,?,?)`,
		id, hash, project, repoVal, expiresAt, util.NowISO(), encoded, boolInt(legacy), role); err != nil {
		return "", "", err
	}
	return id, "l00p_" + id + "_" + secret, nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// VerifyToken returns the principal for a valid, non-revoked, non-expired token, else nil.
func VerifyToken(q state.Querier, raw string) *Principal {
	m := tokenRE.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return nil
	}
	id, secret := m[1], m[2]
	var (
		rowID, project, hash, scopesRaw, role string
		repo                                  sql.NullString
		expiresAt                             sql.NullString
		revoked                               int
		legacy                                int
	)
	err := q.QueryRowContext(state.Ctx(),
		`SELECT id, hash, project, repo, revoked, expires_at, scopes, legacy, role FROM tokens WHERE id = ?`, id).
		Scan(&rowID, &hash, &project, &repo, &revoked, &expiresAt, &scopesRaw, &legacy, &role)
	if err != nil {
		return nil
	}
	if revoked != 0 {
		return nil
	}
	if expiresAt.Valid && expiresAt.String != "" {
		t, perr := parseISO(expiresAt.String)
		if perr != nil || !time.Now().Before(t) {
			return nil
		}
	}
	if !util.TimingSafeEqual(util.SHA256Hex(secret), hash) {
		return nil
	}
	scopes, ok := ParseScopes(scopesRaw)
	if !ok {
		return nil
	}
	set := map[string]bool{}
	for _, s := range scopes {
		set[s] = true
	}
	return &Principal{TokenID: rowID, Project: project, Repo: repo.String, Scopes: scopes, ScopeSet: set, Legacy: legacy != 0, Role: role}
}

// RevokeToken marks a token revoked; returns true if a row changed.
func RevokeToken(q state.Querier, id string) bool {
	res, err := q.ExecContext(state.Ctx(), `UPDATE tokens SET revoked = 1 WHERE id = ?`, id)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

// ListTokens returns all tokens newest first.
func ListTokens(q state.Querier) []TokenRow {
	rows, err := q.QueryContext(state.Ctx(),
		`SELECT id,project,repo,revoked,expires_at,created_at,scopes,legacy,role FROM tokens ORDER BY created_at DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []TokenRow
	for rows.Next() {
		var (
			t               TokenRow
			repo, expiresAt sql.NullString
			revoked, legacy int
			scopesRaw       string
		)
		if err := rows.Scan(&t.ID, &t.Project, &repo, &revoked, &expiresAt, &t.CreatedAt, &scopesRaw, &legacy, &t.Role); err != nil {
			continue
		}
		t.Repo = repo.String
		t.Revoked = revoked != 0
		t.ExpiresAt = expiresAt.String
		t.Scopes, _ = ParseScopes(scopesRaw)
		t.Legacy = legacy != 0
		out = append(out, t)
	}
	return out
}

func parseISO(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02T15:04:05.000Z07:00", s)
}
