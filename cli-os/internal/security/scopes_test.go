package security

import "testing"

func TestNormalizeScopesRejectsUnknownAndSorts(t *testing.T) {
	got, ok := NormalizeScopes([]string{ScopeRepoRead, " CHAT:INVOKE ", ScopeRepoRead})
	if !ok || len(got) != 2 || got[0] != ScopeChatInvoke || got[1] != ScopeRepoRead {
		t.Fatalf("unexpected normalized scopes: %#v ok=%v", got, ok)
	}
	if _, ok := NormalizeScopes([]string{"root:everything"}); ok {
		t.Fatal("unknown scope accepted")
	}
}

func TestPrincipalAdminImpliesEveryScope(t *testing.T) {
	p := &Principal{ScopeSet: map[string]bool{ScopeAdmin: true}}
	if !p.HasScope(ScopeCredentialManage) || !p.HasScope(ScopeChatInvoke) {
		t.Fatal("admin did not imply known scopes")
	}
}

func TestNamedRolesMapToBoundedScopes(t *testing.T) {
	chat, ok := ScopesForRole(RoleChat)
	if !ok || len(chat) != 2 {
		t.Fatalf("chat role: %#v", chat)
	}
	for _, s := range chat {
		if s == ScopeAdmin || s == ScopeCredentialManage {
			t.Fatalf("chat role gained %s", s)
		}
	}
	if _, ok := ScopesForRole("owner-of-everything"); ok {
		t.Fatal("unknown role accepted")
	}
}
