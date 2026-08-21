package auth

import (
	"bytes"
	"strings"
	"testing"
)

func TestGenerateAndParseTokens(t *testing.T) {
	for _, kind := range []TokenKind{EnrollmentToken, AgentCredential} {
		generated, err := Generate(kind)
		if err != nil {
			t.Fatalf("generate %s token: %v", kind, err)
		}
		presented, err := Parse(kind, generated.Secret)
		if err != nil {
			t.Fatalf("parse %s token: %v", kind, err)
		}
		if presented.ID != generated.ID || !DigestMatches(presented.Digest, generated.Digest) {
			t.Fatalf("parsed token does not match generated record: %#v %#v", presented, generated)
		}
		if strings.Contains(generated.Secret, string(generated.Digest)) {
			t.Fatal("opaque token unexpectedly contains its digest bytes")
		}
	}
}

func TestGeneratedTokensAreIndependent(t *testing.T) {
	left, err := Generate(AgentCredential)
	if err != nil {
		t.Fatal(err)
	}
	right, err := Generate(AgentCredential)
	if err != nil {
		t.Fatal(err)
	}
	if left.ID == right.ID || left.Secret == right.Secret || bytes.Equal(left.Digest, right.Digest) {
		t.Fatal("independently generated credentials collided")
	}
}

func TestParseRejectsMalformedAndWrongKindTokens(t *testing.T) {
	token, err := Generate(EnrollmentToken)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"", "spe.bad", "spe.bad.bad", token.Secret + ".extra"} {
		if _, err := Parse(EnrollmentToken, value); err == nil {
			t.Fatalf("malformed token %q was accepted", value)
		}
	}
	if _, err := Parse(AgentCredential, token.Secret); err == nil {
		t.Fatal("enrollment token was accepted as an Agent credential")
	}
}

func TestDigestMatchesRejectsWrongLengthsAndValues(t *testing.T) {
	left := make([]byte, 32)
	right := make([]byte, 32)
	right[31] = 1
	if DigestMatches(left, right) || DigestMatches(left[:31], left) {
		t.Fatal("digest comparison accepted an invalid digest")
	}
}
