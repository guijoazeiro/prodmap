// Package otel translates canonical OTLP/JSON traces into sanitized Prodmap telemetry.
package otel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/inventory"
	"github.com/guijoazeiro/prodmap/internal/telemetry"
	"github.com/guijoazeiro/prodmap/internal/topology"
	"go.opentelemetry.io/collector/pdata/ptrace"
	collecttracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

const maxIdentityLength = 255

type Decoder struct{}

func NewDecoder() *Decoder { return &Decoder{} }

type attributes struct {
	httpMethod      string
	httpRoute       string
	httpStatusCode  int64
	rpcSystem       string
	rpcService      string
	rpcMethod       string
	serverAddress   string
	serverPort      int64
	serverPortSet   bool
	peerService     string
	dbSystem        string
	dbNamespace     string
	messagingSystem string
	messagingDest   string
	errorType       string
}

type decodedSpan struct {
	serviceKey     string
	serviceDisplay string
	kind           tracepb.Span_SpanKind
	traceKey       string
	spanKey        string
	parentKey      string
	evidence       string
	start          time.Time
	duration       int64
	isError        bool
	attrs          attributes
	endpointKey    string
	links          []spanLink
	droppedLinks   uint32
}

type spanLink struct {
	spanKey string
}

type linkedSpan struct {
	span        decodedSpan
	association string
}

type durationAggregate struct {
	count     int64
	errors    int64
	sum       int64
	durations []int64
}

type dependencyAggregate struct {
	observation telemetry.DependencyObservation
	evidence    map[string]string
	claims      map[string]string
	highPairs   map[string][2]string
	hardCap     bool
	conflict    bool
	capLimits   []string
}

type dependencyResolution struct {
	dependency       telemetry.Dependency
	targetServiceKey string
	confidence       topology.Confidence
	basis            string
	limitations      []string
	evidenceClaims   map[string]string
	pair             [2]string
	associatedError  bool
	ambiguous        bool
	ok               bool
}

func (d *Decoder) Decode(ctx context.Context, content []byte, sourceKey, sourceHash, environment string, start, end, observedAt time.Time) (telemetry.Snapshot, error) {
	if !end.After(start) {
		return telemetry.Snapshot{}, fmt.Errorf("%w: window-end must be after window-start", errs.ErrInvalid)
	}
	result := telemetry.Snapshot{SourceKey: sourceKey, SourceHash: sourceHash, Environment: environment, WindowStart: start.UTC(), WindowEnd: end.UTC(), ObservedAt: observedAt.UTC()}
	lines := bytes.Split(content, []byte{'\n'})
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return telemetry.Snapshot{}, fmt.Errorf("%w: telemetry file is empty", errs.ErrInvalid)
	}
	if len(lines) > telemetry.MaxLines {
		return telemetry.Snapshot{}, fmt.Errorf("%w: telemetry input exceeds %d lines", errs.ErrInvalid, telemetry.MaxLines)
	}
	spans := make([]decodedSpan, 0)
	seenSpanIDs := make(map[string]struct{})
	totalLinks := 0
	missingService := 0
	for index, line := range lines {
		if err := ctx.Err(); err != nil {
			return telemetry.Snapshot{}, err
		}
		lineNumber := index + 1
		if len(line) == 0 {
			return telemetry.Snapshot{}, lineError(lineNumber, "empty line")
		}
		if len(line) > telemetry.MaxLineBytes {
			return telemetry.Snapshot{}, lineError(lineNumber, fmt.Sprintf("line exceeds %d bytes", telemetry.MaxLineBytes))
		}
		request, err := decodeOTLPJSONLine(line)
		if err != nil {
			return telemetry.Snapshot{}, lineError(lineNumber, err.Error())
		}
		result.Stats.Lines++
		for _, resourceSpans := range request.GetResourceSpans() {
			result.Stats.ResourceSpans++
			serviceName, namespace, validService, identityErr := resourceIdentity(resourceSpans.GetResource().GetAttributes())
			if identityErr != nil {
				return telemetry.Snapshot{}, lineError(lineNumber, identityErr.Error())
			}
			serviceKey := ""
			if validService {
				serviceKey = inventory.NormalizeServiceLogicalKey(serviceName, namespace)
				validService = serviceKey != ""
			}
			for _, scopeSpans := range resourceSpans.GetScopeSpans() {
				for _, span := range scopeSpans.GetSpans() {
					if result.Stats.SpansSeen%256 == 0 {
						if err := ctx.Err(); err != nil {
							return telemetry.Snapshot{}, err
						}
					}
					result.Stats.SpansSeen++
					if result.Stats.SpansSeen > telemetry.MaxSpans {
						return telemetry.Snapshot{}, lineError(lineNumber, fmt.Sprintf("span count exceeds %d", telemetry.MaxSpans))
					}
					decoded, inside, err := decodeSpan(span, serviceKey, serviceName, sourceHash, start, end)
					if err != nil {
						return telemetry.Snapshot{}, lineError(lineNumber, err.Error())
					}
					totalLinks += len(decoded.links)
					if totalLinks > telemetry.MaxLinks {
						return telemetry.Snapshot{}, lineError(lineNumber, fmt.Sprintf("span link count exceeds %d", telemetry.MaxLinks))
					}
					if _, duplicate := seenSpanIDs[decoded.spanKey]; duplicate {
						return telemetry.Snapshot{}, lineError(lineNumber, "duplicate trace/span identifier")
					}
					seenSpanIDs[decoded.spanKey] = struct{}{}
					if !inside {
						result.Stats.SpansIgnored++
						continue
					}
					if !validService {
						missingService++
						result.Stats.SpansIgnored++
						continue
					}
					result.Stats.SpansAccepted++
					spans = append(spans, decoded)
				}
			}
		}
	}
	if result.Stats.SpansIgnored > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("%d spans were ignored because they were outside the window or lacked a useful service identity.", result.Stats.SpansIgnored))
	}
	if missingService > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("%d spans lacked a valid service.name and created no topology identity.", missingService))
	}
	if err := aggregateContext(ctx, &result, spans); err != nil {
		return telemetry.Snapshot{}, err
	}
	return result, nil
}

