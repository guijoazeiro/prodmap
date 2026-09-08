// Package investigationpackage creates and verifies bounded, portable
// investigation artifacts without accessing configuration or SQLite.
package investigationpackage

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/guijoazeiro/prodmap/internal/baseline"
	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/investigation"
	"github.com/guijoazeiro/prodmap/internal/redaction"
	"github.com/guijoazeiro/prodmap/internal/regression"
	"github.com/guijoazeiro/prodmap/internal/timeline"
	"github.com/guijoazeiro/prodmap/internal/topology"
)

const (
	FormatVersion    = "prodmap-investigation-package/v1"
	RedactionProfile = "safe-default/v1"
	MediaType        = "application/vnd.prodmap.investigation+json"

	maxTotalSize = 8 << 20
	maxEntrySize = 4 << 20
)

var requiredFiles = []string{"SHA256SUMS", "investigation.json", "manifest.json"}

type CreateRequest struct {
	OutputPath    string
	CreatedAt     time.Time
	Investigation investigation.Result
}

type CreateResult struct {
	FileName         string
	PackageSHA256    string
	InvestigationKey string
	PackageSize      int64
}

type VerifyResult struct {
	FileName             string
	PackageSHA256        string
	FormatVersion        string
	CreatedAt            string
	InvestigationVersion string
	InvestigationKey     string
	RedactionProfile     string
	CausalityClaimed     bool
}

// Output is the immutable, allowlisted investigation projection shared by
// portable packages and other local read-only transports.
//
// Its JSON form is deliberately stable: it contains no raw sources, paths, or
// credentials and is validated by OutputFrom before it is returned.
type Output = investigationDocument

// OutputFrom returns the validated, allowlisted output projection for an
// investigation. Callers must treat the returned value as immutable.
func OutputFrom(result investigation.Result) (Output, error) {
	output := documentFrom(result)
	data, err := marshalDocument(output)
	if err != nil {
		return Output{}, err
	}
	if err := scanRedaction(data); err != nil {
		return Output{}, err
	}
	return output, nil
}

type manifest struct {
	FormatVersion        string        `json:"format_version"`
	CreatedAt            string        `json:"created_at"`
	InvestigationVersion string        `json:"investigation_version"`
	InvestigationKey     string        `json:"investigation_key"`
	RedactionProfile     string        `json:"redaction_profile"`
	CausalityClaimed     bool          `json:"causality_claimed"`
	Investigation        fileInventory `json:"investigation"`
}

