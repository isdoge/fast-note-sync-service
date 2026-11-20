package oauth

import (
	"errors"
	"testing"
)

func TestMapOAuthScopesToFNS_scopesGenerateMCP3DRBACScope(t *testing.T) {
	got, err := MapOAuthScopesToFNS("cursor", []string{"notes:read", "notes:write", "files:read", "files:write"})
	if err != nil {
		t.Fatalf("MapOAuthScopesToFNS() error = %v", err)
	}

	want := "p:mcp c:cursor f:note_r,note_w,file_r,file_w"
	if got != want {
		t.Fatalf("MapOAuthScopesToFNS() = %q, want %q", got, want)
	}
}

// vaults:read 只映射 vault_r，不再顺带授予笔记读权限（B5 修正）。
func TestMapOAuthScopesToFNS_vaultReadMapsToVaultRead(t *testing.T) {
	got, err := MapOAuthScopesToFNS("claude", []string{"vaults:read"})
	if err != nil {
		t.Fatalf("MapOAuthScopesToFNS() error = %v", err)
	}

	want := "p:mcp c:claude f:vault_r"
	if got != want {
		t.Fatalf("MapOAuthScopesToFNS() = %q, want %q", got, want)
	}
}

// 新增的库写与清空回收站 scope 映射。
func TestMapOAuthScopesToFNS_vaultWriteAndPurgeScopes(t *testing.T) {
	got, err := MapOAuthScopesToFNS("cursor", []string{"vaults:write", "notes:purge", "files:purge"})
	if err != nil {
		t.Fatalf("MapOAuthScopesToFNS() error = %v", err)
	}

	want := "p:mcp c:cursor f:vault_w,note_purge,file_purge"
	if got != want {
		t.Fatalf("MapOAuthScopesToFNS() = %q, want %q", got, want)
	}
}

func TestMapOAuthScopesToFNS_unknownScopeIsInsufficientScope(t *testing.T) {
	_, err := MapOAuthScopesToFNS("cursor", []string{"notes:delete"})
	if !errors.Is(err, ErrInsufficientScope) {
		t.Fatalf("MapOAuthScopesToFNS() error = %v, want ErrInsufficientScope", err)
	}
}
