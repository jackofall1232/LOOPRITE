package security

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"

	"github.com/jackofall1232/l00prite/cli-os/internal/config"
	"github.com/jackofall1232/l00prite/cli-os/internal/state"
)

func setupHome(t *testing.T) config.Config {
	t.Helper()
	t.Setenv("LOOPRITE_HOME", t.TempDir())
	t.Setenv("LOOPRITE_MASTER_KEY", "") // treated as unset
	cfg := config.Load()
	if err := config.EnsureHome(cfg); err != nil {
		t.Fatalf("ensure home: %v", err)
	}
	return cfg
}

func TestVaultRoundtripHidesPlaintext(t *testing.T) {
	cfg := setupHome(t)
	if err := EnsureMasterKey(cfg.MasterKeyPath); err != nil {
		t.Fatalf("master key: %v", err)
	}
	secret := "sk-super-secret-123"
	blob, err := EncryptSecret(cfg.MasterKeyPath, secret)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if strings.Contains(blob, secret) {
		t.Fatalf("ciphertext must not contain plaintext: %s", blob)
	}
	got, err := DecryptSecret(cfg.MasterKeyPath, blob)
	if err != nil || got != secret {
		t.Fatalf("decrypt roundtrip failed: got %q err %v", got, err)
	}
	info, err := os.Stat(cfg.MasterKeyPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("master key must be 0600, got %o", info.Mode().Perm())
	}
}

func TestTokensMintVerifyRevoke(t *testing.T) {
	cfg := setupHome(t)
	db, err := state.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	repo := "r1"
	id, token, err := MintToken(db, "demo", &repo, nil)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	p := VerifyToken(db, token)
	if p == nil || p.Project != "demo" || p.Repo != "r1" {
		t.Fatalf("verify: %+v", p)
	}
	if VerifyToken(db, "l00p_"+id+"_wrongsecret") != nil {
		t.Fatalf("wrong secret must fail")
	}
	if VerifyToken(db, "garbage") != nil {
		t.Fatalf("garbage must fail")
	}
	RevokeToken(db, id)
	if VerifyToken(db, token) != nil {
		t.Fatalf("revoked token must fail")
	}
}

func TestExpiresZeroNeverExpires(t *testing.T) {
	cfg := setupHome(t)
	db, err := state.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	zero := 0
	_, token, err := MintToken(db, "demo", nil, &zero)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if VerifyToken(db, token) == nil {
		t.Fatalf("expiresDays=0 must mean never-expires; token should still verify")
	}
}

func TestVerifyTokenRejectsMalformedExpiry(t *testing.T) {
	cfg := setupHome(t)
	db, err := state.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	id, token, err := MintTokenWithRole(db, "demo", nil, nil, RoleAdmin)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := db.Exec(`UPDATE tokens SET expires_at='not-a-timestamp' WHERE id=?`, id); err != nil {
		t.Fatalf("corrupt expiry: %v", err)
	}
	if VerifyToken(db, token) != nil {
		t.Fatal("token with malformed expiry must fail closed")
	}
}

func TestVaultAcceptsBase64URLKey(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i * 7)
	}
	t.Setenv("LOOPRITE_HOME", t.TempDir())
	t.Setenv("LOOPRITE_MASTER_KEY", base64.RawURLEncoding.EncodeToString(key)) // unpadded base64url
	blob, err := EncryptSecret("/no-such-path", "sk-x")
	if err != nil {
		t.Fatalf("encrypt with a base64url env key must work (Node accepts it): %v", err)
	}
	got, err := DecryptSecret("/no-such-path", blob)
	if err != nil || got != "sk-x" {
		t.Fatalf("roundtrip with base64url key failed: %v %q", err, got)
	}
}
