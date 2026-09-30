package digestauth

import "testing"

func TestParseChallengeAndBuildAuthorization(t *testing.T) {
	params, err := ParseChallenge(`Digest realm="cam", nonce="nonce", qop="auth", opaque="opaque"`)
	if err != nil {
		t.Fatal(err)
	}
	header, err := BuildAuthorization("operator", "secret", "GET", "/ISAPI/Streaming/channels", params)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`Digest username="operator"`, `realm="cam"`, `nonce="nonce"`, "qop=auth", `opaque="opaque"`} {
		if !contains(header, want) {
			t.Fatalf("header %q missing %q", header, want)
		}
	}
}

func TestParseChallengeRejectsIncompleteInput(t *testing.T) {
	if _, err := ParseChallenge(`Digest realm="cam"`); err == nil {
		t.Fatal("expected malformed challenge error")
	}
}

func contains(value, part string) bool {
	for i := 0; i+len(part) <= len(value); i++ {
		if value[i:i+len(part)] == part {
			return true
		}
	}
	return false
}
