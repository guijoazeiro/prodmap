// Package redaction provides deterministic, field-aware validation for public
// values that can cross Prodmap's offline and read-only boundaries.
package redaction

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ErrSensitive is deliberately value-free so callers can report rejection
// without exposing the rejected content.
var ErrSensitive = errors.New("sensitive public content")

// ValueKind identifies the contract category of a public string.
type ValueKind string

const (
	Identifier ValueKind = "identifier"
	FreeText   ValueKind = "free_text"
)

var (
	explicitAssignment  = regexp.MustCompile(`(?i)(?:^|[\s,{])"?(?:token|access[_-]?token|refresh[_-]?token|id[_-]?token|password|passwd|api[_-]?key|client[_-]?secret)"?\s*(?:=|:)\s*"?[^\s,}"']+`)
	authorizationHeader = regexp.MustCompile(`(?i)\bauthorization\s*:\s*(?:bearer|basic)\s+\S+`)
	bearerCredential    = regexp.MustCompile(`(?i)^\s*bearer\s+[A-Za-z0-9._~+/=-]{8,}\s*$`)
)

// ProhibitedKey reports whether a field name itself can carry content that is
// outside the allowlisted public contracts. It recognizes common naming styles
// without treating substrings such as "tokenizer" as secrets.
func ProhibitedKey(key string) bool {
	canonical := canonicalKey(key)
	if canonical == "" {
		return false
	}
	switch canonical {
	case "token", "access_token", "refresh_token", "id_token",
		"api_key", "private_key", "client_secret",
		"password", "passwd", "authorization", "proxy_authorization",
		"cookie", "set_cookie", "dsn", "body",
		"request_body", "response_body", "payload",
		"external_id", "git_head", "vcs_revision",
		"image_reference", "image_id", "artifact_identity":
		return true
	}
	return canonical == "raw" || strings.HasPrefix(canonical, "raw_")
}

// ValidatePublicValue rejects controls, credentials, private material, local
// paths, and unsafe URL forms. It intentionally accepts capability names such
// as "token-service" and ordinary prose mentioning authentication concepts.
func ValidatePublicValue(kind ValueKind, value string) error {
	if kind != Identifier && kind != FreeText {
		return ErrSensitive
	}
	if !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return ErrSensitive
	}
	if containsAbsolutePath(value) || ContainsCredential(value) {
		return ErrSensitive
	}
	return nil
}

// ContainsCredential recognizes high-confidence secret-bearing structures.
// It does not reject individual words such as "token" or "password".
func ContainsCredential(value string) bool {
	trimmed := strings.TrimSpace(value)
	lower := strings.ToLower(trimmed)
	if authorizationHeader.MatchString(trimmed) || bearerCredential.MatchString(trimmed) || explicitAssignment.MatchString(trimmed) {
		return true
	}
	if strings.Contains(lower, "-----begin") && strings.Contains(lower, "private key-----") {
		return true
	}
	if hasJWT(trimmed) || hasSensitiveURL(trimmed) {
		return true
	}
	return strings.HasPrefix(lower, "postgres://") || strings.HasPrefix(lower, "postgresql://") ||
		strings.HasPrefix(lower, "mysql://") || strings.HasPrefix(lower, "sqlite:") || strings.HasPrefix(lower, "file:")
}

func canonicalKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var result strings.Builder
	previousLetter := false
	lastSeparator := false
	var previous rune
	runes := []rune(value)
	for index, character := range runes {
		if character == '_' || character == '-' || character == '.' || unicode.IsSpace(character) {
			if result.Len() > 0 && !lastSeparator {
				result.WriteByte('_')
			}
			lastSeparator = true
			previousLetter = false
			previous = 0
			continue
		}
		nextLower := index+1 < len(runes) && unicode.IsLower(runes[index+1])
		if unicode.IsUpper(character) && previousLetter && (unicode.IsLower(previous) || unicode.IsDigit(previous) || (unicode.IsUpper(previous) && nextLower)) && !lastSeparator {
			result.WriteByte('_')
		}
		result.WriteRune(unicode.ToLower(character))
		lastSeparator = false
		previousLetter = unicode.IsLetter(character) || unicode.IsDigit(character)
		previous = character
	}
	return strings.Trim(result.String(), "_")
}

func containsAbsolutePath(value string) bool {
	for _, candidate := range strings.Fields(value) {
		candidate = strings.Trim(candidate, `"'()[]{}.,;`)
		if strings.HasPrefix(candidate, "/") || strings.HasPrefix(candidate, "\\") {
			return true
		}
		if len(candidate) >= 3 && ((candidate[0] >= 'a' && candidate[0] <= 'z') || (candidate[0] >= 'A' && candidate[0] <= 'Z')) && candidate[1] == ':' && (candidate[2] == '/' || candidate[2] == '\\') {
			return true
		}
	}
	return false
}

func hasSensitiveURL(value string) bool {
	for _, candidate := range strings.Fields(value) {
		parsed, err := url.Parse(strings.Trim(candidate, `"'()[]{}.,;`))
		if err == nil && parsed.Scheme != "" && parsed.User != nil {
			return true
		}
	}
	return false
}

func hasJWT(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 3 || len(parts[0]) < 8 || len(parts[1]) < 8 || len(parts[2]) < 8 {
		return false
	}
	for _, part := range parts {
		if strings.ContainsAny(part, " \t\r\n") {
			return false
		}
	}
	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || !json.Valid(header) {
		return false
	}
	var document map[string]any
	return json.Unmarshal(header, &document) == nil && document != nil
}
