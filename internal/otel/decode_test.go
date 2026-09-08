package otel

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/guijoazeiro/prodmap/internal/telemetry"
	"github.com/guijoazeiro/prodmap/internal/topology"
	"go.opentelemetry.io/collector/pdata/ptrace"
	collecttracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func TestDecodeLinkedServicesIsSanitizedAndNeverExact(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "testdata", "otel", "linked-services.otlp.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	snapshot, err := NewDecoder().Decode(context.Background(), content, "file:test", "sha256:"+strings.Repeat("a", 64), "default", start, start.Add(time.Minute), start.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Stats.SpansSeen != 3 || snapshot.Stats.SpansAccepted != 3 || snapshot.Stats.Services != 2 || snapshot.Stats.Endpoints != 2 || snapshot.Stats.Dependencies != 1 || snapshot.Stats.Observations != 1 {
		t.Fatalf("unexpected stats: %+v", snapshot.Stats)
	}
	observation := snapshot.Observations[0]
	if observation.Confidence != topology.High || observation.Basis != "parent/child client/server propagation" || observation.TargetServiceKey != "payment" {
		t.Fatalf("observation = %+v", observation)
	}
	if observation.Confidence == topology.Exact {
		t.Fatal("OTel observation was classified EXACT")
	}
	if observation.RequestCount != 1 || observation.ErrorCount != 1 {
		t.Fatalf("distributed operation counts = %d/%d", observation.RequestCount, observation.ErrorCount)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"fixture-secret-never-persist", "authorization", "url.full", "01010101010101010101010101010101", "0202020202020202"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("sanitized snapshot contains %q: %s", forbidden, encoded)
		}
	}
	if len(snapshot.Evidence) != 2 || !strings.HasPrefix(snapshot.Evidence[0].Fingerprint, telemetry.EvidenceFingerprintV1+":") {
		t.Fatalf("evidence = %+v", snapshot.Evidence)
	}
}

func TestDecodeSpanClassifiesStatusErrorAndHTTP5xx(t *testing.T) {
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		statusCode tracepb.Status_StatusCode
		httpStatus int64
		wantError  bool
	}{
		{name: "success", wantError: false},
		{name: "HTTP 499", httpStatus: 499, wantError: false},
		{name: "HTTP 500 without span status", httpStatus: 500, wantError: true},
		{name: "HTTP 599", httpStatus: 599, wantError: true},
		{name: "span ERROR without HTTP status", statusCode: tracepb.Status_STATUS_CODE_ERROR, wantError: true},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			span := &tracepb.Span{
				TraceId: bytesOf(byte(index+1), 16), SpanId: bytesOf(byte(index+11), 8), Kind: tracepb.Span_SPAN_KIND_SERVER,
				StartTimeUnixNano: uint64(start.UnixNano()), EndTimeUnixNano: uint64(start.Add(time.Nanosecond).UnixNano()),
				Status: &tracepb.Status{Code: test.statusCode},
			}
			if test.httpStatus != 0 {
				span.Attributes = []*commonpb.KeyValue{{Key: "http.response.status_code", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: test.httpStatus}}}}
			}
			decoded, _, err := decodeSpan(span, "payment", "payment", "sha256:"+strings.Repeat("a", 64), start, start.Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			if decoded.isError != test.wantError {
				t.Fatalf("isError=%t want=%t", decoded.isError, test.wantError)
			}
		})
	}
}

func TestDirectClientServerOperationCountsErrorOnceFromEitherSide(t *testing.T) {
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	decodeOperationSpan := func(t *testing.T, service string, kind tracepb.Span_SpanKind, id, parent byte, status tracepb.Status_StatusCode, httpStatus int64) decodedSpan {
		t.Helper()
		span := &tracepb.Span{
			TraceId: bytesOf(1, 16), SpanId: bytesOf(id, 8), Kind: kind,
			StartTimeUnixNano: uint64(start.Add(time.Second).UnixNano()), EndTimeUnixNano: uint64(start.Add(2 * time.Second).UnixNano()),
			Status: &tracepb.Status{Code: status},
		}
		if parent != 0 {
			span.ParentSpanId = bytesOf(parent, 8)
		}
		if httpStatus != 0 {
			span.Attributes = []*commonpb.KeyValue{{Key: "http.response.status_code", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: httpStatus}}}}
		}
		decoded, _, err := decodeSpan(span, service, service, "sha256:"+strings.Repeat("a", 64), start, start.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		return decoded
	}
	for _, test := range []struct {
		name         string
		clientStatus tracepb.Status_StatusCode
		serverStatus tracepb.Status_StatusCode
		serverHTTP   int64
		wantErrors   int64
	}{
		{name: "CLIENT success SERVER ERROR and HTTP 500", serverStatus: tracepb.Status_STATUS_CODE_ERROR, serverHTTP: 500, wantErrors: 1},
		{name: "CLIENT ERROR SERVER ERROR", clientStatus: tracepb.Status_STATUS_CODE_ERROR, serverStatus: tracepb.Status_STATUS_CODE_ERROR, wantErrors: 1},
		{name: "both success", wantErrors: 0},
		{name: "HTTP 500 without span status", serverHTTP: 500, wantErrors: 1},
		{name: "span ERROR without HTTP status", serverStatus: tracepb.Status_STATUS_CODE_ERROR, wantErrors: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := decodeOperationSpan(t, "checkout", tracepb.Span_SPAN_KIND_CLIENT, 2, 0, test.clientStatus, 0)
			server := decodeOperationSpan(t, "payment", tracepb.Span_SPAN_KIND_SERVER, 3, 2, test.serverStatus, test.serverHTTP)
			build := func(spans []decodedSpan) telemetry.DependencyObservation {
				snapshot := telemetry.Snapshot{WindowStart: start, WindowEnd: start.Add(time.Minute), ObservedAt: start.Add(time.Minute)}
				if err := aggregate(&snapshot, spans); err != nil {
					t.Fatal(err)
				}
				if len(snapshot.Observations) != 1 {
					t.Fatalf("observations=%+v", snapshot.Observations)
				}
				return snapshot.Observations[0]
			}
			forward, reverse := build([]decodedSpan{client, server}), build([]decodedSpan{server, client})
			if !reflect.DeepEqual(forward, reverse) {
				t.Fatalf("operation changed by order: forward=%+v reverse=%+v", forward, reverse)
			}
			if forward.RequestCount != 1 || forward.ErrorCount != test.wantErrors {
				t.Fatalf("counts=%d/%d want=1/%d", forward.RequestCount, forward.ErrorCount, test.wantErrors)
			}
		})
	}
}

func TestIncompatibleAssociationDoesNotContributeRemoteError(t *testing.T) {
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	client := decodedSpan{serviceKey: "checkout", serviceDisplay: "checkout", traceKey: "client", spanKey: "client:span", kind: tracepb.Span_SPAN_KIND_CLIENT, duration: 1, evidence: "client", attrs: attributes{peerService: "payment"}}
	consumer := decodedSpan{serviceKey: "worker", serviceDisplay: "worker", traceKey: "consumer", spanKey: "consumer:span", kind: tracepb.Span_SPAN_KIND_CONSUMER, duration: 1, evidence: "consumer", isError: true, links: []spanLink{{spanKey: client.spanKey}}}
	build := func(spans []decodedSpan) telemetry.DependencyObservation {
		snapshot := telemetry.Snapshot{WindowStart: start, WindowEnd: start.Add(time.Minute), ObservedAt: start.Add(time.Minute)}
		if err := aggregate(&snapshot, spans); err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Observations) != 1 {
			t.Fatalf("observations=%+v", snapshot.Observations)
		}
		return snapshot.Observations[0]
	}
	forward, reverse := build([]decodedSpan{client, consumer}), build([]decodedSpan{consumer, client})
	if !reflect.DeepEqual(forward, reverse) || forward.Confidence != topology.Medium || forward.ErrorCount != 0 {
		t.Fatalf("incompatible remote error changed fallback: forward=%+v reverse=%+v", forward, reverse)
	}
}

func TestResourceServiceIdentityAllowsAuthenticationCapabilityNames(t *testing.T) {
	for _, value := range []string{"token-service", "password-reset", "authorization-api", "bearer-worker", "secret-manager", "cookie-parser", "oauth-token-validator"} {
		if !safeResourceServiceIdentity(value) {
			t.Errorf("safeResourceServiceIdentity(%q) = false", value)
		}
	}
}

