// Package github implements the bounded GitHub Actions artifact boundary.
package github

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/guijoazeiro/prodmap/internal/deployment"
	"github.com/guijoazeiro/prodmap/internal/errs"
)

const (
	apiVersion       = "2026-03-10"
	defaultUserAgent = "prodmap/github-actions-ledger"
	maxPages         = 10
	maxArtifacts     = 1000
	pageSize         = 100
	maxMetadataBytes = 2 << 20
	maxZIPBytes      = 20 << 20
	maxRedirects     = 3
	maxRetryAttempts = 3
	maxRetryBudget   = 30 * time.Second
)

// Source reads one immutable GitHub Actions artifact without exposing HTTP
// details to its consumer.
type Source struct {
	client                  *http.Client
	baseURL                 *url.URL
	token, userAgent        string
	now                     func() time.Time
	sleep                   func(context.Context, time.Duration) error
	allowInsecureRedirect   bool
	allowUnsafeRedirectHost bool // test-only dependency injection
}

// Option configures dependencies of Source. It is intentionally not a product
// configuration or CLI surface.
type Option func(*Source) error

// New creates a production source rooted at the fixed GitHub API origin.
func New(token string, options ...Option) (*Source, error) {
	base, _ := url.Parse("https://api.github.com")
	source := &Source{client: http.DefaultClient, baseURL: base, token: token, userAgent: defaultUserAgent, now: time.Now, sleep: sleepContext}
	for _, option := range options {
		if err := option(source); err != nil {
			return nil, err
		}
	}
	if source.client == nil || source.baseURL == nil || source.token == "" || strings.ContainsAny(source.token, "\r\n") || source.userAgent == "" || source.now == nil || source.sleep == nil || source.baseURL.Host == "" || source.baseURL.Scheme != "https" && !(source.allowInsecureRedirect && source.baseURL.Scheme == "http") {
		return nil, fmt.Errorf("%w: invalid GitHub Actions source configuration", errs.ErrInvalid)
	}
	return source, nil
}

func withHTTPClient(client *http.Client) Option {
	return func(source *Source) error {
		source.client = client
		return nil
	}
}

func withBaseURL(raw string) Option {
	return func(source *Source) error {
		base, err := url.Parse(raw)
		if err != nil || base.Scheme == "" || base.Host == "" || base.User != nil {
			return fmt.Errorf("%w: invalid test GitHub API URL", errs.ErrInvalid)
		}
		source.baseURL = base
		source.allowInsecureRedirect = base.Scheme == "http"
		return nil
	}
}

func withClock(now func() time.Time) Option {
	return func(source *Source) error { source.now = now; return nil }
}

func withSleep(sleep func(context.Context, time.Duration) error) Option {
	return func(source *Source) error { source.sleep = sleep; return nil }
}

func withUserAgent(userAgent string) Option {
	return func(source *Source) error { source.userAgent = userAgent; return nil }
}

type artifactList struct {
	Artifacts []artifactMetadata `json:"artifacts"`
}

