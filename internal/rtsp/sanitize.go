package rtsp

import (
	"regexp"
	"strings"
	"unicode"
)

var (
	// uriWithAuthRegex matches scheme://user:pass@host or scheme://user@host
	uriWithAuthRegex = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)([^:/@\s]+)(:[^/@\s]+)?@`)
	// paramSecretRegex matches sensitive query/header key-value pairs
	paramSecretRegex = regexp.MustCompile(`(?i)(password|pass|secret|token|auth)=([^&\s]+)`)
)

// SanitizeError strips sensitive credentials, tokens, passwords, and control
// characters from an error message, truncating it to at most 255 bytes.
func SanitizeError(err error) string {
	if err == nil {
		return ""
	}
	return SanitizeErrorMessage(err.Error())
}

// SanitizeErrorMessage scrubs secrets from a message string.
func SanitizeErrorMessage(msg string) string {
	if msg == "" {
		return ""
	}

	// 1. Redact credentials in URLs / URIs (e.g. rtsp://user:pass@host -> rtsp://[REDACTED]@host)
	cleaned := uriWithAuthRegex.ReplaceAllString(msg, "${1}[REDACTED]@")

	// 2. Redact query-string / parameter secrets
	cleaned = paramSecretRegex.ReplaceAllString(cleaned, "${1}=[REDACTED]")

	// 3. Strip control characters / non-printable characters and normalize spaces
	var sb strings.Builder
	sb.Grow(len(cleaned))
	for _, r := range cleaned {
		if unicode.IsPrint(r) && r != '\n' && r != '\r' && r != '\t' {
			sb.WriteRune(r)
		} else {
			sb.WriteRune(' ')
		}
	}
	res := strings.Join(strings.Fields(sb.String()), " ")

	// 4. Bound to 255 bytes safely without slicing in the middle of a multi-byte UTF-8 rune
	if len(res) > 255 {
		res = truncateUTF8(res, 255)
	}
	return res
}

func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	for i := maxBytes; i > 0; i-- {
		if (s[i] & 0xc0) != 0x80 {
			return s[:i]
		}
	}
	return ""
}
