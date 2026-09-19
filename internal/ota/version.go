// Package ota implements Hito T's OTA foundation: version comparison
// (T1), update discovery after a successful heartbeat (T2), verified
// download and atomic staging (T3), and Ed25519 manifest signing/checksum
// verification (T4). It never applies an update -- staging
// DataDir/ota/apply.request is the full extent of its authority; a
// privileged updater (IA2) owns everything past that point.
package ota

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a parsed release version (major.minor.patch).
type Version struct {
	Major, Minor, Patch int
}

// ParseVersion parses a release version. The canonical form is "vX.Y.Z";
// a bare "X.Y.Z" is also accepted since internal/agent.Version's
// unbuilt-dev default ("0.1.0") has no "v" prefix and this package must
// not treat that as invalid. Anything else -- extra components,
// pre-release/build metadata, non-numeric parts, leading zeros -- is
// rejected outright. Invalid values are never compared lexicographically.
func ParseVersion(s string) (Version, error) {
	trimmed := strings.TrimPrefix(s, "v")
	parts := strings.Split(trimmed, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("ota: version %q is not in vX.Y.Z form", s)
	}
	nums := make([]int, 3)
	for i, p := range parts {
		if p == "" || (len(p) > 1 && p[0] == '0') || strings.ContainsAny(p, "+-") {
			return Version{}, fmt.Errorf("ota: version %q has an invalid numeric component %q", s, p)
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return Version{}, fmt.Errorf("ota: version %q has an invalid numeric component %q", s, p)
		}
		nums[i] = n
	}
	return Version{Major: nums[0], Minor: nums[1], Patch: nums[2]}, nil
}

// Compare returns -1, 0, or 1 as v is less than, equal to, or greater than other.
func (v Version) Compare(other Version) int {
	switch {
	case v.Major != other.Major:
		return cmpInt(v.Major, other.Major)
	case v.Minor != other.Minor:
		return cmpInt(v.Minor, other.Minor)
	default:
		return cmpInt(v.Patch, other.Patch)
	}
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// String renders the canonical "vX.Y.Z" form.
func (v Version) String() string {
	return fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// IsUpdateEligible reports whether candidate is a valid, strictly newer
// version than current. Invalid version strings are rejected, never
// compared lexicographically -- a malformed candidate is simply not
// eligible, never accepted by falling back to a looser comparison. A
// downgrade or equal version is also not eligible: rollback is a separate,
// operator-driven mechanism (T7), not this forward-update check.
func IsUpdateEligible(current, candidate string) (bool, error) {
	cur, err := ParseVersion(current)
	if err != nil {
		return false, fmt.Errorf("ota: current version invalid: %w", err)
	}
	cand, err := ParseVersion(candidate)
	if err != nil {
		return false, fmt.Errorf("ota: candidate version invalid: %w", err)
	}
	return cand.Compare(cur) > 0, nil
}
