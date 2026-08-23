package inventory

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeServiceLogicalKeyIsSharedAndNamespaceAware(t *testing.T) {
	if got := NormalizeServiceLogicalKey(" Checkout API ", " Payments Team "); got != "payments-team.checkout-api" {
		t.Fatalf("namespaced logical key = %q", got)
	}
	if got := NormalizeServiceLogicalKey("Checkout API", ""); got != "checkout-api" {
		t.Fatalf("unnamespaced logical key = %q", got)
	}
}

func TestNormalizeArtifactAcceptsOnlyValidatedDigests(t *testing.T) {
	sha256Lower := strings.Repeat("a", 64)
	sha256Upper := strings.Repeat("A", 64)
	sha512Lower := strings.Repeat("b", 128)
	tests := []struct {
		name     string
		repo     string
		imageID  string
		wantKind string
		want     string
	}{
		{name: "sha256 repository digest", repo: "registry.example/api@sha256:" + sha256Lower, wantKind: "repo_digest", want: "sha256:" + sha256Lower},
		{name: "sha512 repository digest", repo: "registry.example/api@sha512:" + sha512Lower, wantKind: "repo_digest", want: "sha512:" + sha512Lower},
		{name: "uppercase normalized", repo: "registry.example/api@SHA256:" + sha256Upper, wantKind: "repo_digest", want: "sha256:" + sha256Lower},
		{name: "valid image ID", imageID: "sha256:" + sha256Lower, wantKind: "image_id", want: "sha256:" + sha256Lower},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation := RuntimeObservation{ImageReference: "api:latest", ImageID: test.imageID}
			if test.repo != "" {
				observation.RepoDigests = []string{test.repo}
			}
			artifact, conflict := NormalizeArtifact(observation)
			if conflict || artifact.IdentityKind != test.wantKind || artifact.Identity != test.want {
				t.Fatalf("NormalizeArtifact() = %+v, conflict=%t", artifact, conflict)
			}
		})
	}
}

func TestNormalizeArtifactRejectsMalformedDigests(t *testing.T) {
	valid := strings.Repeat("a", 64)
	tests := []struct {
		name   string
		digest string
	}{
		{name: "short sha256", digest: "sha256:" + strings.Repeat("a", 63)},
		{name: "long sha256", digest: "sha256:" + strings.Repeat("a", 65)},
		{name: "non hexadecimal", digest: "sha256:" + strings.Repeat("g", 64)},
		{name: "unknown algorithm", digest: "md5:" + strings.Repeat("a", 32)},
		{name: "empty value", digest: "sha256:"},
		{name: "leading whitespace", digest: " sha256:" + valid},
		{name: "trailing whitespace", digest: "sha256:" + valid + " "},
		{name: "internal whitespace", digest: "sha256:" + strings.Repeat("a", 31) + " " + strings.Repeat("a", 32)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			artifact, conflict := NormalizeArtifact(RuntimeObservation{
				ImageReference: "api:latest",
				RepoDigests:    []string{"registry.example/api@" + test.digest},
			})
			if conflict || artifact.IdentityKind != "mutable_tag" || artifact.Identity != "api:latest" {
				t.Fatalf("invalid digest became immutable: %+v conflict=%t", artifact, conflict)
			}
			if !containsString(artifact.IdentityIssues, issueInvalidRepoDigest) {
				t.Fatalf("identity issues = %#v, want invalid digest issue", artifact.IdentityIssues)
			}
		})
	}
}

func TestNormalizeArtifactFallsBackToValidImageIDWithEvidence(t *testing.T) {
	imageDigest := strings.Repeat("b", 64)
	artifact, conflict := NormalizeArtifact(RuntimeObservation{
		ImageReference: "api:latest",
		ImageID:        "sha256:" + imageDigest,
		RepoDigests:    []string{"registry.example/api@sha256:short"},
	})
	if conflict || artifact.IdentityKind != "image_id" || artifact.Identity != "sha256:"+imageDigest {
		t.Fatalf("fallback artifact = %+v conflict=%t", artifact, conflict)
	}
	if !containsString(artifact.IdentityIssues, issueInvalidRepoDigest) || !containsString(artifact.IdentityIssues, issueImageIDFallback) {
		t.Fatalf("fallback was not explained: %#v", artifact.IdentityIssues)
	}
}

func TestNormalizeArtifactDetectsDifferentValidDigests(t *testing.T) {
	artifact, conflict := NormalizeArtifact(RuntimeObservation{
		RepoDigests: []string{
			"registry.example/api@sha256:" + strings.Repeat("a", 64),
			"registry.example/api@sha256:" + strings.Repeat("b", 64),
		},
	})
	if !conflict || artifact.IdentityKind != "repo_digest" {
		t.Fatalf("artifact = %+v conflict=%t", artifact, conflict)
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

func TestNormalizeArtifactSanitizesOCILabelsByField(t *testing.T) {
	secret := "must-not-persist"
	artifact, _ := NormalizeArtifact(RuntimeObservation{
		ImageReference: "api:latest",
		OCILabels: map[string]string{
			ociRevisionLabel:     "token=" + secret + "\n",
			ociSourceLabel:       "https://user:" + secret + "@EXAMPLE.invalid/%72epo?token=" + secret + "#" + secret,
			ociCreatedLabel:      "2026-08-19T13:30:00-03:00",
			ociVersionLabel:      "token-" + secret,
			"com.example.secret": secret,
		},
	})
	if artifact.OCIRevision != "<invalid>" || !artifact.RevisionInvalid {
		t.Fatalf("invalid revision was not replaced: %+v", artifact)
	}
	if got := artifact.OCILabels[ociSourceLabel]; got != "https://example.invalid/repo" {
		t.Fatalf("canonical source = %q", got)
	}
	if got := artifact.OCILabels[ociCreatedLabel]; got != "2026-08-19T16:30:00Z" {
		t.Fatalf("canonical created = %q", got)
	}
	if _, exists := artifact.OCILabels[ociVersionLabel]; exists {
		t.Fatalf("version survived Phase 1 policy: %#v", artifact.OCILabels)
	}
	if !containsString(artifact.MetadataIssues, issueDiscardedVersion) {
		t.Fatalf("version discard was not explained: %#v", artifact.MetadataIssues)
	}
	encoded, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "com.example.secret") {
		t.Fatalf("sensitive label data survived normalization: %s", encoded)
	}
}

func TestNormalizeArtifactDiscardsUnsafeOptionalMetadataWithoutEcho(t *testing.T) {
	secret := "opaque-secret-0123456789"
	tests := []struct {
		name   string
		labels map[string]string
		issue  string
	}{
		{name: "unexpected source scheme", labels: map[string]string{ociSourceLabel: "file:///tmp/" + secret}, issue: issueInvalidOCISource},
		{name: "invalid source", labels: map[string]string{ociSourceLabel: secret}, issue: issueInvalidOCISource},
		{name: "invalid percent encoding", labels: map[string]string{ociSourceLabel: "https://example.invalid/%zz" + secret}, issue: issueInvalidOCISource},
		{name: "invalid created", labels: map[string]string{ociCreatedLabel: secret}, issue: issueInvalidOCICreated},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			artifact, _ := NormalizeArtifact(RuntimeObservation{ImageReference: "api:latest", OCILabels: test.labels})
			encoded, err := json.Marshal(artifact)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), secret) {
				t.Fatalf("unsafe raw metadata survived: %s", encoded)
			}
			if !containsString(artifact.MetadataIssues, test.issue) {
				t.Fatalf("metadata issues = %#v, want %q", artifact.MetadataIssues, test.issue)
			}
		})
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
