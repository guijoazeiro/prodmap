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
	Runtime     RuntimeSource
	Commits     CommitSource
	Store       SnapshotStore
}

func (r Refresher) Refresh(ctx context.Context) (RefreshResult, error) {
	if r.Runtime == nil || r.Store == nil {
		return RefreshResult{}, fmt.Errorf("%w: runtime source and snapshot store are required", errs.ErrInvalid)
	}
	refreshOperationID := strings.TrimSpace(r.OperationID)
	if refreshOperationID == "" {
		var err error
		refreshOperationID, err = identity.NewV7(time.Now().UTC())
		if err != nil {
			return RefreshResult{}, err
		}
	}
	batch, err := r.Runtime.InspectRuntime(ctx)
	if err != nil {
		return RefreshResult{}, fmt.Errorf("inspect Docker runtime: %w", err)
	}
	var repository Repository
	var repositoryErr error
	if r.Commits == nil {
		repositoryErr = fmt.Errorf("%w: Git commit source is not configured", errs.ErrUnavailable)
	} else {
		repository, repositoryErr = r.Commits.Repository(ctx)
	}
	snapshot := Snapshot{OperationID: refreshOperationID, ObservedAt: batch.ObservedAt, Rejected: batch.Rejected, Warnings: append([]string{}, batch.Warnings...)}
	if repositoryErr == nil {
		snapshot.Repository = &repository
	} else if !errors.Is(repositoryErr, errs.ErrNotFound) && !errors.Is(repositoryErr, errs.ErrUnavailable) {
		return RefreshResult{}, fmt.Errorf("inspect Git repository: %w", repositoryErr)
	} else {
		snapshot.Warnings = append(snapshot.Warnings, repositoryErr.Error())
	}
	for _, observation := range batch.Observations {
		if err := ctx.Err(); err != nil {
			return RefreshResult{}, err
		}
		if snapshot.ObservedAt.IsZero() || observation.ObservedAt.After(snapshot.ObservedAt) {
			snapshot.ObservedAt = observation.ObservedAt
		}
		artifact, identityConflict := NormalizeArtifact(observation)
		input := correlation.ProvenanceInput{
			ImmutableIdentity: artifact.Identity,
			MutableAlias:      artifact.ObservedReference,
			OCIRevision:       artifact.OCIRevision,
			IdentityConflict:  identityConflict,
			ObservedAt:        observation.ObservedAt,
		}
		if artifact.IdentityKind == "mutable_tag" {
			input.ImmutableIdentity = ""
		}
		var commit *Commit
		if artifact.OCIRevision != "" && snapshot.Repository != nil && !identityConflict {
			if !fullSHA.MatchString(strings.TrimSpace(artifact.OCIRevision)) {
				input.RevisionInvalid = true
			} else {
				resolved, resolveErr := r.Commits.ResolveCommit(ctx, artifact.OCIRevision)
				if resolveErr == nil && strings.EqualFold(resolved.SHA, artifact.OCIRevision) {
					commit = &resolved
					input.ResolvedCommitSHA = resolved.SHA
					input.CommitTime = &resolved.CommitTime
				} else if resolveErr == nil || errors.Is(resolveErr, errs.ErrConflict) {
					input.RevisionMismatch = true
				} else if errors.Is(resolveErr, errs.ErrNotFound) {
					input.RevisionNotFound = true
				} else {
					return RefreshResult{}, fmt.Errorf("resolve OCI revision: %w", resolveErr)
				}
			}
		}
		if created := artifact.OCILabels["org.opencontainers.image.created"]; created != "" {
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
		snapshot.Items = append(snapshot.Items, SnapshotItem{
			ServiceLogicalKey: NormalizeServiceKey(observation), ServiceDisplayName: NormalizeServiceKey(observation), Environment: "default",
			Runtime: observation, Artifact: artifact, Commit: commit, Correlation: result,
		})
	}
	result, err := r.Store.SaveRuntimeSnapshot(ctx, snapshot)
	if err != nil {
		return RefreshResult{}, fmt.Errorf("save runtime snapshot: %w", err)
	}
	return result, nil
}
