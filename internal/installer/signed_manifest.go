package installer

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Signed manifest schema and algorithm constants.
const (
	CurrentSignedManifestSchemaVersion = "1.0.0"

	SignatureAlgorithmEd25519   = "ed25519"
	SignatureAlgorithmECDSAP256 = "ecdsa-p256"

	domainSeparatorHeader = "GEO-CAM-EDGE-SIGNED-MANIFEST-V1"
)

// Specific cryptographic and validation errors.
var (
	ErrSignedManifestSchemaInvalid = errors.New("signed runtime manifest schema validation failed")
	ErrUntrustedKey                = errors.New("signing key is not recognized or trusted by trust anchor")
	ErrKeyRevoked                  = errors.New("signing key has been revoked")
	ErrSignatureInvalid            = errors.New("cryptographic signature verification failed")
	ErrManifestExpired             = errors.New("signed runtime manifest has expired")
	ErrManifestNotYetValid         = errors.New("signed runtime manifest is not yet valid")
	ErrUnsupportedAlgorithm        = errors.New("unsupported signature algorithm")
	ErrPublicKeyMismatch           = errors.New("embedded public key does not match trust anchor")
	ErrManifestTypeNotRelease      = errors.New("signed runtime manifest must be release type")
)

// ManifestSignature contains cryptographic signature metadata and timestamps.
type ManifestSignature struct {
	KeyID     string     `json:"key_id"`
	Algorithm string     `json:"algorithm"`
	PublicKey string     `json:"public_key,omitempty"` // Base64 or Hex encoded public key representation
	Signature string     `json:"signature"`            // Base64 encoded signature
	IssuedAt  time.Time  `json:"issued_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	NotBefore *time.Time `json:"not_before,omitempty"`
}

// SignedRuntimeManifest encapsulates a RuntimeManifest along with detached or embedded
// cryptographic proof signed by an authoritative trust anchor.
type SignedRuntimeManifest struct {
	SchemaVersion string            `json:"schema_version"`
	Manifest      RuntimeManifest   `json:"manifest"`
	Signature     ManifestSignature `json:"signature"`
}

// TrustAnchor holds an authenticated public key and its operational revocation state.
// The trust anchor is decoupled from the pack and resides in trusted client storage or binary config.
type TrustAnchor struct {
	KeyID            string     `json:"key_id"`
	Algorithm        string     `json:"algorithm"`  // SignatureAlgorithmEd25519 or SignatureAlgorithmECDSAP256
	PublicKey        []byte     `json:"public_key"` // Raw 32 bytes for Ed25519, or PKIX ASN.1 DER for ECDSA P-256
	Revoked          bool       `json:"revoked"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
	RevocationReason string     `json:"revocation_reason,omitempty"`
}

// TrustStore provides access to authoritative trust anchors.
type TrustStore interface {
	GetTrustAnchor(keyID string) (*TrustAnchor, error)
}

// MemoryTrustStore is a thread-safe in-memory TrustStore implementation.
type MemoryTrustStore struct {
	mu      sync.RWMutex
	anchors map[string]*TrustAnchor
}

// NewMemoryTrustStore creates a new empty MemoryTrustStore.
func NewMemoryTrustStore() *MemoryTrustStore {
	return &MemoryTrustStore{
		anchors: make(map[string]*TrustAnchor),
	}
}

