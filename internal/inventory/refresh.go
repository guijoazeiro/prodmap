package inventory

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/guijoazeiro/prodmap/internal/correlation"
	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/identity"
)

var fullSHA = regexp.MustCompile(`^[0-9a-fA-F]{40}([0-9a-fA-F]{24})?$`)

type Refresher struct {
	OperationID string
	Environment string
	Runtime     RuntimeSource
	Commits     CommitSource
	Store       SnapshotStore
	Now         func() time.Time
}

func (r Refresher) Refresh(ctx context.Context) (RefreshResult, error) {
	if r.Runtime == nil || r.Store == nil {
		return RefreshResult{}, fmt.Errorf("%w: runtime source and snapshot store are required", errs.ErrInvalid)
	}
	environment := "default"
	if r.Environment != "" {
		var environmentErr error
		environment, environmentErr = identity.ValidEnvironment(r.Environment)
		if environmentErr != nil {
			return RefreshResult{}, environmentErr
		}
	}
	refreshOperationID := strings.TrimSpace(r.OperationID)
	if refreshOperationID == "" {
		now := r.Now
		if now == nil {
			now = time.Now
		}
		var err error
		refreshOperationID, err = identity.NewV7(now().UTC())
		if err != nil {
			return RefreshResult{}, err
		}
	}
	batch, err := r.Runtime.InspectRuntime(ctx)
	if err != nil {
		return RefreshResult{}, fmt.Errorf("inspect Docker runtime: %w", err)
	}

	var repository Repository
	repositoryAvailable := false
	if r.Commits == nil {
		batch.Warnings = append(batch.Warnings, "Git commit source is not configured")
	} else {
		repositoryResult, repositoryErr := r.Commits.Repository(ctx)
		switch {
		case repositoryErr == nil:
			repository = repositoryResult
			repositoryAvailable = true
		case errors.Is(repositoryErr, errs.ErrNotFound):
			batch.Warnings = append(batch.Warnings, "Git repository is not available")
		case errors.Is(repositoryErr, errs.ErrUnavailable):
			batch.Warnings = append(batch.Warnings, "Git source is unavailable")
		default:
			return RefreshResult{}, fmt.Errorf("inspect Git repository: %w", repositoryErr)
		}
	}

	snapshot := Snapshot{
		OperationID: refreshOperationID,
		ObservedAt:  batch.ObservedAt,
		Rejected:    batch.Rejected,
		Warnings:    append([]string{}, batch.Warnings...),
	}
	if repositoryAvailable {
		snapshot.Repository = &repository
	}

	for _, observation := range batch.Observations {
		if err := ctx.Err(); err != nil {
			return RefreshResult{}, err
		}
		if snapshot.ObservedAt.IsZero() || observation.ObservedAt.After(snapshot.ObservedAt) {
			snapshot.ObservedAt = observation.ObservedAt
		}
		observedAt := observation.ObservedAt
		if observedAt.IsZero() {
			observedAt = batch.ObservedAt
		}

		artifact, identityConflict := NormalizeArtifact(observation)
		runtimeIdentity := resolveRuntimeServiceIdentity(observation, artifact)
		input := correlation.ProvenanceInput{
			ImmutableIdentity: artifact.Identity,
			MutableAlias:      artifact.ObservedReference,
			IdentityIssues:    append([]string{}, artifact.IdentityIssues...),
			OCIRevision:       artifact.OCIRevision,
			RevisionState:     correlation.RevisionNotApplicable,
			IdentityConflict:  identityConflict,
			ObservedAt:        observedAt,
		}
		if artifact.IdentityKind == "mutable_tag" {
			input.ImmutableIdentity = ""
		}
		for _, issue := range artifact.MetadataIssues {
			if issue == issueInvalidOCITitle {
				snapshot.Warnings = append(snapshot.Warnings, issue)
				continue
			}
			if issue == issueInvalidOCICreated {
				input.ImageCreatedInvalid = true
				continue
			}
			input.IdentityIssues = append(input.IdentityIssues, issue)
		}

		var commit *Commit
		switch {
		case artifact.RevisionInvalid:
			input.OCIRevision = ""
			input.RevisionState = correlation.RevisionInvalid
		case artifact.OCIRevision == "":
			input.RevisionState = correlation.RevisionNotApplicable
		case identityConflict:
			input.RevisionState = correlation.RevisionQueryNotRun
		case !repositoryAvailable:
			input.RevisionState = correlation.RevisionSourceUnavailable
		default:
			resolved, resolveErr := r.Commits.ResolveCommit(ctx, artifact.OCIRevision)
			switch {
			case resolveErr == nil && strings.EqualFold(resolved.SHA, artifact.OCIRevision):
				resolved.SHA = strings.ToLower(resolved.SHA)
				commit = &resolved
				input.ResolvedCommitSHA = resolved.SHA
				input.CommitTime = &resolved.CommitTime
				input.RevisionState = correlation.RevisionResolved
			case resolveErr == nil || errors.Is(resolveErr, errs.ErrConflict):
				input.RevisionState = correlation.RevisionMismatched
			case errors.Is(resolveErr, errs.ErrNotFound):
				input.RevisionState = correlation.RevisionNotFound
			case errors.Is(resolveErr, errs.ErrUnavailable):
				input.RevisionState = correlation.RevisionSourceUnavailable
				snapshot.Warnings = append(snapshot.Warnings, "Git source became unavailable before OCI revision resolution completed")
			default:
				return RefreshResult{}, fmt.Errorf("resolve OCI revision: %w", resolveErr)
			}
		}

		if created := artifact.OCILabels[ociCreatedLabel]; created != "" {
			parsed, parseErr := time.Parse(time.RFC3339Nano, created)
			if parseErr != nil {
				input.ImageCreatedInvalid = true
			} else {
				parsed = parsed.UTC()
				input.ImageCreatedAt = &parsed
			}
		}
		result := correlation.EvaluateProvenance(input)
		snapshot.Warnings = append(snapshot.Warnings, result.Warnings...)
		sanitizedRuntime := observation
		sanitizedRuntime.ObservedAt = observedAt
		sanitizedRuntime.ImageID = artifact.ImageID
		sanitizedRuntime.RepoDigests = nil
		sanitizedRuntime.OCILabels = make(map[string]string, len(artifact.OCILabels))
		for key, value := range artifact.OCILabels {
			sanitizedRuntime.OCILabels[key] = value
		}
		snapshot.Items = append(snapshot.Items, SnapshotItem{
			ServiceLogicalKey:  runtimeIdentity.LogicalKey,
			ServiceDisplayName: runtimeIdentity.DisplayName,
			Environment:        environment,
			RuntimeIdentity:    runtimeIdentity,
			Runtime:            sanitizedRuntime,
			Artifact:           artifact,
			Commit:             commit,
			Correlation:        result,
		})
	}
	result, err := r.Store.SaveRuntimeSnapshot(ctx, snapshot)
	if err != nil {
		return RefreshResult{}, fmt.Errorf("save runtime snapshot: %w", err)
	}
	return result, nil
}