type otlpJSONEnvelope struct {
	ResourceSpans []struct {
		ScopeSpans []struct {
			Spans []struct {
				TraceID           string          `json:"traceId"`
				SpanID            string          `json:"spanId"`
				ParentSpanID      string          `json:"parentSpanId"`
				Kind              json.RawMessage `json:"kind"`
				StartTimeUnixNano json.RawMessage `json:"startTimeUnixNano"`
				EndTimeUnixNano   json.RawMessage `json:"endTimeUnixNano"`
				Links             []struct {
					TraceID string `json:"traceId"`
					SpanID  string `json:"spanId"`
				} `json:"links"`
				Status *struct {
					Code json.RawMessage `json:"code"`
				} `json:"status"`
			} `json:"spans"`
		} `json:"scopeSpans"`
	} `json:"resourceSpans"`
}

func decodeOTLPJSONLine(line []byte) (*collecttracepb.ExportTraceServiceRequest, error) {
	if err := validateOTLPJSONDialect(line); err != nil {
		return nil, err
	}
	traces, err := (&ptrace.JSONUnmarshaler{}).UnmarshalTraces(line)
	if err != nil {
		return nil, fmt.Errorf("invalid OTLP/JSON")
	}
	binary, err := (&ptrace.ProtoMarshaler{}).MarshalTraces(traces)
	if err != nil {
		return nil, fmt.Errorf("invalid OTLP/JSON trace data")
	}
	request := &collecttracepb.ExportTraceServiceRequest{}
	if err := proto.Unmarshal(binary, request); err != nil {
		return nil, fmt.Errorf("invalid OTLP/JSON trace bridge")
	}
	return request, nil
}

func validateOTLPJSONDialect(line []byte) error {
	var envelope otlpJSONEnvelope
	if err := json.Unmarshal(line, &envelope); err != nil {
		return fmt.Errorf("invalid OTLP/JSON")
	}
	for _, resourceSpans := range envelope.ResourceSpans {
		for _, scopeSpans := range resourceSpans.ScopeSpans {
			for _, span := range scopeSpans.Spans {
				if !validOTLPHexIdentifier(span.TraceID, 16, true) || !validOTLPHexIdentifier(span.SpanID, 8, true) || !validOTLPHexIdentifier(span.ParentSpanID, 8, false) {
					return fmt.Errorf("OTLP/JSON trace or span identifier is invalid")
				}
				for _, link := range span.Links {
					if !validOTLPHexIdentifier(link.TraceID, 16, true) || !validOTLPHexIdentifier(link.SpanID, 8, true) {
						return fmt.Errorf("OTLP/JSON SpanLink identifier is invalid")
					}
				}
				if len(span.Kind) > 0 && !isJSONInteger(span.Kind) {
					return fmt.Errorf("OTLP/JSON span kind must be numeric")
				}
				if span.Status != nil && len(span.Status.Code) > 0 && !isJSONInteger(span.Status.Code) {
					return fmt.Errorf("OTLP/JSON status code must be numeric")
				}
				if !isJSONDecimalString(span.StartTimeUnixNano) || !isJSONDecimalString(span.EndTimeUnixNano) {
					return fmt.Errorf("OTLP/JSON span timestamps must be decimal strings")
				}
			}
		}
	}
	return nil
}

func validOTLPHexIdentifier(value string, byteLength int, required bool) bool {
	if value == "" {
		return !required
	}
	if len(value) != byteLength*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && !allZero(decoded)
}

func isJSONInteger(raw json.RawMessage) bool {
	var value int32
	return json.Unmarshal(raw, &value) == nil
}

func isJSONDecimalString(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var value string
	if json.Unmarshal(raw, &value) != nil || value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func lineError(line int, category string) error {
	return fmt.Errorf("%w: telemetry line %d: %s", errs.ErrInvalid, line, category)
}

func resourceIdentity(values []*commonpb.KeyValue) (string, string, bool, error) {
	allowed := map[string]bool{
		"service.name": false, "service.namespace": false, "service.instance.id": false,
		"service.version": false, "deployment.environment.name": false,
	}
	resourceValues := make(map[string]string, len(allowed))
	seenKeys := make(map[string]struct{}, len(values))
	for _, item := range values {
		if !validAttributeKey(item.GetKey()) {
			return "", "", false, fmt.Errorf("resource attribute has invalid key")
		}
		if _, duplicate := seenKeys[item.GetKey()]; duplicate {
			return "", "", false, fmt.Errorf("duplicate resource attribute key")
		}
		seenKeys[item.GetKey()] = struct{}{}
		seen, allowedKey := allowed[item.GetKey()]
		if !allowedKey {
			continue
		}
		if seen {
			return "", "", false, fmt.Errorf("duplicate allowlisted resource attribute")
		}
		allowed[item.GetKey()] = true
		typed, ok := item.GetValue().GetValue().(*commonpb.AnyValue_StringValue)
		if !ok {
			return "", "", false, fmt.Errorf("allowlisted resource attribute has invalid type")
		}
		value := strings.TrimSpace(typed.StringValue)
		if (value == "" && item.GetKey() != "service.namespace") || !utf8.ValidString(value) || len([]rune(value)) > 512 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return "", "", false, fmt.Errorf("allowlisted resource attribute has invalid value")
		}
		resourceValues[item.GetKey()] = value
	}
	name := resourceValues["service.name"]
	namespace := resourceValues["service.namespace"]
	if !allowed["service.name"] {
		return "", "", false, nil
	}
	if !safeIdentity(name, maxIdentityLength) {
		return "", "", false, fmt.Errorf("service.name has invalid value")
	}
	lower := strings.ToLower(strings.TrimSpace(name))
	if strings.HasPrefix(lower, "unknown_service") {
		return "", "", false, nil
	}
	if !safeResourceServiceIdentity(name) {
		return "", "", false, fmt.Errorf("service.name has invalid value")
	}
	if namespace != "" && !safeResourceServiceIdentity(namespace) {
		return "", "", false, fmt.Errorf("service.namespace has invalid value")
	}
	if inventory.NormalizeServiceLogicalKey(name, "") == "" {
		return "", "", false, fmt.Errorf("service.name has no canonical identity")
	}
	if namespace != "" && inventory.NormalizeServiceLogicalKey(namespace, "") == "" {
		return "", "", false, fmt.Errorf("service.namespace has no canonical identity")
	}
	return strings.TrimSpace(name), strings.TrimSpace(namespace), true, nil
}

