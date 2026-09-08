package investigationpackage

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/guijoazeiro/prodmap/internal/baseline"
	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/investigation"
	"github.com/guijoazeiro/prodmap/internal/regression"
	"github.com/guijoazeiro/prodmap/internal/timeline"
	"github.com/guijoazeiro/prodmap/internal/topology"
)

func TestCreateAndVerifyPackage(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 123, time.UTC)
	path := filepath.Join(t.TempDir(), "investigation.zip")
	created, err := Create(t.Context(), CreateRequest{OutputPath: path, CreatedAt: now, Investigation: validInvestigation(now)})
	if err != nil {
		t.Fatal(err)
	}
	if created.FileName != "investigation.zip" || !validSHA256(created.PackageSHA256) || !validInvestigationKey(created.InvestigationKey) || created.PackageSize <= 0 {
		t.Fatalf("created=%+v", created)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions=%#o", info.Mode().Perm())
	}
	archive, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.File) != 3 {
		t.Fatalf("entries=%d", len(archive.File))
	}
	names := make([]string, 0, len(archive.File))
	for _, file := range archive.File {
		names = append(names, file.Name)
	}
	archive.Close()
	if strings.Join(names, ",") != "SHA256SUMS,investigation.json,manifest.json" {
		t.Fatalf("entries=%v", names)
	}
	files := packageFiles(t, path)
	var manifestValue manifest
	if err := json.Unmarshal(files["manifest.json"], &manifestValue); err != nil {
		t.Fatal(err)
	}
	if manifestValue.Investigation.Name != "investigation.json" || manifestValue.Investigation.SHA256 != digest(files["investigation.json"]) || manifestValue.Investigation.Size != int64(len(files["investigation.json"])) || !bytes.Equal(files["SHA256SUMS"], checksumFile(files["manifest.json"], files["investigation.json"])) {
		t.Fatalf("manifest=%+v sums=%q", manifestValue, files["SHA256SUMS"])
	}
	verified, err := Verify(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if verified.InvestigationKey != created.InvestigationKey || verified.CreatedAt != now.Format(time.RFC3339Nano) || verified.CausalityClaimed || verified.RedactionProfile != RedactionProfile {
		t.Fatalf("verified=%+v", verified)
	}

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Create(t.Context(), CreateRequest{OutputPath: path, CreatedAt: now, Investigation: validInvestigation(now)})
	if !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("existing file error=%v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("existing package was overwritten")
	}
}

func TestVerifyRejectsUnsafeOrInvalidZIPs(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	valid := validFiles(t, now)
	tests := map[string]func(map[string][]byte) []zipFixture{
		"missing": func(files map[string][]byte) []zipFixture { return entriesWithout(files, "SHA256SUMS") },
		"extra": func(files map[string][]byte) []zipFixture {
			return append(entriesFrom(files), zipFixture{Name: "extra.txt", Data: []byte("x")})
		},
		"duplicate": func(files map[string][]byte) []zipFixture {
			return append(entriesFrom(files), zipFixture{Name: "manifest.json", Data: files["manifest.json"]})
		},
		"traversal": func(files map[string][]byte) []zipFixture {
			return append(entriesWithout(files, "SHA256SUMS"), zipFixture{Name: "../SHA256SUMS", Data: files["SHA256SUMS"]})
		},
		"directory": func(files map[string][]byte) []zipFixture {
			return append(entriesWithout(files, "SHA256SUMS"), zipFixture{Name: "SHA256SUMS", Data: files["SHA256SUMS"], Mode: os.ModeDir | 0o755})
		},
		"symlink": func(files map[string][]byte) []zipFixture {
			return append(entriesWithout(files, "SHA256SUMS"), zipFixture{Name: "SHA256SUMS", Data: files["SHA256SUMS"], Mode: os.ModeSymlink | 0o777})
		},
		"individual limit": func(files map[string][]byte) []zipFixture {
			return []zipFixture{{Name: "manifest.json", Data: bytes.Repeat([]byte("m"), maxEntrySize+1)}, {Name: "investigation.json", Data: files["investigation.json"]}, {Name: "SHA256SUMS", Data: files["SHA256SUMS"]}}
		},
		"total limit": func(files map[string][]byte) []zipFixture {
			return []zipFixture{{Name: "manifest.json", Data: bytes.Repeat([]byte("m"), 3<<20)}, {Name: "investigation.json", Data: bytes.Repeat([]byte("i"), 3<<20)}, {Name: "SHA256SUMS", Data: bytes.Repeat([]byte("s"), 3<<20)}}
		},
	}
	for name, build := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "invalid.zip")
			writeZIP(t, path, build(valid))
			if _, err := Verify(t.Context(), path); !errors.Is(err, errs.ErrInvalid) {
				t.Fatalf("Verify error=%v", err)
			}
		})
	}
	t.Run("truncated", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "truncated.zip")
		if err := os.WriteFile(path, []byte("PK\x03\x04"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Verify(t.Context(), path); !errors.Is(err, errs.ErrInvalid) {
			t.Fatalf("Verify error=%v", err)
		}
	})
}

