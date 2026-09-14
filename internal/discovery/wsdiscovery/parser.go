package wsdiscovery

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strings"
)

const (
	MaxDatagramBytes           = 16384 // 16 KB
	MaxProbeMatchesPerDatagram = 16
	MaxXAddrsPerMatch          = 8
	MaxEPRLength               = 512
	MaxTypesLength             = 512
	MaxScopesLength            = 2048
	MaxMetadataLength          = 128
)

var (
	ErrOversizedDatagram = errors.New("wsdiscovery: datagram exceeds maximum permitted size")
	ErrDTDForbidden      = errors.New("wsdiscovery: DTD entity declarations forbidden")
)

// RawProbeMatch holds raw text fields extracted from a single <ProbeMatch> element.
type RawProbeMatch struct {
	EPRAddress      string
	Types           string
	Scopes          string
	XAddrs          []string
	MetadataVersion string
}

// ParseProbeMatches extracts up to MaxProbeMatchesPerDatagram matches from a UDP datagram.
// Fail-closed design:
//   - Discards datagrams exceeding MaxDatagramBytes (16 KB) immediately without truncation.
//   - Discards datagrams containing DOCTYPE or ENTITY definitions.
//   - Matches XML elements by Local name to handle arbitrary vendor namespace prefixes.
//   - Never panics on corrupted or malformed payloads.
func ParseProbeMatches(datagram []byte) ([]RawProbeMatch, error) {
	if len(datagram) == 0 {
		return nil, nil
	}
	if len(datagram) > MaxDatagramBytes {
		return nil, ErrOversizedDatagram
	}

	// Reject DTD / entities before XML parsing to prevent billion-laughs or entity injection
	lower := bytes.ToLower(datagram)
	if bytes.Contains(lower, []byte("<!doctype")) || bytes.Contains(lower, []byte("<!entity")) {
		return nil, ErrDTDForbidden
	}

	dec := xml.NewDecoder(bytes.NewReader(datagram))
	dec.Strict = false

	var matches []RawProbeMatch

	for {
		token, err := dec.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			// Malformed XML: return what was parsed so far, do not panic
			return matches, nil
		}

		startElem, ok := token.(xml.StartElement)
		if !ok {
			continue
		}

		if strings.EqualFold(startElem.Name.Local, "ProbeMatch") {
			match, err := parseSingleProbeMatch(dec, startElem)
			if err != nil {
				continue
			}
			matches = append(matches, match)
			if len(matches) >= MaxProbeMatchesPerDatagram {
				break
			}
		}
	}

	return matches, nil
}

func parseSingleProbeMatch(dec *xml.Decoder, start xml.StartElement) (RawProbeMatch, error) {
	var match RawProbeMatch
	depth := 1

	for depth > 0 {
		token, err := dec.Token()
		if err != nil {
			return match, err
		}

		switch t := token.(type) {
		case xml.StartElement:
			depth++
			local := strings.ToLower(t.Name.Local)
			switch local {
			case "address":
				var text string
				if err := dec.DecodeElement(&text, &t); err == nil {
					match.EPRAddress = sanitizeText(text, MaxEPRLength)
					depth-- // DecodeElement consumed the EndElement
				}
			case "types":
				var text string
				if err := dec.DecodeElement(&text, &t); err == nil {
					match.Types = sanitizeText(text, MaxTypesLength)
					depth--
				}
			case "scopes":
				var text string
				if err := dec.DecodeElement(&text, &t); err == nil {
					match.Scopes = sanitizeText(text, MaxScopesLength)
					depth--
				}
			case "xaddrs":
				var text string
				if err := dec.DecodeElement(&text, &t); err == nil {
					depth--
					rawFields := strings.Fields(text)
					for _, rawURL := range rawFields {
						clean := strings.TrimSpace(rawURL)
						if clean != "" && len(match.XAddrs) < MaxXAddrsPerMatch {
							match.XAddrs = append(match.XAddrs, clean)
						}
					}
				}
			case "metadataversion":
				var text string
				if err := dec.DecodeElement(&text, &t); err == nil {
					match.MetadataVersion = sanitizeText(text, 32)
					depth--
				}
			}
		case xml.EndElement:
			depth--
		}
	}

	return match, nil
}

func sanitizeText(s string, maxLen int) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r >= 0x20 && r != 0x7F {
			b.WriteRune(r)
		}
	}
	trimmed := strings.TrimSpace(b.String())
	fields := strings.Fields(trimmed)
	collapsed := strings.Join(fields, " ")
	if maxLen > 0 && len(collapsed) > maxLen {
		return collapsed[:maxLen]
	}
	return collapsed
}
