package sqlite

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/guijoazeiro/prodmap/internal/correlation"
	"github.com/guijoazeiro/prodmap/internal/inventory"
)

func TestServicesDerivesRuntimeAssociationFromExplicitOCITitle(t *testing.T) {
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name               string
		runtimeEnvironment string
		telemetry          bool
		title              string
		secondWithoutTitle bool
		wantStatus         string
		wantConfidence     correlation.Level
		wantBasis          string
		wantMatched        int
		wantUnverified     int
	}{
		{name: "matched", runtimeEnvironment: "reference", telemetry: true, title: "checkout", wantStatus: "MATCHED", wantConfidence: correlation.LevelHigh, wantBasis: runtimeAssociationBasis, wantMatched: 1},
		{name: "same fallback name is unknown", runtimeEnvironment: "reference", telemetry: true, wantStatus: "UNKNOWN", wantConfidence: correlation.LevelUnknown, wantBasis: "runtime identity lacks allowlisted OCI title evidence", wantUnverified: 1},
		{name: "runtime only", runtimeEnvironment: "reference", title: "checkout", wantStatus: "UNKNOWN", wantConfidence: correlation.LevelUnknown, wantBasis: "no telemetry observation", wantMatched: 1},
		{name: "partial", runtimeEnvironment: "reference", telemetry: true, title: "checkout", secondWithoutTitle: true, wantStatus: "PARTIAL", wantConfidence: correlation.LevelHigh, wantBasis: runtimeAssociationBasis, wantMatched: 1, wantUnverified: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := Open(context.Background(), filepath.Join(t.TempDir(), "association.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if test.telemetry {
				snapshot := telemetrySnapshot(start, "a")
				snapshot.Environment = "reference"
				if _, err := store.SaveTelemetry(context.Background(), snapshot); err != nil {
					t.Fatal(err)
				}
			}
			runtime := runtimeAssociationSnapshot(start, test.runtimeEnvironment, "checkout", test.title, "runtime-1")
			if test.secondWithoutTitle {
				second := runtime.Items[0]
				second.Runtime.ExternalID = "runtime-2"
				second.Runtime.ImageReference = "registry.example/checkout:canary"
				second.Runtime.ImageID = "sha256:" + strings.Repeat("b", 64)
				second.Runtime.RepoDigests = []string{"checkout@sha256:" + strings.Repeat("b", 64)}
				second.Artifact.Identity = second.Runtime.ImageID
				second.Artifact.ImageID = second.Runtime.ImageID
				second.Artifact.Digest = strings.Repeat("b", 64)
				second.Artifact.ObservedReference = second.Runtime.ImageReference
				second.Artifact.Aliases = []string{second.Runtime.ImageReference}
				second.Artifact.OCILabels = map[string]string{}
				second.Runtime.OCILabels = map[string]string{}
				runtime.Items = append(runtime.Items, second)
			}
			if _, err := store.SaveRuntimeSnapshot(context.Background(), runtime); err != nil {
				t.Fatal(err)
			}
			service := associationService(t, store, test.runtimeEnvironment, "checkout")
			if service.TelemetryObserved != test.telemetry || service.RuntimeAssociation.Status != test.wantStatus || service.RuntimeAssociation.Confidence != test.wantConfidence || service.RuntimeAssociation.Basis != test.wantBasis || service.RuntimeAssociation.MatchedInstances != test.wantMatched || service.RuntimeAssociation.UnverifiedInstances != test.wantUnverified {
				t.Fatalf("service=%+v", service)
			}
			if test.wantStatus == "PARTIAL" && len(service.RuntimeAssociation.Limitations) != 1 {
				t.Fatalf("partial limitations=%v", service.RuntimeAssociation.Limitations)
			}
		})
	}
}

