package jwtauth

import (
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// AuthScheme defines which Authorization schemes the server accepts.
type AuthScheme int

const (
	// AuthSchemeBasic accepts only HTTP Basic Auth.
	AuthSchemeBasic AuthScheme = iota
	// AuthSchemeBearer accepts only JWT Bearer tokens.
	AuthSchemeBearer
	// AuthSchemeBoth accepts either Basic or Bearer (default).
	AuthSchemeBoth
)

// ParseAuthScheme converts a config string to AuthScheme.
func ParseAuthScheme(s string) AuthScheme {
	switch strings.ToLower(s) {
	case "basic":
		return AuthSchemeBasic
	case "bearer":
		return AuthSchemeBearer
	default:
		return AuthSchemeBoth
	}
}

// Algorithm identifies a supported JWT signing algorithm.
type Algorithm int

const (
	AlgorithmHS256 Algorithm = iota
	AlgorithmRS256
	AlgorithmES256
)

// algorithmName maps Algorithm to the JWT alg header string.
var algorithmName = map[Algorithm]string{
	AlgorithmHS256: "HS256",
	AlgorithmRS256: "RS256",
	AlgorithmES256: "ES256",
}

// ParseAlgorithm converts a config string (case-insensitive) to Algorithm.
// Returns an error if the string is not a supported algorithm.
func ParseAlgorithm(s string) (Algorithm, error) {
	switch strings.ToLower(s) {
	case "hs256":
		return AlgorithmHS256, nil
	case "rs256":
		return AlgorithmRS256, nil
	case "es256":
		return AlgorithmES256, nil
	default:
		return 0, fmt.Errorf("unsupported JWT algorithm: %q (supported: hs256, rs256, es256)", s)
	}
}

// Claims holds the validated JWT standard claims.
// Sub contains the armi user ID.
type Claims struct {
	jwt.RegisteredClaims
}

// Config carries all parameters needed to build a Verifier.
type Config struct {
	// Algorithms is the list of accepted signing algorithms.
	Algorithms []Algorithm
	// Issuer is the expected "iss" claim value.
	Issuer string
	// Audience is the expected "aud" claim value (URI).
	Audience string
	// HS256Secret is the raw or base64-encoded HMAC secret (used when AlgorithmHS256 is enabled).
	HS256Secret string
	// RS256PublicKeyPEM is the PEM-encoded RSA public key (used when AlgorithmRS256 is enabled).
	RS256PublicKeyPEM string
	// ES256PublicKeyPEM is the PEM-encoded EC public key (used when AlgorithmES256 is enabled).
	ES256PublicKeyPEM string
}

// Verifier parses and validates JWT tokens according to the given Config.
type Verifier struct {
	cfg        Config
	allowedAlg map[Algorithm]bool
	hmacSecret []byte
	rsaKey     *rsa.PublicKey
	ecKey      *ecdsa.PublicKey
	parser     *jwt.Parser
}

// NewVerifier constructs a Verifier from the provided Config.
// Returns an error if required keys are missing for any enabled algorithm.
func NewVerifier(cfg Config) (*Verifier, error) {
	if len(cfg.Algorithms) == 0 {
		return nil, errors.New("jwtauth: at least one algorithm must be configured")
	}
	if cfg.Issuer == "" {
		return nil, errors.New("jwtauth: jwt.issuer must not be empty")
	}
	if cfg.Audience == "" {
		return nil, errors.New("jwtauth: jwt.audience must not be empty")
	}

	v := &Verifier{
		cfg:        cfg,
		allowedAlg: make(map[Algorithm]bool, len(cfg.Algorithms)),
	}

	var algNames []string
	for _, alg := range cfg.Algorithms {
		v.allowedAlg[alg] = true
		name, ok := algorithmName[alg]
		if !ok {
			return nil, fmt.Errorf("jwtauth: unknown algorithm constant %d", alg)
		}
		algNames = append(algNames, name)

		switch alg {
		case AlgorithmHS256:
			if cfg.HS256Secret == "" {
				return nil, errors.New("jwtauth: jwt.hs256.secret is required when hs256 is enabled")
			}
			secret, err := decodeSecret(cfg.HS256Secret)
			if err != nil {
				return nil, fmt.Errorf("jwtauth: failed to decode hs256 secret: %w", err)
			}
			v.hmacSecret = secret

		case AlgorithmRS256:
			if cfg.RS256PublicKeyPEM == "" {
				return nil, errors.New("jwtauth: jwt.rs256.public_key_pem is required when rs256 is enabled")
			}
			rsaKey, err := parseRSAPublicKey(cfg.RS256PublicKeyPEM)
			if err != nil {
				return nil, fmt.Errorf("jwtauth: failed to parse rs256 public key: %w", err)
			}
			v.rsaKey = rsaKey

		case AlgorithmES256:
			if cfg.ES256PublicKeyPEM == "" {
				return nil, errors.New("jwtauth: jwt.es256.public_key_pem is required when es256 is enabled")
			}
			ecKey, err := parseECPublicKey(cfg.ES256PublicKeyPEM)
			if err != nil {
				return nil, fmt.Errorf("jwtauth: failed to parse es256 public key: %w", err)
			}
			v.ecKey = ecKey
		}
	}

	v.parser = jwt.NewParser(
		jwt.WithValidMethods(algNames),
		jwt.WithIssuedAt(),
		jwt.WithExpirationRequired(),
		jwt.WithIssuer(cfg.Issuer),
		jwt.WithAudience(cfg.Audience),
		jwt.WithLeeway(0),
	)

	slog.Info("jwtauth: verifier initialized",
		"algorithms", algNames,
		"issuer", cfg.Issuer,
		"audience", cfg.Audience,
	)
	return v, nil
}

// Verify parses and fully validates the given raw JWT string.
// Returns validated Claims (including Sub as armi user ID) or an error.
func (v *Verifier) Verify(tokenStr string) (*Claims, error) {
	claims := &Claims{}
	token, err := v.parser.ParseWithClaims(tokenStr, claims, v.keyFunc)
	if err != nil {
		return nil, fmt.Errorf("jwt validation failed: %w", err)
	}
	if !token.Valid {
		return nil, errors.New("jwt token is not valid")
	}
	if claims.Subject == "" {
		return nil, errors.New("jwt missing sub claim")
	}
	return claims, nil
}

// keyFunc selects the appropriate verification key based on the token's alg header.
func (v *Verifier) keyFunc(token *jwt.Token) (interface{}, error) {
	switch token.Method.(type) {
	case *jwt.SigningMethodHMAC:
		if !v.allowedAlg[AlgorithmHS256] {
			return nil, fmt.Errorf("algorithm HS256 is not enabled")
		}
		return v.hmacSecret, nil

	case *jwt.SigningMethodRSA:
		if !v.allowedAlg[AlgorithmRS256] {
			return nil, fmt.Errorf("algorithm RS256 is not enabled")
		}
		if v.rsaKey == nil {
			return nil, errors.New("rs256 public key not loaded")
		}
		return v.rsaKey, nil

	case *jwt.SigningMethodECDSA:
		if !v.allowedAlg[AlgorithmES256] {
			return nil, fmt.Errorf("algorithm ES256 is not enabled")
		}
		if v.ecKey == nil {
			return nil, errors.New("es256 public key not loaded")
		}
		return v.ecKey, nil

	default:
		return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
	}
}

// decodeSecret tries base64-decoding first; falls back to raw bytes.
func decodeSecret(s string) ([]byte, error) {
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.URLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return []byte(s), nil
}

// parseRSAPublicKey decodes a PEM-encoded RSA public key.
func parseRSAPublicKey(pemStr string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("failed to decode PEM block for RSA public key")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("x509 parse failed: %w", err)
	}
	rsaKey, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("PEM key is not an RSA public key")
	}
	return rsaKey, nil
}

// parseECPublicKey decodes a PEM-encoded EC public key.
func parseECPublicKey(pemStr string) (*ecdsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("failed to decode PEM block for EC public key")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("x509 parse failed: %w", err)
	}
	ecKey, ok := pub.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("PEM key is not an EC public key")
	}
	return ecKey, nil
}
