package credentials

import (
	"regexp"
	"testing"
)

var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

func TestGenerateCredentialFormatAndUniqueness(t *testing.T) {
	a, err := GenerateCredential()
	if err != nil {
		t.Fatalf("GenerateCredential() error = %v", err)
	}
	if len(a) <= len(credentialPrefix) || a[:len(credentialPrefix)] != credentialPrefix {
		t.Errorf("GenerateCredential() = %q, want prefix %q", a, credentialPrefix)
	}

	b, err := GenerateCredential()
	if err != nil {
		t.Fatalf("GenerateCredential() error = %v", err)
	}
	if a == b {
		t.Error("GenerateCredential() produced the same value twice")
	}
}

func TestHashCredentialIsDeterministic64HexAndNeverEqualsInput(t *testing.T) {
	cred, err := GenerateCredential()
	if err != nil {
		t.Fatalf("GenerateCredential() error = %v", err)
	}

	h1 := HashCredential(cred)
	h2 := HashCredential(cred)
	if h1 != h2 {
		t.Errorf("HashCredential() not deterministic: %q != %q", h1, h2)
	}
	if !hex64.MatchString(h1) {
		t.Errorf("HashCredential() = %q, want 64 lowercase hex chars", h1)
	}
	if h1 == cred {
		t.Error("HashCredential() output equals the plaintext credential")
	}
}