func TestServicesAssociationRequiresSameEnvironmentAndIsOrderIndependent(t *testing.T) {
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	run := func(t *testing.T, telemetryFirst bool) inventory.ServiceRecord {
		store, err := Open(context.Background(), filepath.Join(t.TempDir(), "ordering.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		runtime := runtimeAssociationSnapshot(start, "reference", "checkout", "checkout", "runtime")
		telemetrySnapshot := telemetrySnapshot(start, "b")
		telemetrySnapshot.Environment = "reference"
		if telemetryFirst {
			if _, err := store.SaveTelemetry(context.Background(), telemetrySnapshot); err != nil {
				t.Fatal(err)
			}
			if _, err := store.SaveRuntimeSnapshot(context.Background(), runtime); err != nil {
				t.Fatal(err)
			}
		} else {
			if _, err := store.SaveRuntimeSnapshot(context.Background(), runtime); err != nil {
				t.Fatal(err)
			}
			if _, err := store.SaveTelemetry(context.Background(), telemetrySnapshot); err != nil {
				t.Fatal(err)
			}
		}
		return associationService(t, store, "reference", "checkout")
	}
	forward, reverse := run(t, true), run(t, false)
	if !reflect.DeepEqual(forward.RuntimeAssociation, reverse.RuntimeAssociation) || !forward.TelemetryObserved || forward.RuntimeAssociation.Status != "MATCHED" {
		t.Fatalf("forward=%+v reverse=%+v", forward, reverse)
	}

	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "different-environment.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.SaveRuntimeSnapshot(context.Background(), runtimeAssociationSnapshot(start, "reference", "checkout", "checkout", "runtime")); err != nil {
		t.Fatal(err)
	}
	telemetrySnapshot := telemetrySnapshot(start, "c")
	telemetrySnapshot.Environment = "default"
	if _, err := store.SaveTelemetry(context.Background(), telemetrySnapshot); err != nil {
		t.Fatal(err)
	}
	reference := associationService(t, store, "reference", "checkout")
	if reference.TelemetryObserved || reference.RuntimeAssociation.Status != "UNKNOWN" || reference.RuntimeAssociation.Basis != "no telemetry observation" {
		t.Fatalf("cross-environment association=%+v", reference)
	}
}

func runtimeAssociationSnapshot(at time.Time, environment, serviceKey, title, externalID string) inventory.Snapshot {
	imageID := "sha256:" + strings.Repeat("a", 64)
	runtime := inventory.RuntimeObservation{ExternalID: externalID, ContainerName: "not-evidence", ImageReference: "registry.example/" + serviceKey + ":v1", ImageID: imageID, RepoDigests: []string{serviceKey + "@" + imageID}, State: "running", Health: "healthy", ObservedAt: at, OCILabels: map[string]string{}}
	artifact := inventory.Artifact{Name: serviceKey, IdentityKind: "repo_digest", Identity: imageID, DigestAlgorithm: "sha256", Digest: strings.Repeat("a", 64), ImageID: imageID, ObservedReference: runtime.ImageReference, Aliases: []string{runtime.ImageReference}, OCILabels: map[string]string{}}
	if title != "" {
		runtime.OCILabels["org.opencontainers.image.title"] = title
		artifact.OCILabels["org.opencontainers.image.title"] = title
	}
	return inventory.Snapshot{ObservedAt: at, Items: []inventory.SnapshotItem{{ServiceLogicalKey: serviceKey, ServiceDisplayName: serviceKey, Environment: environment, Runtime: runtime, Artifact: artifact, Correlation: correlation.EvaluateProvenance(correlation.ProvenanceInput{ImmutableIdentity: imageID, ObservedAt: at})}}}
}

func associationService(t *testing.T, store *Store, environment, logicalKey string) inventory.ServiceRecord {
	t.Helper()
	services, err := store.Services(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, service := range services {
		if service.Environment == environment && service.LogicalKey == logicalKey {
			return service
		}
	}
	t.Fatalf("missing service environment=%q logical_key=%q: %+v", environment, logicalKey, services)
	return inventory.ServiceRecord{}
}

func TestTelemetryDependencyDoesNotCreateRuntimeServiceAssociation(t *testing.T) {
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "dependency.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshot := telemetrySnapshot(start, "d")
	snapshot.Services = snapshot.Services[:1]
	snapshot.Stats.Services = 1
	snapshot.Dependencies[0].LogicalKey, snapshot.Dependencies[0].Kind, snapshot.Dependencies[0].DisplayName = "postgresql:reference", "database", "postgresql:reference"
	snapshot.Observations[0].TargetServiceKey = ""
	if _, err := store.SaveTelemetry(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveRuntimeSnapshot(context.Background(), runtimeAssociationSnapshot(start, "default", "postgresql", "postgresql", "postgres-runtime")); err != nil {
		t.Fatal(err)
	}
	for _, service := range mustServices(t, store) {
		if service.LogicalKey == "postgresql:reference" {
			t.Fatalf("dependency became a service: %+v", service)
		}
		if service.LogicalKey == "postgresql" && service.TelemetryObserved {
			t.Fatalf("runtime-only PostgreSQL became telemetry observed: %+v", service)
		}
	}
}

func TestServicesTelemetryOnlyIsUnknownAndRuntimeEnvironmentIsHistorical(t *testing.T) {
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "telemetry-only.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshot := telemetrySnapshot(start, "e")
	snapshot.Environment = "reference"
	if _, err := store.SaveTelemetry(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	telemetryOnly := associationService(t, store, "reference", "checkout")
	if !telemetryOnly.TelemetryObserved || telemetryOnly.RuntimeAssociation.Status != "UNKNOWN" || telemetryOnly.RuntimeAssociation.Basis != "no current runtime evidence" {
		t.Fatalf("telemetry-only service=%+v", telemetryOnly)
	}

	runtime := runtimeAssociationSnapshot(start, "default", "checkout", "checkout", "runtime")
	if _, err := store.SaveRuntimeSnapshot(context.Background(), runtime); err != nil {
		t.Fatal(err)
	}
	replayed := runtime
	replayed.Items[0].Environment = "reference"
	if _, err := store.SaveRuntimeSnapshot(context.Background(), replayed); err == nil {
		t.Fatal("same runtime observation changed historical environment")
	}
	defaultService := associationService(t, store, "default", "checkout")
	if defaultService.Environment != "default" || defaultService.RuntimeAssociation.Status != "UNKNOWN" {
		t.Fatalf("historical runtime service=%+v", defaultService)
	}
}

func mustServices(t *testing.T, store *Store) []inventory.ServiceRecord {
	t.Helper()
	items, err := store.Services(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return items
}