// AddAnchor registers a TrustAnchor.
func (s *MemoryTrustStore) AddAnchor(anchor *TrustAnchor) error {
	if anchor == nil || anchor.KeyID == "" {
		return errors.New("invalid trust anchor: missing key ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Clone to avoid external mutation
	clone := *anchor
	if len(anchor.PublicKey) > 0 {
		clone.PublicKey = make([]byte, len(anchor.PublicKey))
		copy(clone.PublicKey, anchor.PublicKey)
	}
	s.anchors[anchor.KeyID] = &clone
	return nil
}

// RevokeKey marks an anchor as revoked.
func (s *MemoryTrustStore) RevokeKey(keyID string, revokedAt time.Time, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	anchor, ok := s.anchors[keyID]
	if !ok {
		return fmt.Errorf("%w: key %q not found", ErrUntrustedKey, keyID)
	}
	anchor.Revoked = true
	t := revokedAt
	anchor.RevokedAt = &t
	anchor.RevocationReason = reason
	return nil
}

// GetTrustAnchor retrieves a TrustAnchor by its KeyID.
func (s *MemoryTrustStore) GetTrustAnchor(keyID string) (*TrustAnchor, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	anchor, ok := s.anchors[keyID]
	if !ok {
		return nil, fmt.Errorf("%w: key %q not present in trust store", ErrUntrustedKey, keyID)
	}
	clone := *anchor
	if len(anchor.PublicKey) > 0 {
		clone.PublicKey = make([]byte, len(anchor.PublicKey))
		copy(clone.PublicKey, anchor.PublicKey)
	}
	return &clone, nil
}

// NewEd25519TrustAnchor creates an Ed25519 TrustAnchor from an ed25519.PublicKey.
func NewEd25519TrustAnchor(keyID string, pub ed25519.PublicKey) *TrustAnchor {
	pubBytes := make([]byte, len(pub))
	copy(pubBytes, pub)
	return &TrustAnchor{
		KeyID:     keyID,
		Algorithm: SignatureAlgorithmEd25519,
		PublicKey: pubBytes,
		Revoked:   false,
	}
}

// NewECDSAP256TrustAnchor creates an ECDSA P-256 TrustAnchor from an *ecdsa.PublicKey.
func NewECDSAP256TrustAnchor(keyID string, pub *ecdsa.PublicKey) (*TrustAnchor, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal ECDSA public key: %w", err)
	}
	return &TrustAnchor{
		KeyID:     keyID,
		Algorithm: SignatureAlgorithmECDSAP256,
		PublicKey: der,
		Revoked:   false,
	}, nil
}

// CanonicalManifestBytes generates a deterministic, sorted, canonical JSON representation
// of a RuntimeManifest to ensure bit-level reproducibility of the signature target.
func CanonicalManifestBytes(m *RuntimeManifest) ([]byte, error) {
	if m == nil {
		return nil, errors.New("cannot canonicalize nil manifest")
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal manifest for canonicalization: %w", err)
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("failed to decode manifest for canonicalization: %w", err)
	}

	// json.Marshal on map[string]any recursively sorts all dictionary keys in Go.
	canonical, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("failed to produce canonical json: %w", err)
	}
	return canonical, nil
}

func formatRFC3339(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func computeSignatureMessage(m *RuntimeManifest, sig *ManifestSignature) ([]byte, error) {
	canonBytes, err := CanonicalManifestBytes(m)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	buf.WriteString(domainSeparatorHeader)
	buf.WriteByte('\n')
	buf.WriteString(fmt.Sprintf("alg:%s\n", sig.Algorithm))
	buf.WriteString(fmt.Sprintf("key:%s\n", sig.KeyID))
	buf.WriteString(fmt.Sprintf("issued:%s\n", sig.IssuedAt.UTC().Format(time.RFC3339Nano)))
	buf.WriteString(fmt.Sprintf("expires:%s\n", formatRFC3339(sig.ExpiresAt)))
	buf.WriteString(fmt.Sprintf("not_before:%s\n", formatRFC3339(sig.NotBefore)))
	buf.WriteByte('\n')
	buf.Write(canonBytes)

	return buf.Bytes(), nil
}

// SignOptions controls timestamp constraints when signing a manifest.
type SignOptions struct {
	IssuedAt  time.Time
	ExpiresAt *time.Time
	NotBefore *time.Time
}

// SignManifestEd25519 signs a RuntimeManifest using an Ed25519 private key.
func SignManifestEd25519(manifest *RuntimeManifest, keyID string, priv ed25519.PrivateKey, opts SignOptions) (*SignedRuntimeManifest, error) {
	if manifest == nil {
		return nil, errors.New("manifest is nil")
	}
	if len(priv) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid Ed25519 private key length")
	}
	if keyID == "" {
		return nil, errors.New("keyID is required")
	}

	issuedAt := opts.IssuedAt
	if issuedAt.IsZero() {
		issuedAt = time.Now().UTC()
	}

	sigMeta := ManifestSignature{
		KeyID:     keyID,
		Algorithm: SignatureAlgorithmEd25519,
		PublicKey: base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)),
		IssuedAt:  issuedAt,
		ExpiresAt: opts.ExpiresAt,
		NotBefore: opts.NotBefore,
	}

	msg, err := computeSignatureMessage(manifest, &sigMeta)
	if err != nil {
		return nil, err
	}

	sigBytes := ed25519.Sign(priv, msg)
	sigMeta.Signature = base64.StdEncoding.EncodeToString(sigBytes)

	return &SignedRuntimeManifest{
		SchemaVersion: CurrentSignedManifestSchemaVersion,
		Manifest:      *manifest,
		Signature:     sigMeta,
	}, nil
}

