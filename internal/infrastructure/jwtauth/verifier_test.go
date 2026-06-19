package jwtauth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestParseAuthScheme(t *testing.T) {
	if ParseAuthScheme("basic") != AuthSchemeBasic {
		t.Error("expected AuthSchemeBasic")
	}
	if ParseAuthScheme("BEARER") != AuthSchemeBearer {
		t.Error("expected AuthSchemeBearer")
	}
	if ParseAuthScheme("other") != AuthSchemeBoth {
		t.Error("expected AuthSchemeBoth")
	}
}

func TestParseAlgorithm(t *testing.T) {
	alg, err := ParseAlgorithm("hs256")
	if err != nil || alg != AlgorithmHS256 {
		t.Errorf("failed to parse hs256: %v", err)
	}
	alg, err = ParseAlgorithm("RS256")
	if err != nil || alg != AlgorithmRS256 {
		t.Errorf("failed to parse rs256: %v", err)
	}
	alg, err = ParseAlgorithm("es256")
	if err != nil || alg != AlgorithmES256 {
		t.Errorf("failed to parse es256: %v", err)
	}
	_, err = ParseAlgorithm("unknown")
	if err == nil {
		t.Error("expected error for unknown algorithm")
	}
}

func TestClaims_HasScope(t *testing.T) {
	claims := &Claims{
		Scope: "read write admin",
	}
	if !claims.HasScope("read") {
		t.Error("expected true for 'read'")
	}
	if !claims.HasScope("admin") {
		t.Error("expected true for 'admin'")
	}
	if claims.HasScope("delete") {
		t.Error("expected false for 'delete'")
	}
}

func TestNewVerifier_Validation(t *testing.T) {
	// Missing algorithms
	_, err := NewVerifier(Config{Algorithms: []Algorithm{}})
	if err == nil {
		t.Error("expected error for empty algorithms")
	}

	// Missing issuer
	_, err = NewVerifier(Config{Algorithms: []Algorithm{AlgorithmHS256}, Issuer: ""})
	if err == nil {
		t.Error("expected error for empty issuer")
	}

	// Missing audience
	_, err = NewVerifier(Config{Algorithms: []Algorithm{AlgorithmHS256}, Issuer: "iss", Audience: ""})
	if err == nil {
		t.Error("expected error for empty audience")
	}

	// HS256 enabled but missing secret
	_, err = NewVerifier(Config{Algorithms: []Algorithm{AlgorithmHS256}, Issuer: "iss", Audience: "aud"})
	if err == nil {
		t.Error("expected error for missing HS256 secret")
	}

	// RS256 enabled but missing key
	_, err = NewVerifier(Config{Algorithms: []Algorithm{AlgorithmRS256}, Issuer: "iss", Audience: "aud"})
	if err == nil {
		t.Error("expected error for missing RS256 public key")
	}

	// ES256 enabled but missing key
	_, err = NewVerifier(Config{Algorithms: []Algorithm{AlgorithmES256}, Issuer: "iss", Audience: "aud"})
	if err == nil {
		t.Error("expected error for missing ES256 public key")
	}
}

func TestVerifier_HS256(t *testing.T) {
	secret := "my-secret-key-12345"
	cfg := Config{
		Algorithms:  []Algorithm{AlgorithmHS256},
		Issuer:      "test-issuer",
		Audience:    "test-audience",
		HS256Secret: secret,
	}

	v, err := NewVerifier(cfg)
	if err != nil {
		t.Fatalf("failed to create verifier: %v", err)
	}

	// 1. Success verification
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user-123",
			Issuer:    "test-issuer",
			Audience:  jwt.ClaimStrings{"test-audience"},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
		Scope: "read",
	})
	tokenStr, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("failed to sign token: %v", err)
	}

	claims, err := v.Verify(tokenStr)
	if err != nil {
		t.Fatalf("expected token to verify successfully, got: %v", err)
	}
	if claims.Subject != "user-123" {
		t.Errorf("expected subject user-123, got %s", claims.Subject)
	}

	// 2. Expired token
	expiredToken := jwt.NewWithClaims(jwt.SigningMethodHS256, &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user-123",
			Issuer:    "test-issuer",
			Audience:  jwt.ClaimStrings{"test-audience"},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
		},
	})
	expiredStr, _ := expiredToken.SignedString([]byte(secret))
	_, err = v.Verify(expiredStr)
	if err == nil {
		t.Error("expected validation to fail for expired token")
	}

	// 3. Wrong signature
	wrongStr, _ := expiredToken.SignedString([]byte("wrong-secret"))
	_, err = v.Verify(wrongStr)
	if err == nil {
		t.Error("expected validation to fail for wrong signature")
	}

	// 4. Missing subject claim
	noSubToken := jwt.NewWithClaims(jwt.SigningMethodHS256, &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "test-issuer",
			Audience:  jwt.ClaimStrings{"test-audience"},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	})
	noSubStr, _ := noSubToken.SignedString([]byte(secret))
	_, err = v.Verify(noSubStr)
	if err == nil || err.Error() != "jwt missing sub claim" {
		t.Errorf("expected 'jwt missing sub claim' error, got: %v", err)
	}
}

func TestVerifier_RS256(t *testing.T) {
	// Generate a private/public key pair
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate RSA key: %v", err)
	}

	// Encode public key to PEM
	pubDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("failed to marshal public key: %v", err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubDER,
	})

	cfg := Config{
		Algorithms:        []Algorithm{AlgorithmRS256},
		Issuer:            "test-issuer",
		Audience:          "test-audience",
		RS256PublicKeyPEM: string(pubPEM),
	}

	v, err := NewVerifier(cfg)
	if err != nil {
		t.Fatalf("failed to create verifier: %v", err)
	}

	// Sign and verify
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user-rsa",
			Issuer:    "test-issuer",
			Audience:  jwt.ClaimStrings{"test-audience"},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	})
	tokenStr, err := token.SignedString(privateKey)
	if err != nil {
		t.Fatalf("failed to sign token: %v", err)
	}

	claims, err := v.Verify(tokenStr)
	if err != nil {
		t.Fatalf("expected successful verification, got: %v", err)
	}
	if claims.Subject != "user-rsa" {
		t.Errorf("expected subject user-rsa, got %s", claims.Subject)
	}
}

func TestVerifier_ES256(t *testing.T) {
	// Generate a private/public key pair
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate EC key: %v", err)
	}

	// Encode public key to PEM
	pubDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("failed to marshal public key: %v", err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubDER,
	})

	cfg := Config{
		Algorithms:        []Algorithm{AlgorithmES256},
		Issuer:            "test-issuer",
		Audience:          "test-audience",
		ES256PublicKeyPEM: string(pubPEM),
	}

	v, err := NewVerifier(cfg)
	if err != nil {
		t.Fatalf("failed to create verifier: %v", err)
	}

	// Sign and verify
	token := jwt.NewWithClaims(jwt.SigningMethodES256, &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user-es",
			Issuer:    "test-issuer",
			Audience:  jwt.ClaimStrings{"test-audience"},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	})
	tokenStr, err := token.SignedString(privateKey)
	if err != nil {
		t.Fatalf("failed to sign token: %v", err)
	}

	claims, err := v.Verify(tokenStr)
	if err != nil {
		t.Fatalf("expected successful verification, got: %v", err)
	}
	if claims.Subject != "user-es" {
		t.Errorf("expected subject user-es, got %s", claims.Subject)
	}
}