func TestVerifyRejectsChecksumJSONManifestAndRedactionFailures(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	for name, mutate := range map[string]func(t *testing.T, files map[string][]byte){
		"checksum":     func(_ *testing.T, files map[string][]byte) { files["SHA256SUMS"] = []byte("0  investigation.json\n") },
		"invalid JSON": func(_ *testing.T, files map[string][]byte) { files["investigation.json"] = []byte("{") },
		"unknown field": func(_ *testing.T, files map[string][]byte) {
			files["investigation.json"] = append(bytes.TrimSuffix(files["investigation.json"], []byte("}")), []byte(`,"unknown":true}`)...)
			refreshChecksums(t, files)
		},
		"manifest mismatch": func(t *testing.T, files map[string][]byte) {
			var value manifest
			if err := json.Unmarshal(files["manifest.json"], &value); err != nil {
				t.Fatal(err)
			}
			value.InvestigationKey = "sha256:" + strings.Repeat("b", 64)
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			files["manifest.json"] = encoded
			refreshChecksums(t, files)
		},
		"redaction": func(t *testing.T, files map[string][]byte) {
			var value investigationDocument
			if err := json.Unmarshal(files["investigation.json"], &value); err != nil {
				t.Fatal(err)
			}
			value.Limitations = []string{"Bearer secret-value"}
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			files["investigation.json"] = encoded
			refreshManifestAndChecksums(t, files)
		},
	} {
		t.Run(name, func(t *testing.T) {
			files := validFiles(t, now)
			mutate(t, files)
			path := filepath.Join(t.TempDir(), "invalid.zip")
			writeZIP(t, path, entriesFrom(files))
			if _, err := Verify(t.Context(), path); !errors.Is(err, errs.ErrInvalid) {
				t.Fatalf("Verify error=%v", err)
			}
		})
	}
}

func TestCreateAndVerifyRespectCancellation(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	path := filepath.Join(t.TempDir(), "cancelled.zip")
	if _, err := Create(ctx, CreateRequest{OutputPath: path, CreatedAt: now, Investigation: validInvestigation(now)}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Create error=%v", err)
	}
	if _, err := Verify(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("Verify error=%v", err)
	}
}

func TestCreateRejectsRedactionViolationWithoutWritingPackage(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "unsafe.zip")
	unsafe := validInvestigation(now)
	unsafe.Limitations = []string{"Bearer secret-value"}
	if _, err := Create(t.Context(), CreateRequest{OutputPath: path, CreatedAt: now, Investigation: unsafe}); !errors.Is(err, errs.ErrInvalid) || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("Create error=%v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsafe package was written: %v", err)
	}
}

