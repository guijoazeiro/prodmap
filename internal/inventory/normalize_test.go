package inventory

import (
	"strings"
	"testing"
)

func TestNormalizeArtifactPriorityAndConflict(t *testing.T) {
	observation := RuntimeObservation{
		ImageReference: "registry.example/api:latest",
		ImageID:        "sha256:local",
		RepoDigests:    []string{"registry.example/api@sha256:abc", "registry.example/api@sha256:abc"},
		RepoTags:       []string{"registry.example/api:latest"},
		OCILabels:      map[string]string{"org.opencontainers.image.revision": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}
	artifact, conflict := NormalizeArtifact(observation)
	if conflict || artifact.IdentityKind != "repo_digest" || artifact.Identity != "sha256:abc" || artifact.OCIRevision == "" {
		t.Fatalf("artifact = %+v conflict=%v", artifact, conflict)
	}

	observation.RepoDigests = append(observation.RepoDigests, "registry.example/api@sha256:def")
	_, conflict = NormalizeArtifact(observation)
	if !conflict {
		t.Fatal("conflicting digests were not detected")
	}
}

func TestNormalizeArtifactUsesImageIDThenMutableTag(t *testing.T) {
	artifact, _ := NormalizeArtifact(RuntimeObservation{ImageReference: "api:latest", ImageID: "sha256:abc"})
	if artifact.IdentityKind != "image_id" || artifact.Identity != "sha256:abc" {
		t.Fatalf("image ID artifact = %+v", artifact)
	}
	artifact, _ = NormalizeArtifact(RuntimeObservation{ImageReference: "api:latest"})
	if artifact.IdentityKind != "mutable_tag" || artifact.Identity != "api:latest" {
		t.Fatalf("mutable artifact = %+v", artifact)
	}
}

func TestNormalizeServiceKeyDoesNotUseContainerName(t *testing.T) {
	got := NormalizeServiceKey(RuntimeObservation{ExternalID: "container-1", ContainerName: "attacker-controlled", ImageReference: "registry.example/team/Checkout-API:latest"})
	if got != "checkout-api" {
		t.Fatalf("NormalizeServiceKey() = %q", got)
	}
}

func TestNormalizeServiceKeySanitizesUntrustedReference(t *testing.T) {
	got := NormalizeServiceKey(RuntimeObservation{ExternalID: "container-1", ImageReference: "registry.example/team/API\x1b[31m:latest"})
	if got != "api-31m" || strings.ContainsRune(got, '\x1b') {
		t.Fatalf("NormalizeServiceKey(untrusted) = %q", got)
	}
}

func TestNormalizeArtifactAllowlistsAndRedactsOCILabels(t *testing.T) {
	artifact, _ := NormalizeArtifact(RuntimeObservation{
		ImageReference: "api:latest",
		OCILabels: map[string]string{
			"org.opencontainers.image.revision": "token=must-not-persist\n",
			"org.opencontainers.image.source":   "https://example.invalid/repository",
			"com.example.secret":                "must-not-persist",
		},
	})
	if artifact.OCIRevision != "<invalid>" || artifact.OCILabels["org.opencontainers.image.revision"] != "<invalid>" {
		t.Fatalf("invalid revision was not replaced: %+v", artifact)
	}
	if artifact.OCILabels["org.opencontainers.image.source"] == "" {
		t.Fatalf("allowlisted source was discarded: %+v", artifact.OCILabels)
	}
	encoded := artifact.OCIRevision
	for key, value := range artifact.OCILabels {
		encoded += key + value
	}
	if strings.Contains(encoded, "must-not-persist") || strings.Contains(encoded, "com.example.secret") {
		t.Fatalf("sensitive label data survived normalization: %q", encoded)
	}
}
