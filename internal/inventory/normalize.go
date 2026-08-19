package inventory

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"unicode"
)

var allowedOCIMetadata = map[string]int{
	"org.opencontainers.image.revision": 128,
	"org.opencontainers.image.source":   2048,
	"org.opencontainers.image.version":  255,
	"org.opencontainers.image.created":  128,
}

// NormalizeArtifact preserves immutable identity separately from mutable aliases.
func NormalizeArtifact(observation RuntimeObservation) (Artifact, bool) {
	digests := uniqueSorted(observation.RepoDigests)
	aliases := uniqueSorted(append(append([]string{}, observation.RepoTags...), observation.ImageReference))
	ociLabels := normalizeOCILabels(observation.OCILabels)
	artifact := Artifact{
		ImageID: observation.ImageID, ObservedReference: observation.ImageReference, Aliases: aliases,
		OCIRevision: ociLabels["org.opencontainers.image.revision"], OCILabels: ociLabels,
	}
	conflict := false
	if len(digests) > 0 {
		algorithm, value := splitDigest(digests[0])
		artifact.Name = strings.SplitN(digests[0], "@", 2)[0]
		artifact.IdentityKind = "repo_digest"
		artifact.Identity = algorithm + ":" + value
		artifact.DigestAlgorithm = algorithm
		artifact.Digest = value
		for _, candidate := range digests[1:] {
			otherAlgorithm, otherValue := splitDigest(candidate)
			if otherAlgorithm != algorithm || otherValue != value {
				conflict = true
			}
		}
		return artifact, conflict
	}
	if algorithm, value := splitDigest(observation.ImageID); value != "" {
		artifact.Name = observation.ImageReference
		artifact.IdentityKind = "image_id"
		artifact.Identity = algorithm + ":" + value
		artifact.DigestAlgorithm = algorithm
		artifact.Digest = value
		return artifact, false
	}
	artifact.Name = observation.ImageReference
	artifact.IdentityKind = "mutable_tag"
	artifact.Identity = observation.ImageReference
	return artifact, false
}

func normalizeOCILabels(values map[string]string) map[string]string {
	result := make(map[string]string)
	for key, limit := range allowedOCIMetadata {
		value, ok := values[key]
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if key == "org.opencontainers.image.revision" {
			if fullSHA.MatchString(value) {
				result[key] = strings.ToLower(value)
			} else {
				// Preserve the fact that the declared value was invalid without
				// retaining an attacker-controlled label value.
				result[key] = "<invalid>"
			}
			continue
		}
		if len(value) > limit || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			continue
		}
		result[key] = value
	}
	return result
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

func splitDigest(value string) (string, string) {
	if at := strings.LastIndexByte(value, '@'); at >= 0 {
		value = value[at+1:]
	}
	algorithm, digest, ok := strings.Cut(value, ":")
	if !ok || algorithm == "" || digest == "" {
		return "", ""
	}
	return strings.ToLower(algorithm), strings.ToLower(digest)
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