// SignManifestECDSAP256 signs a RuntimeManifest using an ECDSA P-256 private key.
func SignManifestECDSAP256(manifest *RuntimeManifest, keyID string, priv *ecdsa.PrivateKey, opts SignOptions) (*SignedRuntimeManifest, error) {
	if manifest == nil {
		return nil, errors.New("manifest is nil")
	}
	if priv == nil || priv.Curve != elliptic.P256() {
		return nil, errors.New("valid ECDSA P-256 private key is required")
	}
	if keyID == "" {
		return nil, errors.New("keyID is required")
	}

	pubDer, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to encode ECDSA public key: %w", err)
	}

	issuedAt := opts.IssuedAt
	if issuedAt.IsZero() {
		issuedAt = time.Now().UTC()
	}

	sigMeta := ManifestSignature{
		KeyID:     keyID,
		Algorithm: SignatureAlgorithmECDSAP256,
		PublicKey: base64.StdEncoding.EncodeToString(pubDer),
		IssuedAt:  issuedAt,
		ExpiresAt: opts.ExpiresAt,
		NotBefore: opts.NotBefore,
	}

	msg, err := computeSignatureMessage(manifest, &sigMeta)
	if err != nil {
		return nil, err
	}

	digest := sha256.Sum256(msg)
	asn1Sig, err := ecdsa.SignASN1(rand.Reader, priv, digest[:])
	if err != nil {
		return nil, fmt.Errorf("failed to sign message with ECDSA: %w", err)
	}

	sigMeta.Signature = base64.StdEncoding.EncodeToString(asn1Sig)

	return &SignedRuntimeManifest{
		SchemaVersion: CurrentSignedManifestSchemaVersion,
		Manifest:      *manifest,
		Signature:     sigMeta,
	}, nil
}

// ValidateSchema verifies the structural integrity and fields of a SignedRuntimeManifest.
func (s *SignedRuntimeManifest) ValidateSchema() error {
	if s == nil {
		return &SafeError{
			Code:        "SIGNED_MANIFEST_EMPTY",
			SafeMessage: "El manifiesto firmado está vacío.",
			Recoverable: false,
			Err:         ErrSignedManifestSchemaInvalid,
		}
	}

	if s.SchemaVersion != CurrentSignedManifestSchemaVersion {
		return &SafeError{
			Code:        "SIGNED_MANIFEST_VERSION_UNSUPPORTED",
			SafeMessage: fmt.Sprintf("Versión de esquema no soportada: %q (esperada: %q)", s.SchemaVersion, CurrentSignedManifestSchemaVersion),
			Recoverable: false,
			Err:         ErrSignedManifestSchemaInvalid,
		}
	}

	if strings.TrimSpace(s.Signature.KeyID) == "" {
		return &SafeError{
			Code:        "SIGNED_MANIFEST_KEY_ID_MISSING",
			SafeMessage: "El identificador de clave de firma es obligatorio.",
			Recoverable: false,
			Err:         ErrSignedManifestSchemaInvalid,
		}
	}

	if s.Signature.Algorithm != SignatureAlgorithmEd25519 && s.Signature.Algorithm != SignatureAlgorithmECDSAP256 {
		return &SafeError{
			Code:        "SIGNED_MANIFEST_ALGORITHM_UNSUPPORTED",
			SafeMessage: fmt.Sprintf("Algoritmo de firma no soportado: %q", s.Signature.Algorithm),
			Recoverable: false,
			Err:         ErrUnsupportedAlgorithm,
		}
	}

	if strings.TrimSpace(s.Signature.Signature) == "" {
		return &SafeError{
			Code:        "SIGNED_MANIFEST_SIGNATURE_MISSING",
			SafeMessage: "La firma criptográfica es obligatoria.",
			Recoverable: false,
			Err:         ErrSignedManifestSchemaInvalid,
		}
	}

	if s.Signature.IssuedAt.IsZero() {
		return &SafeError{
			Code:        "SIGNED_MANIFEST_ISSUED_AT_MISSING",
			SafeMessage: "La fecha de emisión de firma (issued_at) es obligatoria.",
			Recoverable: false,
			Err:         ErrSignedManifestSchemaInvalid,
		}
	}

	// Validate underlying manifest schema
	return s.Manifest.ValidateSchema()
}

