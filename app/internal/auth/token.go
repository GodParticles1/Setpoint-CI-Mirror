package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

type TokenKind string

const (
	EnrollmentToken TokenKind = "enrollment"
	AgentCredential TokenKind = "agent_credential"
)

const (
	enrollmentPrefix = "spe"
	credentialPrefix = "spc"
	idBytes          = 16
	secretBytes      = 32
)

type GeneratedToken struct {
	ID     string
	Secret string
	Digest []byte
}

type PresentedToken struct {
	ID     string
	Digest []byte
}

func Generate(kind TokenKind) (GeneratedToken, error) {
	id := make([]byte, idBytes)
	secret := make([]byte, secretBytes)
	if _, err := rand.Read(id); err != nil {
		return GeneratedToken{}, fmt.Errorf("generate token ID: %w", err)
	}
	if _, err := rand.Read(secret); err != nil {
		return GeneratedToken{}, fmt.Errorf("generate token secret: %w", err)
	}
	prefix, err := prefixFor(kind)
	if err != nil {
		return GeneratedToken{}, err
	}
	idText := hex.EncodeToString(id)
	secretText := base64.RawURLEncoding.EncodeToString(secret)
	digest := sha256.Sum256(secret)
	return GeneratedToken{
		ID: idText, Secret: prefix + "." + idText + "." + secretText,
		Digest: append([]byte(nil), digest[:]...),
	}, nil
}

func Parse(kind TokenKind, value string) (PresentedToken, error) {
	prefix, err := prefixFor(kind)
	if err != nil {
		return PresentedToken{}, err
	}
	parts := strings.Split(value, ".")
	if len(parts) != 3 || parts[0] != prefix {
		return PresentedToken{}, errors.New("invalid token format")
	}
	id, err := hex.DecodeString(parts[1])
	if err != nil || len(id) != idBytes {
		return PresentedToken{}, errors.New("invalid token ID")
	}
	secret, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(secret) != secretBytes {
		return PresentedToken{}, errors.New("invalid token secret")
	}
	digest := sha256.Sum256(secret)
	return PresentedToken{ID: parts[1], Digest: append([]byte(nil), digest[:]...)}, nil
}

func DigestMatches(actual, expected []byte) bool {
	return len(actual) == sha256.Size && len(expected) == sha256.Size && subtle.ConstantTimeCompare(actual, expected) == 1
}

func prefixFor(kind TokenKind) (string, error) {
	switch kind {
	case EnrollmentToken:
		return enrollmentPrefix, nil
	case AgentCredential:
		return credentialPrefix, nil
	default:
		return "", fmt.Errorf("unsupported token kind %q", kind)
	}
}