func TestCanonicalOTLPJSONDecodesHexIdentifiersNumericEnumsAndSpanLinks(t *testing.T) {
	line := []byte(`{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"api"}}]},"scopeSpans":[{"spans":[{"traceId":"0102030405060708090A0B0C0D0E0F10","spanId":"1112131415161718","parentSpanId":"2122232425262728","kind":3,"startTimeUnixNano":"1787140801000000000","endTimeUnixNano":"1787140801000000001","status":{"code":2},"links":[{"traceId":"3132333435363738393A3B3C3D3E3F40","spanId":"4142434445464748"}]}]}]}]}`)
	request, err := decodeOTLPJSONLine(line)
	if err != nil {
		t.Fatal(err)
	}
	span := request.GetResourceSpans()[0].GetScopeSpans()[0].GetSpans()[0]
	assertHexBytes(t, "trace ID", span.GetTraceId(), "0102030405060708090a0b0c0d0e0f10")
	assertHexBytes(t, "span ID", span.GetSpanId(), "1112131415161718")
	assertHexBytes(t, "parent span ID", span.GetParentSpanId(), "2122232425262728")
	if span.GetKind() != tracepb.Span_SPAN_KIND_CLIENT || span.GetStatus().GetCode() != tracepb.Status_STATUS_CODE_ERROR {
		t.Fatalf("kind=%v status=%v", span.GetKind(), span.GetStatus().GetCode())
	}
	if len(span.GetLinks()) != 1 {
		t.Fatalf("links=%d", len(span.GetLinks()))
	}
	assertHexBytes(t, "link trace ID", span.GetLinks()[0].GetTraceId(), "3132333435363738393a3b3c3d3e3f40")
	assertHexBytes(t, "link span ID", span.GetLinks()[0].GetSpanId(), "4142434445464748")
}

func TestCanonicalOTLPJSONRejectsProtobufJSONDialectAndInvalidIdentifiers(t *testing.T) {
	validPrefix := `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"api"}}]},"scopeSpans":[{"spans":[{`
	validSuffix := `,"startTimeUnixNano":"1787140801000000000","endTimeUnixNano":"1787140801000000001"}]}]}]}`
	tests := map[string]string{
		"Base64 trace ID":      `"traceId":"AQEBAQEBAQEBAQEBAQEBAQ==","spanId":"0202020202020202","kind":1`,
		"Base64 span ID":       `"traceId":"01010101010101010101010101010101","spanId":"AgICAgICAgI=","kind":1`,
		"symbolic kind":        `"traceId":"01010101010101010101010101010101","spanId":"0202020202020202","kind":"SPAN_KIND_INTERNAL"`,
		"symbolic status":      `"traceId":"01010101010101010101010101010101","spanId":"0202020202020202","kind":1,"status":{"code":"STATUS_CODE_OK"}`,
		"zero trace ID":        `"traceId":"00000000000000000000000000000000","spanId":"0202020202020202","kind":1`,
		"zero span ID":         `"traceId":"01010101010101010101010101010101","spanId":"0000000000000000","kind":1`,
		"zero parent span ID":  `"traceId":"01010101010101010101010101010101","spanId":"0202020202020202","parentSpanId":"0000000000000000","kind":1`,
		"short trace ID":       `"traceId":"0101","spanId":"0202020202020202","kind":1`,
		"short span ID":        `"traceId":"01010101010101010101010101010101","spanId":"0202","kind":1`,
		"short parent span ID": `"traceId":"01010101010101010101010101010101","spanId":"0202020202020202","parentSpanId":"0303","kind":1`,
	}
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	for name, fields := range tests {
		t.Run(name, func(t *testing.T) {
			input := []byte(validPrefix + fields + validSuffix + "\n")
			if _, err := NewDecoder().Decode(context.Background(), input, "file:test", "sha256:"+strings.Repeat("a", 64), "default", start, start.Add(time.Minute), start); err == nil {
				t.Fatal("Decode() unexpectedly accepted invalid OTLP/JSON")
			}
		})
	}
}

func TestCanonicalOTLPJSONFingerprintUsesDecodedHexBytesAndRawIDsDoNotEscape(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "testdata", "otel", "linked-services.otlp.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	sourceHash := "sha256:" + strings.Repeat("a", 64)
	snapshot, err := NewDecoder().Decode(context.Background(), content, "file:test", sourceHash, "default", start, start.Add(time.Minute), start)
	if err != nil {
		t.Fatal(err)
	}
	traceID, _ := hex.DecodeString("01010101010101010101010101010101")
	spanID, _ := hex.DecodeString("0202020202020202")
	want := evidenceFingerprint(sourceHash, traceID, spanID)
	found := false
	for _, evidence := range snapshot.Evidence {
		if evidence.Fingerprint == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("decoded-byte fingerprint %q not found in %+v", want, snapshot.Evidence)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, rawID := range []string{"01010101010101010101010101010101", "0202020202020202", "0303030303030303", "0404040404040404"} {
		if strings.Contains(string(encoded), rawID) {
			t.Fatalf("raw identifier %q escaped into snapshot: %s", rawID, encoded)
		}
	}
}

func TestConflictingLinkedTargetKeepsDirectTargetAndContradiction(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "testdata", "otel", "conflicting-linked-target.otlp.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	snapshot, err := NewDecoder().Decode(context.Background(), content, "file:conflict", "sha256:"+strings.Repeat("b", 64), "default", start, start.Add(time.Minute), start.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Observations) != 1 || snapshot.Observations[0].TargetServiceKey != "payment" || snapshot.Observations[0].Confidence != topology.Medium || snapshot.Observations[0].Basis != "linked spans with conflicting semantic target" {
		t.Fatalf("contradictory observation=%+v", snapshot.Observations)
	}
	if strings.Join(snapshot.Observations[0].Limitations, " ") != "Directly linked remote service contradicted peer.service or rpc.service metadata." {
		t.Fatalf("limitations=%#v", snapshot.Observations[0].Limitations)
	}
	if len(snapshot.Dependencies) != 1 || snapshot.Dependencies[0].LogicalKey != "payment" {
		t.Fatalf("contradictory fallback created an edge: %+v", snapshot.Dependencies)
	}
	claims := map[string]bool{}
	for _, evidence := range snapshot.Evidence {
		claims[evidence.Claim] = true
	}
	if !claims["outbound client span"] || !claims["linked remote server span"] {
		t.Fatalf("evidence claims=%+v", snapshot.Evidence)
	}
}

func TestAnyContradictoryLinkedSampleCapsAggregatedObservationDeterministically(t *testing.T) {
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	coherentClient := decodedSpan{serviceKey: "checkout", serviceDisplay: "checkout", traceKey: "coherent", spanKey: "coherent:client", kind: tracepb.Span_SPAN_KIND_CLIENT, duration: 1, evidence: "coherent-client", attrs: attributes{peerService: "payment"}}
	coherentServer := decodedSpan{serviceKey: "payment", serviceDisplay: "payment", traceKey: "coherent", spanKey: "coherent:server", parentKey: coherentClient.spanKey, kind: tracepb.Span_SPAN_KIND_SERVER, duration: 1, evidence: "coherent-server"}
	conflictClient := decodedSpan{serviceKey: "checkout", serviceDisplay: "checkout", traceKey: "conflict", spanKey: "conflict:client", kind: tracepb.Span_SPAN_KIND_CLIENT, duration: 1, evidence: "conflict-client", attrs: attributes{peerService: "wrong-fallback"}}
	conflictServer := decodedSpan{serviceKey: "payment", serviceDisplay: "payment", traceKey: "conflict", spanKey: "conflict:server", parentKey: conflictClient.spanKey, kind: tracepb.Span_SPAN_KIND_SERVER, duration: 1, evidence: "conflict-server"}
	build := func(spans []decodedSpan) telemetry.DependencyObservation {
		snapshot := telemetry.Snapshot{WindowStart: start, WindowEnd: start.Add(time.Minute), ObservedAt: start.Add(time.Minute)}
		if err := aggregate(&snapshot, spans); err != nil {
			t.Fatal(err)
		}
		return snapshot.Observations[0]
	}
	forward := build([]decodedSpan{coherentClient, coherentServer, conflictClient, conflictServer})
	reverse := build([]decodedSpan{conflictServer, conflictClient, coherentServer, coherentClient})
	if !reflect.DeepEqual(forward, reverse) || forward.Confidence != topology.Medium || forward.Basis != "linked spans with conflicting semantic target" || !strings.Contains(strings.Join(forward.Limitations, " "), "contradicted") {
		t.Fatalf("aggregate contradiction changed by order: forward=%+v reverse=%+v", forward, reverse)
	}
}

