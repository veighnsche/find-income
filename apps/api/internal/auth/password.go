package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	argonMemory  = 64 * 1024 // KiB
	argonTime    = 3
	argonThreads = 1
	keyLength    = 32
)

var ErrWeakPassword = errors.New("password must be between 12 and 1024 bytes")

// HashPassword uses Argon2id with a new random salt. The encoded parameters
// are bounded on verification so a corrupted database cannot force huge work.
func HashPassword(password []byte) (string, error) {
	if len(password) < 12 || len(password) > 1024 {
		return "", ErrWeakPassword
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey(password, salt, argonTime, argonMemory, argonThreads, keyLength)
	encode := base64.RawStdEncoding.EncodeToString
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		argonMemory, argonTime, argonThreads, encode(salt), encode(key)), nil
}

func VerifyPassword(encoded string, password []byte) bool {
	if len(password) == 0 || len(password) > 1024 {
		return false
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" ||
		parts[3] != "m=65536,t=3,p=1" {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) != 16 {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(expected) != keyLength {
		return false
	}
	actual := argon2.IDKey(password, salt, argonTime, argonMemory, argonThreads, keyLength)
	return subtle.ConstantTimeCompare(actual, expected) == 1
}
