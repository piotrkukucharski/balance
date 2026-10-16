package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters as strictly mandated in spec 3.1:
// memory = 64 MiB (64 * 1024 KiB), iterations = 3, parallelism = 2, salt = 16 bytes CSPRNG, keyLen = 32 bytes
const (
	ArgonMemory      = 64 * 1024
	ArgonIterations  = 3
	ArgonParallelism = 2
	ArgonSaltLen     = 16
	ArgonKeyLen      = 32
)

// HashPassword creates a standard formatted Argon2id hash: $argon2id$v=19$m=65536,t=3,p=2$<b64salt>$<b64hash>
func HashPassword(password string) (string, error) {
	salt := make([]byte, ArgonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("read csprng salt: %w", err)
	}

	hash := argon2.IDKey([]byte(password), salt, ArgonIterations, ArgonMemory, ArgonParallelism, ArgonKeyLen)

	b64Salt := base64.RawStdEncoding.EncodeToString(salt)
	b64Hash := base64.RawStdEncoding.EncodeToString(hash)

	encoded := fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, ArgonMemory, ArgonIterations, ArgonParallelism, b64Salt, b64Hash)

	return encoded, nil
}

// VerifyPassword checks if a plain password matches the Argon2id hash.
func VerifyPassword(password, encodedHash string) (bool, error) {
	parts := strings.Split(encodedHash, "$")
	if len(parts) != 6 {
		return false, errors.New("invalid argon2id hash format")
	}
	if parts[1] != "argon2id" {
		return false, errors.New("incompatible password hash type")
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, err
	}
	if version != argon2.Version {
		return false, errors.New("incompatible argon2 version")
	}

	var memory uint32
	var iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return false, err
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, err
	}

	expectedHash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, err
	}

	computedHash := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, uint32(len(expectedHash)))

	if subtle.ConstantTimeCompare(expectedHash, computedHash) == 1 {
		return true, nil
	}
	return false, nil
}

// GenerateCSPRNGToken generates 32 bytes (256 bits) of CSPRNG encoded as Base64URL without padding.
func GenerateCSPRNGToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read csprng: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashToken returns SHA-256 hex string of a raw token.
func HashToken(rawToken string) string {
	sum := sha256.Sum256([]byte(rawToken))
	return fmt.Sprintf("%x", sum[:])
}

// GeneratePAT returns (fullPAT, tokenHash). Format: bal_pat_<256-bit-csprng>
func GeneratePAT() (string, string, error) {
	secret, err := GenerateCSPRNGToken()
	if err != nil {
		return "", "", err
	}
	full := "bal_pat_" + secret
	return full, HashToken(full), nil
}