func TestProducerConsumerSpanLinkIsOrderIndependentSanitizedAndNeverExact(t *testing.T) {
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	producer := decodedSpan{serviceKey: "publisher", serviceDisplay: "publisher", traceKey: "producer-trace", spanKey: "producer-trace:producer", kind: tracepb.Span_SPAN_KIND_PRODUCER, duration: 10, evidence: "producer-fingerprint"}
	consumer := decodedSpan{serviceKey: "worker", serviceDisplay: "worker", traceKey: "consumer-trace", spanKey: "consumer-trace:consumer", kind: tracepb.Span_SPAN_KIND_CONSUMER, duration: 5, evidence: "consumer-fingerprint", isError: true, links: []spanLink{{spanKey: producer.spanKey}}}
	build := func(spans []decodedSpan) telemetry.Snapshot {
		snapshot := telemetry.Snapshot{WindowStart: start, WindowEnd: start.Add(time.Minute), ObservedAt: start.Add(time.Minute)}
		if err := aggregate(&snapshot, spans); err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	forward, reverse := build([]decodedSpan{producer, consumer}), build([]decodedSpan{consumer, producer})
	if !reflect.DeepEqual(forward, reverse) || len(forward.Observations) != 1 {
		t.Fatalf("SpanLink result changed by order:\nforward=%+v\nreverse=%+v", forward, reverse)
	}
	observation := forward.Observations[0]
	if observation.TargetServiceKey != "worker" || observation.Confidence != topology.High || observation.Basis != "producer/consumer SpanLink association" || observation.Confidence == topology.Exact {
		t.Fatalf("SpanLink observation=%+v", observation)
	}
	if observation.RequestCount != 1 || observation.ErrorCount != 0 {
		t.Fatalf("SpanLink counts=%d/%d", observation.RequestCount, observation.ErrorCount)
	}
	claims := map[string]bool{}
	for _, item := range forward.Evidence {
		claims[item.Claim] = true
	}
	if !claims["outbound producer span"] || !claims["linked consumer span"] {
		t.Fatalf("SpanLink claims=%+v", forward.Evidence)
	}
}

func TestDirectAssociationMatrixAcceptsOnlySupportedPairs(t *testing.T) {
	client := decodedSpan{serviceKey: "checkout", serviceDisplay: "checkout", traceKey: "sync", spanKey: "sync:client", kind: tracepb.Span_SPAN_KIND_CLIENT, evidence: "client"}
	server := decodedSpan{serviceKey: "payment", serviceDisplay: "payment", traceKey: "sync", spanKey: "sync:server", kind: tracepb.Span_SPAN_KIND_SERVER, evidence: "server"}
	producer := decodedSpan{serviceKey: "publisher", serviceDisplay: "publisher", traceKey: "producer", spanKey: "producer:span", kind: tracepb.Span_SPAN_KIND_PRODUCER, evidence: "producer"}
	consumer := decodedSpan{serviceKey: "worker", serviceDisplay: "worker", traceKey: "consumer", spanKey: "consumer:span", kind: tracepb.Span_SPAN_KIND_CONSUMER, evidence: "consumer"}

	synchronous := resolveDependency(client, []linkedSpan{{span: server, association: "parent"}}, map[string]telemetry.Service{})
	if !synchronous.ok || synchronous.confidence != topology.High || synchronous.basis != "parent/child client/server propagation" || synchronous.pair != [2]string{"client", "server"} {
		t.Fatalf("valid synchronous association=%+v", synchronous)
	}
	asynchronous := resolveDependency(producer, []linkedSpan{{span: consumer, association: "span_link"}}, map[string]telemetry.Service{})
	if !asynchronous.ok || asynchronous.confidence != topology.High || asynchronous.basis != "producer/consumer SpanLink association" || asynchronous.pair != [2]string{"producer", "consumer"} {
		t.Fatalf("valid asynchronous association=%+v", asynchronous)
	}

	for _, test := range []struct {
		name      string
		outbound  decodedSpan
		candidate linkedSpan
	}{
		{name: "client consumer SpanLink", outbound: client, candidate: linkedSpan{span: consumer, association: "span_link"}},
		{name: "producer server parent", outbound: producer, candidate: linkedSpan{span: server, association: "parent"}},
		{name: "client consumer parent", outbound: client, candidate: linkedSpan{span: consumer, association: "parent"}},
		{name: "producer consumer parent", outbound: producer, candidate: linkedSpan{span: consumer, association: "parent"}},
		{name: "client server SpanLink", outbound: client, candidate: linkedSpan{span: server, association: "span_link"}},
		{name: "client server mismatched trace", outbound: client, candidate: linkedSpan{span: decodedSpan{serviceKey: "payment", serviceDisplay: "payment", traceKey: "other", kind: tracepb.Span_SPAN_KIND_SERVER, evidence: "other"}, association: "parent"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolution := resolveDependency(test.outbound, []linkedSpan{test.candidate}, map[string]telemetry.Service{})
			if resolution.ok || resolution.ambiguous || resolution.confidence == topology.High || resolution.pair != [2]string{} {
				t.Fatalf("incompatible association was accepted: %+v", resolution)
			}
		})
	}
}

func TestIncompatibleAssociationsUseOnlySemanticFallback(t *testing.T) {
	consumer := decodedSpan{serviceKey: "worker", serviceDisplay: "worker", traceKey: "consumer", kind: tracepb.Span_SPAN_KIND_CONSUMER, evidence: "consumer"}
	client := decodedSpan{serviceKey: "checkout", serviceDisplay: "checkout", traceKey: "client", kind: tracepb.Span_SPAN_KIND_CLIENT, evidence: "client", attrs: attributes{peerService: "payment"}}
	clientFallback := resolveDependency(client, []linkedSpan{{span: consumer, association: "span_link"}}, map[string]telemetry.Service{})
	if !clientFallback.ok || clientFallback.confidence != topology.Medium || clientFallback.basis != "peer.service semantic convention" || clientFallback.pair != [2]string{} || len(clientFallback.evidenceClaims) != 1 {
		t.Fatalf("CLIENT/CONSUMER fallback=%+v", clientFallback)
	}

	server := decodedSpan{serviceKey: "broker", serviceDisplay: "broker", traceKey: "producer", kind: tracepb.Span_SPAN_KIND_SERVER, evidence: "server"}
	producer := decodedSpan{serviceKey: "publisher", serviceDisplay: "publisher", traceKey: "producer", kind: tracepb.Span_SPAN_KIND_PRODUCER, evidence: "producer", attrs: attributes{messagingSystem: "kafka", messagingDest: "orders"}}
	producerFallback := resolveDependency(producer, []linkedSpan{{span: server, association: "parent"}}, map[string]telemetry.Service{})
	if !producerFallback.ok || producerFallback.confidence != topology.Medium || producerFallback.basis != "messaging semantic convention" || producerFallback.dependency.Kind != "queue" || producerFallback.pair != [2]string{} || len(producerFallback.evidenceClaims) != 1 {
		t.Fatalf("PRODUCER/SERVER fallback=%+v", producerFallback)
	}
}

func TestAggregateIgnoresIncompatibleAssociationsInEitherSpanOrder(t *testing.T) {
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	build := func(spans []decodedSpan) telemetry.Snapshot {
		snapshot := telemetry.Snapshot{WindowStart: start, WindowEnd: start.Add(time.Minute), ObservedAt: start.Add(time.Minute)}
		if err := aggregate(&snapshot, spans); err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	for _, test := range []struct {
		name            string
		outbound        decodedSpan
		remote          decodedSpan
		expectFallback  bool
		expectedBasis   string
		expectedDepKind string
	}{
		{
			name:     "client consumer SpanLink without fallback",
			outbound: decodedSpan{serviceKey: "client", serviceDisplay: "client", traceKey: "client-trace", spanKey: "client-trace:client", kind: tracepb.Span_SPAN_KIND_CLIENT, duration: 1, evidence: "client"},
			remote:   decodedSpan{serviceKey: "consumer", serviceDisplay: "consumer", traceKey: "consumer-trace", spanKey: "consumer-trace:consumer", kind: tracepb.Span_SPAN_KIND_CONSUMER, duration: 1, evidence: "consumer", links: []spanLink{{spanKey: "client-trace:client"}}},
		},
		{
			name: "client consumer SpanLink with peer fallback", expectFallback: true, expectedBasis: "peer.service semantic convention", expectedDepKind: "service",
			outbound: decodedSpan{serviceKey: "client", serviceDisplay: "client", traceKey: "client-trace", spanKey: "client-trace:client", kind: tracepb.Span_SPAN_KIND_CLIENT, duration: 1, evidence: "client", attrs: attributes{peerService: "declared-target"}},
			remote:   decodedSpan{serviceKey: "consumer", serviceDisplay: "consumer", traceKey: "consumer-trace", spanKey: "consumer-trace:consumer", kind: tracepb.Span_SPAN_KIND_CONSUMER, duration: 1, evidence: "consumer", links: []spanLink{{spanKey: "client-trace:client"}}},
		},
		{
			name:     "producer server parent without fallback",
			outbound: decodedSpan{serviceKey: "producer", serviceDisplay: "producer", traceKey: "producer-trace", spanKey: "producer-trace:producer", kind: tracepb.Span_SPAN_KIND_PRODUCER, duration: 1, evidence: "producer"},
			remote:   decodedSpan{serviceKey: "server", serviceDisplay: "server", traceKey: "producer-trace", spanKey: "producer-trace:server", parentKey: "producer-trace:producer", kind: tracepb.Span_SPAN_KIND_SERVER, duration: 1, evidence: "server"},
		},
		{
			name: "producer server parent with messaging fallback", expectFallback: true, expectedBasis: "messaging semantic convention", expectedDepKind: "queue",
			outbound: decodedSpan{serviceKey: "producer", serviceDisplay: "producer", traceKey: "producer-trace", spanKey: "producer-trace:producer", kind: tracepb.Span_SPAN_KIND_PRODUCER, duration: 1, evidence: "producer", attrs: attributes{messagingSystem: "kafka", messagingDest: "orders"}},
			remote:   decodedSpan{serviceKey: "server", serviceDisplay: "server", traceKey: "producer-trace", spanKey: "producer-trace:server", parentKey: "producer-trace:producer", kind: tracepb.Span_SPAN_KIND_SERVER, duration: 1, evidence: "server"},
		},
		{
			name:     "client consumer parent without fallback",
			outbound: decodedSpan{serviceKey: "client", serviceDisplay: "client", traceKey: "trace", spanKey: "trace:client", kind: tracepb.Span_SPAN_KIND_CLIENT, duration: 1, evidence: "client"},
			remote:   decodedSpan{serviceKey: "consumer", serviceDisplay: "consumer", traceKey: "trace", spanKey: "trace:consumer", parentKey: "trace:client", kind: tracepb.Span_SPAN_KIND_CONSUMER, duration: 1, evidence: "consumer"},
		},
		{
			name:     "producer consumer parent without fallback",
			outbound: decodedSpan{serviceKey: "producer", serviceDisplay: "producer", traceKey: "trace", spanKey: "trace:producer", kind: tracepb.Span_SPAN_KIND_PRODUCER, duration: 1, evidence: "producer"},
			remote:   decodedSpan{serviceKey: "consumer", serviceDisplay: "consumer", traceKey: "trace", spanKey: "trace:consumer", parentKey: "trace:producer", kind: tracepb.Span_SPAN_KIND_CONSUMER, duration: 1, evidence: "consumer"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			forward := build([]decodedSpan{test.outbound, test.remote})
			reverse := build([]decodedSpan{test.remote, test.outbound})
			if !reflect.DeepEqual(forward, reverse) {
				t.Fatalf("result changed by span order:\nforward=%+v\nreverse=%+v", forward, reverse)
			}
			if !test.expectFallback {
				if len(forward.Observations) != 0 || len(forward.Evidence) != 0 {
					t.Fatalf("incompatible association created a relation: %+v", forward)
				}
				return
			}
			if len(forward.Observations) != 1 || forward.Observations[0].Confidence != topology.Medium || forward.Observations[0].Basis != test.expectedBasis || len(forward.Observations[0].EvidenceFingerprints) != 1 || len(forward.Evidence) != 1 || forward.Dependencies[0].Kind != test.expectedDepKind {
				t.Fatalf("semantic fallback=%+v", forward)
			}
		})
	}
}

func TestInvalidAssociationsDoNotAffectAmbiguityOrInputOrder(t *testing.T) {
	client := decodedSpan{serviceKey: "checkout", serviceDisplay: "checkout", traceKey: "sync", kind: tracepb.Span_SPAN_KIND_CLIENT, evidence: "client"}
	valid := linkedSpan{span: decodedSpan{serviceKey: "payment", serviceDisplay: "payment", traceKey: "sync", kind: tracepb.Span_SPAN_KIND_SERVER, evidence: "server"}, association: "parent"}
	invalid := linkedSpan{span: decodedSpan{serviceKey: "wrong", serviceDisplay: "wrong", traceKey: "async", kind: tracepb.Span_SPAN_KIND_CONSUMER, evidence: "consumer"}, association: "span_link"}
	forward := resolveDependency(client, []linkedSpan{invalid, valid}, map[string]telemetry.Service{})
	reverse := resolveDependency(client, []linkedSpan{valid, invalid}, map[string]telemetry.Service{})
	if !reflect.DeepEqual(forward, reverse) || !forward.ok || forward.ambiguous || forward.targetServiceKey != "payment" || forward.confidence != topology.High {
		t.Fatalf("invalid candidate affected direct resolution: forward=%+v reverse=%+v", forward, reverse)
	}
	invalidOnly := resolveDependency(client, []linkedSpan{invalid, {span: decodedSpan{serviceKey: "another", kind: tracepb.Span_SPAN_KIND_CONSUMER}, association: "span_link"}}, map[string]telemetry.Service{})
	if invalidOnly.ok || invalidOnly.ambiguous {
		t.Fatalf("invalid targets participated in ambiguity: %+v", invalidOnly)
	}
}

func TestDroppedLinksOnlyCapSpanLinkAssociations(t *testing.T) {
	client := decodedSpan{serviceKey: "checkout", serviceDisplay: "checkout", traceKey: "sync", kind: tracepb.Span_SPAN_KIND_CLIENT, evidence: "client", droppedLinks: 3}
	server := decodedSpan{serviceKey: "payment", serviceDisplay: "payment", traceKey: "sync", kind: tracepb.Span_SPAN_KIND_SERVER, evidence: "server", droppedLinks: 4}
	synchronous := resolveDependency(client, []linkedSpan{{span: server, association: "parent"}}, map[string]telemetry.Service{})
	if synchronous.confidence != topology.High || len(synchronous.limitations) != 0 {
		t.Fatalf("dropped links capped parent/child propagation: %+v", synchronous)
	}

	producer := decodedSpan{serviceKey: "publisher", serviceDisplay: "publisher", kind: tracepb.Span_SPAN_KIND_PRODUCER, evidence: "producer"}
	consumer := decodedSpan{serviceKey: "worker", serviceDisplay: "worker", kind: tracepb.Span_SPAN_KIND_CONSUMER, evidence: "consumer", droppedLinks: 1}
	asynchronous := resolveDependency(producer, []linkedSpan{{span: consumer, association: "span_link"}}, map[string]telemetry.Service{})
	if asynchronous.confidence != topology.Medium || !strings.Contains(strings.Join(asynchronous.limitations, " "), "Dropped SpanLinks") {
		t.Fatalf("dropped links did not cap SpanLink association: %+v", asynchronous)
	}
}

func TestDecodeProducerConsumerSpanLinkUsesHashedEvidenceAndDropsLinkAttributes(t *testing.T) {
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	producerTrace, producerID := bytesOf(9, 16), bytesOf(8, 8)
	producer := &tracepb.Span{
		TraceId: producerTrace, SpanId: producerID, Kind: tracepb.Span_SPAN_KIND_PRODUCER,
		StartTimeUnixNano: uint64(start.Add(time.Second).UnixNano()), EndTimeUnixNano: uint64(start.Add(time.Second + time.Millisecond).UnixNano()),
	}
	consumer := &tracepb.Span{
		TraceId: bytesOf(7, 16), SpanId: bytesOf(6, 8), Kind: tracepb.Span_SPAN_KIND_CONSUMER,
		StartTimeUnixNano: uint64(start.Add(2 * time.Second).UnixNano()), EndTimeUnixNano: uint64(start.Add(2*time.Second + time.Millisecond).UnixNano()),
		Links: []*tracepb.Span_Link{{
			TraceId: producerTrace, SpanId: producerID,
			Attributes: []*commonpb.KeyValue{{Key: "authorization", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "Bearer span-link-secret"}}}},
		}},
	}
	resource := func(name string, spans ...*tracepb.Span) *tracepb.ResourceSpans {
		return &tracepb.ResourceSpans{
			Resource:   &resourcepb.Resource{Attributes: []*commonpb.KeyValue{{Key: "service.name", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: name}}}}},
			ScopeSpans: []*tracepb.ScopeSpans{{Spans: spans}},
		}
	}
	decode := func(resources []*tracepb.ResourceSpans) telemetry.Snapshot {
		request := &collecttracepb.ExportTraceServiceRequest{ResourceSpans: resources}
		content := marshalOTLPJSON(t, request)
		snapshot, err := NewDecoder().Decode(context.Background(), append(content, '\n'), "file:span-links", "sha256:"+strings.Repeat("e", 64), "default", start, start.Add(time.Minute), start.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	publisher, worker := resource("publisher", producer), resource("worker", consumer)
	forward, reverse := decode([]*tracepb.ResourceSpans{publisher, worker}), decode([]*tracepb.ResourceSpans{worker, publisher})
	if !reflect.DeepEqual(forward, reverse) || len(forward.Observations) != 1 || forward.Observations[0].Confidence != topology.High || forward.Observations[0].TargetServiceKey != "worker" {
		t.Fatalf("decoded SpanLink differs by resource order:\nforward=%+v\nreverse=%+v", forward, reverse)
	}
	encoded, err := json.Marshal(forward)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"span-link-secret", "authorization", "CQkJCQ", "CAgICA"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("SpanLink snapshot exposed %q: %s", forbidden, encoded)
		}
	}
	for _, evidence := range forward.Evidence {
		if !strings.HasPrefix(evidence.Fingerprint, telemetry.EvidenceFingerprintV1+":") || strings.Contains(evidence.Fingerprint, hex.EncodeToString(producerTrace)) {
			t.Fatalf("unsafe evidence=%+v", evidence)
		}
	}
}

