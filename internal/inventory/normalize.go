package inventory

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	ociRevisionLabel = "org.opencontainers.image.revision"
	ociSourceLabel   = "org.opencontainers.image.source"
	ociVersionLabel  = "org.opencontainers.image.version"
	ociCreatedLabel  = "org.opencontainers.image.created"

	issueInvalidRepoDigest = "invalid repository digest was discarded"
	issueInvalidImageID    = "invalid image ID was discarded"
	issueImageIDFallback   = "valid image ID was used because repository digests were unusable"
	issueInvalidOCISource  = "OCI source metadata could not be safely canonicalized and was discarded"
	issueDiscardedVersion  = "OCI version metadata is not retained by the Phase 1 safety policy"
	issueInvalidOCICreated = "OCI creation timestamp was invalid and was discarded"
)

const maxOCISourceLength = 2048

type normalizedDigest struct {
	name      string
	algorithm string
	value     string
}

// NormalizeArtifact preserves immutable identity separately from mutable aliases.
func NormalizeArtifact(observation RuntimeObservation) (Artifact, bool) {
	aliases := uniqueSorted(append(append([]string{}, observation.RepoTags...), observation.ImageReference))
	ociLabels, metadataIssues, revisionInvalid := normalizeOCILabels(observation.OCILabels)
	artifact := Artifact{
		ObservedReference: observation.ImageReference,
		Aliases:           aliases,
		OCIRevision:       ociLabels[ociRevisionLabel],
		OCILabels:         ociLabels,
		MetadataIssues:    metadataIssues,
		RevisionInvalid:   revisionInvalid,
	}
	imageID, validImageID := parseBareDigest(observation.ImageID)
	if validImageID {
		artifact.ImageID = imageID.algorithm + ":" + imageID.value
	} else if observation.ImageID != "" {
		artifact.IdentityIssues = append(artifact.IdentityIssues, issueInvalidImageID)
	}

	validRepoDigests := make([]normalizedDigest, 0, len(observation.RepoDigests))
	invalidRepoDigest := false
	for _, raw := range observation.RepoDigests {
		candidate, ok := parseRepositoryDigest(raw)
		if !ok {
			invalidRepoDigest = true
			continue
		}
		validRepoDigests = append(validRepoDigests, candidate)
	}
	if invalidRepoDigest {
		artifact.IdentityIssues = append(artifact.IdentityIssues, issueInvalidRepoDigest)
	}

	if len(validRepoDigests) > 0 {
		sort.Slice(validRepoDigests, func(i, j int) bool {
			left := validRepoDigests[i].algorithm + ":" + validRepoDigests[i].value + "@" + validRepoDigests[i].name
			right := validRepoDigests[j].algorithm + ":" + validRepoDigests[j].value + "@" + validRepoDigests[j].name
			return left < right
		})
		selected := validRepoDigests[0]
		artifact.Name = selected.name
		artifact.IdentityKind = "repo_digest"
		artifact.Identity = selected.algorithm + ":" + selected.value
		artifact.DigestAlgorithm = selected.algorithm
		artifact.Digest = selected.value

		conflict := false
		for _, candidate := range validRepoDigests[1:] {
			if candidate.algorithm != selected.algorithm || candidate.value != selected.value {
				conflict = true
				break
			}
		}
		return artifact, conflict
	}

	if validImageID {
		artifact.Name = observation.ImageReference
		artifact.IdentityKind = "image_id"
		artifact.Identity = imageID.algorithm + ":" + imageID.value
		artifact.DigestAlgorithm = imageID.algorithm
		artifact.Digest = imageID.value
		if invalidRepoDigest {
			artifact.IdentityIssues = append(artifact.IdentityIssues, issueImageIDFallback)
		}
		return artifact, false
	}
	artifact.Name = observation.ImageReference
	artifact.IdentityKind = "mutable_tag"
	artifact.Identity = observation.ImageReference
	return artifact, false
}

func parseRepositoryDigest(value string) (normalizedDigest, bool) {
	if strings.Count(value, "@") != 1 {
		return normalizedDigest{}, false
	}
	name, rawDigest, _ := strings.Cut(value, "@")
	if name == "" || name != strings.TrimSpace(name) || strings.IndexFunc(name, unicode.IsSpace) >= 0 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return normalizedDigest{}, false
	}
	digest, ok := parseBareDigest(rawDigest)
	if !ok {
		return normalizedDigest{}, false
	}
	digest.name = name
	return digest, true
}