func safeIdentity(value string, limit int) bool {
	value = strings.TrimSpace(value)
	return value != "" && utf8.ValidString(value) && len([]rune(value)) <= limit && strings.IndexFunc(value, unicode.IsControl) < 0
}

func safeResourceServiceIdentity(value string) bool {
	if !safeIdentity(value, maxIdentityLength) || strings.Contains(value, "://") || strings.ContainsAny(value, "?&#=@/:\\") {
		return false
	}
	useful := false
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			useful = true
			continue
		}
		if character != ' ' && character != '.' && character != '_' && character != '-' {
			return false
		}
	}
	return useful
}

func decodeSpan(span *tracepb.Span, serviceKey, display, sourceHash string, start, end time.Time) (decodedSpan, bool, error) {
	if len(span.GetTraceId()) != 16 || len(span.GetSpanId()) != 8 || (len(span.GetParentSpanId()) != 0 && len(span.GetParentSpanId()) != 8) {
		return decodedSpan{}, false, fmt.Errorf("invalid trace or span identifier length")
	}
	if allZero(span.GetTraceId()) || allZero(span.GetSpanId()) || (len(span.GetParentSpanId()) > 0 && allZero(span.GetParentSpanId())) {
		return decodedSpan{}, false, fmt.Errorf("trace and span identifiers must be non-zero")
	}
	switch span.GetKind() {
	case tracepb.Span_SPAN_KIND_UNSPECIFIED, tracepb.Span_SPAN_KIND_INTERNAL, tracepb.Span_SPAN_KIND_SERVER,
		tracepb.Span_SPAN_KIND_CLIENT, tracepb.Span_SPAN_KIND_PRODUCER, tracepb.Span_SPAN_KIND_CONSUMER:
	default:
		return decodedSpan{}, false, fmt.Errorf("span kind is invalid")
	}
	switch span.GetStatus().GetCode() {
	case tracepb.Status_STATUS_CODE_UNSET, tracepb.Status_STATUS_CODE_OK, tracepb.Status_STATUS_CODE_ERROR:
	default:
		return decodedSpan{}, false, fmt.Errorf("span status code is invalid")
	}
	startNS, endNS := span.GetStartTimeUnixNano(), span.GetEndTimeUnixNano()
	if startNS == 0 || endNS == 0 || endNS < startNS || startNS > uint64(^uint64(0)>>1) || endNS > uint64(^uint64(0)>>1) {
		return decodedSpan{}, false, fmt.Errorf("invalid span timestamps or negative duration")
	}
	started := time.Unix(0, int64(startNS)).UTC()
	inside := !started.Before(start) && started.Before(end)
	traceKey := hex.EncodeToString(span.GetTraceId())
	spanKey := traceKey + ":" + hex.EncodeToString(span.GetSpanId())
	parentKey := ""
	if len(span.GetParentSpanId()) > 0 {
		parentKey = traceKey + ":" + hex.EncodeToString(span.GetParentSpanId())
	}
	attrs, err := spanAttributes(span.GetAttributes())
	if err != nil {
		return decodedSpan{}, false, err
	}
	decoded := decodedSpan{
		serviceKey: serviceKey, serviceDisplay: strings.TrimSpace(display), kind: span.GetKind(), traceKey: traceKey,
		spanKey: spanKey, parentKey: parentKey, evidence: evidenceFingerprint(sourceHash, span.GetTraceId(), span.GetSpanId()),
		start: started, duration: int64(endNS - startNS), isError: span.GetStatus().GetCode() == tracepb.Status_STATUS_CODE_ERROR || attrs.httpStatusCode >= 500, attrs: attrs,
		droppedLinks: span.GetDroppedLinksCount(),
	}
	if len(span.GetLinks()) > telemetry.MaxLinksPerSpan {
		return decodedSpan{}, false, fmt.Errorf("span link count exceeds %d", telemetry.MaxLinksPerSpan)
	}
	for _, link := range span.GetLinks() {
		if len(link.GetTraceId()) != 16 || len(link.GetSpanId()) != 8 || allZero(link.GetTraceId()) || allZero(link.GetSpanId()) {
			return decodedSpan{}, false, fmt.Errorf("span link has invalid trace or span identifier")
		}
		decoded.links = append(decoded.links, spanLink{spanKey: hex.EncodeToString(link.GetTraceId()) + ":" + hex.EncodeToString(link.GetSpanId())})
	}
	if decoded.kind == tracepb.Span_SPAN_KIND_SERVER {
		if validHTTPMethod(strings.ToUpper(attrs.httpMethod)) && validRoute(attrs.httpRoute) {
			operation := strings.ToUpper(attrs.httpMethod) + " " + attrs.httpRoute
			decoded.endpointKey = serviceKey + "\x00http\x00" + operation
		} else if safeServiceTarget(attrs.rpcService) && safeServiceTarget(attrs.rpcMethod) {
			protocol := "rpc"
			if strings.EqualFold(attrs.rpcSystem, "grpc") {
				protocol = "grpc"
			}
			decoded.endpointKey = serviceKey + "\x00" + protocol + "\x00" + attrs.rpcService + "/" + attrs.rpcMethod
		}
	}
	return decoded, inside, nil
}

func evidenceFingerprint(sourceHash string, traceID, spanID []byte) string {
	h := sha256.New()
	h.Write([]byte(telemetry.EvidenceFingerprintV1))
	h.Write([]byte{0})
	h.Write([]byte(sourceHash))
	h.Write([]byte{0})
	h.Write(traceID)
	h.Write([]byte{0})
	h.Write(spanID)
	return telemetry.EvidenceFingerprintV1 + ":" + hex.EncodeToString(h.Sum(nil))
}

func allZero(value []byte) bool {
	for _, item := range value {
		if item != 0 {
			return false
		}
	}
	return true
}