func TestSpanLinkValidationDropsAttributesAndReportsQualityAndAmbiguity(t *testing.T) {
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	stringValue := func(value string) *commonpb.AnyValue {
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}}
	}
	span := &tracepb.Span{
		TraceId: bytesOf(1, 16), SpanId: bytesOf(2, 8), Kind: tracepb.Span_SPAN_KIND_CONSUMER,
		StartTimeUnixNano: uint64(start.UnixNano()), EndTimeUnixNano: uint64(start.Add(time.Nanosecond).UnixNano()), DroppedLinksCount: 2,
		Links: []*tracepb.Span_Link{{TraceId: bytesOf(3, 16), SpanId: bytesOf(4, 8), Attributes: []*commonpb.KeyValue{{Key: "authorization", Value: stringValue("Bearer secret")}}}},
	}
	decoded, _, err := decodeSpan(span, "worker", "worker", "sha256:"+strings.Repeat("a", 64), start, start.Add(time.Minute))
	if err != nil || len(decoded.links) != 1 || decoded.droppedLinks != 2 {
		t.Fatalf("decoded link=%+v err=%v", decoded, err)
	}
	if encoded := fmt.Sprintf("%+v", decoded.links); strings.Contains(encoded, "secret") || strings.Contains(encoded, "authorization") {
		t.Fatalf("link attributes retained: %s", encoded)
	}
	invalid := proto.Clone(span).(*tracepb.Span)
	invalid.Links = []*tracepb.Span_Link{{TraceId: bytesOf(0, 16), SpanId: bytesOf(4, 8)}}
	if _, _, err := decodeSpan(invalid, "worker", "worker", "sha256:"+strings.Repeat("a", 64), start, start.Add(time.Minute)); err == nil || !strings.Contains(err.Error(), "span link") {
		t.Fatalf("invalid link error=%v", err)
	}
	tooMany := proto.Clone(span).(*tracepb.Span)
	tooMany.Links = make([]*tracepb.Span_Link, telemetry.MaxLinksPerSpan+1)
	if _, _, err := decodeSpan(tooMany, "worker", "worker", "sha256:"+strings.Repeat("a", 64), start, start.Add(time.Minute)); err == nil || !strings.Contains(err.Error(), "link count") {
		t.Fatalf("link limit error=%v", err)
	}

	producer := decodedSpan{serviceKey: "publisher", serviceDisplay: "publisher", spanKey: "producer", kind: tracepb.Span_SPAN_KIND_PRODUCER, duration: 1, evidence: "producer", attrs: attributes{messagingSystem: "kafka", messagingDest: "orders"}}
	alpha := decodedSpan{serviceKey: "alpha", serviceDisplay: "alpha", kind: tracepb.Span_SPAN_KIND_CONSUMER, evidence: "alpha"}
	beta := decodedSpan{serviceKey: "beta", serviceDisplay: "beta", kind: tracepb.Span_SPAN_KIND_CONSUMER, evidence: "beta"}
	resolution := resolveDependency(producer, []linkedSpan{{span: beta, association: "span_link"}, {span: alpha, association: "span_link"}}, map[string]telemetry.Service{})
	if !resolution.ok || !resolution.ambiguous || resolution.dependency.Kind != "queue" || !strings.Contains(strings.Join(resolution.limitations, " "), "Multiple directly linked") {
		t.Fatalf("ambiguous SpanLinks resolution=%+v", resolution)
	}

	dropped := resolveDependency(producer, []linkedSpan{{span: decodedSpan{serviceKey: "worker", serviceDisplay: "worker", kind: tracepb.Span_SPAN_KIND_CONSUMER, evidence: "consumer", droppedLinks: 1}, association: "span_link"}}, map[string]telemetry.Service{})
	if dropped.confidence != topology.Medium || !strings.Contains(strings.Join(dropped.limitations, " "), "Dropped SpanLinks") {
		t.Fatalf("dropped-link resolution=%+v", dropped)
	}
}

