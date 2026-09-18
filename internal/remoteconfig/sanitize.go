package remoteconfig

import "regexp"

const maxSafeErrorLen = 255

// paramSecretRegex redacts password/secret/token/auth/bearer-shaped
// key=value fragments a RuntimeAdapter error might echo back (e.g. from an
// invalid RTSP or credential-bearing knob in its payload). Mirrors
// internal/rtsp's sanitize pattern; kept local rather than imported since
// this package must not depend on the RTSP protocol package for a plain
// string-safety helper.
var paramSecretRegex = regexp.MustCompile(`(?i)(password|pass|secret|token|auth|bearer)[=:][^&\s]+`)

// sanitizeApplyError turns a RuntimeAdapter error into a bounded, redacted
// string safe to persist and expose on /status. Never returns the raw
// error unmodified: adapter errors originate from IA2's own runtime code
// and payload echoing, which this package does not control.
func sanitizeApplyError(err error) string {
	if err == nil {
		return ""
	}
	msg := paramSecretRegex.ReplaceAllString(err.Error(), "${1}=[REDACTED]")
	return truncate(msg, maxSafeErrorLen)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
