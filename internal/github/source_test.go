package github

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guijoazeiro/prodmap/internal/deployment"
	"github.com/guijoazeiro/prodmap/internal/errs"
)

func TestFetchValidatesArtifactHeadersDigestRedirectAndLedger(t *testing.T) {
	ledger := fixtureLedger(t)
	archive := ledgerZIP(t, map[string][]byte{"deployments.jsonl": ledger})
	digest := zipDigest(archive)
	var downloadAuthorization string
	download := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		downloadAuthorization = request.Header.Get("Authorization")
		writer.Write(archive)
	}))
	defer download.Close()
	var listedAuthorization, accept, version, userAgent string
	api := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/zip") {
			writer.Header().Set("Location", download.URL+"/signed?opaque=value")
			writer.WriteHeader(http.StatusFound)
			return
		}
		listedAuthorization = request.Header.Get("Authorization")
		accept, version, userAgent = request.Header.Get("Accept"), request.Header.Get("X-GitHub-Api-Version"), request.Header.Get("User-Agent")
		writeArtifacts(t, writer, []artifactMetadata{testArtifact(7, "ledger", digest, "2026-08-26T12:00:00Z")})
	}))
	defer api.Close()
	source := testSource(t, api.URL, "token-not-for-output")
	source.allowUnsafeRedirectHost = true
	result, err := source.Fetch(t.Context(), deployment.SourceQuery{Owner: "Acme", Repository: "Prodmap", ArtifactName: "ledger", ObservedAt: time.Date(2026, 8, 26, 12, 1, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if listedAuthorization != "Bearer token-not-for-output" || downloadAuthorization != "" || accept != "application/vnd.github+json" || version != apiVersion || userAgent == "" {
		t.Fatalf("headers api=%q/%q/%q/%q download=%q", listedAuthorization, accept, version, userAgent, downloadAuthorization)
	}
	if result.ArtifactID != 7 || result.ArtifactDigest != digest || len(result.Snapshot.Records) != 2 || result.Snapshot.SourceHash == digest || result.Snapshot.SourceKey == "" || result.Repository != "acme/prodmap" {
		t.Fatalf("result=%#v", result)
	}
}

func TestFetchPaginationSelectionAndSourceKeyAreDeterministic(t *testing.T) {
	ledger := fixtureLedger(t)
	archive := ledgerZIP(t, map[string][]byte{"deployments.jsonl": ledger})
	digest := zipDigest(archive)
	var requests atomic.Int64
	api := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/zip") {
			writer.Write(archive)
			return
		}
		requests.Add(1)
		page := request.URL.Query().Get("page")
		if page == "1" {
			items := make([]artifactMetadata, pageSize)
			for index := range items {
				items[index] = testArtifact(int64(index+1), "other", digest, "2026-08-26T11:00:00Z")
			}
			writeArtifacts(t, writer, items)
			return
		}
		// Tie on created_at: the higher ID must win regardless of response order.
		writeArtifacts(t, writer, []artifactMetadata{testArtifact(8, "ledger", digest, "2026-08-26T12:00:00Z"), testArtifact(9, "ledger", digest, "2026-08-26T12:00:00Z")})
	}))
	defer api.Close()
	source := testSource(t, api.URL, "token")
	query := deployment.SourceQuery{Owner: "acme", Repository: "prodmap", ArtifactName: "ledger", ObservedAt: time.Date(2026, 8, 26, 12, 1, 0, 0, time.UTC)}
	result, err := source.Fetch(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 || result.ArtifactID != 9 {
		t.Fatalf("requests=%d result=%#v", requests.Load(), result)
	}
	firstKey := result.Snapshot.SourceKey
	// The mock intentionally has no matching alternate name, so verify logical
	// SourceKey variation through the trusted loader directly.
	first, firstErr := deployment.LoadGitHubActionsLedger(t.Context(), ledger, "acme", "prodmap", "ledger", query.ObservedAt)
	second, secondErr := deployment.LoadGitHubActionsLedger(t.Context(), ledger, "acme", "other", "ledger", query.ObservedAt)
	third, thirdErr := deployment.LoadGitHubActionsLedger(t.Context(), ledger, "acme", "prodmap", "other-ledger", query.ObservedAt)
	if firstErr != nil || secondErr != nil || thirdErr != nil || first.SourceKey == second.SourceKey || first.SourceKey == third.SourceKey || first.SourceKey != firstKey {
		t.Fatalf("source key stability errors=%v/%v/%v keys=%q/%q/%q", firstErr, secondErr, thirdErr, first.SourceKey, second.SourceKey, third.SourceKey)
	}
}