func spanAttributes(values []*commonpb.KeyValue) (attributes, error) {
	var result attributes
	seen := make(map[string]struct{})
	for _, item := range values {
		key := item.GetKey()
		value := item.GetValue()
		if !validAttributeKey(key) {
			return attributes{}, fmt.Errorf("span attribute has invalid key")
		}
		if _, duplicate := seen[key]; duplicate {
			return attributes{}, fmt.Errorf("duplicate span attribute key")
		}
		seen[key] = struct{}{}
		allowed := key == "http.request.method" || key == "http.route" || key == "http.response.status_code" ||
			key == "rpc.system" || key == "rpc.service" || key == "rpc.method" || key == "server.address" ||
			key == "server.port" || key == "peer.service" || key == "db.system.name" || key == "db.namespace" ||
			key == "messaging.system" || key == "messaging.destination.name" || key == "error.type"
		if !allowed {
			continue
		}
		switch key {
		case "http.request.method":
			candidate, err := requiredScalarString(value)
			if err != nil || !validHTTPMethod(strings.ToUpper(candidate)) {
				return attributes{}, fmt.Errorf("http.request.method has invalid value")
			}
			result.httpMethod = strings.ToUpper(candidate)
		case "http.route":
			candidate, err := requiredScalarString(value)
			if err != nil || !validRoute(candidate) {
				return attributes{}, fmt.Errorf("http.route has invalid value")
			}
			result.httpRoute = candidate
		case "rpc.system":
			candidate, err := requiredScalarString(value)
			if err != nil || !safeServiceTarget(candidate) {
				return attributes{}, fmt.Errorf("rpc.system has invalid value")
			}
			result.rpcSystem = candidate
		case "rpc.service":
			candidate, err := requiredScalarString(value)
			if err != nil || !safeServiceTarget(candidate) {
				return attributes{}, fmt.Errorf("rpc.service has invalid value")
			}
			result.rpcService = candidate
		case "rpc.method":
			candidate, err := requiredScalarString(value)
			if err != nil || !safeServiceTarget(candidate) {
				return attributes{}, fmt.Errorf("rpc.method has invalid value")
			}
			result.rpcMethod = candidate
		case "server.address":
			candidate, err := requiredScalarString(value)
			if err != nil || !safeServerAddress(candidate) {
				return attributes{}, fmt.Errorf("server.address has invalid value")
			}
			result.serverAddress = candidate
		case "server.port":
			if typed, ok := value.GetValue().(*commonpb.AnyValue_IntValue); ok {
				result.serverPort = typed.IntValue
				result.serverPortSet = true
			} else {
				return attributes{}, fmt.Errorf("allowlisted span attribute has invalid type")
			}
		case "http.response.status_code":
			typed, ok := value.GetValue().(*commonpb.AnyValue_IntValue)
			if !ok {
				return attributes{}, fmt.Errorf("allowlisted span attribute has invalid type")
			}
			if typed.IntValue < 100 || typed.IntValue > 599 {
				return attributes{}, fmt.Errorf("http.response.status_code is outside its valid range")
			}
			result.httpStatusCode = typed.IntValue
		case "peer.service":
			candidate, err := requiredScalarString(value)
			if err != nil || !safeServiceTarget(candidate) {
				return attributes{}, fmt.Errorf("peer.service has invalid value")
			}
			result.peerService = candidate
		case "db.system.name":
			candidate, err := requiredScalarString(value)
			if err != nil || !safeSemanticTarget(candidate) {
				return attributes{}, fmt.Errorf("db.system.name has invalid value")
			}
			result.dbSystem = candidate
		case "db.namespace":
			candidate, err := requiredScalarString(value)
			if err != nil || !safeSemanticTarget(candidate) {
				return attributes{}, fmt.Errorf("db.namespace has invalid value")
			}
			result.dbNamespace = candidate
		case "messaging.system":
			candidate, err := requiredScalarString(value)
			if err != nil || !safeSemanticTarget(candidate) {
				return attributes{}, fmt.Errorf("messaging.system has invalid value")
			}
			result.messagingSystem = candidate
		case "messaging.destination.name":
			candidate, err := requiredScalarString(value)
			if err != nil || !safeSemanticTarget(candidate) {
				return attributes{}, fmt.Errorf("messaging.destination.name has invalid value")
			}
			result.messagingDest = candidate
		case "error.type":
			candidate, err := requiredScalarString(value)
			if err != nil || !safeSemanticTarget(candidate) {
				return attributes{}, fmt.Errorf("error.type has invalid value")
			}
			result.errorType = candidate
		}
	}
	if result.serverPortSet && (result.serverPort < 1 || result.serverPort > 65535) {
		return attributes{}, fmt.Errorf("server.port is outside its valid range")
	}
	return result, nil
}

func validAttributeKey(value string) bool {
	return value != "" && utf8.ValidString(value) && len(value) <= 256 && strings.IndexFunc(value, unicode.IsControl) < 0
}

func requiredScalarString(value *commonpb.AnyValue) (string, error) {
	typed, ok := value.GetValue().(*commonpb.AnyValue_StringValue)
	if !ok {
		return "", fmt.Errorf("allowlisted span attribute has invalid type")
	}
	candidate := strings.TrimSpace(typed.StringValue)
	if !safeIdentity(candidate, 512) {
		return "", fmt.Errorf("allowlisted span attribute has invalid value")
	}
	return candidate, nil
}

func validRoute(route string) bool {
	route = strings.TrimSpace(route)
	if !safeIdentity(route, 512) || !strings.HasPrefix(route, "/") || strings.HasPrefix(route, "//") || strings.Contains(route, "://") || strings.ContainsAny(route, "?#@\\") {
		return false
	}
	for _, segment := range strings.Split(route, "/") {
		if segment == "" {
			continue
		}
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") && !strings.ContainsAny(segment[1:len(segment)-1], "{}:") && safeRouteToken(segment[1:len(segment)-1]) {
			continue
		}
		if strings.HasPrefix(segment, ":") && safeRouteToken(segment[1:]) {
			continue
		}
		if strings.ContainsAny(segment, "{}:") || !safeRouteToken(segment) {
			return false
		}
		allDigits, allHex := true, len(segment) >= 16
		for _, character := range segment {
			if character < '0' || character > '9' {
				allDigits = false
			}
			if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F') || character == '-') {
				allHex = false
			}
		}
		if allDigits || allHex {
			return false
		}
	}
	return true
}

