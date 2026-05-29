package utils

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// VerifyArgon2id compares a plaintext password with an encoded argon2id hash.
// Format expected: $argon2id$v=19$m=...,t=...,p=...$<salt>$<hash>
func VerifyArgon2id(password, encodedHash string) (bool, error) {
	vals := strings.Split(encodedHash, "$")
	if len(vals) != 6 {
		return false, errors.New("invalid hash format")
	}

	if vals[1] != "argon2id" {
		return false, errors.New("incompatible variant of argon2, expected argon2id")
	}

	var version int
	_, err := fmt.Sscanf(vals[2], "v=%d", &version)
	if err != nil {
		return false, err
	}
	if version != argon2.Version {
		return false, errors.New("incompatible version of argon2")
	}

	var memory, iterations, parallelism uint32
	_, err = fmt.Sscanf(vals[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism)
	if err != nil {
		return false, err
	}
	if parallelism < 1 || parallelism > 255 {
		return false, errors.New("invalid argon2 parallelism parameter")
	}

	salt, err := base64.RawStdEncoding.Strict().DecodeString(vals[4])
	if err != nil {
		return false, err
	}

	decodedHash, err := base64.RawStdEncoding.Strict().DecodeString(vals[5])
	if err != nil {
		return false, err
	}
	keyLen := uint32(len(decodedHash))

	comparisonHash := argon2.IDKey([]byte(password), salt, iterations, memory, uint8(parallelism), keyLen)

	return subtle.ConstantTimeCompare(decodedHash, comparisonHash) == 1, nil
}

// GenerateArgon2idHash creates an argon2id hash and returns it in the standard PHC format.
func GenerateArgon2idHash(password string) (string, error) {
	// Standard recommended parameters for Argon2id
	var memory uint32 = 64 * 1024 // 64 MB
	var iterations uint32 = 3
	var parallelism uint8 = 2
	var saltLen uint32 = 16
	var keyLen uint32 = 32

	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}

	hash := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, keyLen)

	b64Salt := base64.RawStdEncoding.EncodeToString(salt)
	b64Hash := base64.RawStdEncoding.EncodeToString(hash)

	encodedHash := fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, memory, iterations, parallelism, b64Salt, b64Hash)

	return encodedHash, nil
}