func TestDecodeIgnoresUnknownFieldAndRejectsNegativeDurationWithoutRawPayload(t *testing.T) {
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	unknown := []byte(`{"unknownSecretField":"do-not-echo","resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"api"}}]},"scopeSpans":[{"spans":[{"traceId":"01010101010101010101010101010101","spanId":"0202020202020202","kind":1,"startTimeUnixNano":"1787140801000000000","endTimeUnixNano":"1787140801000000001"}]}]}]}` + "\n")
	snapshot, err := NewDecoder().Decode(context.Background(), unknown, "file:test", "sha256:"+strings.Repeat("a", 64), "default", start, start.Add(time.Minute), start)
	if err != nil {
		t.Fatalf("unknown field should be ignored: %v", err)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "do-not-echo") || strings.Contains(string(encoded), "unknownSecretField") {
		t.Fatalf("unknown field escaped sanitization: %s", encoded)
	}

	negative := []byte(`{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"api"}}]},"scopeSpans":[{"spans":[{"traceId":"01010101010101010101010101010101","spanId":"0202020202020202","kind":1,"startTimeUnixNano":"1787140803000000000","endTimeUnixNano":"1787140802000000000"}]}]}]}` + "\n")
	_, err = NewDecoder().Decode(context.Background(), negative, "file:test", "sha256:"+strings.Repeat("a", 64), "default", start, start.Add(time.Minute), start)
	if err == nil || strings.Contains(err.Error(), "do-not-echo") {
		t.Fatalf("negative duration error = %v", err)
	}
}

func TestDecodeRejectsOversizedLine(t *testing.T) {
	input := append(bytesOf(' ', telemetry.MaxLineBytes+1), '\n')
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	if _, err := NewDecoder().Decode(context.Background(), input, "file:test", "sha256:"+strings.Repeat("a", 64), "default", start, start.Add(time.Minute), start); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized error = %v", err)
	}
}

func TestDecodeDatabaseMissingEndpointMissingServiceAndUnknownTarget(t *testing.T) {
	input := []byte(`{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"api"}}]},"scopeSpans":[{"spans":[{"traceId":"01010101010101010101010101010101","spanId":"0101010101010101","kind":2,"startTimeUnixNano":"1787140801000000000","endTimeUnixNano":"1787140801000000010","attributes":[{"key":"url.path","value":{"stringValue":"/customer/123456"}}]},{"traceId":"02020202020202020202020202020202","spanId":"0202020202020202","kind":3,"startTimeUnixNano":"1787140802000000000","endTimeUnixNano":"1787140802000000020","attributes":[{"key":"db.system.name","value":{"stringValue":"postgresql"}},{"key":"db.namespace","value":{"stringValue":"orders"}}]},{"traceId":"03030303030303030303030303030303","spanId":"0303030303030303","kind":3,"startTimeUnixNano":"1787140803000000000","endTimeUnixNano":"1787140803000000030"}]}]},{"resource":{},"scopeSpans":[{"spans":[{"traceId":"04040404040404040404040404040404","spanId":"0404040404040404","kind":2,"startTimeUnixNano":"1787140804000000000","endTimeUnixNano":"1787140804000000040"}]}]}]}` + "\n")
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	snapshot, err := NewDecoder().Decode(context.Background(), input, "file:test", "sha256:"+strings.Repeat("a", 64), "default", start, start.Add(time.Minute), start)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Stats.Services != 1 || snapshot.Stats.Endpoints != 0 || snapshot.Stats.Dependencies != 1 || len(snapshot.Windows) != 1 {
		t.Fatalf("snapshot stats=%+v windows=%+v", snapshot.Stats, snapshot.Windows)
	}
	if snapshot.Dependencies[0].Kind != "database" || snapshot.Observations[0].Confidence != topology.Medium {
		t.Fatalf("dependency=%+v observation=%+v", snapshot.Dependencies[0], snapshot.Observations[0])
	}
	encoded, _ := json.Marshal(snapshot)
	if strings.Contains(string(encoded), "/customer/123456") || strings.Contains(string(encoded), "url.path") {
		t.Fatalf("concrete path escaped: %s", encoded)
	}
	joined := strings.Join(snapshot.Warnings, " ")
	if !strings.Contains(joined, "safe endpoint") || !strings.Contains(joined, "destination data") || !strings.Contains(joined, "service.name") {
		t.Fatalf("warnings=%#v", snapshot.Warnings)
	}
}