type artifactMetadata struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Size        int64  `json:"size_in_bytes"`
	Expired     bool   `json:"expired"`
	Digest      string `json:"digest"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	ExpiresAt   string `json:"expires_at"`
	WorkflowRun struct {
		ID      int64  `json:"id"`
		HeadSHA string `json:"head_sha"`
	} `json:"workflow_run"`
}

type usableArtifact struct {
	metadata                  artifactMetadata
	created, updated, expires time.Time
}

// Fetch selects one compatible artifact, verifies its ZIP digest, and returns
// the existing deployment-ledger snapshot. A workflow run itself is never a
// deployment.
func (source *Source) Fetch(ctx context.Context, query deployment.SourceQuery) (deployment.SourceResult, error) {
	if err := ctx.Err(); err != nil {
		return deployment.SourceResult{}, err
	}
	if err := validateQuery(query); err != nil {
		return deployment.SourceResult{}, err
	}
	observedAt := query.ObservedAt.UTC()
	if observedAt.IsZero() {
		observedAt = source.now().UTC()
	}
	artifact, err := source.selectArtifact(ctx, query)
	if err != nil {
		return deployment.SourceResult{}, err
	}
	contents, err := source.downloadArtifact(ctx, query, artifact.metadata.ID, artifact.metadata.Digest)
	if err != nil {
		return deployment.SourceResult{}, err
	}
	ledger, err := unzipLedger(contents)
	if err != nil {
		return deployment.SourceResult{}, err
	}
	snapshot, err := deployment.LoadGitHubActionsLedger(ctx, ledger, query.Owner, query.Repository, query.ArtifactName, observedAt)
	if err != nil {
		return deployment.SourceResult{}, fmt.Errorf("%w: GitHub Actions deployment ledger", errs.ErrIncompatible)
	}
	if err := crossCheckWorkflowHead(snapshot, artifact.metadata.WorkflowRun.HeadSHA); err != nil {
		return deployment.SourceResult{}, err
	}
	return deployment.SourceResult{
		Repository:      strings.ToLower(query.Owner) + "/" + strings.ToLower(query.Repository),
		ArtifactName:    artifact.metadata.Name,
		ArtifactDigest:  artifact.metadata.Digest,
		WorkflowHeadSHA: artifact.metadata.WorkflowRun.HeadSHA,
		ArtifactID:      artifact.metadata.ID,
		WorkflowRunID:   artifact.metadata.WorkflowRun.ID,
		CreatedAt:       artifact.created,
		UpdatedAt:       artifact.updated,
		ExpiresAt:       artifact.expires,
		Snapshot:        snapshot,
		Warnings:        []string{},
	}, nil
}

func validateQuery(query deployment.SourceQuery) error {
	for _, value := range []string{query.Owner, query.Repository} {
		if !validRepositoryPart(value) {
			return fmt.Errorf("%w: invalid GitHub Actions repository", errs.ErrInvalid)
		}
	}
	if !validArtifactName(query.ArtifactName) {
		return fmt.Errorf("%w: invalid GitHub Actions artifact name", errs.ErrInvalid)
	}
	return nil
}

func validRepositoryPart(value string) bool {
	if value == "" || len(value) > 100 || strings.Contains(value, "..") {
		return false
	}
	for index, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_' || character == '.') || index == 0 && (character == '-' || character == '_' || character == '.') {
			return false
		}
	}
	return true
}

func validArtifactName(value string) bool {
	if value == "" || len(value) > 255 || strings.Contains(value, "..") || strings.ContainsAny(value, "/\\") {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func (source *Source) selectArtifact(ctx context.Context, query deployment.SourceQuery) (usableArtifact, error) {
	usable := make([]usableArtifact, 0)
	examined := 0
	for page := 1; page <= maxPages; page++ {
		endpoint := source.endpoint("repos", query.Owner, query.Repository, "actions", "artifacts")
		values := endpoint.Query()
		values.Set("name", query.ArtifactName)
		values.Set("per_page", strconv.Itoa(pageSize))
		values.Set("page", strconv.Itoa(page))
		endpoint.RawQuery = values.Encode()
		body, err := source.getBytes(ctx, endpoint, true, maxMetadataBytes)
		if err != nil {
			return usableArtifact{}, err
		}
		var listed artifactList
		if err := json.Unmarshal(body, &listed); err != nil {
			return usableArtifact{}, fmt.Errorf("%w: invalid GitHub Actions artifact metadata", errs.ErrIncompatible)
		}
		examined += len(listed.Artifacts)
		if len(listed.Artifacts) > pageSize || examined > maxArtifacts {
			return usableArtifact{}, fmt.Errorf("%w: GitHub Actions artifact limit exceeded", errs.ErrIncompatible)
		}
		for _, metadata := range listed.Artifacts {
			if candidate, ok := compatibleArtifact(metadata, query.ArtifactName); ok {
				usable = append(usable, candidate)
			}
		}
		if len(listed.Artifacts) < pageSize {
			break
		}
		if page == maxPages {
			break
		}
	}
	if len(usable) == 0 {
		return usableArtifact{}, fmt.Errorf("%w: GitHub Actions deployment artifact", errs.ErrNotFound)
	}
	best := usable[0]
	for _, candidate := range usable[1:] {
		if candidate.created.After(best.created) || candidate.created.Equal(best.created) && candidate.metadata.ID > best.metadata.ID {
			best = candidate
		}
	}
	return best, nil
}

func compatibleArtifact(metadata artifactMetadata, wantedName string) (usableArtifact, bool) {
	if metadata.Name != wantedName || metadata.ID <= 0 || metadata.Expired || metadata.Size <= 0 || metadata.Size > maxZIPBytes || !validDigest(metadata.Digest) || !validGitSHA(metadata.WorkflowRun.HeadSHA) || metadata.WorkflowRun.ID <= 0 {
		return usableArtifact{}, false
	}
	created, createdErr := time.Parse(time.RFC3339Nano, metadata.CreatedAt)
	updated, updatedErr := time.Parse(time.RFC3339Nano, metadata.UpdatedAt)
	expires, expiresErr := time.Parse(time.RFC3339Nano, metadata.ExpiresAt)
	if createdErr != nil || updatedErr != nil || expiresErr != nil {
		return usableArtifact{}, false
	}
	return usableArtifact{metadata: metadata, created: created.UTC(), updated: updated.UTC(), expires: expires.UTC()}, true
}

func (source *Source) endpoint(parts ...string) *url.URL {
	copy := *source.baseURL
	copy.Path = "/" + strings.Join(parts, "/")
	copy.RawQuery = ""
	copy.Fragment = ""
	return &copy
}

func (source *Source) downloadArtifact(ctx context.Context, query deployment.SourceQuery, artifactID int64, digest string) ([]byte, error) {
	endpoint := source.endpoint("repos", query.Owner, query.Repository, "actions", "artifacts", strconv.FormatInt(artifactID, 10), "zip")
	current := endpoint
	includeAuthorization := true
	visited := map[string]struct{}{}
	for redirects := range maxRedirects + 1 {
		key := current.String()
		if _, found := visited[key]; found {
			return nil, fmt.Errorf("%w: GitHub Actions artifact redirect loop", errs.ErrIncompatible)
		}
		visited[key] = struct{}{}
		response, err := source.getResponse(ctx, current, includeAuthorization)
		if err != nil {
			return nil, err
		}
		if response.StatusCode == http.StatusOK {
			contents, readErr := readBounded(response.Body, response.ContentLength, maxZIPBytes)
			response.Body.Close()
			if readErr != nil {
				return nil, readErr
			}
			if len(contents) == 0 {
				return nil, fmt.Errorf("%w: empty GitHub Actions artifact", errs.ErrIncompatible)
			}
			if !digestMatches(contents, digest) {
				return nil, fmt.Errorf("%w: GitHub Actions artifact digest mismatch", errs.ErrIncompatible)
			}
			return contents, nil
		}
		if response.StatusCode < http.StatusMultipleChoices || response.StatusCode > http.StatusTemporaryRedirect {
			response.Body.Close()
			return nil, responseError(response)
		}
		location := response.Header.Get("Location")
		response.Body.Close()
		if redirects == maxRedirects {
			return nil, fmt.Errorf("%w: GitHub Actions artifact redirect limit exceeded", errs.ErrIncompatible)
		}
		next, err := source.safeRedirect(location)
		if err != nil {
			return nil, err
		}
		includeAuthorization = includeAuthorization && strings.EqualFold(current.Host, next.Host)
		current = next
	}
	return nil, fmt.Errorf("%w: GitHub Actions artifact redirect limit exceeded", errs.ErrIncompatible)
}

func (source *Source) safeRedirect(raw string) (*url.URL, error) {
	next, err := url.Parse(raw)
	if err != nil || next.Scheme == "" || next.Host == "" || next.User != nil || next.Fragment != "" {
		return nil, fmt.Errorf("%w: unsafe GitHub Actions artifact redirect", errs.ErrIncompatible)
	}
	if next.Scheme != "https" && !(source.allowInsecureRedirect && next.Scheme == "http") || !source.allowUnsafeRedirectHost && unsafeRedirectHost(next.Hostname()) {
		return nil, fmt.Errorf("%w: unsafe GitHub Actions artifact redirect", errs.ErrIncompatible)
	}
	return next, nil
}

func unsafeRedirectHost(host string) bool {
	if host == "" {
		return true
	}
	if address, err := netip.ParseAddr(host); err == nil {
		return address.IsLoopback() || address.IsPrivate() || address.IsLinkLocalUnicast() || address.IsUnspecified()
	}
	return net.ParseIP(host) != nil
}

func (source *Source) getBytes(ctx context.Context, endpoint *url.URL, includeAuthorization bool, maximum int64) ([]byte, error) {
	response, err := source.getResponse(ctx, endpoint, includeAuthorization)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, responseError(response)
	}
	return readBounded(response.Body, response.ContentLength, maximum)
}

func (source *Source) getResponse(ctx context.Context, endpoint *url.URL, includeAuthorization bool) (*http.Response, error) {
	var lastStatus int
	var waited time.Duration
	for attempt := range maxRetryAttempts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return nil, fmt.Errorf("%w: construct GitHub Actions request", errs.ErrInvalid)
		}
		request.Header.Set("Accept", "application/vnd.github+json")
		request.Header.Set("X-GitHub-Api-Version", apiVersion)
		request.Header.Set("User-Agent", source.userAgent)
		if includeAuthorization && source.token != "" {
			request.Header.Set("Authorization", "Bearer "+source.token)
		}
		client := *source.client
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		response, err := client.Do(request)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, fmt.Errorf("%w: GitHub Actions request", errs.ErrUnavailable)
		}
		if !retryable(response) || attempt == maxRetryAttempts-1 {
			return response, nil
		}
		lastStatus = response.StatusCode
		wait := retryDelay(response.Header, source.now())
		response.Body.Close()
		if wait <= 0 {
			wait = time.Second
		}
		if waited+wait > maxRetryBudget {
			return nil, fmt.Errorf("%w: GitHub Actions retry budget exhausted", errs.ErrUnavailable)
		}
		waited += wait
		if err := source.sleep(ctx, wait); err != nil {
			return nil, err
		}
	}
	return nil, statusError(lastStatus)
}

func retryable(response *http.Response) bool {
	if response.StatusCode == http.StatusForbidden {
		return response.Header.Get("X-RateLimit-Remaining") == "0"
	}
	switch response.StatusCode {
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, http.StatusTooManyRequests:
		return true
	default:
		return false
	}
}

func retryDelay(header http.Header, now time.Time) time.Duration {
	if seconds, err := strconv.Atoi(header.Get("Retry-After")); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if header.Get("X-RateLimit-Remaining") == "0" {
		if reset, err := strconv.ParseInt(header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			return max(0, time.Unix(reset, 0).Sub(now))
		}
	}
	return time.Second
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func readBounded(reader io.Reader, contentLength, maximum int64) ([]byte, error) {
	if contentLength > maximum {
		return nil, fmt.Errorf("%w: GitHub Actions response exceeds limit", errs.ErrIncompatible)
	}
	contents, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read GitHub Actions response", errs.ErrUnavailable)
	}
	if int64(len(contents)) > maximum {
		return nil, fmt.Errorf("%w: GitHub Actions response exceeds limit", errs.ErrIncompatible)
	}
	return contents, nil
}

func unzipLedger(contents []byte) ([]byte, error) {
	archive, err := zip.NewReader(bytes.NewReader(contents), int64(len(contents)))
	if err != nil || len(archive.File) != 1 {
		return nil, fmt.Errorf("%w: invalid GitHub Actions artifact ZIP", errs.ErrIncompatible)
	}
	file := archive.File[0]
	if file.Name != "deployments.jsonl" || file.FileInfo().IsDir() || !file.Mode().IsRegular() || file.UncompressedSize64 == 0 || file.UncompressedSize64 > uint64(deployment.MaxFileBytes) {
		return nil, fmt.Errorf("%w: unsafe GitHub Actions artifact ZIP", errs.ErrIncompatible)
	}
	reader, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("%w: invalid GitHub Actions artifact ZIP", errs.ErrIncompatible)
	}
	defer reader.Close()
	ledger, err := readBounded(reader, int64(file.UncompressedSize64), deployment.MaxFileBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid GitHub Actions artifact ZIP", errs.ErrIncompatible)
	}
	return ledger, nil
}

func digestMatches(contents []byte, wanted string) bool {
	if !validDigest(wanted) {
		return false
	}
	want, err := hex.DecodeString(wanted[len("sha256:"):])
	if err != nil {
		return false
	}
	got := sha256.Sum256(contents)
	return subtle.ConstantTimeCompare(got[:], want) == 1
}

func validDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != 71 {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func validGitSHA(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func crossCheckWorkflowHead(snapshot deployment.Snapshot, headSHA string) error {
	for _, record := range snapshot.Records {
		if record.VCSRevisionVerified && (record.VCSRevision != headSHA || record.GitHead != headSHA) {
			return fmt.Errorf("%w: GitHub Actions workflow revision does not match verified deployment ledger", errs.ErrIncompatible)
		}
	}
	return nil
}

func statusError(status int) error {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w: GitHub Actions access", errs.ErrUnauthorized)
	case http.StatusNotFound, http.StatusGone:
		return fmt.Errorf("%w: GitHub Actions artifact", errs.ErrNotFound)
	case http.StatusTooManyRequests:
		return fmt.Errorf("%w: GitHub Actions rate limit", errs.ErrUnavailable)
	default:
		return fmt.Errorf("%w: GitHub Actions response", errs.ErrUnavailable)
	}
}

func responseError(response *http.Response) error {
	if response.StatusCode == http.StatusForbidden && response.Header.Get("X-RateLimit-Remaining") == "0" {
		return fmt.Errorf("%w: GitHub Actions rate limit", errs.ErrUnavailable)
	}
	return statusError(response.StatusCode)
}
