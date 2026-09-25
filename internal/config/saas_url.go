package config

import (
	"errors"
	"net/url"
	"strings"
)

// ValidateSaaSURL rejects credentials embedded in the configured base URL.
// Device authentication belongs in the existing credential header, never in
// URL userinfo or query parameters that can be printed or retained in logs.
func ValidateSaaSURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("invalid GEOCAM_SAAS_URL")
	}
	if u.User != nil {
		return errors.New("GEOCAM_SAAS_URL must not contain URL userinfo")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return errors.New("GEOCAM_SAAS_URL contains an invalid query")
	}
	for key := range query {
		if isSensitiveSaaSQueryKey(key) {
			return errors.New("GEOCAM_SAAS_URL must not contain authentication or secret query parameters")
		}
	}
	return nil
}

// SanitizeSaaSURL is defense in depth for diagnostic output. It strips URL
// userinfo, redacts sensitive query values, and never echoes malformed input.
func SanitizeSaaSURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<invalid>"
	}
	u.User = nil
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "<invalid>"
	}
	for key := range query {
		if isSensitiveSaaSQueryKey(key) {
			query[key] = []string{"[redacted]"}
		}
	}
	u.RawQuery = query.Encode()
	u.Fragment = ""
	return u.String()
}

func normalizeSaaSQueryKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	key = strings.ReplaceAll(key, "-", "_")
	return key
}

func isSensitiveSaaSQueryKey(key string) bool {
	key = normalizeSaaSQueryKey(key)
	if key == "key" || key == "auth" || key == "authorization" || key == "sig" ||
		strings.HasSuffix(key, "_key") || strings.Contains(key, "apikey") {
		return true
	}
	return strings.Contains(key, "token") || strings.Contains(key, "secret") ||
		strings.Contains(key, "password") || strings.Contains(key, "credential") ||
		strings.Contains(key, "signature") || strings.Contains(key, "session") || strings.Contains(key, "cookie") || strings.Contains(key, "bearer")
}