func TestAggregateServerWindowsRollUpServicesAndEndpointsDeterministically(t *testing.T) {
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	server := func(key string, duration int64, failed bool) decodedSpan {
		return decodedSpan{
			serviceKey: "payment", serviceDisplay: "payment", kind: tracepb.Span_SPAN_KIND_SERVER,
			duration: duration, isError: failed, endpointKey: key,
		}
	}
	firstEndpoint := "payment\x00http\x00GET /payments/{id}"
	secondEndpoint := "payment\x00http\x00POST /payments"
	build := func(spans []decodedSpan) telemetry.Snapshot {
		t.Helper()
		snapshot := telemetry.Snapshot{WindowStart: start, WindowEnd: start.Add(time.Minute), ObservedAt: start.Add(time.Minute)}
		if err := aggregate(&snapshot, spans); err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	windowByKey := func(t *testing.T, snapshot telemetry.Snapshot, key string) telemetry.Window {
		t.Helper()
		for _, window := range snapshot.Windows {
			if window.Key == key {
				return window
			}
		}
		t.Fatalf("window %q not found in %+v", key, snapshot.Windows)
		return telemetry.Window{}
	}

	t.Run("one endpoint materializes service and endpoint windows", func(t *testing.T) {
		snapshot := build([]decodedSpan{server(firstEndpoint, 10, true)})
		if snapshot.Stats.TelemetryWindows != 2 || len(snapshot.Windows) != 2 {
			t.Fatalf("windows=%+v stats=%+v", snapshot.Windows, snapshot.Stats)
		}
		serviceWindow := windowByKey(t, snapshot, "payment\x00")
		endpointWindow := windowByKey(t, snapshot, firstEndpoint)
		if serviceWindow.EndpointKey != "" || endpointWindow.EndpointKey != firstEndpoint || serviceWindow.RequestCount != 1 || endpointWindow.RequestCount != 1 || serviceWindow.ErrorCount != 1 || endpointWindow.ErrorCount != 1 {
			t.Fatalf("service=%+v endpoint=%+v", serviceWindow, endpointWindow)
		}
	})

	t.Run("multiple endpoints use raw spans for service percentiles", func(t *testing.T) {
		snapshot := build([]decodedSpan{
			server(firstEndpoint, 10, false),
			server(secondEndpoint, 20, true),
			server(firstEndpoint, 30, false),
		})
		serviceWindow := windowByKey(t, snapshot, "payment\x00")
		if snapshot.Stats.TelemetryWindows != 3 || serviceWindow.RequestCount != 3 || serviceWindow.ErrorCount != 1 || serviceWindow.DurationSumNS != 60 || serviceWindow.P50NS != 20 || serviceWindow.P95NS != 30 || serviceWindow.P99NS != 30 {
			t.Fatalf("service window=%+v stats=%+v", serviceWindow, snapshot.Stats)
		}
	})

	t.Run("no endpoint materializes service only without duplication", func(t *testing.T) {
		snapshot := build([]decodedSpan{server("", 10, false)})
		if snapshot.Stats.TelemetryWindows != 1 || len(snapshot.Windows) != 1 {
			t.Fatalf("windows=%+v stats=%+v", snapshot.Windows, snapshot.Stats)
		}
		serviceWindow := windowByKey(t, snapshot, "payment\x00")
		if serviceWindow.RequestCount != 1 || serviceWindow.EndpointKey != "" {
			t.Fatalf("service window=%+v", serviceWindow)
		}
	})

	t.Run("input order is stable", func(t *testing.T) {
		forward := build([]decodedSpan{server(firstEndpoint, 10, false), server(secondEndpoint, 20, true)})
		reverse := build([]decodedSpan{server(secondEndpoint, 20, true), server(firstEndpoint, 10, false)})
		if !reflect.DeepEqual(forward, reverse) {
			t.Fatalf("order changed snapshot:\nforward=%+v\nreverse=%+v", forward, reverse)
		}
	})
}

func TestAggregateFailsExplicitlyAtServiceCardinalityLimit(t *testing.T) {
	spans := make([]decodedSpan, telemetry.MaxServices+1)
	for index := range spans {
		spans[index] = decodedSpan{serviceKey: fmt.Sprintf("service-%04d", index), serviceDisplay: "service", kind: tracepb.Span_SPAN_KIND_SERVER, duration: 1}
	}
	snapshot := telemetry.Snapshot{WindowStart: time.Now(), WindowEnd: time.Now().Add(time.Minute)}
	if err := aggregate(&snapshot, spans); err == nil || !strings.Contains(err.Error(), "cardinality") {
		t.Fatalf("aggregate error=%v", err)
	}
}

func TestAggregateFailsExplicitlyAtEndpointAndDependencyCardinalityLimits(t *testing.T) {
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	endpointSpans := make([]decodedSpan, telemetry.MaxEndpoints+1)
	for index := range endpointSpans {
		endpointSpans[index] = decodedSpan{
			serviceKey: "api", serviceDisplay: "api", kind: tracepb.Span_SPAN_KIND_SERVER, duration: 1,
			endpointKey: fmt.Sprintf("api\x00http\x00GET /route-%04d", index),
		}
	}
	endpointSnapshot := telemetry.Snapshot{WindowStart: start, WindowEnd: start.Add(time.Minute)}
	if err := aggregate(&endpointSnapshot, endpointSpans); err == nil || !strings.Contains(err.Error(), "endpoint cardinality") {
		t.Fatalf("endpoint aggregate error=%v", err)
	}

	dependencySpans := make([]decodedSpan, telemetry.MaxDependencies+1)
	for index := range dependencySpans {
		dependencySpans[index] = decodedSpan{
			serviceKey: "api", serviceDisplay: "api", kind: tracepb.Span_SPAN_KIND_CLIENT, duration: 1,
			attrs: attributes{serverAddress: fmt.Sprintf("target-%04d.invalid", index)}, evidence: fmt.Sprintf("evidence-%04d", index),
		}
	}
	dependencySnapshot := telemetry.Snapshot{WindowStart: start, WindowEnd: start.Add(time.Minute)}
	if err := aggregate(&dependencySnapshot, dependencySpans); err == nil || !strings.Contains(err.Error(), "dependency cardinality") {
		t.Fatalf("dependency aggregate error=%v", err)
	}
}

func TestAggregateCapsEvidenceSamples(t *testing.T) {
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	spans := make([]decodedSpan, telemetry.MaxEvidenceSamples+3)
	for index := range spans {
		spans[index] = decodedSpan{
			serviceKey: "api", serviceDisplay: "api", kind: tracepb.Span_SPAN_KIND_CLIENT, duration: 1,
			attrs: attributes{serverAddress: "example.invalid"}, evidence: fmt.Sprintf("evidence-%d", index),
		}
	}
	snapshot := telemetry.Snapshot{WindowStart: start, WindowEnd: start.Add(time.Minute), ObservedAt: start.Add(time.Minute)}
	if err := aggregate(&snapshot, spans); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Observations) != 1 || len(snapshot.Observations[0].EvidenceFingerprints) != telemetry.MaxEvidenceSamples || len(snapshot.Evidence) != telemetry.MaxEvidenceSamples {
		t.Fatalf("evidence observations=%+v evidence=%+v", snapshot.Observations, snapshot.Evidence)
	}
}

func TestDecodeSpanRejectsZeroIdentifiersIncludingParent(t *testing.T) {
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	validTrace := bytesOf(1, 16)
	validSpan := bytesOf(2, 8)
	for _, test := range []struct {
		name   string
		trace  []byte
		span   []byte
		parent []byte
	}{
		{name: "trace", trace: bytesOf(0, 16), span: validSpan},
		{name: "span", trace: validTrace, span: bytesOf(0, 8)},
		{name: "parent", trace: validTrace, span: validSpan, parent: bytesOf(0, 8)},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := decodeSpan(&tracepb.Span{
				TraceId: test.trace, SpanId: test.span, ParentSpanId: test.parent,
				StartTimeUnixNano: uint64(start.UnixNano()), EndTimeUnixNano: uint64(start.Add(time.Nanosecond).UnixNano()),
			}, "api", "api", "sha256:"+strings.Repeat("a", 64), start, start.Add(time.Minute))
			if err == nil || !strings.Contains(err.Error(), "non-zero") {
				t.Fatalf("decodeSpan error=%v", err)
			}
		})
	}
}