func safeRouteToken(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if !(unicode.IsLetter(character) || unicode.IsDigit(character) || character == '.' || character == '_' || character == '-' || character == '~') {
			return false
		}
	}
	return true
}

func validHTTPMethod(value string) bool {
	if value == "" || len(value) > 32 {
		return false
	}
	for _, character := range value {
		if !((character >= 'A' && character <= 'Z') || character == '-') {
			return false
		}
	}
	return true
}

func aggregate(result *telemetry.Snapshot, spans []decodedSpan) error {
	return aggregateContext(context.Background(), result, spans)
}

func aggregateContext(ctx context.Context, result *telemetry.Snapshot, spans []decodedSpan) error {
	services := make(map[string]telemetry.Service)
	endpoints := make(map[string]telemetry.Endpoint)
	spanByKey := make(map[string]decodedSpan, len(spans))
	linkedByOutbound := make(map[string][]linkedSpan)
	windowAgg := make(map[string]*durationAggregate)
	endpointMissing := 0
	for index, span := range spans {
		if index%256 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		mergeService(services, telemetry.Service{LogicalKey: span.serviceKey, DisplayName: span.serviceDisplay})
		spanByKey[span.spanKey] = span
		if span.parentKey != "" {
			linkedByOutbound[span.parentKey] = append(linkedByOutbound[span.parentKey], linkedSpan{span: span, association: "parent"})
		}
		if span.kind == tracepb.Span_SPAN_KIND_CONSUMER {
			for _, link := range span.links {
				linkedByOutbound[link.spanKey] = append(linkedByOutbound[link.spanKey], linkedSpan{span: span, association: "span_link"})
			}
		}
		if span.kind != tracepb.Span_SPAN_KIND_SERVER {
			continue
		}
		windowKey := span.serviceKey + "\x00"
		if span.endpointKey != "" {
			windowKey = span.endpointKey
			parts := strings.Split(span.endpointKey, "\x00")
			endpoint := telemetry.Endpoint{Key: span.endpointKey, ServiceKey: span.serviceKey, Protocol: parts[1], Operation: parts[2]}
			if parts[1] == "http" {
				_, endpoint.RouteTemplate, _ = strings.Cut(parts[2], " ")
			}
			endpoints[span.endpointKey] = endpoint
		} else {
			endpointMissing++
		}
		if err := addDuration(windowAgg, windowKey, span.duration, span.isError); err != nil {
			return err
		}
	}
	if len(services) > telemetry.MaxServices {
		return fmt.Errorf("%w: service cardinality exceeds %d", errs.ErrInvalid, telemetry.MaxServices)
	}
	if len(endpoints) > telemetry.MaxEndpoints {
		return fmt.Errorf("%w: endpoint cardinality exceeds %d", errs.ErrInvalid, telemetry.MaxEndpoints)
	}
	if endpointMissing > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("%d server spans lacked a safe endpoint identity and were aggregated at service level.", endpointMissing))
	}

	dependencies := make(map[string]telemetry.Dependency)
	observations := make(map[string]*dependencyAggregate)
	missingTarget := 0
	ambiguousTarget := 0
	for index, span := range spans {
		if index%256 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if span.kind != tracepb.Span_SPAN_KIND_CLIENT && span.kind != tracepb.Span_SPAN_KIND_PRODUCER {
			continue
		}
		resolution := resolveDependency(span, linkedByOutbound[span.spanKey], services)
		if !resolution.ok {
			missingTarget++
			if resolution.ambiguous {
				ambiguousTarget++
			}
			continue
		}
		dependency, confidence, basis, limitations := resolution.dependency, resolution.confidence, resolution.basis, resolution.limitations
		mergeDependency(dependencies, dependency)
		origin := originEndpoint(span, spanByKey)
		key := span.serviceKey + "\x00" + origin + "\x00" + dependency.Key
		aggregate, exists := observations[key]
		if !exists {
			aggregate = &dependencyAggregate{observation: telemetry.DependencyObservation{
				Key: key, FromServiceKey: span.serviceKey, OriginEndpointKey: origin, DependencyKey: dependency.Key,
				TargetServiceKey: resolution.targetServiceKey, WindowStart: result.WindowStart, WindowEnd: result.WindowEnd,
				Confidence: confidence, Basis: basis, Limitations: limitations,
			}, evidence: make(map[string]string), claims: make(map[string]string), highPairs: make(map[string][2]string)}
			observations[key] = aggregate
		} else if aggregate.observation.TargetServiceKey == "" {
			aggregate.observation.TargetServiceKey = resolution.targetServiceKey
		} else if resolution.targetServiceKey != "" && aggregate.observation.TargetServiceKey != resolution.targetServiceKey {
			return fmt.Errorf("%w: conflicting dependency targets within one observation", errs.ErrInvalid)
		}
		aggregate.observation.RequestCount++
		if span.isError || resolution.associatedError {
			aggregate.observation.ErrorCount++
		}
		var sumOK bool
		aggregate.observation.DurationSumNS, sumOK = checkedAdd(aggregate.observation.DurationSumNS, span.duration)
		if !sumOK {
			return fmt.Errorf("%w: dependency duration sum exceeds int64", errs.ErrInvalid)
		}
		for fingerprint, claim := range resolution.evidenceClaims {
			if current, exists := aggregate.claims[fingerprint]; !exists || claim < current {
				aggregate.claims[fingerprint] = claim
			}
			addBoundedEvidence(aggregate.evidence, fingerprint, claim, telemetry.MaxEvidenceSamples)
		}
		if resolution.pair[0] != "" && resolution.pair[1] != "" {
			pairKey := resolution.pair[0] + "\x00" + resolution.pair[1]
			aggregate.highPairs[pairKey] = resolution.pair
			if len(aggregate.highPairs) > telemetry.MaxEvidenceSamples/2 {
				delete(aggregate.highPairs, greatestStringKey(aggregate.highPairs))
			}
		}
		if confidence == topology.Medium && resolution.pair[0] != "" {
			aggregate.hardCap = true
			aggregate.capLimits = mergeSortedStrings(aggregate.capLimits, limitations)
			if basis == "linked spans with conflicting semantic target" {
				aggregate.conflict = true
			}
		}
		if topology.Rank(confidence) > topology.Rank(aggregate.observation.Confidence) ||
			(topology.Rank(confidence) == topology.Rank(aggregate.observation.Confidence) && observationMetadataLess(basis, limitations, aggregate.observation.Basis, aggregate.observation.Limitations)) {
			aggregate.observation.Confidence = confidence
			aggregate.observation.Basis = basis
			aggregate.observation.Limitations = limitations
		}
		if aggregate.hardCap {
			aggregate.observation.Confidence = topology.Medium
			aggregate.observation.Limitations = mergeSortedStrings(aggregate.observation.Limitations, aggregate.capLimits)
			if aggregate.conflict {
				aggregate.observation.Basis = "linked spans with conflicting semantic target"
			}
		}
	}
	if len(dependencies) > telemetry.MaxDependencies {
		return fmt.Errorf("%w: dependency cardinality exceeds %d", errs.ErrInvalid, telemetry.MaxDependencies)
	}
	if missingTarget > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("%d outbound spans lacked enough destination data; no UNKNOWN edge was created.", missingTarget))
	}
	if ambiguousTarget > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("%d outbound spans had multiple directly linked target services; no target was selected by input order.", ambiguousTarget))
	}
	finalizeIndex := 0
	checkContext := func() error {
		finalizeIndex++
		if finalizeIndex%256 == 0 {
			return ctx.Err()
		}
		return nil
	}
	for _, service := range services {
		if err := checkContext(); err != nil {
			return err
		}
		result.Services = append(result.Services, service)
	}
	for _, endpoint := range endpoints {
		if err := checkContext(); err != nil {
			return err
		}
		result.Endpoints = append(result.Endpoints, endpoint)
	}
	for _, dependency := range dependencies {
		if err := checkContext(); err != nil {
			return err
		}
		result.Dependencies = append(result.Dependencies, dependency)
	}
	evidenceClaims := make(map[string]string)
	for _, aggregate := range observations {
		if err := checkContext(); err != nil {
			return err
		}
		selectedEvidence := make(map[string]struct{}, telemetry.MaxEvidenceSamples)
		pairKeys := make([]string, 0, len(aggregate.highPairs))
		for key := range aggregate.highPairs {
			pairKeys = append(pairKeys, key)
		}
		sort.Strings(pairKeys)
		for _, key := range pairKeys {
			pair := aggregate.highPairs[key]
			selectedEvidence[pair[0]] = struct{}{}
			selectedEvidence[pair[1]] = struct{}{}
		}
		regularEvidence := make([]string, 0, len(aggregate.evidence))
		for evidence := range aggregate.evidence {
			regularEvidence = append(regularEvidence, evidence)
		}
		sort.Strings(regularEvidence)
		for _, evidence := range regularEvidence {
			if len(selectedEvidence) >= telemetry.MaxEvidenceSamples {
				break
			}
			selectedEvidence[evidence] = struct{}{}
		}
		for evidence := range selectedEvidence {
			aggregate.observation.EvidenceFingerprints = append(aggregate.observation.EvidenceFingerprints, evidence)
			claim := aggregate.claims[evidence]
			if current, exists := evidenceClaims[evidence]; !exists || claim < current {
				evidenceClaims[evidence] = claim
			}
		}
		sortStrings(aggregate.observation.EvidenceFingerprints)
		result.Observations = append(result.Observations, aggregate.observation)
	}
	for fingerprint, claim := range evidenceClaims {
		result.Evidence = append(result.Evidence, telemetry.Evidence{Fingerprint: fingerprint, Claim: claim, ObservedAt: result.ObservedAt})
	}
	for key, aggregate := range windowAgg {
		if err := checkContext(); err != nil {
			return err
		}
		serviceKey, endpointKey := key, ""
		if endpoint, ok := endpoints[key]; ok {
			serviceKey, endpointKey = endpoint.ServiceKey, endpoint.Key
		} else {
			serviceKey = strings.TrimSuffix(key, "\x00")
		}
		result.Windows = append(result.Windows, telemetry.Window{
			Key: key, ServiceKey: serviceKey, EndpointKey: endpointKey, WindowStart: result.WindowStart, WindowEnd: result.WindowEnd,
			RequestCount: aggregate.count, ErrorCount: aggregate.errors, DurationSumNS: aggregate.sum,
			P50NS: telemetry.NearestRank(aggregate.durations, .50), P95NS: telemetry.NearestRank(aggregate.durations, .95), P99NS: telemetry.NearestRank(aggregate.durations, .99),
			IsComplete: false, CoverageRatio: nil,
		})
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	sort.Slice(result.Services, func(i, j int) bool { return result.Services[i].LogicalKey < result.Services[j].LogicalKey })
	sort.Slice(result.Endpoints, func(i, j int) bool { return result.Endpoints[i].Key < result.Endpoints[j].Key })
	sort.Slice(result.Dependencies, func(i, j int) bool { return result.Dependencies[i].Key < result.Dependencies[j].Key })
	sort.Slice(result.Observations, func(i, j int) bool { return result.Observations[i].Key < result.Observations[j].Key })
	sort.Slice(result.Windows, func(i, j int) bool { return result.Windows[i].Key < result.Windows[j].Key })
	sort.Slice(result.Evidence, func(i, j int) bool { return result.Evidence[i].Fingerprint < result.Evidence[j].Fingerprint })
	if err := ctx.Err(); err != nil {
		return err
	}
	result.Stats.Services = len(result.Services)
	result.Stats.Endpoints = len(result.Endpoints)
	result.Stats.Dependencies = len(result.Dependencies)
	result.Stats.Observations = len(result.Observations)
	result.Stats.TelemetryWindows = len(result.Windows)
	return nil
}

