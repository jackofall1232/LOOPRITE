package gateway

import "testing"

// The auth matcher must stay narrow: a missing git binary is an environment problem, and
// decorating it with a GitHub-permissions hint points the user at exactly the wrong repair.
func TestLooksLikeAuthFailure(t *testing.T) {
	auth := []string{
		"fatal: Authentication failed for 'https://github.com/x/y.git/'",
		"fatal: could not read Username for 'https://github.com': terminal prompts disabled",
		"git@github.com: Permission denied (publickey).",
		"remote: Repository not found.",
		"fatal: repository 'https://github.com/x/y.git/' not found",
		"The requested URL returned error: 403 Forbidden",
	}
	for _, s := range auth {
		if !looksLikeAuthFailure(s) {
			t.Errorf("must classify as auth-shaped: %q", s)
		}
	}
	notAuth := []string{
		`exec: "git": executable file not found in $PATH`,
		"fatal: unable to access 'https://github.example/x.git/': Could not resolve host: github.example",
		"error: RPC failed; curl 18 transfer closed with outstanding read data remaining",
	}
	for _, s := range notAuth {
		if looksLikeAuthFailure(s) {
			t.Errorf("must NOT classify as auth-shaped: %q", s)
		}
	}
}
