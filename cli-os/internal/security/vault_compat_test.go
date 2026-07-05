package security

import "testing"

// TestVaultDecryptsNodeBlob is a cross-compatibility regression test: the fixed blob below was
// produced by the ORIGINAL Node vault.js algorithm (aes-256-gcm, v1.<iv>.<tag>.<ct>, standard
// base64, 12-byte IV, 16-byte tag) under the fixed master key. The Go vault must decrypt it
// unchanged, proving existing Node-written vault files are not orphaned by the port. The plaintext
// includes non-ASCII bytes to exercise UTF-8 round-tripping.
func TestVaultDecryptsNodeBlob(t *testing.T) {
	const nodeKey = "0YNouHIHvQxqpmCbZNoAXS/+tSeXi4M3nvFOpPMhQNo="
	const nodeBlob = "v1.5yoxCf07n0v9wgaY.pM/dz4z3lOY8fjYmtv5k4g==.OUUF4JDHp/4DMK5a63kFZ1gbrn0H1Nqzn2K+JM4D"
	const want = "sk-ant-CROSSCOMPAT-secret-éè"

	t.Setenv("LOOPRITE_MASTER_KEY", nodeKey)
	got, err := DecryptSecret("/does-not-exist", nodeBlob) // env key wins; path unused
	if err != nil {
		t.Fatalf("decrypt of a Node-written vault blob failed: %v", err)
	}
	if got != want {
		t.Fatalf("cross-compat mismatch: got %q want %q", got, want)
	}
}