// VerifySignedManifestOptions parameterizes verification.
type VerifySignedManifestOptions struct {
	Now            time.Time // Verification time reference (defaults to time.Now() if zero)
	TargetPlatform string    // Target platform constraint (e.g. "darwin")
	TargetArch     string    // Target architecture constraint (e.g. "arm64")
	PackRoot       string    // If set, physically validates all artifacts inside this directory
	RequireRelease bool      // If true, fails if manifest_type == "template"
}

// ValidateSignedManifest performs full cryptographic and semantic validation:
// 1. Validates SignedRuntimeManifest schema.
// 2. Looks up KeyID in the provided TrustStore (trust anchor).
// 3. Verifies key is not revoked.
// 4. Verifies temporal validity window (NotBefore / ExpiresAt).
// 5. Validates cryptographic signature against the canonical manifest payload.
// 6. Optionally validates target platform compatibility and physical pack artifacts.
func ValidateSignedManifest(signed *SignedRuntimeManifest, store TrustStore, opts VerifySignedManifestOptions) error {
	if store == nil {
		return errors.New("trust store is nil")
	}

	if err := signed.ValidateSchema(); err != nil {
		return err
	}

	if opts.RequireRelease && signed.Manifest.IsTemplate() {
		return &SafeError{
			Code:        "SIGNED_MANIFEST_NOT_RELEASE",
			SafeMessage: "El manifiesto firmado es una plantilla de especificación y no un paquete de release sellado.",
			Recoverable: false,
			Err:         ErrManifestTypeNotRelease,
		}
	}

	// 1. Resolve Trust Anchor
	anchor, err := store.GetTrustAnchor(signed.Signature.KeyID)
	if err != nil {
		return &SafeError{
			Code:        "SIGNING_KEY_UNTRUSTED",
			SafeMessage: fmt.Sprintf("La clave de firma %q no pertenece a ningún trust anchor autorizado: %v", signed.Signature.KeyID, err),
			Recoverable: false,
			Err:         ErrUntrustedKey,
		}
	}

	// 2. Revocation check
	if anchor.Revoked {
		return &SafeError{
			Code:        "SIGNING_KEY_REVOKED",
			SafeMessage: fmt.Sprintf("La clave de firma %q fue revocada: %s", anchor.KeyID, anchor.RevocationReason),
			Recoverable: false,
			Details:     anchor.RevocationReason,
			Err:         ErrKeyRevoked,
		}
	}

	// 3. Algorithm agreement
	if anchor.Algorithm != signed.Signature.Algorithm {
		return &SafeError{
			Code:        "SIGNING_ALGORITHM_MISMATCH",
			SafeMessage: fmt.Sprintf("El algoritmo declarado %q no coincide con el algoritmo del trust anchor %q", signed.Signature.Algorithm, anchor.Algorithm),
			Recoverable: false,
			Err:         ErrUnsupportedAlgorithm,
		}
	}

	// 4. Embedded public key check (if present)
	if signed.Signature.PublicKey != "" {
		embeddedKeyBytes, err := decodeKeyOrSig(signed.Signature.PublicKey)
		if err != nil {
			return &SafeError{
				Code:        "EMBEDDED_KEY_MALFORMED",
				SafeMessage: "La clave pública embebida no tiene codificación válida.",
				Recoverable: false,
				Err:         ErrPublicKeyMismatch,
			}
		}
		if !bytes.Equal(anchor.PublicKey, embeddedKeyBytes) {
			return &SafeError{
				Code:        "EMBEDDED_KEY_MISMATCH",
				SafeMessage: "La clave pública embebida en el manifiesto no coincide con el trust anchor configurado.",
				Recoverable: false,
				Err:         ErrPublicKeyMismatch,
			}
		}
	}

	// 5. Time window checks
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	if signed.Signature.NotBefore != nil && now.Before(*signed.Signature.NotBefore) {
		return &SafeError{
			Code:        "MANIFEST_NOT_YET_VALID",
			SafeMessage: fmt.Sprintf("El manifiesto firmado aún no es válido (not_before: %s, actual: %s)", signed.Signature.NotBefore.Format(time.RFC3339), now.Format(time.RFC3339)),
			Recoverable: false,
			Err:         ErrManifestNotYetValid,
		}
	}

	if signed.Signature.ExpiresAt != nil && now.After(*signed.Signature.ExpiresAt) {
		return &SafeError{
			Code:        "MANIFEST_EXPIRED",
			SafeMessage: fmt.Sprintf("El manifiesto firmado ha expirado (expires_at: %s, actual: %s)", signed.Signature.ExpiresAt.Format(time.RFC3339), now.Format(time.RFC3339)),
			Recoverable: false,
			Err:         ErrManifestExpired,
		}
	}

	// 6. Cryptographic signature check
	sigBytes, err := decodeKeyOrSig(signed.Signature.Signature)
	if err != nil {
		return &SafeError{
			Code:        "SIGNATURE_MALFORMED",
			SafeMessage: "La firma no tiene una codificación base64 o hex válida.",
			Recoverable: false,
			Err:         ErrSignatureInvalid,
		}
	}

	msg, err := computeSignatureMessage(&signed.Manifest, &signed.Signature)
	if err != nil {
		return err
	}

	switch anchor.Algorithm {
	case SignatureAlgorithmEd25519:
		if len(anchor.PublicKey) != ed25519.PublicKeySize {
			return &SafeError{
				Code:        "TRUST_ANCHOR_KEY_INVALID",
				SafeMessage: "El trust anchor contiene una clave pública Ed25519 inválida.",
				Recoverable: false,
				Err:         ErrUntrustedKey,
			}
		}
		if len(sigBytes) != ed25519.SignatureSize || !ed25519.Verify(anchor.PublicKey, msg, sigBytes) {
			return &SafeError{
				Code:        "SIGNATURE_VERIFICATION_FAILED",
				SafeMessage: "La firma Ed25519 no pudo ser verificada con el trust anchor.",
				Recoverable: false,
				Err:         ErrSignatureInvalid,
			}
		}

	case SignatureAlgorithmECDSAP256:
		parsedPub, err := x509.ParsePKIXPublicKey(anchor.PublicKey)
		if err != nil {
			return &SafeError{
				Code:        "TRUST_ANCHOR_KEY_INVALID",
				SafeMessage: fmt.Sprintf("No se pudo parsear la clave pública ECDSA del trust anchor: %v", err),
				Recoverable: false,
				Err:         ErrUntrustedKey,
			}
		}
		ecdsaPub, ok := parsedPub.(*ecdsa.PublicKey)
		if !ok || ecdsaPub.Curve != elliptic.P256() {
			return &SafeError{
				Code:        "TRUST_ANCHOR_KEY_INVALID",
				SafeMessage: "El trust anchor no contiene una clave pública válida de la curva P-256.",
				Recoverable: false,
				Err:         ErrUntrustedKey,
			}
		}

		digest := sha256.Sum256(msg)
		if !verifyECDSASignature(ecdsaPub, digest[:], sigBytes) {
			return &SafeError{
				Code:        "SIGNATURE_VERIFICATION_FAILED",
				SafeMessage: "La firma ECDSA P-256 no pudo ser verificada con el trust anchor.",
				Recoverable: false,
				Err:         ErrSignatureInvalid,
			}
		}

	default:
		return &SafeError{
			Code:        "UNSUPPORTED_ALGORITHM",
			SafeMessage: fmt.Sprintf("Algoritmo de firma no soportado: %s", anchor.Algorithm),
			Recoverable: false,
			Err:         ErrUnsupportedAlgorithm,
		}
	}

	// 7. Platform compatibility check (if requested)
	if opts.TargetPlatform != "" && opts.TargetArch != "" {
		if err := signed.Manifest.ValidateCompatibility(opts.TargetPlatform, opts.TargetArch); err != nil {
			return err
		}
	}

	// 8. Physical artifact validation against pack root (if requested)
	if opts.PackRoot != "" {
		if _, err := signed.Manifest.ValidateArtifacts(opts.PackRoot); err != nil {
			return err
		}
	}

	return nil
}