func TestFetchRejectsSanitizedErrorsAndRetries(t *testing.T) {
	ledger := fixtureLedger(t)
	archive := ledgerZIP(t, map[string][]byte{"deployments.jsonl": ledger})
	digest := zipDigest(archive)
	tests := []struct {
		name     string
		status   int
		rate     bool
		want     error
		attempts int64
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, want: errs.ErrUnauthorized, attempts: 1},
		{name: "forbidden", status: http.StatusForbidden, want: errs.ErrUnauthorized, attempts: 1},
		{name: "not found", status: http.StatusNotFound, want: errs.ErrNotFound, attempts: 1},
		{name: "gone", status: http.StatusGone, want: errs.ErrNotFound, attempts: 1},
		{name: "rate limited", status: http.StatusTooManyRequests, want: errs.ErrUnavailable, attempts: 3},
		{name: "rate limited forbidden", status: http.StatusForbidden, rate: true, want: errs.ErrUnavailable, attempts: 3},
		{name: "server retry succeeds", status: http.StatusServiceUnavailable, want: nil, attempts: 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var attempts atomic.Int64
			api := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				attempt := attempts.Add(1)
				if strings.HasSuffix(request.URL.Path, "/zip") {
					writer.Write(archive)
					return
				}
				if tt.name == "server retry succeeds" && attempt == 3 {
					writeArtifacts(t, writer, []artifactMetadata{testArtifact(1, "ledger", digest, "2026-08-26T12:00:00Z")})
					return
				}
				if tt.rate {
					writer.Header().Set("X-RateLimit-Remaining", "0")
				}
				writer.Header().Set("Retry-After", "0")
				writer.WriteHeader(tt.status)
			}))
			defer api.Close()
			source := testSource(t, api.URL, "never-log-token")
			source.sleep = func(context.Context, time.Duration) error { return nil }
			_, err := source.Fetch(t.Context(), deployment.SourceQuery{Owner: "acme", Repository: "prodmap", ArtifactName: "ledger"})
			if tt.want != nil {
				if !errors.Is(err, tt.want) || strings.Contains(fmtString(err), "never-log-token") {
					t.Fatalf("error=%v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if attempts.Load() != tt.attempts {
				t.Fatalf("attempts=%d want=%d", attempts.Load(), tt.attempts)
			}
		})
	}
}

func TestFetchRejectsUnsafeArtifactAndLedgerInputs(t *testing.T) {
	ledger := fixtureLedger(t)
	valid := ledgerZIP(t, map[string][]byte{"deployments.jsonl": ledger})
	cases := []struct {
		name     string
		archive  []byte
		digest   string
		metadata func(artifactMetadata) artifactMetadata
	}{
		{name: "digest mismatch", archive: valid, digest: "sha256:" + strings.Repeat("b", 64)},
		{name: "two entries", archive: ledgerZIP(t, map[string][]byte{"deployments.jsonl": ledger, "other": []byte("x")})},
		{name: "wrong name", archive: ledgerZIP(t, map[string][]byte{"other.jsonl": ledger})},
		{name: "path traversal", archive: ledgerZIP(t, map[string][]byte{"../deployments.jsonl": ledger})},
		{name: "backslash", archive: ledgerZIP(t, map[string][]byte{"dir\\deployments.jsonl": ledger})},
		{name: "empty ledger", archive: ledgerZIP(t, map[string][]byte{"deployments.jsonl": {}})},
		{name: "invalid ledger", archive: ledgerZIP(t, map[string][]byte{"deployments.jsonl": []byte("not-json\n")})},
		{name: "invalid metadata", archive: valid, metadata: func(value artifactMetadata) artifactMetadata { value.Digest = "invalid"; return value }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			digest := tt.digest
			if digest == "" {
				digest = zipDigest(tt.archive)
			}
			api := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if strings.HasSuffix(request.URL.Path, "/zip") {
					writer.Write(tt.archive)
					return
				}
				metadata := testArtifact(1, "ledger", digest, "2026-08-26T12:00:00Z")
				if tt.metadata != nil {
					metadata = tt.metadata(metadata)
				}
				writeArtifacts(t, writer, []artifactMetadata{metadata})
			}))
			defer api.Close()
			_, err := testSource(t, api.URL, "secret").Fetch(t.Context(), deployment.SourceQuery{Owner: "acme", Repository: "prodmap", ArtifactName: "ledger"})
			if err == nil || !(errors.Is(err, errs.ErrIncompatible) || errors.Is(err, errs.ErrNotFound)) || strings.Contains(fmtString(err), "secret") || strings.Contains(fmtString(err), "not-json") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestRedirectPolicyAndCancellation(t *testing.T) {
	if _, err := New(""); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("empty token error=%v", err)
	}
	source, err := New("token")
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"", "https://user:secret@example.test/file", "https://127.0.0.1/file", "https://10.0.0.1/file", "https://169.254.1.1/file", "https://[::1]/file", "http://public.example/file"} {
		if _, err := source.safeRedirect(raw); err == nil {
			t.Fatalf("unsafe redirect accepted: %q", raw)
		}
	}
	contextCanceled, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = source.Fetch(contextCanceled, deployment.SourceQuery{Owner: "acme", Repository: "prodmap", ArtifactName: "ledger"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error=%v", err)
	}
}