func TestPackageAcceptsAuthenticationCapabilityNamesAndRejectsCredentials(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	result := validInvestigation(now)
	result.Deployment.Service = "token-service"
	result.Regression.Deployment.Service = "token-service"
	result.Topology.Roots = []string{"service-payment"}
	result.Topology.Nodes = []topology.Node{
		{ID: "service-payment", Type: "service", LogicalKey: "payment-api", DisplayName: "payment-api"},
		{ID: "service-token", Type: "service", LogicalKey: "token-service", DisplayName: "token-service"},
	}
	result.Topology.Edges = []topology.Edge{{ID: "edge-token", From: "service-payment", To: "service-token", DependencyKind: "service", WindowStart: result.Timeline.Since, WindowEnd: result.Deployment.StartedAt, RequestCount: 1, Confidence: topology.Low, Basis: "observed relation", AlgorithmVersion: topology.AlgorithmVersion, EvidenceIDs: []string{"evidence-token"}, Limitations: []string{}}}
	result.EvidenceReferences.GraphEvidenceIDs = []string{"evidence-token"}
	calculated, err := investigation.CalculateKey(result)
	if err != nil {
		t.Fatal(err)
	}
	result.InvestigationKey = calculated
	output, err := OutputFrom(result)
	if err != nil {
		t.Fatalf("OutputFrom() error = %v", err)
	}
	if output.Deployment.Service != "token-service" || output.Topology.Nodes[1].LogicalKey != "token-service" {
		t.Fatalf("output=%+v", output)
	}
	path := filepath.Join(t.TempDir(), "token-service.zip")
	created, err := Create(t.Context(), CreateRequest{OutputPath: path, CreatedAt: now, Investigation: result})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	verified, err := Verify(t.Context(), path)
	if err != nil || verified.InvestigationKey != created.InvestigationKey {
		t.Fatalf("Verify() result=%+v error=%v", verified, err)
	}

	unsafe := result
	unsafe.Limitations = []string{"token=fake-sensitive-value"}
	if _, err := OutputFrom(unsafe); !errors.Is(err, errs.ErrInvalid) || strings.Contains(err.Error(), "fake-sensitive-value") {
		t.Fatalf("OutputFrom sensitive error=%v", err)
	}
}

func TestVerifyRequiresRegularInputFile(t *testing.T) {
	if _, err := Verify(t.Context(), t.TempDir()); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("directory Verify error=%v", err)
	}
	path := filepath.Join(t.TempDir(), "package-link.zip")
	if err := os.Symlink("missing.zip", path); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(t.Context(), path); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("symlink Verify error=%v", err)
	}
}

func TestVerifyRejectsDuplicateJSONKeysBeforeTypedDecoding(t *testing.T) {
	for name, document := range map[string][]byte{
		"root":             []byte(`{"name":"a","name":"b"}`),
		"nested":           []byte(`{"regression":{"status":"a","status":"b"}}`),
		"array object":     []byte(`{"items":[{"id":"a","id":"b"}]}`),
		"escaped spelling": []byte(`{"name":"a","\u006eame":"b"}`),
	} {
		t.Run(name, func(t *testing.T) {
			var value any
			if err := decodeStrict(document, &value); err == nil {
				t.Fatal("duplicate key was accepted")
			}
		})
	}
	var value any
	if err := decodeStrict([]byte(`{"left":{"name":"a"},"right":{"name":"b"}}`), &value); err != nil {
		t.Fatalf("same key in different objects rejected: %v", err)
	}
}

