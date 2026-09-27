package auditjournal

import (
	"os"
	"strings"
	"testing"
)

func TestRedactSafeReason_StripsSecretShapedFragments(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"password", "login failed password=hunter2secret"},
		{"device credential", "credential=aGVsbG8td29ybGQtc2VjcmV0"},
		{"rtsp url with userinfo", "connect failed rtsp=rtsp://admin:S3cretPass@10.0.0.5:554/stream"},
		{"authorization bearer", "rejected Authorization=Bearer abcdef0123456789"},
		{"enrollment token", "token=abcdef0123456789.secret.part"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := RedactSafeReason(c.input)
			if out == c.input {
				t.Fatalf("input passed through unredacted: %q", out)
			}
			if containsAny(out, "hunter2secret", "aGVsbG8td29ybGQtc2VjcmV0", "S3cretPass", "abcdef0123456789") {
				t.Fatalf("secret leaked into redacted output: %q", out)
			}
		})
	}
}

func TestRedactSafeReason_TruncatesLongInput(t *testing.T) {
	long := make([]byte, maxSafeReasonLen+100)
	for i := range long {
		long[i] = 'a'
	}
	out := RedactSafeReason(string(long))
	if len(out) > maxSafeReasonLen {
		t.Fatalf("len = %d, want <= %d", len(out), maxSafeReasonLen)
	}
}

func TestAppend_NeverPersistsSecretShapedSafeReason(t *testing.T) {
	j, path := newTestJournal(t)
	_, err := j.Append(Record{
		EventType:  EventControlCommandFailed,
		Result:     ResultFailure,
		SafeReason: "camera rejected rtsp=rtsp://admin:TopSecret123@10.0.0.9/live",
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if containsAny(string(data), "TopSecret123") {
		t.Fatalf("credential leaked into durable journal: %s", data)
	}
}

func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}
