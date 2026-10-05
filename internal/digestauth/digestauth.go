// Package digestauth implements the RFC 2617 MD5 digest subset shared by
// local device protocols. It never retains or logs passwords.
package digestauth

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var paramRE = regexp.MustCompile(`(\w+)\s*=\s*(?:"([^"]*)"|([^,\s]+))`)

// ParseChallenge extracts the common Digest challenge parameters.
func ParseChallenge(header string) (map[string]string, error) {
	if !strings.HasPrefix(strings.TrimSpace(header), "Digest") {
		return nil, errors.New("digest auth: WWW-Authenticate is not a Digest challenge")
	}
	params := map[string]string{}
	for _, match := range paramRE.FindAllStringSubmatch(header, -1) {
		value := match[2]
		if value == "" {
			value = match[3]
		}
		params[strings.ToLower(match[1])] = value
	}
	if params["realm"] == "" || params["nonce"] == "" {
		return nil, errors.New("digest auth: digest challenge missing realm or nonce")
	}
	return params, nil
}

// BuildAuthorization builds a Digest Authorization value. The caller owns the
// resulting header and must not log it because it includes the username.
func BuildAuthorization(username, password, method, uri string, params map[string]string) (string, error) {
	realm := params["realm"]
	nonce := params["nonce"]
	if realm == "" || nonce == "" {
		return "", errors.New("digest auth: missing realm or nonce")
	}
	if algorithm := strings.ToUpper(params["algorithm"]); algorithm != "" && algorithm != "MD5" {
		return "", errors.New("digest auth: unsupported algorithm")
	}
	qop, err := firstQop(params["qop"])
	if err != nil {
		return "", err
	}
	ha1 := md5Hex(username + ":" + realm + ":" + password)
	ha2 := md5Hex(method + ":" + uri)

	var response, cnonce, nc string
	if qop != "" {
		cnonceBytes := make([]byte, 8)
		if _, err := rand.Read(cnonceBytes); err != nil {
			return "", fmt.Errorf("digest auth: generate cnonce: %w", err)
		}
		cnonce = hex.EncodeToString(cnonceBytes)
		nc = "00000001"
		response = md5Hex(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":" + qop + ":" + ha2)
	} else {
		response = md5Hex(ha1 + ":" + nonce + ":" + ha2)
	}

	var b strings.Builder
	userField, err := quote(username)
	if err != nil {
		return "", err
	}
	realmField, err := quote(realm)
	if err != nil {
		return "", err
	}
	nonceField, err := quote(nonce)
	if err != nil {
		return "", err
	}
	uriField, err := quote(uri)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(&b, `Digest username=%s, realm=%s, nonce=%s, uri=%s, response="%s"`, userField, realmField, nonceField, uriField, response)
	if opaque := params["opaque"]; opaque != "" {
		opaqueField, err := quote(opaque)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, `, opaque=%s`, opaqueField)
	}
	if qop != "" {
		cnonceField, err := quote(cnonce)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, `, qop=%s, nc=%s, cnonce=%s`, qop, nc, cnonceField)
	}
	return b.String(), nil
}

func quote(value string) (string, error) {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return "", errors.New("digest auth: control character in quoted field")
		}
	}
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`, nil
}

func firstQop(qop string) (string, error) {
	if qop == "" {
		return "", nil
	}
	for _, value := range strings.Split(qop, ",") {
		if strings.EqualFold(strings.TrimSpace(value), "auth") {
			return "auth", nil
		}
	}
	return "", errors.New("digest auth: unsupported qop")
}

func md5Hex(value string) string {
	sum := md5.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}