func TestVerifyRejectsSemanticMutationsWithRefreshedIntegrity(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	for name, mutate := range map[string]func(*investigationDocument){
		"unknown investigation version": func(value *investigationDocument) { value.InvestigationVersion = "investigation-view/v999" },
		"unknown comparison version":    func(value *investigationDocument) { value.Regression.AlgorithmVersion = "deployment-comparison/v999" },
		"confirmed classification":      func(value *investigationDocument) { value.Regression.Classification.Result = "CONFIRMED" },
		"raised confidence":             func(value *investigationDocument) { value.Regression.Classification.Confidence.Level = "HIGH" },
		"inconsistent delta": func(value *investigationDocument) {
			changed := *value.Regression.AbsoluteDelta + 1
			value.Regression.AbsoluteDelta = &changed
		},
		"orphan reference":        func(value *investigationDocument) { value.EvidenceReferences.TimelineEventIDs = []string{"invented"} },
		"stale investigation key": func(value *investigationDocument) { value.InvestigationKey = "sha256:" + strings.Repeat("b", 64) },
		"causality": func(value *investigationDocument) {
			value.Timeline.Items = append(value.Timeline.Items, timelineEventDocument{ID: "event", Time: value.Timeline.Since, Kind: "runtime_observed", Environment: value.Deployment.Environment, Service: value.Deployment.Service, RelationType: "OBSERVED", Subject: subjectDocument{Type: "runtime_instance", ID: "runtime"}, Source: sourceDocument{Kind: "docker", ObservedAt: value.Timeline.Since}, Confidence: timelineConfidenceDocument{Level: "LOW", Basis: "observed"}, Runtime: &runtimeDocument{State: "running", Health: "healthy"}, Concurrency: concurrencyDocument{}, Limitations: []string{}, CausalityClaimed: true})
		},
	} {
		t.Run(name, func(t *testing.T) {
			files := validFiles(t, now)
			var document investigationDocument
			if err := json.Unmarshal(files["investigation.json"], &document); err != nil {
				t.Fatal(err)
			}
			mutate(&document)
			encoded, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			files["investigation.json"] = encoded
			refreshManifestAndChecksums(t, files)
			path := filepath.Join(t.TempDir(), "mutated.zip")
			writeZIP(t, path, entriesFrom(files))
			if _, err := Verify(t.Context(), path); !errors.Is(err, errs.ErrInvalid) {
				t.Fatalf("Verify error=%v", err)
			}
		})
	}
}

func TestVerifyRejectsDuplicateKeysEvenWithRefreshedIntegrity(t *testing.T) {
	files := validFiles(t, time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC))
	files["investigation.json"] = bytes.Replace(files["investigation.json"], []byte(`"status":"AVAILABLE"`), []byte(`"status":"AVAILABLE","status":"AVAILABLE"`), 1)
	refreshManifestAndChecksums(t, files)
	path := filepath.Join(t.TempDir(), "duplicate-key.zip")
	writeZIP(t, path, entriesFrom(files))
	if _, err := Verify(t.Context(), path); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("Verify error=%v", err)
	}
}

func TestVerifyRejectsDuplicateManifestKeys(t *testing.T) {
	files := validFiles(t, time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC))
	// Insert an equivalent duplicate after the original field while retaining a
	// structurally valid ZIP and refreshed external hashes.
	files["manifest.json"] = bytes.Replace(files["manifest.json"], []byte(`","created_at"`), []byte(`","format_version":"prodmap-investigation-package/v1","created_at"`), 1)
	refreshChecksums(t, files)
	path := filepath.Join(t.TempDir(), "duplicate-manifest-key.zip")
	writeZIP(t, path, entriesFrom(files))
	if _, err := Verify(t.Context(), path); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("Verify error=%v", err)
	}
}