func TestDecodeSpanRejectsUnknownEnumValues(t *testing.T) {
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	valid := func() *tracepb.Span {
		return &tracepb.Span{
			TraceId: bytesOf(1, 16), SpanId: bytesOf(2, 8),
			StartTimeUnixNano: uint64(start.UnixNano()), EndTimeUnixNano: uint64(start.Add(time.Nanosecond).UnixNano()),
		}
	}
	invalidKind := valid()
	invalidKind.Kind = tracepb.Span_SpanKind(999)
	invalidStatus := valid()
	invalidStatus.Status = &tracepb.Status{Code: tracepb.Status_StatusCode(999)}
	for _, test := range []struct {
		name string
		span *tracepb.Span
	}{
		{name: "kind", span: invalidKind},
		{name: "status", span: invalidStatus},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := decodeSpan(test.span, "api", "api", "sha256:"+strings.Repeat("a", 64), start, start.Add(time.Minute)); err == nil || !strings.Contains(err.Error(), "invalid") {
				t.Fatalf("decodeSpan error=%v", err)
			}
		})
	}
}

func TestAllowlistedAttributesAreStrictAndDiagnosticsAreSanitized(t *testing.T) {
	stringValue := func(value string) *commonpb.AnyValue {
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}}
	}
	intValue := func(value int64) *commonpb.AnyValue {
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: value}}
	}
	for _, test := range []struct {
		name  string
		key   string
		value *commonpb.AnyValue
	}{
		{name: "wrong type", key: "peer.service", value: intValue(1)},
		{name: "empty", key: "peer.service", value: stringValue("   ")},
		{name: "control", key: "peer.service", value: stringValue("secret\nvalue")},
		{name: "oversize", key: "peer.service", value: stringValue(strings.Repeat("x", 513))},
		{name: "url destination", key: "peer.service", value: stringValue("https://user:secret@example.invalid?q=secret")},
		{name: "rpc query", key: "rpc.method", value: stringValue("GetUser?token=secret")},
		{name: "database query", key: "db.namespace", value: stringValue("orders?token=secret")},
		{name: "database credential", key: "db.namespace", value: stringValue("user:secret")},
		{name: "messaging credentials", key: "messaging.destination.name", value: stringValue("user:secret@queue")},
		{name: "server URL", key: "server.address", value: stringValue("https://example.invalid/path")},
		{name: "zero port", key: "server.port", value: intValue(0)},
		{name: "large port", key: "server.port", value: intValue(65536)},
		{name: "invalid status", key: "http.response.status_code", value: intValue(99)},
		{name: "invalid method", key: "http.request.method", value: stringValue("GET /secret")},
		{name: "route URL credential", key: "http.route", value: stringValue("/proxy/https://user:secret@example.invalid")},
		{name: "network path route", key: "http.route", value: stringValue("//example.invalid/path")},
		{name: "empty semantic database", key: "db.system.name", value: stringValue("---")},
		{name: "empty semantic messaging", key: "messaging.system", value: stringValue("...")},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := spanAttributes([]*commonpb.KeyValue{{Key: test.key, Value: test.value}})
			if err == nil {
				t.Fatal("spanAttributes accepted invalid allowlisted value")
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "example.invalid") {
				t.Fatalf("diagnostic exposed input: %v", err)
			}
		})
	}
}

func TestDuplicateUnknownAttributesAreRejected(t *testing.T) {
	value := &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "redacted"}}
	attributes := []*commonpb.KeyValue{{Key: "custom.secret", Value: value}, {Key: "custom.secret", Value: value}}
	if _, err := spanAttributes(attributes); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("span duplicate error=%v", err)
	}
	if _, _, _, err := resourceIdentity(attributes); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("resource duplicate error=%v", err)
	}
}

func TestResourceIdentityRejectsInvalidAllowlistedValuesAndUnknownServicePrefixes(t *testing.T) {
	stringValue := func(value string) *commonpb.AnyValue {
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}}
	}
	for _, value := range []string{"", strings.Repeat("x", maxIdentityLength+1), "api\nsecret", "---", "https://user:secret@example.invalid?q=secret"} {
		_, _, _, err := resourceIdentity([]*commonpb.KeyValue{{Key: "service.name", Value: stringValue(value)}})
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("service.name %q error=%v", value, err)
		}
	}
	for _, value := range []string{"unknown_service", "unknown_service.foo", "unknown_service_go", "unknown_service/api"} {
		_, _, valid, err := resourceIdentity([]*commonpb.KeyValue{{Key: "service.name", Value: stringValue(value)}})
		if err != nil || valid {
			t.Fatalf("unknown service %q valid=%t err=%v", value, valid, err)
		}
	}
}

func TestEvidenceFingerprintFixedVector(t *testing.T) {
	traceID := make([]byte, 16)
	spanID := make([]byte, 8)
	for index := range traceID {
		traceID[index] = byte(index)
	}
	for index := range spanID {
		spanID[index] = byte(index)
	}
	const want = "sha256-v1:1b6c39feeee44366a02aebfb1dd30f9b72e4d5c73115265f6d8af056f3795b9d"
	if got := evidenceFingerprint("sha256:"+strings.Repeat("a", 64), traceID, spanID); got != want {
		t.Fatalf("fingerprint=%q want=%q", got, want)
	}
}

func TestAggregateRejectsWindowAndDependencyDurationOverflow(t *testing.T) {
	const maxInt64 = int64(^uint64(0) >> 1)
	windowSnapshot := telemetry.Snapshot{WindowStart: time.Unix(0, 0), WindowEnd: time.Unix(1, 0)}
	windowSpans := []decodedSpan{
		{serviceKey: "api", serviceDisplay: "api", kind: tracepb.Span_SPAN_KIND_SERVER, duration: maxInt64},
		{serviceKey: "api", serviceDisplay: "api", kind: tracepb.Span_SPAN_KIND_SERVER, duration: 1},
	}
	if err := aggregate(&windowSnapshot, windowSpans); err == nil || !strings.Contains(err.Error(), "window duration") {
		t.Fatalf("window overflow error=%v", err)
	}

	dependencySnapshot := telemetry.Snapshot{WindowStart: time.Unix(0, 0), WindowEnd: time.Unix(1, 0)}
	dependencySpans := []decodedSpan{
		{serviceKey: "api", serviceDisplay: "api", kind: tracepb.Span_SPAN_KIND_CLIENT, duration: maxInt64, attrs: attributes{serverAddress: "example.invalid"}},
		{serviceKey: "api", serviceDisplay: "api", kind: tracepb.Span_SPAN_KIND_CLIENT, duration: 1, attrs: attributes{serverAddress: "example.invalid"}},
	}
	if err := aggregate(&dependencySnapshot, dependencySpans); err == nil || !strings.Contains(err.Error(), "dependency duration") {
		t.Fatalf("dependency overflow error=%v", err)
	}
}

func TestHighConfidenceAlwaysCarriesTheLinkedEvidencePairAtTheSampleCap(t *testing.T) {
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	spans := make([]decodedSpan, 0, telemetry.MaxEvidenceSamples+3)
	for index := 0; index < telemetry.MaxEvidenceSamples+1; index++ {
		spans = append(spans, decodedSpan{
			serviceKey: "checkout", serviceDisplay: "checkout", traceKey: fmt.Sprintf("medium-%d", index), spanKey: fmt.Sprintf("medium-%d:client", index),
			kind: tracepb.Span_SPAN_KIND_CLIENT, duration: 1, attrs: attributes{peerService: "payment"}, evidence: fmt.Sprintf("medium-%02d", index),
		})
	}
	spans = append(spans,
		decodedSpan{serviceKey: "checkout", serviceDisplay: "checkout", traceKey: "linked", spanKey: "linked:client", kind: tracepb.Span_SPAN_KIND_CLIENT, duration: 1, evidence: "zz-client"},
		decodedSpan{serviceKey: "payment", serviceDisplay: "payment", traceKey: "linked", spanKey: "linked:server", parentKey: "linked:client", kind: tracepb.Span_SPAN_KIND_SERVER, duration: 1, evidence: "zz-server"},
	)
	snapshot := telemetry.Snapshot{WindowStart: start, WindowEnd: start.Add(time.Minute), ObservedAt: start.Add(time.Minute)}
	if err := aggregate(&snapshot, spans); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Observations) != 1 || snapshot.Observations[0].Confidence != topology.High {
		t.Fatalf("observations=%+v", snapshot.Observations)
	}
	evidence := strings.Join(snapshot.Observations[0].EvidenceFingerprints, ",")
	if len(snapshot.Observations[0].EvidenceFingerprints) != telemetry.MaxEvidenceSamples || !strings.Contains(evidence, "zz-client") || !strings.Contains(evidence, "zz-server") {
		t.Fatalf("HIGH evidence did not preserve pair: %v", snapshot.Observations[0].EvidenceFingerprints)
	}
}