func verifyECDSASignature(pub *ecdsa.PublicKey, digest []byte, sigBytes []byte) bool {
	// First try ASN.1 DER verification
	if ecdsa.VerifyASN1(pub, digest, sigBytes) {
		return true
	}

	// Also support raw IEEE P1363 (r || s, 64 bytes)
	if len(sigBytes) == 64 {
		r := new(big.Int).SetBytes(sigBytes[:32])
		s := new(big.Int).SetBytes(sigBytes[32:])
		return ecdsa.Verify(pub, digest, r, s)
	}

	return false
}

func decodeKeyOrSig(str string) ([]byte, error) {
	str = strings.TrimSpace(str)
	// Try standard base64
	if b, err := base64.StdEncoding.DecodeString(str); err == nil {
		return b, nil
	}
	// Try URL-safe base64
	if b, err := base64.URLEncoding.DecodeString(str); err == nil {
		return b, nil
	}
	// Try hex
	if b, err := hex.DecodeString(str); err == nil {
		return b, nil
	}
	return nil, errors.New("cannot decode as base64 or hex")
}

// ParseSignedRuntimeManifest unmarshals JSON data into a SignedRuntimeManifest.
func ParseSignedRuntimeManifest(data []byte) (*SignedRuntimeManifest, error) {
	if len(data) == 0 {
		return nil, &SafeError{
			Code:        "MANIFEST_EMPTY",
			SafeMessage: "El manifiesto firmado está vacío.",
			Recoverable: false,
			Err:         ErrSignedManifestSchemaInvalid,
		}
	}

	var sm SignedRuntimeManifest
	if err := json.Unmarshal(data, &sm); err != nil {
		return nil, &SafeError{
			Code:        "MANIFEST_INVALID_JSON",
			SafeMessage: fmt.Sprintf("%v: %v", ErrManifestInvalidJSON, err),
			Recoverable: false,
			Details:     err.Error(),
			Err:         ErrManifestInvalidJSON,
		}
	}

	if err := sm.ValidateSchema(); err != nil {
		return nil, err
	}

	return &sm, nil
}

