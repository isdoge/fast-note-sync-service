package app

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
)

// resetSigningSeed clears the global signing-seed override after each test.
func resetSigningSeed(t *testing.T) {
	t.Cleanup(func() {
		util.SetTokenSigningSeed("")
	})
}

// TestTokenManager_SigningSeedOverrideKeepsOldTokensValid verifies tokens signed
// with the machine-ID-derived key (legacy) still parse after a stable signing
// seed is enabled, and new tokens are signed with the new seed.
func TestTokenManager_SigningSeedOverrideKeepsOldTokensValid(t *testing.T) {
	resetSigningSeed(t)

	cfg := TokenConfig{
		SecretKey:     "user-secret",
		ShareTokenKey: "share-secret",
		ShareExpiry:   1 * time.Hour,
		Expiry:        24 * time.Hour,
		Issuer:        "test-issuer",
	}
	tm := NewTokenManager(cfg)

	// 1. Sign a token while no override is set (uses machine-ID seed, i.e. the legacy key)
	legacyToken, err := tm.Generate(1, "nick", "", 2, "nonce-a")
	if err != nil {
		t.Fatalf("Generate legacy token failed: %v", err)
	}

	// 2. Enable stable signing seed (simulating upgrade / migration)
	util.SetTokenSigningSeed("stable-seed-value")

	if _, err := tm.Parse(legacyToken); err != nil {
		t.Fatalf("legacy token should still parse via fallback key, got: %v", err)
	}

	// 3. New tokens must be signed with the current seed, not the legacy key
	newToken, err := tm.Generate(1, "nick", "", 3, "nonce-b")
	if err != nil {
		t.Fatalf("Generate new token failed: %v", err)
	}

	expectedKey := []byte(cfg.SecretKey + "_stable-seed-value")
	claims := &UserEntity{}
	parsed, err := jwt.ParseWithClaims(newToken, claims, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return expectedKey, nil
	})
	if err != nil || !parsed.Valid {
		t.Fatalf("new token must be signed with the current seed key: err=%v", err)
	}

	// 4. A token signed with a wrong seed must be rejected
	wrongClaims := &UserEntity{
		UID: 1, Nickname: "nick", TokenID: 4, Nonce: "nonce-c",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			Issuer:    cfg.Issuer, Subject: "user-token",
		},
	}
	wrongToken, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, wrongClaims).SignedString(
		[]byte(cfg.SecretKey + "_totally-wrong-seed"))
	if _, err := tm.Parse(wrongToken); err == nil {
		t.Fatalf("token signed with unknown seed must be rejected")
	}

	// 5. Validate/ParseTokenWithKey follow the same fallback rules
	if err := tm.Validate(legacyToken); err != nil {
		t.Fatalf("Validate(legacyToken) should pass via fallback: %v", err)
	}
	if _, err := ParseTokenWithKey(legacyToken, cfg.SecretKey); err != nil {
		t.Fatalf("ParseTokenWithKey(legacyToken) should pass via fallback: %v", err)
	}
	if _, err := ParseTokenWithKey(newToken, cfg.SecretKey); err != nil {
		t.Fatalf("ParseTokenWithKey(newToken) should pass: %v", err)
	}
}

// TestTokenManager_ShareTokenSigningSeedFallback verifies share tokens signed
// with the machine-ID-derived key still validate after enabling a stable seed,
// and new share tokens use the current seed.
func TestTokenManager_ShareTokenSigningSeedFallback(t *testing.T) {
	resetSigningSeed(t)

	cfg := TokenConfig{
		SecretKey:     "user-secret",
		ShareTokenKey: "share-secret",
		ShareExpiry:   1 * time.Hour,
		Issuer:        "test-issuer",
	}
	tm := NewTokenManager(cfg)
	resources := map[string][]string{"note": {"n1"}}

	// Legacy share token (new 40-byte format) signed with machine-ID seed
	legacyShare, err := tm.ShareGenerate(7, 42, resources)
	if err != nil {
		t.Fatalf("ShareGenerate legacy failed: %v", err)
	}

	util.SetTokenSigningSeed("stable-seed-value")

	// Fallback: legacy share token must still parse
	entity, err := tm.ShareParse(legacyShare)
	if err != nil {
		t.Fatalf("legacy share token should parse via fallback key, got: %v", err)
	}
	if entity.SID != 7 || entity.UID != 42 {
		t.Fatalf("unexpected share entity: %+v", entity)
	}

	// New share token must verify against the current seed
	newShare, err := tm.ShareGenerate(8, 43, resources)
	if err != nil {
		t.Fatalf("ShareGenerate failed: %v", err)
	}
	data, err := base64.RawURLEncoding.DecodeString(newShare)
	if err != nil || len(data) != 40 {
		t.Fatalf("unexpected share token encoding: err=%v len=%d", err, len(data))
	}
	payload, tag := data[:24], data[24:]
	key := sha256.Sum256([]byte(cfg.ShareTokenKey + "_stable-seed-value"))
	mac := hmacSum(key[:], payload)
	if !hmac.Equal(tag, mac) {
		t.Fatalf("new share token must be signed with the current seed")
	}

	// Tampered share token must be rejected
	data[0] ^= 0xFF
	tampered := base64.RawURLEncoding.EncodeToString(data)
	if _, err := tm.ShareParse(tampered); err == nil {
		t.Fatalf("tampered share token must be rejected")
	}
}

func hmacSum(key, payload []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	return mac.Sum(nil)[:16]
}

func hmacEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := range a {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

// TestResolveTokenSigningSeed_* covers the seed-file lifecycle: those tests
// live in internal/app because they need AppConfig. Here we only pin util API.
func TestUtilSigningSeedOverride(t *testing.T) {
	resetSigningSeed(t)

	if util.GetTokenSigningSeed() == "" {
		t.Fatalf("GetTokenSigningSeed should fall back to machine ID, got empty")
	}
	util.SetTokenSigningSeed("my-seed")
	if got := util.GetTokenSigningSeed(); got != "my-seed" {
		t.Fatalf("GetTokenSigningSeed override failed: %q", got)
	}
}
