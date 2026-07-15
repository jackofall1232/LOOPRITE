package security

import (
	"sort"
	"strings"
)

const (
	ScopeChatInvoke       = "chat:invoke"
	ScopeRepoRead         = "repo:read"
	ScopeRunCreate        = "run:create"
	ScopeRunApprove       = "run:approve"
	ScopeProviderManage   = "provider:manage"
	ScopeCredentialManage = "credential:manage"
	ScopeBudgetManage     = "budget:manage"
	ScopeAuditRead        = "audit:read"
	ScopeAdmin            = "admin"
)

var knownScopes = map[string]bool{
	ScopeChatInvoke: true, ScopeRepoRead: true, ScopeRunCreate: true, ScopeRunApprove: true,
	ScopeProviderManage: true, ScopeCredentialManage: true, ScopeBudgetManage: true,
	ScopeAuditRead: true, ScopeAdmin: true,
}

var LegacyAdminScopes = []string{ScopeAdmin}

const (
	RoleAdmin    = "admin"
	RoleOperator = "operator"
	RoleChat     = "chat"
	RoleCustom   = "custom"
)

var roleScopes = map[string][]string{
	RoleAdmin: {ScopeAdmin},
	RoleOperator: {ScopeAuditRead, ScopeBudgetManage, ScopeChatInvoke, ScopeCredentialManage,
		ScopeProviderManage, ScopeRepoRead, ScopeRunApprove, ScopeRunCreate},
	RoleChat: {ScopeChatInvoke, ScopeRepoRead},
}

func ScopesForRole(role string) ([]string, bool) {
	s, ok := roleScopes[strings.ToLower(strings.TrimSpace(role))]
	return append([]string(nil), s...), ok
}

func NormalizeScopes(in []string) ([]string, bool) {
	set := map[string]bool{}
	for _, raw := range in {
		s := strings.TrimSpace(strings.ToLower(raw))
		if s == "" {
			continue
		}
		if !knownScopes[s] {
			return nil, false
		}
		set[s] = true
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out, true
}

func ParseScopes(raw string) ([]string, bool) {
	if strings.TrimSpace(raw) == "" || raw == "legacy_admin" {
		return append([]string(nil), LegacyAdminScopes...), true
	}
	return NormalizeScopes(strings.Split(raw, ","))
}

func EncodeScopes(scopes []string) (string, bool) {
	n, ok := NormalizeScopes(scopes)
	if !ok || len(n) == 0 {
		return "", false
	}
	return strings.Join(n, ","), true
}

func (p *Principal) HasScope(scope string) bool {
	if p == nil {
		return false
	}
	if p.ScopeSet[ScopeAdmin] {
		return true
	}
	return p.ScopeSet[scope]
}