// LoadSignedRuntimeManifest reads and parses a signed runtime manifest from a file.
func LoadSignedRuntimeManifest(path string) (*SignedRuntimeManifest, error) {
	cleanPath := filepath.Clean(path)
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &SafeError{
				Code:        "MANIFEST_NOT_FOUND",
				SafeMessage: fmt.Sprintf("%v: %s", ErrManifestNotFound, cleanPath),
				Recoverable: true,
				Err:         ErrManifestNotFound,
			}
		}
		return nil, &SafeError{
			Code:        "MANIFEST_READ_ERROR",
			SafeMessage: "Error al leer el archivo de manifiesto firmado.",
			Recoverable: true,
			Details:     err.Error(),
			Err:         err,
		}
	}
	return ParseSignedRuntimeManifest(data)
}

// SaveToFile serializes and writes the signed manifest to a file with safe permissions (0644).
func (s *SignedRuntimeManifest) SaveToFile(path string) error {
	if s == nil {
		return errors.New("cannot save nil signed manifest")
	}
	cleanPath := filepath.Clean(path)
	if err := os.MkdirAll(filepath.Dir(cleanPath), 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode signed manifest to json: %w", err)
	}

	tmpFile := fmt.Sprintf("%s.tmp.%d", cleanPath, time.Now().UnixNano())
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return err
	}

	return os.Rename(tmpFile, cleanPath)
}
