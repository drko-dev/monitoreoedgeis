package auditjournal

import "regexp"

const maxSafeReasonLen = 255

// secretShapeRegex redacts password/secret/token/auth/bearer/credential/rtsp
// -shaped key=value or key:value fragments a caller might accidentally pass
// through in SafeReason. Mirrors internal/remoteconfig's sanitizeApplyError
// pattern; kept local rather than imported so this package has no dependency
// on remoteconfig for a plain string-safety helper.
var secretShapeRegex = regexp.MustCompile(`(?i)(password|pass|secret|token|auth(?:orization)?|bearer|credential|rtsp)[=:][^&\s]+`)

// bearerTokenRegex catches the header-value shape "Bearer <token>" (space
// separated, as it appears once an Authorization header value is echoed),
// which secretShapeRegex's key[=:]value pattern alone would not fully cover.
// Applied first so its output still feeds secretShapeRegex's own [=:] pass.
var bearerTokenRegex = regexp.MustCompile(`(?i)\bbearer\s+\S+`)

// RedactSafeReason bounds and redacts a human-readable reason/error string
// before it is ever persisted to the journal. It is applied unconditionally
// by Journal.Append — callers cannot opt out — so a caller passing a raw
// error message can never leak a credential-shaped fragment into durable
// storage.
func RedactSafeReason(s string) string {
	if s == "" {
		return s
	}
	s = bearerTokenRegex.ReplaceAllString(s, "bearer=[REDACTED]")
	s = secretShapeRegex.ReplaceAllString(s, "${1}=[REDACTED]")
	return truncate(s, maxSafeReasonLen)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
