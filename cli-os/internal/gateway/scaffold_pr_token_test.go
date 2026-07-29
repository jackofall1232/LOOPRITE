package gateway

// Coverage for the token-first scaffold push/PR path (openScaffoldPR + probePushPRCapability with a
// connected GitHub credential) — the Android/no-gh transport. Uses a fake gitx.Client so the push
// is observed without a real remote, and stubGitHub (github_test.go) for the REST calls.

import (
	"context"
	"testing"

	"github.com/jackofall1232/l00prite/cli-os/internal/gitx"
)

// fakeGitClient records Push calls and answers RemoteURL; every other Client method is an inert
// no-op (openScaffoldPR/probePushPRCapability only touch RemoteURL + Push).
type fakeGitClient struct {
	kind      string
	remoteURL string
	pushed    []string // "<remote>/<branch>" per Push
	pushErr   error
}

func (f *fakeGitClient) Kind() string { return f.kind }
func (f *fakeGitClient) RemoteURL(repo, remote string) (string, error) {
	return f.remoteURL, nil
}
func (f *fakeGitClient) Push(ctx context.Context, repo, remote, branch string, auth *gitx.PushAuth) error {
	f.pushed = append(f.pushed, remote+"/"+branch)
	return f.pushErr
}
func (f *fakeGitClient) Clone(ctx context.Context, url, dest string, depth int, auth *gitx.PushAuth) error {
	return nil
}
func (f *fakeGitClient) RevParseHead(repo string) (string, error)                     { return "", nil }
func (f *fakeGitClient) CurrentBranch(repo string) (string, error)                    { return "", nil }
func (f *fakeGitClient) StatusPorcelain(repo string) (string, error)                  { return "", nil }
func (f *fakeGitClient) CheckoutNewBranch(repo, name string) error                    { return nil }
func (f *fakeGitClient) AddAll(repo string) error                                     { return nil }
func (f *fakeGitClient) AddPaths(repo string, paths []string) error                   { return nil }
func (f *fakeGitClient) Commit(repo, message string) (string, error)                  { return "", nil }
func (f *fakeGitClient) DiffHead(repo string) (string, error)                         { return "", nil }
func (f *fakeGitClient) Log(repo string, limit int) (string, error)                   { return "", nil }
func (f *fakeGitClient) Show(repo, ref string) (string, error)                        { return "", nil }
func (f *fakeGitClient) Raw(ctx context.Context, repo string, args ...string) (string, error) {
	return "", nil
}

// TestScaffoldTokenPathPushesAndOpensPR: with a connected github.com credential, the probe reports
// capable (on BOTH backends, incl. gogit — the Android case) and openScaffoldPR pushes the run
// branch via gitx.Push and opens the PR over the REST API, returning its URL — no gh binary needed.
func TestScaffoldTokenPathPushesAndOpensPR(t *testing.T) {
	stubGitHub(t)
	auth := &gitx.PushAuth{Username: "x-access-token", Token: "goodtok", Host: "github.com"}
	branch := "l00prite/add-protocol-xyz"

	for _, kind := range []string{"exec", "gogit"} {
		t.Run(kind, func(t *testing.T) {
			fake := &fakeGitClient{kind: kind, remoteURL: "https://github.com/octocat/hello.git"}

			ok, gap, rawErr := probePushPRCapability(context.Background(), fake, "/repo", branch, auth)
			if !ok || gap != "" || rawErr != nil {
				t.Fatalf("token-path probe should be capable, got ok=%v gap=%q err=%v", ok, gap, rawErr)
			}

			pushed, prURL, err := openScaffoldPR(context.Background(), fake, "/repo", branch, scaffoldPRTitle, scaffoldPRBody(branch), auth)
			if err != nil {
				t.Fatalf("openScaffoldPR: %v", err)
			}
			if !pushed {
				t.Fatal("expected pushed=true")
			}
			if prURL != "https://github.com/octocat/hello/pull/7" {
				t.Fatalf("prURL=%q", prURL)
			}
			if len(fake.pushed) != 1 || fake.pushed[0] != "origin/"+branch {
				t.Fatalf("push not recorded as origin/<branch>: %v", fake.pushed)
			}
		})
	}
}

// TestTokenUsableForOrigin pins the selection logic: the token transport is used ONLY for an https
// github.com origin (incl. an explicit port); an ssh github remote or any non-github remote is not
// token-usable, so the caller nils the auth and falls back to the ambient git/gh path uniformly.
func TestTokenUsableForOrigin(t *testing.T) {
	auth := &gitx.PushAuth{Username: "x-access-token", Token: "goodtok", Host: "github.com"}
	cases := []struct {
		origin string
		want   bool
	}{
		{"https://github.com/o/r.git", true},
		{"https://github.com:443/o/r.git", true}, // explicit port still usable
		{"git@github.com:o/r.git", false},        // ssh github -> ambient
		{"ssh://git@github.com/o/r.git", false},  // ssh github -> ambient
		{"https://gitlab.com/o/r.git", false},    // non-github -> ambient (the regression fix)
		{"http://github.com/o/r.git", false},     // cleartext -> not usable
	}
	for _, c := range cases {
		fake := &fakeGitClient{kind: "exec", remoteURL: c.origin}
		if got := tokenUsableForOrigin(fake, "/repo", auth); got != c.want {
			t.Errorf("tokenUsableForOrigin(%q) = %v, want %v", c.origin, got, c.want)
		}
	}
	// A nil auth is never usable.
	if tokenUsableForOrigin(&fakeGitClient{kind: "exec", remoteURL: "https://github.com/o/r.git"}, "/repo", nil) {
		t.Fatal("nil auth must never be token-usable")
	}
}

// TestScaffoldTokenPathRejectsNonGitHubOrigin: a connected token must NOT be used for a non-github
// origin — openScaffoldPR errors out (the caller then reports the honest gap) rather than pushing.
func TestScaffoldTokenPathRejectsNonGitHubOrigin(t *testing.T) {
	stubGitHub(t)
	auth := &gitx.PushAuth{Username: "x-access-token", Token: "goodtok", Host: "github.com"}
	fake := &fakeGitClient{kind: "exec", remoteURL: "https://gitlab.com/o/r.git"}
	_, _, err := openScaffoldPR(context.Background(), fake, "/repo", "b", scaffoldPRTitle, scaffoldPRBody("b"), auth)
	if err == nil {
		t.Fatal("openScaffoldPR must refuse a non-github origin on the token path")
	}
	if len(fake.pushed) != 0 {
		t.Fatalf("must not push to a non-github origin with the token: %v", fake.pushed)
	}
}