func addDuration(values map[string]*durationAggregate, key string, duration int64, failed bool) error {
	aggregate := values[key]
	if aggregate == nil {
		aggregate = &durationAggregate{}
		values[key] = aggregate
	}
	aggregate.count++
	var ok bool
	aggregate.sum, ok = checkedAdd(aggregate.sum, duration)
	if !ok {
		return fmt.Errorf("%w: telemetry window duration sum exceeds int64", errs.ErrInvalid)
	}
	aggregate.durations = append(aggregate.durations, duration)
	if failed {
		aggregate.errors++
	}
	return nil
}

func checkedAdd(left, right int64) (int64, bool) {
	const maxInt64 = int64(^uint64(0) >> 1)
	if left < 0 || right < 0 || right > maxInt64-left {
		return 0, false
	}
	return left + right, true
}

func resolveDependency(span decodedSpan, candidates []linkedSpan, services map[string]telemetry.Service) dependencyResolution {
	result := dependencyResolution{evidenceClaims: map[string]string{span.evidence: outboundEvidenceClaim(span.kind)}}
	linked := make([]linkedSpan, 0, len(candidates))
	linkedServices := make(map[string]struct{})
	for _, candidate := range candidates {
		if _, valid := directAssociationBasis(span, candidate); valid && candidate.span.serviceKey != span.serviceKey {
			linked = append(linked, candidate)
			linkedServices[candidate.span.serviceKey] = struct{}{}
		}
	}
	sort.Slice(linked, func(i, j int) bool {
		if linked[i].span.serviceKey != linked[j].span.serviceKey {
			return linked[i].span.serviceKey < linked[j].span.serviceKey
		}
		if linked[i].association != linked[j].association {
			return linked[i].association < linked[j].association
		}
		return linked[i].span.evidence < linked[j].span.evidence
	})
	if len(linkedServices) == 1 {
		candidate := linked[0]
		result.dependency = serviceDependency(candidate.span.serviceKey, candidate.span.serviceDisplay)
		result.targetServiceKey = candidate.span.serviceKey
		result.confidence = topology.High
		result.basis, _ = directAssociationBasis(span, candidate)
		result.pair = [2]string{span.evidence, candidate.span.evidence}
		if span.kind == tracepb.Span_SPAN_KIND_CLIENT {
			for _, linkedCandidate := range linked {
				if linkedCandidate.span.kind == tracepb.Span_SPAN_KIND_SERVER && linkedCandidate.span.isError {
					result.associatedError = true
					break
				}
			}
		}
		result.evidenceClaims[candidate.span.evidence] = linkedEvidenceClaim(candidate.span.kind)
		result.ok = true
		for _, semanticKey := range comparableSemanticServiceKeys(span.attrs) {
			if semanticKey != candidate.span.serviceKey {
				result.confidence = topology.Medium
				result.basis = "linked spans with conflicting semantic target"
				result.limitations = append(result.limitations, "Directly linked remote service contradicted peer.service or rpc.service metadata.")
				break
			}
		}
		if candidate.association == "span_link" && (span.droppedLinks > 0 || candidate.span.droppedLinks > 0) {
			result.confidence = topology.Medium
			result.limitations = append(result.limitations, "Dropped SpanLinks were reported; association evidence may be incomplete.")
		}
		return result
	}
	result = semanticDependency(span, services)
	if len(linkedServices) > 1 {
		result.ambiguous = true
		if result.ok {
			result.limitations = []string{"Multiple directly linked remote service identities were observed; semantic fallback selected the declared target."}
			if result.dependency.Kind == "service" && result.targetServiceKey == "" {
				result.limitations = append(result.limitations, "Declared semantic target service was not observed.")
			}
			sortStrings(result.limitations)
		}
	}
	return result
}