func parseBareDigest(value string) (normalizedDigest, bool) {
	if value == "" || value != strings.TrimSpace(value) || strings.IndexFunc(value, unicode.IsSpace) >= 0 || strings.Count(value, ":") != 1 {
		return normalizedDigest{}, false
	}
	algorithm, digest, _ := strings.Cut(value, ":")
	algorithm = strings.ToLower(algorithm)
	wantLength := 0
	switch algorithm {
	case "sha256":
		wantLength = 64
	case "sha512":
		wantLength = 128
	default:
		return normalizedDigest{}, false
	}
	if len(digest) != wantLength {
		return normalizedDigest{}, false
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return normalizedDigest{}, false
	}
	return normalizedDigest{algorithm: algorithm, value: strings.ToLower(digest)}, true
}

func normalizeOCILabels(values map[string]string) (map[string]string, []string, bool) {
	result := make(map[string]string)
	issues := make([]string, 0, 3)
	revisionInvalid := false

	if raw, exists := values[ociRevisionLabel]; exists {
		value := strings.TrimSpace(raw)
		if value != "" {
			if fullSHA.MatchString(value) {
				result[ociRevisionLabel] = strings.ToLower(value)
			} else {
				result[ociRevisionLabel] = "<invalid>"
				revisionInvalid = true
			}
		}
	}

	if raw, exists := values[ociSourceLabel]; exists && strings.TrimSpace(raw) != "" {
		if canonical, ok := canonicalizeOCISource(raw); ok {
			result[ociSourceLabel] = canonical
		} else {
			issues = append(issues, issueInvalidOCISource)
		}
	}

	if raw, exists := values[ociCreatedLabel]; exists && strings.TrimSpace(raw) != "" {
		parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw))
		if err != nil {
			issues = append(issues, issueInvalidOCICreated)
		} else {
			result[ociCreatedLabel] = parsed.UTC().Format(time.RFC3339Nano)
		}
	}

	if raw, exists := values[ociVersionLabel]; exists && strings.TrimSpace(raw) != "" {
		issues = append(issues, issueDiscardedVersion)
	}

	return result, uniqueSorted(issues), revisionInvalid
}

func canonicalizeOCISource(raw string) (string, bool) {
	if len(raw) > maxOCISourceLength*4 || raw != strings.TrimSpace(raw) || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Opaque != "" {
		return "", false
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	switch parsed.Scheme {
	case "https", "http", "ssh", "git":
	default:
		return "", false
	}
	canonicalURL := url.URL{
		Scheme: parsed.Scheme,
		Host:   strings.ToLower(parsed.Host),
		Path:   parsed.Path,
	}
	canonical := canonicalURL.String()
	if canonical == "" || len(canonical) > maxOCISourceLength || strings.IndexFunc(canonical, unicode.IsControl) >= 0 {
		return "", false
	}
	return canonical, true
}

func NormalizeServiceKey(observation RuntimeObservation) string {
	base := strings.TrimSpace(observation.ImageReference)
	if at := strings.IndexByte(base, '@'); at >= 0 {
		base = base[:at]
	}
	if slash := strings.LastIndexByte(base, '/'); slash >= 0 {
		base = base[slash+1:]
	}
	if colon := strings.LastIndexByte(base, ':'); colon >= 0 {
		base = base[:colon]
	}
	base = normalizeLogicalKey(base)
	if base != "" {
		return base
	}
	digest := sha256.Sum256([]byte(observation.ExternalID))
	return "container-" + hex.EncodeToString(digest[:6])
}

func normalizeLogicalKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var normalized strings.Builder
	previousSeparator := false
	for _, character := range value {
		allowed := (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '.' || character == '_' || character == '-'
		if allowed {
			normalized.WriteRune(character)
			previousSeparator = false
			continue
		}
		if !previousSeparator {
			normalized.WriteByte('-')
			previousSeparator = true
		}
	}
	result := strings.Trim(normalized.String(), "-._")
	if len(result) <= 255 {
		return result
	}
	digest := sha256.Sum256([]byte(result))
	return strings.TrimRight(result[:240], "-._") + "-" + hex.EncodeToString(digest[:6])
}

func uniqueSorted(values []string) []string {
	set := make(map[string]struct{})
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			set[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
