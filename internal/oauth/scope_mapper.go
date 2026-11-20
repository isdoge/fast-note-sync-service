package oauth

import (
	"fmt"
	"strings"
)

var oauthScopeFunctions = map[string]string{
	"notes:read":  "note_r",
	"notes:write": "note_w",
	"notes:purge": "note_purge",
	"files:read":  "file_r",
	"files:write": "file_w",
	"files:purge": "file_purge",
	"vaults:read": "vault_r",
	"vaults:write": "vault_w",
}

func MapOAuthScopesToFNS(client string, scopes []string) (string, error) {
	client = strings.TrimSpace(client)
	if client == "" {
		client = "*"
	}

	functions := make([]string, 0, len(scopes))
	seen := make(map[string]bool, len(scopes))
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		function, ok := oauthScopeFunctions[scope]
		if !ok {
			continue
		}
		if seen[function] {
			continue
		}
		seen[function] = true
		functions = append(functions, function)
	}

	if len(functions) == 0 {
		return "", fmt.Errorf("%w: no usable oauth scopes", ErrInsufficientScope)
	}

	return "p:mcp c:" + client + " f:" + strings.Join(functions, ","), nil
}