func semanticDependency(span decodedSpan, services map[string]telemetry.Service) dependencyResolution {
	result := dependencyResolution{evidenceClaims: map[string]string{span.evidence: outboundEvidenceClaim(span.kind)}}
	if safeServiceTarget(span.attrs.peerService) {
		key := inventory.NormalizeServiceLogicalKey(span.attrs.peerService, "")
		display := span.attrs.peerService
		if service, ok := services[key]; ok {
			display, result.targetServiceKey = service.DisplayName, key
			result.limitations = []string{"No linked remote span was present."}
		} else {
			result.limitations = []string{"No linked remote span or observed target service was present."}
		}
		result.dependency, result.confidence, result.basis, result.ok = serviceDependency(key, display), topology.Medium, "peer.service semantic convention", true
		return result
	}
	if safeSemanticTarget(span.attrs.dbSystem) {
		identity := strings.ToLower(span.attrs.dbSystem)
		if safeSemanticTarget(span.attrs.dbNamespace) {
			identity += ":" + span.attrs.dbNamespace
		}
		kind := "database"
		if strings.EqualFold(span.attrs.dbSystem, "redis") || strings.EqualFold(span.attrs.dbSystem, "memcached") {
			kind = "cache"
		}
		result.dependency, result.confidence, result.basis = dependency(kind, identity), topology.Medium, "database semantic convention"
		result.limitations, result.ok = []string{"No remote server span was present."}, true
		return result
	}
	if safeSemanticTarget(span.attrs.messagingSystem) && safeSemanticTarget(span.attrs.messagingDest) {
		identity := strings.ToLower(span.attrs.messagingSystem) + ":" + span.attrs.messagingDest
		result.dependency, result.confidence, result.basis = dependency("queue", identity), topology.Medium, "messaging semantic convention"
		result.limitations, result.ok = []string{"No linked consumer span was present."}, true
		return result
	}
	if safeServiceTarget(span.attrs.rpcService) {
		key := inventory.NormalizeServiceLogicalKey(span.attrs.rpcService, "")
		display := span.attrs.rpcService
		if service, ok := services[key]; ok {
			display, result.targetServiceKey = service.DisplayName, key
			result.limitations = []string{"No linked remote span was present."}
		} else {
			result.limitations = []string{"No linked remote span or observed target service was present."}
		}
		result.dependency, result.confidence, result.basis, result.ok = serviceDependency(key, display), topology.Medium, "RPC semantic convention", true
		return result
	}
	if safeServerAddress(span.attrs.serverAddress) {
		address := strings.ToLower(span.attrs.serverAddress)
		if parsed := net.ParseIP(address); parsed != nil {
			address = parsed.String()
		}
		if span.attrs.serverPort > 0 && span.attrs.serverPort <= 65535 {
			address = net.JoinHostPort(address, strconv.FormatInt(span.attrs.serverPort, 10))
		}
		result.dependency, result.confidence, result.basis = dependency("external_api", address), topology.Low, "observed server.address"
		result.limitations, result.ok = []string{"Destination is address-derived and was not resolved."}, true
	}
	return result
}