func TestCreateAndVerifyAcceptsConservativeClassificationVariants(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	for name, mutate := range map[string]func(*investigation.Result){
		"candidate": func(*investigation.Result) {},
		"no signal": func(value *investigation.Result) {
			after := 299_000_000.0
			delta := after - *value.Regression.Before.Value
			relative := delta / *value.Regression.Before.Value
			value.Regression.After.Value, value.Regression.AbsoluteDelta, value.Regression.RelativeDelta = &after, &delta, &relative
		},
		"unknown": func(value *investigation.Result) {
			value.Status, value.Regression.Status = "UNKNOWN", "UNKNOWN"
			value.Regression.Before.Status, value.Regression.Before.Value = "UNKNOWN", nil
			value.Regression.AbsoluteDelta, value.Regression.RelativeDelta = nil, nil
			value.Regression.BaselineConfidence = regression.Confidence{Level: "UNKNOWN", Basis: "no exact baseline window", AlgorithmVersion: baseline.AlgorithmVersion, Limitations: []string{"no exact baseline window"}}
			value.Regression.RegressionConfidence = regression.Confidence{Level: "UNKNOWN", Basis: "comparison requires eligible exact baseline and observation windows without known contamination or concurrency", AlgorithmVersion: regression.AlgorithmVersion, Limitations: []string{"comparison requires eligible exact baseline and observation windows without known contamination or concurrency"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			result := validInvestigation(now)
			mutate(&result)
			result.Regression.Classification = nil
			classification, err := regression.Classify(t.Context(), result.Regression)
			if err != nil {
				t.Fatal(err)
			}
			result.Regression.Classification = &classification
			key, err := investigation.CalculateKey(result)
			if err != nil {
				t.Fatal(err)
			}
			result.InvestigationKey = key
			path := filepath.Join(t.TempDir(), "variant.zip")
			if _, err := Create(t.Context(), CreateRequest{OutputPath: path, CreatedAt: now, Investigation: result}); err != nil {
				t.Fatal(err)
			}
			if _, err := Verify(t.Context(), path); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type zipFixture struct {
	Name string
	Data []byte
	Mode os.FileMode
}

func writeZIP(t *testing.T, path string, entries []zipFixture) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.Name, Method: zip.Store}
		if entry.Mode != 0 {
			header.SetMode(entry.Mode)
		} else {
			header.SetMode(0o600)
		}
		writerEntry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writerEntry.Write(entry.Data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func entriesFrom(files map[string][]byte) []zipFixture {
	return []zipFixture{{Name: "SHA256SUMS", Data: files["SHA256SUMS"]}, {Name: "investigation.json", Data: files["investigation.json"]}, {Name: "manifest.json", Data: files["manifest.json"]}}
}
func entriesWithout(files map[string][]byte, name string) []zipFixture {
	entries := entriesFrom(files)
	return slices.DeleteFunc(entries, func(entry zipFixture) bool { return entry.Name == name })
}

func validFiles(t *testing.T, now time.Time) map[string][]byte {
	t.Helper()
	documentJSON, err := marshalDocument(documentFrom(validInvestigation(now)))
	if err != nil {
		t.Fatal(err)
	}
	manifestJSON, err := json.Marshal(manifest{FormatVersion: FormatVersion, CreatedAt: now.Format(time.RFC3339Nano), InvestigationVersion: investigation.Version, InvestigationKey: validInvestigation(now).InvestigationKey, RedactionProfile: RedactionProfile, Investigation: fileInventory{Name: "investigation.json", SHA256: digest(documentJSON), Size: int64(len(documentJSON)), MediaType: MediaType}})
	if err != nil {
		t.Fatal(err)
	}
	return map[string][]byte{"manifest.json": manifestJSON, "investigation.json": documentJSON, "SHA256SUMS": checksumFile(manifestJSON, documentJSON)}
}

func packageFiles(t *testing.T, path string) map[string][]byte {
	t.Helper()
	archive, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	result := make(map[string][]byte, len(archive.File))
	for _, file := range archive.File {
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		contents, err := io.ReadAll(reader)
		if closeErr := reader.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			t.Fatal(err)
		}
		result[file.Name] = contents
	}
	return result
}

func refreshChecksums(t *testing.T, files map[string][]byte) {
	t.Helper()
	files["SHA256SUMS"] = checksumFile(files["manifest.json"], files["investigation.json"])
}
func refreshManifestAndChecksums(t *testing.T, files map[string][]byte) {
	t.Helper()
	var value manifest
	if err := json.Unmarshal(files["manifest.json"], &value); err != nil {
		t.Fatal(err)
	}
	value.Investigation.SHA256 = digest(files["investigation.json"])
	value.Investigation.Size = int64(len(files["investigation.json"]))
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	files["manifest.json"] = encoded
	refreshChecksums(t, files)
}

func validInvestigation(now time.Time) investigation.Result {
	key := "sha256:" + strings.Repeat("a", 64)
	beforeValue := 250_000_000.0
	afterValue := 300_000_000.0
	absolute := afterValue - beforeValue
	relative := 0.20
	baselineConfidence := regression.Confidence{Level: "LOW", Basis: "single exact baseline window; experimental algorithm caps confidence at LOW", AlgorithmVersion: baseline.AlgorithmVersion, Limitations: []string{}}
	observationConfidence := regression.Confidence{Level: "LOW", Basis: "single exact post-deployment observation window; experimental algorithm caps confidence at LOW", AlgorithmVersion: regression.ObservationAlgorithmVersion, Limitations: []string{}}
	regressionConfidence := regression.Confidence{Level: "LOW", Basis: "two exact windows are mechanically comparable; experimental algorithm caps confidence at LOW", AlgorithmVersion: regression.AlgorithmVersion, Limitations: []string{}}
	deployment := regression.Deployment{ID: "01a05d48-09b3-742b-8d9b-54f79d43b28f", Environment: "reference", Service: "payment-api", StartedAt: now.Add(-5 * time.Minute)}
	before := regression.Side{Status: "AVAILABLE", AlgorithmVersion: baseline.AlgorithmVersion, WindowStart: now.Add(-10 * time.Minute), WindowEnd: deployment.StartedAt, Value: &beforeValue, SampleCount: 10, IsComplete: true, AcceptedWindows: []baseline.WindowSummary{}, RejectedWindows: []baseline.RejectedWindow{}}
	after := regression.Side{Status: "AVAILABLE", AlgorithmVersion: regression.ObservationAlgorithmVersion, WindowStart: deployment.StartedAt, WindowEnd: now, Value: &afterValue, SampleCount: 10, IsComplete: true, AcceptedWindows: []baseline.WindowSummary{}, RejectedWindows: []baseline.RejectedWindow{}}
	comparison := regression.Result{ComparisonKey: key, Status: "AVAILABLE", AlgorithmVersion: regression.AlgorithmVersion, Deployment: deployment, Metric: baseline.LatencyP95, Unit: "nanoseconds", Before: before, After: after, AbsoluteDelta: &absolute, RelativeDelta: &relative, Contamination: regression.Contamination{BeforeDeployments: []string{}, AfterDeployments: []string{}, ConcurrentDeployments: []string{}}, BaselineConfidence: baselineConfidence, ObservationConfidence: observationConfidence, RegressionConfidence: regressionConfidence}
	classification, err := regression.Classify(context.Background(), comparison)
	if err != nil {
		panic(err)
	}
	comparison.Classification = &classification
	result := investigation.Result{InvestigationVersion: investigation.Version, Status: "AVAILABLE", Deployment: deployment, Regression: comparison, Topology: topology.Result{At: deployment.StartedAt, Environment: "reference", Roots: []string{}, Nodes: []topology.Node{}, Edges: []topology.Edge{}}, Timeline: timeline.Result{Since: now.Add(-10 * time.Minute), Until: now, Environment: "reference", Items: []timeline.Event{}}, EvidenceReferences: investigation.EvidenceReferences{AcceptedWindowIDs: []string{}, RejectedWindowIDs: []string{}, GraphEvidenceIDs: []string{}, TimelineEventIDs: []string{}}, Limitations: []string{}, CausalityClaimed: false}
	calculated, err := investigation.CalculateKey(result)
	if err != nil {
		panic(err)
	}
	result.InvestigationKey = calculated
	return result
}