func TestFetchLimitsWorkflowValidationAndInputSafety(t *testing.T) {
	ledger := fixtureLedger(t)
	archive := ledgerZIP(t, map[string][]byte{"deployments.jsonl": ledger})
	digest := zipDigest(archive)
	t.Run("page limit", func(t *testing.T) {
		api := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if strings.HasSuffix(request.URL.Path, "/zip") {
				writer.Write(archive)
				return
			}
			items := make([]artifactMetadata, pageSize)
			for index := range items {
				items[index] = testArtifact(int64(index+1), "other", digest, "2026-08-26T12:00:00Z")
			}
			if request.URL.Query().Get("page") == "10" {
				items[0] = testArtifact(1000, "ledger", digest, "2026-08-26T12:00:00Z")
			}
			writeArtifacts(t, writer, items)
		}))
		defer api.Close()
		result, err := testSource(t, api.URL, "token").Fetch(t.Context(), deployment.SourceQuery{Owner: "acme", Repository: "prodmap", ArtifactName: "ledger"})
		if err != nil || result.ArtifactID != 1000 {
			t.Fatalf("result=%#v error=%v", result, err)
		}
	})
	t.Run("workflow mismatch", func(t *testing.T) {
		api := artifactServer(t, archive, func(value *artifactMetadata) { value.WorkflowRun.HeadSHA = strings.Repeat("b", 40) })
		defer api.Close()
		_, err := testSource(t, api.URL, "token").Fetch(t.Context(), deployment.SourceQuery{Owner: "acme", Repository: "prodmap", ArtifactName: "ledger"})
		if !errors.Is(err, errs.ErrIncompatible) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("unverified is retained", func(t *testing.T) {
		unverified := bytes.ReplaceAll(ledger, []byte(`"git_head":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","git_dirty":false,"vcs_revision":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","vcs_revision_verified":true`), []byte(`"git_head":"unknown","git_dirty":true,"vcs_revision":"unknown","vcs_revision_verified":false`))
		api := artifactServer(t, ledgerZIP(t, map[string][]byte{"deployments.jsonl": unverified}), nil)
		defer api.Close()
		result, err := testSource(t, api.URL, "token").Fetch(t.Context(), deployment.SourceQuery{Owner: "acme", Repository: "prodmap", ArtifactName: "ledger"})
		if err != nil || result.Snapshot.Records[0].VCSRevisionVerified {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})
	t.Run("query rejects path manipulation", func(t *testing.T) {
		source := testSource(t, "http://example.test", "token")
		for _, query := range []deployment.SourceQuery{{Owner: "../acme", Repository: "prodmap", ArtifactName: "ledger"}, {Owner: "acme", Repository: "prod/map", ArtifactName: "ledger"}, {Owner: "acme", Repository: "prodmap", ArtifactName: "../ledger"}} {
			if _, err := source.Fetch(t.Context(), query); !errors.Is(err, errs.ErrInvalid) {
				t.Fatalf("query=%#v error=%v", query, err)
			}
		}
	})
}

func TestFetchResponseLimitsAndRetryCancellation(t *testing.T) {
	t.Run("metadata body limit", func(t *testing.T) {
		api := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.Write(bytes.Repeat([]byte("x"), maxMetadataBytes+1))
		}))
		defer api.Close()
		_, err := testSource(t, api.URL, "token").Fetch(t.Context(), deployment.SourceQuery{Owner: "acme", Repository: "prodmap", ArtifactName: "ledger"})
		if !errors.Is(err, errs.ErrIncompatible) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("download content length limit", func(t *testing.T) {
		ledger := fixtureLedger(t)
		archive := ledgerZIP(t, map[string][]byte{"deployments.jsonl": ledger})
		api := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if strings.HasSuffix(request.URL.Path, "/zip") {
				writer.Header().Set("Content-Length", strconv.FormatInt(maxZIPBytes+1, 10))
				writer.WriteHeader(http.StatusOK)
				return
			}
			writeArtifacts(t, writer, []artifactMetadata{testArtifact(1, "ledger", zipDigest(archive), "2026-08-26T12:00:00Z")})
		}))
		defer api.Close()
		_, err := testSource(t, api.URL, "token").Fetch(t.Context(), deployment.SourceQuery{Owner: "acme", Repository: "prodmap", ArtifactName: "ledger"})
		if !errors.Is(err, errs.ErrIncompatible) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("cancellation during retry", func(t *testing.T) {
		api := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer api.Close()
		source := testSource(t, api.URL, "token")
		contextCanceled, cancel := context.WithCancel(t.Context())
		source.sleep = func(context.Context, time.Duration) error { cancel(); return context.Canceled }
		_, err := source.Fetch(contextCanceled, deployment.SourceQuery{Owner: "acme", Repository: "prodmap", ArtifactName: "ledger"})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestUnzipLedgerRejectsUnsafeZIPShapes(t *testing.T) {
	ledger := fixtureLedger(t)
	valid := ledgerZIP(t, map[string][]byte{"deployments.jsonl": ledger})
	symlink := ledgerZIPHeader(t, "deployments.jsonl", ledger, os.ModeSymlink|0o777)
	tests := []struct {
		name    string
		archive []byte
	}{
		{name: "empty", archive: ledgerZIP(t, map[string][]byte{})},
		{name: "directory", archive: ledgerZIP(t, map[string][]byte{"deployments.jsonl/": {}})},
		{name: "absolute", archive: ledgerZIP(t, map[string][]byte{"/deployments.jsonl": ledger})},
		{name: "symlink", archive: symlink},
		{name: "truncated", archive: valid[:len(valid)-4]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := unzipLedger(tt.archive); !errors.Is(err, errs.ErrIncompatible) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func artifactServer(t *testing.T, archive []byte, modify func(*artifactMetadata)) *httptest.Server {
	t.Helper()
	digest := zipDigest(archive)
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/zip") {
			writer.Write(archive)
			return
		}
		metadata := testArtifact(1, "ledger", digest, "2026-08-26T12:00:00Z")
		if modify != nil {
			modify(&metadata)
		}
		writeArtifacts(t, writer, []artifactMetadata{metadata})
	}))
}

func testSource(t *testing.T, base, token string) *Source {
	t.Helper()
	source, err := New(token, withBaseURL(base), withHTTPClient(&http.Client{}), withClock(func() time.Time { return time.Date(2026, 8, 26, 12, 1, 0, 0, time.UTC) }))
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func testArtifact(id int64, name, digest, created string) artifactMetadata {
	artifact := artifactMetadata{ID: id, Name: name, Size: 1024, Digest: digest, CreatedAt: created, UpdatedAt: created, ExpiresAt: "2026-09-26T12:00:00Z"}
	artifact.WorkflowRun.ID = 11
	artifact.WorkflowRun.HeadSHA = strings.Repeat("a", 40)
	return artifact
}

func writeArtifacts(t *testing.T, writer http.ResponseWriter, artifacts []artifactMetadata) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(artifactList{Artifacts: artifacts}); err != nil {
		t.Fatal(err)
	}
}

func fixtureLedger(t *testing.T) []byte {
	t.Helper()
	contents, err := io.ReadAll(mustOpen(t, filepath.Join("..", "..", "testdata", "deployment", "valid-two-services.jsonl")))
	if err != nil {
		t.Fatal(err)
	}
	return contents
}

func mustOpen(t *testing.T, path string) io.ReadCloser {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func ledgerZIP(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, contents := range entries {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(contents); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func ledgerZIPHeader(t *testing.T, name string, contents []byte, mode os.FileMode) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	header := &zip.FileHeader{Name: name, Method: zip.Deflate}
	header.SetMode(mode)
	entry, err := writer.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(contents); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func zipDigest(contents []byte) string {
	digest := sha256.Sum256(contents)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func fmtString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