func comparableSemanticServiceKeys(attrs attributes) []string {
	values := make(map[string]struct{})
	for _, candidate := range []string{attrs.peerService, attrs.rpcService} {
		if safeServiceTarget(candidate) {
			values[inventory.NormalizeServiceLogicalKey(candidate, "")] = struct{}{}
		}
	}
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func directAssociationBasis(outbound decodedSpan, candidate linkedSpan) (string, bool) {
	switch {
	case outbound.kind == tracepb.Span_SPAN_KIND_CLIENT &&
		candidate.association == "parent" &&
		candidate.span.kind == tracepb.Span_SPAN_KIND_SERVER &&
		candidate.span.traceKey == outbound.traceKey:
		return "parent/child client/server propagation", true
	case outbound.kind == tracepb.Span_SPAN_KIND_PRODUCER &&
		candidate.association == "span_link" &&
		candidate.span.kind == tracepb.Span_SPAN_KIND_CONSUMER:
		return "producer/consumer SpanLink association", true
	default:
		return "", false
	}
}

func outboundEvidenceClaim(kind tracepb.Span_SpanKind) string {
	if kind == tracepb.Span_SPAN_KIND_PRODUCER {
		return "outbound producer span"
	}
	return "outbound client span"
}

func linkedEvidenceClaim(kind tracepb.Span_SpanKind) string {
	if kind == tracepb.Span_SPAN_KIND_CONSUMER {
		return "linked consumer span"
	}
	return "linked remote server span"
}

func serviceDependency(key, display string) telemetry.Dependency {
	return telemetry.Dependency{Key: "service\x00" + key, LogicalKey: key, Kind: "service", DisplayName: display}
}

func dependency(kind, identity string) telemetry.Dependency {
	logicalKey := strings.ToLower(strings.TrimSpace(identity))
	return telemetry.Dependency{Key: kind + "\x00" + logicalKey, LogicalKey: logicalKey, Kind: kind, DisplayName: identity}
}

func mergeService(values map[string]telemetry.Service, candidate telemetry.Service) {
	current, exists := values[candidate.LogicalKey]
	if !exists || candidate.DisplayName < current.DisplayName {
		values[candidate.LogicalKey] = candidate
	}
}

func mergeDependency(values map[string]telemetry.Dependency, candidate telemetry.Dependency) {
	current, exists := values[candidate.Key]
	if !exists {
		values[candidate.Key] = candidate
		return
	}
	if candidate.DisplayName < current.DisplayName {
		current.DisplayName = candidate.DisplayName
	}
	values[candidate.Key] = current
}

func safeServiceTarget(value string) bool {
	return safeSemanticTarget(value) && !strings.ContainsAny(value, "/:@") && !strings.HasPrefix(strings.ToLower(value), "unknown_service") && inventory.NormalizeServiceLogicalKey(value, "") != ""
}

func addBoundedEvidence(values map[string]string, value, claim string, limit int) {
	if current, exists := values[value]; !exists || claim < current {
		values[value] = claim
	}
	if len(values) > limit {
		delete(values, greatestStringKey(values))
	}
}

func greatestStringKey[T any](values map[string]T) string {
	greatest := ""
	for key := range values {
		if key > greatest {
			greatest = key
		}
	}
	return greatest
}

func safeSemanticTarget(value string) bool {
	if !safeIdentity(value, maxIdentityLength) || strings.Contains(value, "://") || strings.ContainsAny(value, "?&#=@/:\\") {
		return false
	}
	useful := false
	for _, character := range value {
		if !(unicode.IsLetter(character) || unicode.IsDigit(character) || character == '.' || character == '_' || character == '-') {
			return false
		}
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			useful = true
		}
	}
	return useful
}

func observationMetadataLess(leftBasis string, leftLimitations []string, rightBasis string, rightLimitations []string) bool {
	if leftBasis != rightBasis {
		return leftBasis < rightBasis
	}
	limit := len(leftLimitations)
	if len(rightLimitations) < limit {
		limit = len(rightLimitations)
	}
	for index := 0; index < limit; index++ {
		if leftLimitations[index] != rightLimitations[index] {
			return leftLimitations[index] < rightLimitations[index]
		}
	}
	return len(leftLimitations) < len(rightLimitations)
}

func mergeSortedStrings(left, right []string) []string {
	values := make(map[string]struct{}, len(left)+len(right))
	for _, value := range left {
		values[value] = struct{}{}
	}
	for _, value := range right {
		values[value] = struct{}{}
	}
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func safeServerAddress(value string) bool {
	if !safeIdentity(value, maxIdentityLength) {
		return false
	}
	if net.ParseIP(value) != nil {
		return true
	}
	if strings.ContainsAny(value, "/:?#@=&") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, character := range label {
			if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '-') {
				return false
			}
		}
	}
	return true
}

func originEndpoint(span decodedSpan, spans map[string]decodedSpan) string {
	parent := span.parentKey
	for steps := 0; parent != "" && steps < 32; steps++ {
		candidate, ok := spans[parent]
		if !ok || candidate.traceKey != span.traceKey {
			return ""
		}
		if candidate.serviceKey == span.serviceKey && candidate.kind == tracepb.Span_SPAN_KIND_SERVER && candidate.endpointKey != "" {
			return candidate.endpointKey
		}
		parent = candidate.parentKey
	}
	return ""
}

func sortStrings(values []string) { sort.Strings(values) }