func TestAmbiguousLinkedChildrenDoNotChooseAServiceByInputOrder(t *testing.T) {
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	build := func(childrenOrder []decodedSpan) telemetry.Snapshot {
		spans := []decodedSpan{{serviceKey: "checkout", serviceDisplay: "checkout", traceKey: "trace", spanKey: "trace:client", kind: tracepb.Span_SPAN_KIND_CLIENT, duration: 1, evidence: "client", attrs: attributes{peerService: "fallback"}}}
		spans = append(spans, childrenOrder...)
		spans = append(spans, decodedSpan{serviceKey: "fallback", serviceDisplay: "fallback", traceKey: "other", spanKey: "other:server", kind: tracepb.Span_SPAN_KIND_SERVER, duration: 1, evidence: "fallback"})
		snapshot := telemetry.Snapshot{WindowStart: start, WindowEnd: start.Add(time.Minute), ObservedAt: start.Add(time.Minute)}
		if err := aggregate(&snapshot, spans); err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	first := decodedSpan{serviceKey: "alpha", serviceDisplay: "alpha", traceKey: "trace", spanKey: "trace:alpha", parentKey: "trace:client", kind: tracepb.Span_SPAN_KIND_SERVER, duration: 1, evidence: "alpha"}
	second := decodedSpan{serviceKey: "beta", serviceDisplay: "beta", traceKey: "trace", spanKey: "trace:beta", parentKey: "trace:client", kind: tracepb.Span_SPAN_KIND_SERVER, duration: 1, evidence: "beta"}
	left, right := build([]decodedSpan{first, second}), build([]decodedSpan{second, first})
	if !reflect.DeepEqual(left.Dependencies, right.Dependencies) || !reflect.DeepEqual(left.Observations, right.Observations) {
		t.Fatalf("ambiguous children changed result by order:\nleft=%+v/%+v\nright=%+v/%+v", left.Dependencies, left.Observations, right.Dependencies, right.Observations)
	}
	if len(left.Observations) != 1 || left.Observations[0].Confidence != topology.Medium || left.Dependencies[0].LogicalKey != "fallback" || len(left.Observations[0].EvidenceFingerprints) != 1 || left.Observations[0].EvidenceFingerprints[0] != "client" {
		t.Fatalf("ambiguous children produced unsupported relation: %+v %+v", left.Dependencies, left.Observations)
	}
	limitations := strings.Join(left.Observations[0].Limitations, " ")
	if !strings.Contains(limitations, "Multiple directly linked") || strings.Contains(limitations, "No linked remote span") {
		t.Fatalf("ambiguous limitation is misleading: %q", limitations)
	}
}

func TestDependencyClassificationAndIdentityAreUnambiguous(t *testing.T) {
	services := map[string]telemetry.Service{}
	postgresRedis := resolveDependency(decodedSpan{attrs: attributes{dbSystem: "postgresql", dbNamespace: "redis"}}, nil, services)
	if !postgresRedis.ok || postgresRedis.confidence != topology.Medium || postgresRedis.dependency.Kind != "database" {
		t.Fatalf("postgres/redis resolution=%+v", postgresRedis)
	}
	plainIPv6 := resolveDependency(decodedSpan{attrs: attributes{serverAddress: "2001:db8::1"}}, nil, services)
	if !plainIPv6.ok {
		t.Fatal("plain IPv6 was not resolved")
	}
	portIPv6 := resolveDependency(decodedSpan{attrs: attributes{serverAddress: "2001:db8::1", serverPort: 443, serverPortSet: true}}, nil, services)
	if !portIPv6.ok || plainIPv6.dependency.LogicalKey == portIPv6.dependency.LogicalKey || portIPv6.dependency.LogicalKey != "[2001:db8::1]:443" {
		t.Fatalf("IPv6 identities plain=%q port=%q", plainIPv6.dependency.LogicalKey, portIPv6.dependency.LogicalKey)
	}
}

func TestAggregateMergesDisplayMetadataDeterministically(t *testing.T) {
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	first := decodedSpan{serviceKey: "api", serviceDisplay: "Zulu", traceKey: "one", spanKey: "one:client", kind: tracepb.Span_SPAN_KIND_CLIENT, duration: 1, attrs: attributes{peerService: "Payment"}, evidence: "one"}
	second := decodedSpan{serviceKey: "api", serviceDisplay: "Alpha", traceKey: "two", spanKey: "two:client", kind: tracepb.Span_SPAN_KIND_CLIENT, duration: 1, attrs: attributes{peerService: "payment"}, evidence: "two"}
	build := func(spans []decodedSpan) telemetry.Snapshot {
		snapshot := telemetry.Snapshot{WindowStart: start, WindowEnd: start.Add(time.Minute), ObservedAt: start.Add(time.Minute)}
		if err := aggregate(&snapshot, spans); err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	left, right := build([]decodedSpan{first, second}), build([]decodedSpan{second, first})
	if !reflect.DeepEqual(left.Services, right.Services) || !reflect.DeepEqual(left.Dependencies, right.Dependencies) || left.Services[0].DisplayName != "Alpha" || left.Dependencies[0].DisplayName != "Payment" {
		t.Fatalf("metadata merge depends on order: left=%+v/%+v right=%+v/%+v", left.Services, left.Dependencies, right.Services, right.Dependencies)
	}
}

func TestEqualConfidenceObservationMetadataIsOrderIndependent(t *testing.T) {
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	peer := decodedSpan{serviceKey: "api", serviceDisplay: "api", traceKey: "peer", spanKey: "peer:client", kind: tracepb.Span_SPAN_KIND_CLIENT, duration: 1, attrs: attributes{peerService: "target"}, evidence: "peer"}
	rpc := decodedSpan{serviceKey: "api", serviceDisplay: "api", traceKey: "rpc", spanKey: "rpc:client", kind: tracepb.Span_SPAN_KIND_CLIENT, duration: 1, attrs: attributes{rpcService: "target"}, evidence: "rpc"}
	build := func(spans []decodedSpan) telemetry.Snapshot {
		snapshot := telemetry.Snapshot{WindowStart: start, WindowEnd: start.Add(time.Minute), ObservedAt: start.Add(time.Minute)}
		if err := aggregate(&snapshot, spans); err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	left, right := build([]decodedSpan{peer, rpc}), build([]decodedSpan{rpc, peer})
	if !reflect.DeepEqual(left.Observations, right.Observations) || len(left.Observations) != 1 || left.Observations[0].Basis != "RPC semantic convention" {
		t.Fatalf("equal-rank observation metadata depends on order: left=%+v right=%+v", left.Observations, right.Observations)
	}
}

func TestDecodeRejectsDuplicateSpanIdentityAndAggregateHonorsCancellation(t *testing.T) {
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	duplicate := &tracepb.Span{TraceId: bytesOf(1, 16), SpanId: bytesOf(2, 8), Kind: tracepb.Span_SPAN_KIND_INTERNAL, StartTimeUnixNano: uint64(start.UnixNano()), EndTimeUnixNano: uint64(start.Add(time.Nanosecond).UnixNano())}
	request := &collecttracepb.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
		Resource:   &resourcepb.Resource{Attributes: []*commonpb.KeyValue{{Key: "service.name", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "api"}}}}},
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{duplicate, duplicate}}},
	}}}
	content := marshalOTLPJSON(t, request)
	if _, err := NewDecoder().Decode(context.Background(), append(content, '\n'), "file:test", "sha256:"+strings.Repeat("a", 64), "default", start, start.Add(time.Minute), start); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate span error=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := aggregateContext(ctx, &telemetry.Snapshot{WindowStart: start, WindowEnd: start.Add(time.Minute)}, []decodedSpan{{serviceKey: "api"}}); err != context.Canceled {
		t.Fatalf("aggregate cancellation error=%v", err)
	}
}

func marshalOTLPJSON(t *testing.T, request *collecttracepb.ExportTraceServiceRequest) []byte {
	t.Helper()
	binary, err := proto.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	traces, err := (&ptrace.ProtoUnmarshaler{}).UnmarshalTraces(binary)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := (&ptrace.JSONMarshaler{}).MarshalTraces(traces)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func bytesOf(value byte, count int) []byte {
	result := make([]byte, count)
	for index := range result {
		result[index] = value
	}
	return result
}

func assertHexBytes(t *testing.T, name string, got []byte, want string) {
	t.Helper()
	if encoded := hex.EncodeToString(got); encoded != want {
		t.Fatalf("%s=%q want=%q", name, encoded, want)
	}
}