type fileInventory struct {
	Name      string `json:"name"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	MediaType string `json:"media_type"`
}

// Create builds a package atomically and never replaces an existing output.
func Create(ctx context.Context, request CreateRequest) (CreateResult, error) {
	if err := ctx.Err(); err != nil {
		return CreateResult{}, err
	}
	if strings.TrimSpace(request.OutputPath) == "" || request.CreatedAt.IsZero() {
		return CreateResult{}, fmt.Errorf("%w: output and created_at are required", errs.ErrInvalid)
	}
	if _, err := os.Lstat(request.OutputPath); err == nil {
		return CreateResult{}, fmt.Errorf("%w: package output already exists", errs.ErrConflict)
	} else if !errors.Is(err, os.ErrNotExist) {
		return CreateResult{}, fmt.Errorf("inspect package output: %w", err)
	}

	document, err := OutputFrom(request.Investigation)
	if err != nil {
		return CreateResult{}, err
	}
	investigationJSON, err := marshalDocument(document)
	if err != nil {
		return CreateResult{}, err
	}
	if err := scanRedaction(investigationJSON); err != nil {
		return CreateResult{}, err
	}
	createdAt := request.CreatedAt.UTC()
	manifestJSON, err := json.Marshal(manifest{
		FormatVersion:        FormatVersion,
		CreatedAt:            createdAt.Format(time.RFC3339Nano),
		InvestigationVersion: document.InvestigationVersion,
		InvestigationKey:     document.InvestigationKey,
		RedactionProfile:     RedactionProfile,
		CausalityClaimed:     false,
		Investigation:        fileInventory{Name: "investigation.json", SHA256: digest(investigationJSON), Size: int64(len(investigationJSON)), MediaType: MediaType},
	})
	if err != nil {
		return CreateResult{}, fmt.Errorf("encode package manifest: %w", err)
	}
	if _, err := validateManifestBytes(manifestJSON); err != nil {
		return CreateResult{}, err
	}
	sums := checksumFile(manifestJSON, investigationJSON)
	contents := map[string][]byte{"manifest.json": manifestJSON, "investigation.json": investigationJSON, "SHA256SUMS": sums}
	totalSize := int64(0)
	for _, name := range requiredFiles {
		if int64(len(contents[name])) > maxEntrySize {
			return CreateResult{}, fmt.Errorf("%w: %s exceeds the package entry limit", errs.ErrInvalid, name)
		}
		totalSize += int64(len(contents[name]))
	}
	if totalSize > maxTotalSize {
		return CreateResult{}, fmt.Errorf("%w: package size limit exceeded", errs.ErrInvalid)
	}

	if err := writeAtomicZip(ctx, request.OutputPath, createdAt, contents); err != nil {
		return CreateResult{}, err
	}
	packageSHA, packageSize, err := fileDigest(request.OutputPath)
	if err != nil {
		return CreateResult{}, fmt.Errorf("hash created package: %w", err)
	}
	return CreateResult{FileName: filepath.Base(request.OutputPath), PackageSHA256: packageSHA, InvestigationKey: document.InvestigationKey, PackageSize: packageSize}, nil
}

// Verify validates an investigation package without loading configuration or SQLite.
func Verify(ctx context.Context, path string) (VerifyResult, error) {
	if err := ctx.Err(); err != nil {
		return VerifyResult{}, err
	}
	if strings.TrimSpace(path) == "" {
		return VerifyResult{}, fmt.Errorf("%w: package file is required", errs.ErrInvalid)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return VerifyResult{}, fmt.Errorf("%w: package file must be regular", errs.ErrInvalid)
	}
	archive, err := zip.OpenReader(path)
	if err != nil {
		return VerifyResult{}, fmt.Errorf("%w: read investigation package", errs.ErrInvalid)
	}
	defer archive.Close()
	if archive.Comment != "" {
		return VerifyResult{}, fmt.Errorf("%w: package ZIP comment is not allowed", errs.ErrInvalid)
	}
	files, err := readPackageFiles(ctx, archive.File)
	if err != nil {
		return VerifyResult{}, err
	}
	manifestValue, err := validateManifestBytes(files["manifest.json"])
	if err != nil {
		return VerifyResult{}, err
	}
	document, err := validateDocumentBytes(files["investigation.json"])
	if err != nil {
		return VerifyResult{}, err
	}
	if err := validateChecksums(files["SHA256SUMS"], files["manifest.json"], files["investigation.json"]); err != nil {
		return VerifyResult{}, err
	}
	if manifestValue.Investigation.SHA256 != digest(files["investigation.json"]) || manifestValue.Investigation.Size != int64(len(files["investigation.json"])) || manifestValue.Investigation.Name != "investigation.json" || manifestValue.Investigation.MediaType != MediaType {
		return VerifyResult{}, fmt.Errorf("%w: manifest investigation inventory does not match", errs.ErrInvalid)
	}
	if manifestValue.InvestigationVersion != document.InvestigationVersion || manifestValue.InvestigationKey != document.InvestigationKey || manifestValue.CausalityClaimed || document.CausalityClaimed {
		return VerifyResult{}, fmt.Errorf("%w: manifest and investigation disagree", errs.ErrInvalid)
	}
	if err := scanRedaction(files["investigation.json"]); err != nil {
		return VerifyResult{}, err
	}
	packageSHA, _, err := fileDigest(path)
	if err != nil {
		return VerifyResult{}, fmt.Errorf("hash package: %w", err)
	}
	return VerifyResult{FileName: filepath.Base(path), PackageSHA256: packageSHA, FormatVersion: manifestValue.FormatVersion, CreatedAt: manifestValue.CreatedAt, InvestigationVersion: manifestValue.InvestigationVersion, InvestigationKey: manifestValue.InvestigationKey, RedactionProfile: manifestValue.RedactionProfile, CausalityClaimed: false}, nil
}

func writeAtomicZip(ctx context.Context, outputPath string, createdAt time.Time, contents map[string][]byte) (err error) {
	directory := filepath.Dir(outputPath)
	temporary, err := os.CreateTemp(directory, ".prodmap-package-*")
	if err != nil {
		return fmt.Errorf("create package temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	closed := false
	defer func() {
		var closeErr error
		if !closed {
			closeErr = temporary.Close()
		}
		if err == nil && closeErr != nil {
			err = closeErr
		}
		if removeErr := os.Remove(temporaryPath); err == nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = removeErr
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("secure package temporary file: %w", err)
	}
	writer := zip.NewWriter(temporary)
	for _, name := range requiredFiles {
		if err := ctx.Err(); err != nil {
			return err
		}
		header := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: createdAt}
		header.SetMode(0o600)
		entry, err := writer.CreateHeader(header)
		if err != nil {
			return fmt.Errorf("create ZIP entry: %w", err)
		}
		if _, err := entry.Write(contents[name]); err != nil {
			return fmt.Errorf("write ZIP entry: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("close package ZIP: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync package: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close package temporary file: %w", err)
	}
	closed = true
	if err := os.Link(temporaryPath, outputPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: package output already exists", errs.ErrConflict)
		}
		return fmt.Errorf("publish package without replacement: %w", err)
	}
	directoryFile, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open package directory: %w", err)
	}
	defer directoryFile.Close()
	if err := directoryFile.Sync(); err != nil {
		return fmt.Errorf("sync package directory: %w", err)
	}
	return nil
}

func readPackageFiles(ctx context.Context, files []*zip.File) (map[string][]byte, error) {
	if len(files) != len(requiredFiles) {
		return nil, fmt.Errorf("%w: package must contain exactly three files", errs.ErrInvalid)
	}
	result := make(map[string][]byte, len(files))
	var total uint64
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !utf8.ValidString(file.Name) || !slices.Contains(requiredFiles, file.Name) || strings.ContainsAny(file.Name, `\\/`) || !file.Mode().IsRegular() || file.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%w: invalid package ZIP entry", errs.ErrInvalid)
		}
		if _, exists := result[file.Name]; exists {
			return nil, fmt.Errorf("%w: duplicate package ZIP entry", errs.ErrInvalid)
		}
		if file.UncompressedSize64 > maxEntrySize || total > maxTotalSize-file.UncompressedSize64 {
			return nil, fmt.Errorf("%w: package size limit exceeded", errs.ErrInvalid)
		}
		total += file.UncompressedSize64
		reader, err := file.Open()
		if err != nil {
			return nil, fmt.Errorf("%w: open package ZIP entry", errs.ErrInvalid)
		}
		contents, readErr := io.ReadAll(io.LimitReader(reader, maxEntrySize+1))
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil || int64(len(contents)) > maxEntrySize {
			return nil, fmt.Errorf("%w: read package ZIP entry", errs.ErrInvalid)
		}
		result[file.Name] = contents
	}
	for _, name := range requiredFiles {
		if _, exists := result[name]; !exists {
			return nil, fmt.Errorf("%w: required package file is missing", errs.ErrInvalid)
		}
	}
	return result, nil
}

func validateChecksums(data, manifestJSON, investigationJSON []byte) error {
	if !utf8.Valid(data) {
		return fmt.Errorf("%w: SHA256SUMS must be UTF-8", errs.ErrInvalid)
	}
	expected := checksumFile(manifestJSON, investigationJSON)
	if !bytes.Equal(data, expected) {
		return fmt.Errorf("%w: SHA256SUMS does not match package contents", errs.ErrInvalid)
	}
	return nil
}

func checksumFile(manifestJSON, investigationJSON []byte) []byte {
	return []byte(fmt.Sprintf("%s  investigation.json\n%s  manifest.json\n", digest(investigationJSON), digest(manifestJSON)))
}

func validateManifestBytes(data []byte) (manifest, error) {
	var value manifest
	if err := decodeStrict(data, &value); err != nil {
		return manifest{}, fmt.Errorf("%w: invalid package manifest", errs.ErrInvalid)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, value.CreatedAt)
	if err != nil || value.CreatedAt != createdAt.UTC().Format(time.RFC3339Nano) || value.FormatVersion != FormatVersion || value.InvestigationVersion == "" || !validInvestigationKey(value.InvestigationKey) || value.RedactionProfile != RedactionProfile || value.CausalityClaimed || value.Investigation.Name != "investigation.json" || !validSHA256(value.Investigation.SHA256) || value.Investigation.Size < 0 || value.Investigation.MediaType != MediaType {
		return manifest{}, fmt.Errorf("%w: invalid package manifest", errs.ErrInvalid)
	}
	return value, nil
}

func marshalDocument(document investigationDocument) ([]byte, error) {
	if err := validateDocument(document); err != nil {
		return nil, err
	}
	data, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode investigation document: %w", err)
	}
	return data, nil
}

func validateDocumentBytes(data []byte) (investigationDocument, error) {
	var value investigationDocument
	if err := decodeStrict(data, &value); err != nil {
		return investigationDocument{}, fmt.Errorf("%w: invalid investigation document", errs.ErrInvalid)
	}
	if err := validateDocument(value); err != nil {
		return investigationDocument{}, err
	}
	return value, nil
}

func decodeStrict(data []byte, target any) error {
	if err := rejectDuplicateKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON data")
	}
	return nil
}

// rejectDuplicateKeys walks JSON tokens before typed decoding. Object key tokens
// are decoded by encoding/json, so escaped spellings compare by their actual key.
func rejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := walkJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON data")
	}
	return nil
}

func walkJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		keys := make(map[string]struct{})
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return errors.New("invalid JSON object key")
			}
			if _, exists := keys[name]; exists {
				return errors.New("duplicate JSON object key")
			}
			keys[name] = struct{}{}
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return errors.New("invalid JSON delimiter")
	}
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func fileDigest(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hasher := sha256.New()
	size, err := io.Copy(hasher, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hasher.Sum(nil)), size, nil
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func validInvestigationKey(value string) bool {
	remainder, ok := strings.CutPrefix(value, "sha256:")
	return ok && validSHA256(remainder)
}

func scanRedaction(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("%w: invalid investigation redaction payload", errs.ErrInvalid)
	}
	if err := scanValue(value, ""); err != nil {
		return err
	}
	return nil
}

func scanValue(value any, key string) error {
	switch typed := value.(type) {
	case map[string]any:
		for nestedKey, nestedValue := range typed {
			if redaction.ProhibitedKey(nestedKey) {
				return fmt.Errorf("%w: prohibited field in investigation package", errs.ErrInvalid)
			}
			if err := scanValue(nestedValue, nestedKey); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range typed {
			if err := scanValue(item, key); err != nil {
				return err
			}
		}
	case string:
		if err := redaction.ValidatePublicValue(publicValueKind(key), typed); err != nil {
			return fmt.Errorf("%w: prohibited content in investigation package", errs.ErrInvalid)
		}
	}
	return nil
}

func publicValueKind(key string) redaction.ValueKind {
	switch key {
	case "service", "environment", "logical_key", "display_name", "id", "type", "kind", "metric", "unit", "status", "relation_type":
		return redaction.Identifier
	default:
		return redaction.FreeText
	}
}

type investigationDocument struct {
	InvestigationVersion string             `json:"investigation_version"`
	InvestigationKey     string             `json:"investigation_key"`
	Status               string             `json:"status"`
	Deployment           deploymentDocument `json:"deployment"`
	Regression           regressionDocument `json:"regression"`
	Topology             topologyDocument   `json:"topology"`
	Timeline             timelineDocument   `json:"timeline"`
	EvidenceReferences   evidenceDocument   `json:"evidence_references"`
	Limitations          []string           `json:"limitations"`
	CausalityClaimed     bool               `json:"causality_claimed"`
}

type deploymentDocument struct {
	ID          string `json:"id"`
	Environment string `json:"environment"`
	Service     string `json:"service"`
	StartedAt   string `json:"started_at"`
}

type regressionDocument struct {
	ComparisonKey         string                  `json:"comparison_key"`
	Status                string                  `json:"status"`
	AlgorithmVersion      string                  `json:"algorithm_version"`
	Deployment            deploymentDocument      `json:"deployment"`
	Metric                string                  `json:"metric"`
	Unit                  string                  `json:"unit"`
	Before                sideDocument            `json:"before"`
	After                 sideDocument            `json:"after"`
	AbsoluteDelta         *float64                `json:"absolute_delta"`
	RelativeDelta         *float64                `json:"relative_delta"`
	Contamination         contaminationDocument   `json:"contamination"`
	BaselineConfidence    confidenceDocument      `json:"baseline_confidence"`
	ObservationConfidence confidenceDocument      `json:"observation_confidence"`
	RegressionConfidence  confidenceDocument      `json:"regression_confidence"`
	Classification        *classificationDocument `json:"classification"`
	CausalityClaimed      bool                    `json:"causality_claimed"`
}

type sideDocument struct {
	Status           string           `json:"status"`
	AlgorithmVersion string           `json:"algorithm_version"`
	Window           windowDocument   `json:"window"`
	Value            *float64         `json:"value"`
	SampleCount      int64            `json:"sample_count"`
	CoverageRatio    *float64         `json:"coverage_ratio"`
	IsComplete       bool             `json:"is_complete"`
	AcceptedWindows  []windowSummary  `json:"accepted_windows"`
	RejectedWindows  []rejectedWindow `json:"rejected_windows"`
}

type windowDocument struct {
	Start string `json:"start"`
	End   string `json:"end"`
}
type windowSummary struct {
	ID          string `json:"id"`
	Start       string `json:"start"`
	End         string `json:"end"`
	SampleCount int64  `json:"sample_count"`
}
type rejectedWindow struct {
	ID            string   `json:"id"`
	Start         string   `json:"start"`
	End           string   `json:"end"`
	ObservedAt    string   `json:"observed_at"`
	SampleCount   int64    `json:"sample_count"`
	CoverageRatio *float64 `json:"coverage_ratio"`
	IsComplete    bool     `json:"is_complete"`
	Contaminated  bool     `json:"contaminated"`
	Reason        string   `json:"reason"`
}
type contaminationDocument struct {
	BeforeDeployments     []string `json:"before_deployments"`
	AfterDeployments      []string `json:"after_deployments"`
	ConcurrentDeployments []string `json:"concurrent_deployments"`
	Truncated             bool     `json:"truncated"`
}
type confidenceDocument struct {
	Level            string   `json:"level"`
	Basis            string   `json:"basis"`
	AlgorithmVersion string   `json:"algorithm_version"`
	Limitations      []string `json:"limitations"`
}
type classificationDocument struct {
	ClassificationKey string             `json:"classification_key"`
	Result            string             `json:"result"`
	Direction         string             `json:"direction"`
	Algorithm         string             `json:"algorithm"`
	AlgorithmVersion  string             `json:"algorithm_version"`
	Thresholds        thresholdsDocument `json:"thresholds"`
	ObservedEffect    effectDocument     `json:"observed_effect"`
	Confidence        confidenceDocument `json:"confidence"`
	CausalityClaimed  bool               `json:"causality_claimed"`
}
type thresholdsDocument struct {
	AbsoluteMin *float64 `json:"absolute_min"`
	RelativeMin *float64 `json:"relative_min"`
	RequireAll  bool     `json:"require_all"`
	Unit        string   `json:"unit"`
}
type effectDocument struct {
	AbsoluteDelta *float64 `json:"absolute_delta"`
	RelativeDelta *float64 `json:"relative_delta"`
}
type topologyDocument struct {
	At          string         `json:"at"`
	Environment string         `json:"environment"`
	Roots       []string       `json:"roots"`
	Nodes       []nodeDocument `json:"nodes"`
	Edges       []edgeDocument `json:"edges"`
	Truncated   bool           `json:"truncated"`
}
type nodeDocument struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	LogicalKey  string `json:"logical_key"`
	DisplayName string `json:"display_name"`
}
type edgeDocument struct {
	ID             string                  `json:"id"`
	From           string                  `json:"from"`
	To             string                  `json:"to"`
	RelationType   string                  `json:"relation_type"`
	DependencyKind string                  `json:"dependency_kind"`
	WindowStart    string                  `json:"window_start"`
	WindowEnd      string                  `json:"window_end"`
	RequestCount   int64                   `json:"request_count"`
	ErrorCount     int64                   `json:"error_count"`
	DurationSumNS  int64                   `json:"duration_sum_ns"`
	Confidence     graphConfidenceDocument `json:"confidence"`
	EvidenceIDs    []string                `json:"evidence_ids"`
	Limitations    []string                `json:"limitations"`
}
type graphConfidenceDocument struct {
	Level            string `json:"level"`
	Basis            string `json:"basis"`
	AlgorithmVersion string `json:"algorithm_version"`
}
type timelineDocument struct {
	Since       string                  `json:"since"`
	Until       string                  `json:"until"`
	Environment string                  `json:"environment"`
	Items       []timelineEventDocument `json:"items"`
}
type timelineEventDocument struct {
	ID               string                      `json:"id"`
	Time             string                      `json:"time"`
	Kind             string                      `json:"kind"`
	Environment      string                      `json:"environment"`
	Service          string                      `json:"service"`
	RelationType     string                      `json:"relation_type"`
	Subject          subjectDocument             `json:"subject"`
	Source           sourceDocument              `json:"source"`
	Confidence       timelineConfidenceDocument  `json:"confidence"`
	Deployment       *timelineDeploymentDocument `json:"deployment"`
	Runtime          *runtimeDocument            `json:"runtime"`
	Concurrency      concurrencyDocument         `json:"concurrency"`
	Limitations      []string                    `json:"limitations"`
	CausalityClaimed bool                        `json:"causality_claimed"`
}
type subjectDocument struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}
type sourceDocument struct {
	Kind       string `json:"kind"`
	ObservedAt string `json:"observed_at"`
}
type timelineConfidenceDocument struct {
	Level string `json:"level"`
	Basis string `json:"basis"`
}
type timelineDeploymentDocument struct {
	Status               string                     `json:"status"`
	Strategy             string                     `json:"strategy"`
	ProvenanceStatus     string                     `json:"provenance_status"`
	ProvenanceConfidence timelineConfidenceDocument `json:"provenance_confidence"`
}
type runtimeDocument struct {
	State        string `json:"state"`
	Health       string `json:"health"`
	RestartCount int    `json:"restart_count"`
}
type concurrencyDocument struct {
	Detected bool `json:"detected"`
	Count    int  `json:"count"`
}
type evidenceDocument struct {
	AcceptedWindowIDs []string `json:"accepted_window_ids"`
	RejectedWindowIDs []string `json:"rejected_window_ids"`
	GraphEvidenceIDs  []string `json:"graph_evidence_ids"`
	TimelineEventIDs  []string `json:"timeline_event_ids"`
}

func documentFrom(result investigation.Result) investigationDocument {
	return investigationDocument{InvestigationVersion: result.InvestigationVersion, InvestigationKey: result.InvestigationKey, Status: result.Status, Deployment: deploymentFrom(result.Deployment), Regression: regressionFrom(result.Regression), Topology: topologyFrom(result), Timeline: timelineFrom(result), EvidenceReferences: evidenceDocument{AcceptedWindowIDs: stringsCopy(result.EvidenceReferences.AcceptedWindowIDs), RejectedWindowIDs: stringsCopy(result.EvidenceReferences.RejectedWindowIDs), GraphEvidenceIDs: stringsCopy(result.EvidenceReferences.GraphEvidenceIDs), TimelineEventIDs: stringsCopy(result.EvidenceReferences.TimelineEventIDs)}, Limitations: stringsCopy(result.Limitations), CausalityClaimed: false}
}

func deploymentFrom(value regression.Deployment) deploymentDocument {
	return deploymentDocument{ID: value.ID, Environment: value.Environment, Service: value.Service, StartedAt: timestamp(value.StartedAt)}
}

func regressionFrom(value regression.Result) regressionDocument {
	output := regressionDocument{ComparisonKey: value.ComparisonKey, Status: value.Status, AlgorithmVersion: value.AlgorithmVersion, Deployment: deploymentFrom(value.Deployment), Metric: string(value.Metric), Unit: value.Unit, Before: sideFrom(value.Before), After: sideFrom(value.After), AbsoluteDelta: floatCopy(value.AbsoluteDelta), RelativeDelta: floatCopy(value.RelativeDelta), Contamination: contaminationDocument{BeforeDeployments: stringsCopy(value.Contamination.BeforeDeployments), AfterDeployments: stringsCopy(value.Contamination.AfterDeployments), ConcurrentDeployments: stringsCopy(value.Contamination.ConcurrentDeployments), Truncated: value.Contamination.Truncated}, BaselineConfidence: confidenceFrom(value.BaselineConfidence), ObservationConfidence: confidenceFrom(value.ObservationConfidence), RegressionConfidence: confidenceFrom(value.RegressionConfidence), CausalityClaimed: false}
	if value.Classification != nil {
		classification := value.Classification
		algorithm, _, _ := strings.Cut(classification.AlgorithmVersion, "/")
		output.Classification = &classificationDocument{ClassificationKey: classification.ClassificationKey, Result: classification.Result, Direction: classification.Direction, Algorithm: algorithm, AlgorithmVersion: classification.AlgorithmVersion, Thresholds: thresholdsDocument{AbsoluteMin: floatCopy(classification.Thresholds.AbsoluteMin), RelativeMin: floatCopy(classification.Thresholds.RelativeMin), RequireAll: classification.Thresholds.RequireAll, Unit: classification.Thresholds.Unit}, ObservedEffect: effectDocument{AbsoluteDelta: floatCopy(classification.ObservedEffect.AbsoluteDelta), RelativeDelta: floatCopy(classification.ObservedEffect.RelativeDelta)}, Confidence: confidenceFrom(classification.Confidence), CausalityClaimed: false}
	}
	return output
}

func sideFrom(value regression.Side) sideDocument {
	accepted := make([]windowSummary, 0, len(value.AcceptedWindows))
	for _, window := range value.AcceptedWindows {
		accepted = append(accepted, windowSummary{ID: window.ID, Start: timestamp(window.Start), End: timestamp(window.End), SampleCount: window.SampleCount})
	}
	rejected := make([]rejectedWindow, 0, len(value.RejectedWindows))
	for _, window := range value.RejectedWindows {
		rejected = append(rejected, rejectedWindow{ID: window.ID, Start: timestamp(window.Start), End: timestamp(window.End), ObservedAt: timestamp(window.ObservedAt), SampleCount: window.SampleCount, CoverageRatio: floatCopy(window.CoverageRatio), IsComplete: window.IsComplete, Contaminated: window.Contaminated, Reason: string(window.Reason)})
	}
	return sideDocument{Status: value.Status, AlgorithmVersion: value.AlgorithmVersion, Window: windowDocument{Start: timestamp(value.WindowStart), End: timestamp(value.WindowEnd)}, Value: floatCopy(value.Value), SampleCount: value.SampleCount, CoverageRatio: floatCopy(value.CoverageRatio), IsComplete: value.IsComplete, AcceptedWindows: accepted, RejectedWindows: rejected}
}

func confidenceFrom(value regression.Confidence) confidenceDocument {
	return confidenceDocument{Level: value.Level, Basis: value.Basis, AlgorithmVersion: value.AlgorithmVersion, Limitations: stringsCopy(value.Limitations)}
}

func topologyFrom(result investigation.Result) topologyDocument {
	output := topologyDocument{At: timestamp(result.Topology.At), Environment: result.Topology.Environment, Roots: stringsCopy(result.Topology.Roots), Nodes: make([]nodeDocument, 0, len(result.Topology.Nodes)), Edges: make([]edgeDocument, 0, len(result.Topology.Edges)), Truncated: result.Topology.Truncated}
	for _, node := range result.Topology.Nodes {
		output.Nodes = append(output.Nodes, nodeDocument{ID: node.ID, Type: node.Type, LogicalKey: node.LogicalKey, DisplayName: node.DisplayName})
	}
	for _, edge := range result.Topology.Edges {
		output.Edges = append(output.Edges, edgeDocument{ID: edge.ID, From: edge.From, To: edge.To, RelationType: "OBSERVED", DependencyKind: edge.DependencyKind, WindowStart: timestamp(edge.WindowStart), WindowEnd: timestamp(edge.WindowEnd), RequestCount: edge.RequestCount, ErrorCount: edge.ErrorCount, DurationSumNS: edge.DurationSumNS, Confidence: graphConfidenceDocument{Level: string(edge.Confidence), Basis: edge.Basis, AlgorithmVersion: edge.AlgorithmVersion}, EvidenceIDs: stringsCopy(edge.EvidenceIDs), Limitations: stringsCopy(edge.Limitations)})
	}
	return output
}

func timelineFrom(result investigation.Result) timelineDocument {
	output := timelineDocument{Since: timestamp(result.Timeline.Since), Until: timestamp(result.Timeline.Until), Environment: result.Timeline.Environment, Items: make([]timelineEventDocument, 0, len(result.Timeline.Items))}
	for _, event := range result.Timeline.Items {
		item := timelineEventDocument{ID: event.ID, Time: timestamp(event.Time), Kind: event.Kind, Environment: event.Environment, Service: event.Service, RelationType: event.RelationType, Subject: subjectDocument{Type: event.Subject.Type, ID: event.Subject.ID}, Source: sourceDocument{Kind: event.Source.Kind, ObservedAt: timestamp(event.Source.ObservedAt)}, Confidence: timelineConfidenceDocument{Level: event.Confidence.Level, Basis: event.Confidence.Basis}, Concurrency: concurrencyDocument{Detected: event.Concurrency.Detected, Count: event.Concurrency.Count}, Limitations: stringsCopy(event.Limitations), CausalityClaimed: false}
		if event.Deployment != nil {
			item.Deployment = &timelineDeploymentDocument{Status: event.Deployment.Status, Strategy: event.Deployment.Strategy, ProvenanceStatus: event.Deployment.ProvenanceStatus, ProvenanceConfidence: timelineConfidenceDocument{Level: event.Deployment.ProvenanceConfidence.Level, Basis: event.Deployment.ProvenanceConfidence.Basis}}
		}
		if event.Runtime != nil {
			item.Runtime = &runtimeDocument{State: event.Runtime.State, Health: event.Runtime.Health, RestartCount: event.Runtime.RestartCount}
		}
		output.Items = append(output.Items, item)
	}
	return output
}

func validateDocument(value investigationDocument) error {
	if value.InvestigationVersion == "" || !validInvestigationKey(value.InvestigationKey) || value.Status == "" || value.CausalityClaimed || value.Regression.CausalityClaimed || value.Regression.Classification == nil || value.Regression.Classification.CausalityClaimed || value.Deployment.ID == "" || value.Regression.ComparisonKey == "" || value.Regression.Deployment.ID != value.Deployment.ID {
		return fmt.Errorf("%w: invalid investigation document", errs.ErrInvalid)
	}
	if value.Limitations == nil || value.Topology.Roots == nil || value.Topology.Nodes == nil || value.Topology.Edges == nil || value.Timeline.Items == nil || value.EvidenceReferences.AcceptedWindowIDs == nil || value.EvidenceReferences.RejectedWindowIDs == nil || value.EvidenceReferences.GraphEvidenceIDs == nil || value.EvidenceReferences.TimelineEventIDs == nil || value.Regression.Before.AcceptedWindows == nil || value.Regression.Before.RejectedWindows == nil || value.Regression.After.AcceptedWindows == nil || value.Regression.After.RejectedWindows == nil || value.Regression.Contamination.BeforeDeployments == nil || value.Regression.Contamination.AfterDeployments == nil || value.Regression.Contamination.ConcurrentDeployments == nil || value.Regression.BaselineConfidence.Limitations == nil || value.Regression.ObservationConfidence.Limitations == nil || value.Regression.RegressionConfidence.Limitations == nil || value.Regression.Classification.Confidence.Limitations == nil {
		return fmt.Errorf("%w: investigation arrays must not be null", errs.ErrInvalid)
	}
	for _, edge := range value.Topology.Edges {
		if edge.EvidenceIDs == nil || edge.Limitations == nil {
			return fmt.Errorf("%w: investigation arrays must not be null", errs.ErrInvalid)
		}
	}
	for _, event := range value.Timeline.Items {
		if event.Limitations == nil || event.CausalityClaimed {
			return fmt.Errorf("%w: invalid investigation document", errs.ErrInvalid)
		}
	}
	if err := validateDocumentSemantics(value); err != nil {
		return fmt.Errorf("%w: invalid investigation semantics", errs.ErrInvalid)
	}
	return nil
}

func validateDocumentSemantics(value investigationDocument) error {
	if value.InvestigationVersion != investigation.Version || !availableStatus(value.Status) || !sameDeployment(value.Deployment, value.Regression.Deployment) || !validDeployment(value.Deployment) {
		return errors.New("invalid version, status, or deployment")
	}
	deploymentAt, err := parseTimestamp(value.Deployment.StartedAt)
	if err != nil {
		return err
	}
	comparison, err := regressionFromDocument(value.Regression)
	if err != nil {
		return err
	}
	if comparison.Status != value.Status || comparison.Deployment.StartedAt != deploymentAt {
		return errors.New("inconsistent regression")
	}
	classification, err := regression.Classify(context.Background(), comparison)
	if err != nil {
		return err
	}
	if !sameClassification(classificationDocumentFrom(classification), *value.Regression.Classification) {
		return errors.New("inconsistent classification")
	}
	if err := validateTopology(value.Topology, value.Deployment, deploymentAt); err != nil {
		return err
	}
	if err := validateTimeline(value.Timeline, value.Deployment, comparison); err != nil {
		return err
	}
	if err := validateEvidenceReferences(value); err != nil {
		return err
	}
	result, err := investigationFromDocument(value, comparison, deploymentAt)
	if err != nil {
		return err
	}
	calculated, err := investigation.CalculateKey(result)
	if err != nil || calculated != value.InvestigationKey {
		return errors.New("inconsistent investigation key")
	}
	return nil
}

func availableStatus(value string) bool { return value == "AVAILABLE" || value == "UNKNOWN" }

func validDeployment(value deploymentDocument) bool {
	return validUUIDv7(value.ID) && value.Environment != "" && value.Service != ""
}

func sameDeployment(left, right deploymentDocument) bool {
	return left.ID == right.ID && left.Environment == right.Environment && left.Service == right.Service && left.StartedAt == right.StartedAt
}

func validUUIDv7(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[14] != '7' || value[18] != '-' || value[19] < '8' || value[19] > 'b' || value[23] != '-' {
		return false
	}
	for index := range len(value) {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !((value[index] >= '0' && value[index] <= '9') || (value[index] >= 'a' && value[index] <= 'f')) {
			return false
		}
	}
	return true
}

func parseTimestamp(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.IsZero() || value != parsed.UTC().Format(time.RFC3339Nano) {
		return time.Time{}, errors.New("invalid timestamp")
	}
	return parsed.UTC(), nil
}

func parseWindow(value windowDocument) (time.Time, time.Time, error) {
	start, err := parseTimestamp(value.Start)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	end, err := parseTimestamp(value.End)
	if err != nil || !start.Before(end) {
		return time.Time{}, time.Time{}, errors.New("invalid interval")
	}
	return start, end, nil
}

func finiteDomain(value *float64, minimum, maximum float64) bool {
	return value == nil || (!math.IsNaN(*value) && !math.IsInf(*value, 0) && *value >= minimum && *value <= maximum)
}

func regressionFromDocument(value regressionDocument) (regression.Result, error) {
	if value.AlgorithmVersion != regression.AlgorithmVersion || !validComparisonKey(value.ComparisonKey) || !availableStatus(value.Status) || !validMetricUnit(value.Metric, value.Unit) || value.CausalityClaimed {
		return regression.Result{}, errors.New("invalid regression")
	}
	deploymentTime, err := parseTimestamp(value.Deployment.StartedAt)
	if err != nil || !validDeployment(value.Deployment) {
		return regression.Result{}, errors.New("invalid regression deployment")
	}
	before, err := sideFromDocument(value.Before, baseline.AlgorithmVersion)
	if err != nil {
		return regression.Result{}, err
	}
	after, err := sideFromDocument(value.After, regression.ObservationAlgorithmVersion)
	if err != nil {
		return regression.Result{}, err
	}
	if !before.WindowEnd.Equal(deploymentTime) || !after.WindowStart.Equal(deploymentTime) {
		return regression.Result{}, errors.New("inconsistent comparison intervals")
	}
	if !validConfidence(value.BaselineConfidence, baseline.AlgorithmVersion) || !validConfidence(value.ObservationConfidence, regression.ObservationAlgorithmVersion) || !validConfidence(value.RegressionConfidence, regression.AlgorithmVersion) || !finiteDomain(value.AbsoluteDelta, math.Inf(-1), math.Inf(1)) || !finiteDomain(value.RelativeDelta, math.Inf(-1), math.Inf(1)) || !validIDs(value.Contamination.BeforeDeployments) || !validIDs(value.Contamination.AfterDeployments) || !validIDs(value.Contamination.ConcurrentDeployments) {
		return regression.Result{}, errors.New("invalid regression confidence or values")
	}
	result := regression.Result{ComparisonKey: value.ComparisonKey, Status: value.Status, AlgorithmVersion: value.AlgorithmVersion, Deployment: regression.Deployment{ID: value.Deployment.ID, Environment: value.Deployment.Environment, Service: value.Deployment.Service, StartedAt: deploymentTime}, Metric: baseline.Metric(value.Metric), Unit: value.Unit, Before: before, After: after, AbsoluteDelta: floatCopy(value.AbsoluteDelta), RelativeDelta: floatCopy(value.RelativeDelta), Contamination: regression.Contamination{BeforeDeployments: stringsCopy(value.Contamination.BeforeDeployments), AfterDeployments: stringsCopy(value.Contamination.AfterDeployments), ConcurrentDeployments: stringsCopy(value.Contamination.ConcurrentDeployments), Truncated: value.Contamination.Truncated}, BaselineConfidence: confidenceToDomain(value.BaselineConfidence), ObservationConfidence: confidenceToDomain(value.ObservationConfidence), RegressionConfidence: confidenceToDomain(value.RegressionConfidence)}
	if value.Status == "AVAILABLE" {
		if before.Status != "AVAILABLE" || after.Status != "AVAILABLE" || before.Value == nil || after.Value == nil || result.AbsoluteDelta == nil || (result.Metric != baseline.ErrorRate && result.Metric != baseline.RequestCount && result.RelativeDelta == nil) || result.Contamination.Truncated || len(result.Contamination.BeforeDeployments) != 0 || len(result.Contamination.AfterDeployments) != 0 || len(result.Contamination.ConcurrentDeployments) != 0 || !sameFloat(*result.AbsoluteDelta, *after.Value-*before.Value) || (result.RelativeDelta != nil && *before.Value != 0 && !sameFloat(*result.RelativeDelta, *result.AbsoluteDelta / *before.Value)) {
			return regression.Result{}, errors.New("inconsistent available comparison")
		}
	}
	return result, nil
}

func sideFromDocument(value sideDocument, algorithm string) (regression.Side, error) {
	if !availableStatus(value.Status) || value.AlgorithmVersion != algorithm || value.SampleCount < 0 || !finiteDomain(value.Value, math.Inf(-1), math.Inf(1)) || !finiteDomain(value.CoverageRatio, 0, 1) {
		return regression.Side{}, errors.New("invalid comparison side")
	}
	start, end, err := parseWindow(value.Window)
	if err != nil {
		return regression.Side{}, err
	}
	accepted := make([]baseline.WindowSummary, 0, len(value.AcceptedWindows))
	for _, window := range value.AcceptedWindows {
		windowStart, windowEnd, err := parseWindow(windowDocument{Start: window.Start, End: window.End})
		if err != nil || window.ID == "" || window.SampleCount < 0 || windowStart.Before(start) || windowEnd.After(end) {
			return regression.Side{}, errors.New("invalid accepted window")
		}
		accepted = append(accepted, baseline.WindowSummary{ID: window.ID, Start: windowStart, End: windowEnd, SampleCount: window.SampleCount})
	}
	rejected := make([]baseline.RejectedWindow, 0, len(value.RejectedWindows))
	for _, window := range value.RejectedWindows {
		windowStart, windowEnd, err := parseWindow(windowDocument{Start: window.Start, End: window.End})
		observed, observedErr := parseTimestamp(window.ObservedAt)
		if err != nil || observedErr != nil || window.ID == "" || window.SampleCount < 0 || !finiteDomain(window.CoverageRatio, 0, 1) || !validRejectedReason(window.Reason) || windowStart.Before(start) || windowEnd.After(end) {
			return regression.Side{}, errors.New("invalid rejected window")
		}
		rejected = append(rejected, baseline.RejectedWindow{ID: window.ID, Start: windowStart, End: windowEnd, ObservedAt: observed, SampleCount: window.SampleCount, CoverageRatio: floatCopy(window.CoverageRatio), IsComplete: window.IsComplete, Contaminated: window.Contaminated, Reason: baseline.RejectionReason(window.Reason)})
	}
	return regression.Side{Status: value.Status, AlgorithmVersion: value.AlgorithmVersion, WindowStart: start, WindowEnd: end, Value: floatCopy(value.Value), SampleCount: value.SampleCount, CoverageRatio: floatCopy(value.CoverageRatio), IsComplete: value.IsComplete, AcceptedWindows: accepted, RejectedWindows: rejected}, nil
}

func validMetricUnit(metric, unit string) bool {
	return (metric == string(baseline.RequestCount) && unit == "requests") || (metric == string(baseline.ErrorRate) && unit == "ratio") || ((metric == string(baseline.LatencyP50) || metric == string(baseline.LatencyP95) || metric == string(baseline.LatencyP99)) && unit == "nanoseconds")
}

func validComparisonKey(value string) bool {
	return validInvestigationKey(value) && strings.Trim(value[7:], "0") != ""
}

func validConfidence(value confidenceDocument, algorithm string) bool {
	return (value.Level == "LOW" || value.Level == "UNKNOWN") && value.Basis != "" && value.AlgorithmVersion == algorithm && value.Limitations != nil
}

func confidenceToDomain(value confidenceDocument) regression.Confidence {
	return regression.Confidence{Level: value.Level, Basis: value.Basis, AlgorithmVersion: value.AlgorithmVersion, Limitations: stringsCopy(value.Limitations)}
}

func validRejectedReason(value string) bool {
	switch baseline.RejectionReason(value) {
	case baseline.AmbiguousExactWindow, baseline.ContaminatedByDeployment, baseline.InsufficientSamples, baseline.InsufficientCoverage, baseline.FutureEvidence, baseline.InvalidWindow, baseline.MetricUnavailable:
		return true
	}
	return false
}

func validIDs(values []string) bool {
	if values == nil || !slices.IsSorted(values) {
		return false
	}
	return !slices.Contains(values, "") && len(slices.Compact(slices.Clone(values))) == len(values)
}

func sameFloat(left, right float64) bool {
	return math.Abs(left-right) <= math.Max(math.Abs(left), math.Abs(right))*1e-12+1e-15
}

func classificationDocumentFrom(value regression.Classification) classificationDocument {
	algorithm, _, _ := strings.Cut(value.AlgorithmVersion, "/")
	return classificationDocument{ClassificationKey: value.ClassificationKey, Result: value.Result, Direction: value.Direction, Algorithm: algorithm, AlgorithmVersion: value.AlgorithmVersion, Thresholds: thresholdsDocument{AbsoluteMin: floatCopy(value.Thresholds.AbsoluteMin), RelativeMin: floatCopy(value.Thresholds.RelativeMin), RequireAll: value.Thresholds.RequireAll, Unit: value.Thresholds.Unit}, ObservedEffect: effectDocument{AbsoluteDelta: floatCopy(value.ObservedEffect.AbsoluteDelta), RelativeDelta: floatCopy(value.ObservedEffect.RelativeDelta)}, Confidence: confidenceFrom(value.Confidence), CausalityClaimed: false}
}

func sameClassification(left, right classificationDocument) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func validateTopology(value topologyDocument, deployment deploymentDocument, deploymentAt time.Time) error {
	at, err := parseTimestamp(value.At)
	if err != nil || !at.Equal(deploymentAt) || value.Environment != deployment.Environment || !validIDs(value.Roots) {
		return errors.New("invalid topology")
	}
	nodes := make(map[string]struct{}, len(value.Nodes))
	for _, node := range value.Nodes {
		if node.ID == "" || node.Type == "" || node.LogicalKey == "" || node.DisplayName == "" {
			return errors.New("invalid topology node")
		}
		if _, exists := nodes[node.ID]; exists {
			return errors.New("duplicate topology node")
		}
		nodes[node.ID] = struct{}{}
	}
	for _, root := range value.Roots {
		if _, exists := nodes[root]; !exists {
			return errors.New("orphan topology root")
		}
	}
	edges := make(map[string]struct{}, len(value.Edges))
	for _, edge := range value.Edges {
		start, end, err := parseWindow(windowDocument{Start: edge.WindowStart, End: edge.WindowEnd})
		if err != nil || start.IsZero() || end.IsZero() || edge.ID == "" || edge.RelationType != "OBSERVED" || !validDependencyKind(edge.DependencyKind) || edge.RequestCount < 0 || edge.ErrorCount < 0 || edge.ErrorCount > edge.RequestCount || edge.DurationSumNS < 0 || !validGraphConfidence(edge.Confidence) || edge.Confidence.AlgorithmVersion != topology.AlgorithmVersion || !validIDs(edge.EvidenceIDs) || edge.Limitations == nil {
			return errors.New("invalid topology edge")
		}
		if _, exists := nodes[edge.From]; !exists {
			return errors.New("orphan topology edge from")
		}
		if _, exists := nodes[edge.To]; !exists {
			return errors.New("orphan topology edge to")
		}
		if _, exists := edges[edge.ID]; exists {
			return errors.New("duplicate topology edge")
		}
		edges[edge.ID] = struct{}{}
	}
	return nil
}

func validDependencyKind(value string) bool {
	switch value {
	case "service", "database", "queue", "external_api":
		return true
	}
	return false
}

func validGraphConfidence(value graphConfidenceDocument) bool {
	return (value.Level == "UNKNOWN" || value.Level == "LOW" || value.Level == "MEDIUM" || value.Level == "HIGH") && value.Basis != ""
}

func validateTimeline(value timelineDocument, deployment deploymentDocument, comparison regression.Result) error {
	since, until, err := parseWindow(windowDocument{Start: value.Since, End: value.Until})
	if err != nil || value.Environment != deployment.Environment || !since.Equal(comparison.Before.WindowStart) || !until.Equal(comparison.After.WindowEnd) {
		return errors.New("invalid timeline")
	}
	ids := make(map[string]struct{}, len(value.Items))
	previous := timelineEventDocument{}
	hasPrevious := false
	for _, event := range value.Items {
		eventTime, eventErr := parseTimestamp(event.Time)
		_, sourceErr := parseTimestamp(event.Source.ObservedAt)
		if eventErr != nil || sourceErr != nil || !eventTime.Before(until) || eventTime.Before(since) || event.ID == "" || event.Environment != deployment.Environment || event.Service != deployment.Service || event.CausalityClaimed || !validTimelineEvent(event) || event.Concurrency.Count < 0 || event.Concurrency.Detected != (event.Concurrency.Count > 1) || event.Limitations == nil {
			return errors.New("invalid timeline event")
		}
		if _, exists := ids[event.ID]; exists {
			return errors.New("duplicate timeline event")
		}
		if hasPrevious && !timelineOrdered(previous, event) {
			return errors.New("noncanonical timeline ordering")
		}
		ids[event.ID] = struct{}{}
		previous, hasPrevious = event, true
	}
	return nil
}

func timelineOrdered(left, right timelineEventDocument) bool {
	leftTime, _ := parseTimestamp(left.Time)
	rightTime, _ := parseTimestamp(right.Time)
	if leftTime.After(rightTime) {
		return true
	}
	if !leftTime.Equal(rightTime) {
		return false
	}
	leftPriority, rightPriority := timeline.Priority(left.Kind), timeline.Priority(right.Kind)
	return leftPriority < rightPriority || (leftPriority == rightPriority && left.ID <= right.ID)
}

func validTimelineEvent(value timelineEventDocument) bool {
	if !validTimelineKind(value.Kind) || !validTimelineRelation(value.RelationType) || value.Subject.ID == "" || !validSubjectType(value.Subject.Type) || !validSourceKind(value.Source.Kind) || !validTimelineConfidence(value.Confidence) {
		return false
	}
	if value.Deployment != nil {
		if !strings.HasPrefix(value.Kind, "deployment_") && value.Kind != "rollback_declared" || !validDeploymentPayload(*value.Deployment) {
			return false
		}
	} else if strings.HasPrefix(value.Kind, "deployment_") || value.Kind == "rollback_declared" {
		return false
	}
	if value.Runtime != nil {
		if value.Kind != "runtime_observed" || value.RelationType != "OBSERVED" || value.Subject.Type != "runtime_instance" || value.Source.Kind != "docker" || value.Runtime.RestartCount < 0 || !validRuntimeState(value.Runtime.State) || !validRuntimeHealth(value.Runtime.Health) {
			return false
		}
	} else if value.Kind == "runtime_observed" {
		return false
	}
	if value.Kind != "runtime_observed" && (value.RelationType != "DECLARED" || value.Subject.Type != "deployment" || value.Source.Kind != "deployment_ledger") {
		return false
	}
	return true
}

func validTimelineKind(value string) bool {
	switch value {
	case "deployment_pending", "deployment_running", "deployment_succeeded", "deployment_failed", "deployment_cancelled", "rollback_declared", "runtime_observed":
		return true
	}
	return false
}

func validTimelineRelation(value string) bool { return value == "DECLARED" || value == "OBSERVED" }
func validSubjectType(value string) bool      { return value == "deployment" || value == "runtime_instance" }
func validSourceKind(value string) bool       { return value == "deployment_ledger" || value == "docker" }
func validTimelineConfidence(value timelineConfidenceDocument) bool {
	return (value.Level == "LOW" || value.Level == "MEDIUM" || value.Level == "HIGH" || value.Level == "UNKNOWN") && value.Basis != ""
}
func validDeploymentPayload(value timelineDeploymentDocument) bool {
	if !validTimelineConfidence(value.ProvenanceConfidence) {
		return false
	}
	switch value.Status {
	case "pending", "running", "succeeded", "failed", "cancelled", "rolled_back", "unknown":
	default:
		return false
	}
	return value.Strategy == "unknown" && (value.ProvenanceStatus == "MATCHED" || value.ProvenanceStatus == "PARTIAL" || value.ProvenanceStatus == "UNKNOWN" || value.ProvenanceStatus == "CONTRADICTED")
}
func validRuntimeState(value string) bool {
	return value == "running" || value == "exited" || value == "created" || value == "paused" || value == "restarting" || value == "dead"
}
func validRuntimeHealth(value string) bool {
	return value == "none" || value == "starting" || value == "healthy" || value == "unhealthy" || value == ""
}

func validateEvidenceReferences(value investigationDocument) error {
	accepted, rejected, evidence, events := []string{}, []string{}, []string{}, []string{}
	for _, side := range []sideDocument{value.Regression.Before, value.Regression.After} {
		for _, window := range side.AcceptedWindows {
			accepted = append(accepted, window.ID)
		}
		for _, window := range side.RejectedWindows {
			rejected = append(rejected, window.ID)
		}
	}
	for _, edge := range value.Topology.Edges {
		evidence = append(evidence, edge.EvidenceIDs...)
	}
	for _, event := range value.Timeline.Items {
		events = append(events, event.ID)
	}
	if !sameIDs(value.EvidenceReferences.AcceptedWindowIDs, accepted) || !sameIDs(value.EvidenceReferences.RejectedWindowIDs, rejected) || !sameIDs(value.EvidenceReferences.GraphEvidenceIDs, evidence) || !sameIDs(value.EvidenceReferences.TimelineEventIDs, events) {
		return errors.New("inconsistent evidence references")
	}
	return nil
}

func sameIDs(actual, values []string) bool {
	if !validIDs(actual) {
		return false
	}
	values = slices.Clone(values)
	slices.Sort(values)
	values = slices.Compact(values)
	if values == nil {
		values = []string{}
	}
	return slices.Equal(actual, values)
}

func investigationFromDocument(value investigationDocument, comparison regression.Result, deploymentAt time.Time) (investigation.Result, error) {
	topologyAt, _ := parseTimestamp(value.Topology.At)
	graph := topology.Result{At: topologyAt, Environment: value.Topology.Environment, Roots: stringsCopy(value.Topology.Roots), Nodes: make([]topology.Node, 0, len(value.Topology.Nodes)), Edges: make([]topology.Edge, 0, len(value.Topology.Edges)), Truncated: value.Topology.Truncated}
	for _, node := range value.Topology.Nodes {
		graph.Nodes = append(graph.Nodes, topology.Node{ID: node.ID, Type: node.Type, LogicalKey: node.LogicalKey, DisplayName: node.DisplayName})
	}
	for _, edge := range value.Topology.Edges {
		start, end, _ := parseWindow(windowDocument{Start: edge.WindowStart, End: edge.WindowEnd})
		graph.Edges = append(graph.Edges, topology.Edge{ID: edge.ID, From: edge.From, To: edge.To, DependencyKind: edge.DependencyKind, WindowStart: start, WindowEnd: end, RequestCount: edge.RequestCount, ErrorCount: edge.ErrorCount, DurationSumNS: edge.DurationSumNS, Confidence: topology.Confidence(edge.Confidence.Level), Basis: edge.Confidence.Basis, AlgorithmVersion: edge.Confidence.AlgorithmVersion, EvidenceIDs: stringsCopy(edge.EvidenceIDs), Limitations: stringsCopy(edge.Limitations)})
	}
	since, _ := parseTimestamp(value.Timeline.Since)
	until, _ := parseTimestamp(value.Timeline.Until)
	timelineResult := timeline.Result{Since: since, Until: until, Environment: value.Timeline.Environment, Items: make([]timeline.Event, 0, len(value.Timeline.Items))}
	for _, event := range value.Timeline.Items {
		timeValue, _ := parseTimestamp(event.Time)
		observed, _ := parseTimestamp(event.Source.ObservedAt)
		item := timeline.Event{ID: event.ID, Time: timeValue, Kind: event.Kind, Environment: event.Environment, Service: event.Service, RelationType: event.RelationType, Subject: timeline.Subject{Type: event.Subject.Type, ID: event.Subject.ID}, Source: timeline.Source{Kind: event.Source.Kind, ObservedAt: observed}, Confidence: timeline.Confidence{Level: event.Confidence.Level, Basis: event.Confidence.Basis}, Concurrency: timeline.Concurrency{Detected: event.Concurrency.Detected, Count: event.Concurrency.Count}, Limitations: stringsCopy(event.Limitations)}
		if event.Deployment != nil {
			item.Deployment = &timeline.Deployment{Status: event.Deployment.Status, Strategy: event.Deployment.Strategy, ProvenanceStatus: event.Deployment.ProvenanceStatus, ProvenanceConfidence: timeline.Confidence{Level: event.Deployment.ProvenanceConfidence.Level, Basis: event.Deployment.ProvenanceConfidence.Basis}}
		}
		if event.Runtime != nil {
			item.Runtime = &timeline.Runtime{State: event.Runtime.State, Health: event.Runtime.Health, RestartCount: event.Runtime.RestartCount}
		}
		timelineResult.Items = append(timelineResult.Items, item)
	}
	if slices.Contains(value.Limitations, investigation.TimelineLimitedLimitation) {
		timelineResult.NextCursor = "limited"
	}
	return investigation.Result{InvestigationVersion: value.InvestigationVersion, Status: value.Status, Deployment: regression.Deployment{ID: value.Deployment.ID, Environment: value.Deployment.Environment, Service: value.Deployment.Service, StartedAt: deploymentAt}, Regression: withClassification(comparison, *value.Regression.Classification), Topology: graph, Timeline: timelineResult, EvidenceReferences: investigation.EvidenceReferences{AcceptedWindowIDs: stringsCopy(value.EvidenceReferences.AcceptedWindowIDs), RejectedWindowIDs: stringsCopy(value.EvidenceReferences.RejectedWindowIDs), GraphEvidenceIDs: stringsCopy(value.EvidenceReferences.GraphEvidenceIDs), TimelineEventIDs: stringsCopy(value.EvidenceReferences.TimelineEventIDs)}, Limitations: stringsCopy(value.Limitations)}, nil
}

func withClassification(value regression.Result, document classificationDocument) regression.Result {
	value.Classification = &regression.Classification{ClassificationKey: document.ClassificationKey, Result: document.Result, Direction: document.Direction, AlgorithmVersion: document.AlgorithmVersion, Thresholds: regression.Thresholds{AbsoluteMin: floatCopy(document.Thresholds.AbsoluteMin), RelativeMin: floatCopy(document.Thresholds.RelativeMin), RequireAll: document.Thresholds.RequireAll, Unit: document.Thresholds.Unit}, ObservedEffect: regression.Effect{AbsoluteDelta: floatCopy(document.ObservedEffect.AbsoluteDelta), RelativeDelta: floatCopy(document.ObservedEffect.RelativeDelta)}, Confidence: confidenceToDomain(document.Confidence)}
	return value
}

func timestamp(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
func stringsCopy(values []string) []string {
	if values == nil {
		return []string{}
	}
	return slices.Clone(values)
}
func floatCopy(value *float64) *float64 {
	if value == nil {
		return nil
	}
	return new(*value)
}
