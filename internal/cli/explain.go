package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/guijoazeiro/prodmap/internal/correlation"
	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/inventory"
)

type evidenceOutput struct {
	ID         string                   `json:"id"`
	Kind       correlation.EvidenceKind `json:"kind"`
	Subject    string                   `json:"subject"`
	Claim      string                   `json:"claim"`
	Polarity   correlation.Polarity     `json:"polarity"`
	Strength   float64                  `json:"strength"`
	Source     string                   `json:"source"`
	ObservedAt string                   `json:"observed_at"`
	Details    map[string]string        `json:"details"`
}

type evidenceCountsOutput struct {
	Supporting    int `json:"supporting"`
	Contradicting int `json:"contradicting"`
	Neutral       int `json:"neutral"`
}

type scoreComponentOutput struct {
	Name        string  `json:"name"`
	Value       float64 `json:"value"`
	Description string  `json:"description"`
}

type hardCapOutput struct {
	Name     string            `json:"name"`
	MaxLevel correlation.Level `json:"max_level"`
	Reason   string            `json:"reason"`
}

type resolutionAttemptOutput struct {
	State      correlation.RevisionResolutionState `json:"state"`
	Source     string                              `json:"source"`
	Revision   *string                             `json:"revision"`
	ObservedAt string                              `json:"observed_at"`
}

type sourceFreshnessOutput struct {
	Source     string `json:"source"`
	ObservedAt string `json:"observed_at"`
}

type explainSummaryResult struct {
	TargetID       string                   `json:"target_id"`
	TargetType     string                   `json:"target_type"`
	Conclusion     string                   `json:"conclusion"`
	RelationType   correlation.RelationType `json:"relation_type"`
	Confidence     correlation.Level        `json:"confidence"`
	Entities       map[string]string        `json:"entities"`
	Limitations    []string                 `json:"limitations"`
	EvidenceCounts evidenceCountsOutput     `json:"evidence_counts"`
}

type explainFullResult struct {
	TargetID           string                    `json:"target_id"`
	TargetType         string                    `json:"target_type"`
	Conclusion         string                    `json:"conclusion"`
	RelationType       correlation.RelationType  `json:"relation_type"`
	Confidence         correlation.Level         `json:"confidence"`
	Score              float64                   `json:"score"`
	Algorithm          string                    `json:"algorithm"`
	Entities           map[string]string         `json:"entities"`
	Supporting         []evidenceOutput          `json:"supporting_evidence"`
	Contradicting      []evidenceOutput          `json:"contradicting_evidence"`
	Neutral            []evidenceOutput          `json:"neutral_evidence"`
	Missing            []string                  `json:"missing"`
	Sources            []string                  `json:"sources"`
	Freshness          string                    `json:"freshness"`
	Limitations        []string                  `json:"limitations"`
	ScoreComponents    []scoreComponentOutput    `json:"score_components"`
	HardCaps           []hardCapOutput           `json:"hard_caps"`
	ResolutionAttempts []resolutionAttemptOutput `json:"resolution_attempts"`
	SourceFreshness    []sourceFreshnessOutput   `json:"source_freshness"`
}

func (a *App) runExplain(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("explain", flag.ContinueOnError)
	flags.SetOutput(a.Stderr)
	common := addInventoryFlags(flags)
	detail := flags.String("detail", "summary", "summary or full")
	atValue := flags.String("at", "", "resolve against a runtime timestamp in RFC3339")
	target := ""
	flagArgs := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		target = args[0]
		flagArgs = args[1:]
	}
	if err := flags.Parse(flagArgs); err != nil {
		return fmt.Errorf("parse explain flags: %w: %v", errs.ErrInvalid, err)
	}
	if target == "" && flags.NArg() == 1 {
		target = flags.Arg(0)
	} else if flags.NArg() != 0 {
		return fmt.Errorf("explain requires exactly one target: %w", errs.ErrInvalid)
	}
	if target == "" {
		return fmt.Errorf("explain requires exactly one target: %w", errs.ErrInvalid)
	}
	if *detail != "summary" && *detail != "full" {
		return fmt.Errorf("--detail must be summary or full: %w", errs.ErrInvalid)
	}
	_, store, err := a.openInventory(ctx, common)
	if err != nil {
		return err
	}
	defer store.Close()
	var explanation inventory.Explanation
	if *atValue == "" {
		explanation, err = store.Explain(ctx, target)
	} else {
		at, parseErr := parseRFC3339(*atValue, "--at")
		if parseErr != nil {
			return parseErr
		}
		explanation, err = store.ExplainAt(ctx, target, at)
	}
	if err != nil {
		return fmt.Errorf("explain target: %w", err)
	}

	summary := makeExplainSummaryResult(explanation)
	if *common.jsonOutput {
		if *detail == "full" {
			return WriteSuccess(a.Stdout, "explain", a.Now(), makeExplainFullResult(explanation), explanation.Warnings, nil)
		}
		return WriteSuccess(a.Stdout, "explain", a.Now(), summary, explanation.Warnings, nil)
	}

	fmt.Fprintf(a.Stdout, "Conclusion: %s\nRelation: %s\nConfidence: %s\n", summary.Conclusion, summary.RelationType, summary.Confidence)
	if *detail == "full" {
		full := makeExplainFullResult(explanation)
		fmt.Fprintf(a.Stdout, "Score: %.2f\nAlgorithm: %s\nFreshness: %s\n", full.Score, full.Algorithm, full.Freshness)
		printEvidenceGroup(a, "Supporting evidence", full.Supporting)
		printEvidenceGroup(a, "Contradicting evidence", full.Contradicting)
		printEvidenceGroup(a, "Neutral evidence", full.Neutral)
		for _, component := range full.ScoreComponents {
			fmt.Fprintf(a.Stdout, "Score component: %s=%.2f — %s\n", component.Name, component.Value, component.Description)
		}
		for _, cap := range full.HardCaps {
			fmt.Fprintf(a.Stdout, "Hard cap: %s max=%s — %s\n", cap.Name, cap.MaxLevel, cap.Reason)
		}
		for _, attempt := range full.ResolutionAttempts {
			fmt.Fprintf(a.Stdout, "Resolution attempt: source=%s state=%s revision=%s observed=%s\n", attempt.Source, attempt.State, displayOptional(attempt.Revision), attempt.ObservedAt)
		}
		for _, freshness := range full.SourceFreshness {
			fmt.Fprintf(a.Stdout, "Source freshness: %s observed=%s\n", freshness.Source, freshness.ObservedAt)
		}
	} else {
		counts := summary.EvidenceCounts
		fmt.Fprintf(a.Stdout, "Evidence: supporting=%d contradicting=%d neutral=%d\n", counts.Supporting, counts.Contradicting, counts.Neutral)
	}
	if *detail == "full" && len(explanation.Missing) > 0 {
		fmt.Fprintf(a.Stdout, "Missing: %v\n", explanation.Missing)
	}
	for _, limitation := range summary.Limitations {
		fmt.Fprintf(a.Stdout, "Limitation: %s\n", limitation)
	}
	for _, warning := range explanation.Warnings {
		fmt.Fprintf(a.Stderr, "warning: %s\n", warning)
	}
	return nil
}

func printEvidenceGroup(a *App, label string, values []evidenceOutput) {
	fmt.Fprintf(a.Stdout, "%s:\n", label)
	if len(values) == 0 {
		fmt.Fprintln(a.Stdout, "  none")
		return
	}
	for _, evidence := range values {
		fmt.Fprintf(a.Stdout, "  %s [%s/%s %.2f] %s\n", evidence.ID, evidence.Source, evidence.Kind, evidence.Strength, evidence.Claim)
	}
}

func makeExplainSummaryResult(value inventory.Explanation) explainSummaryResult {
	entities := value.Entities
	if entities == nil {
		entities = map[string]string{}
	}
	return explainSummaryResult{
		TargetID: value.TargetID, TargetType: value.TargetType, Conclusion: value.Conclusion,
		RelationType: value.RelationType, Confidence: value.Confidence, Entities: entities, Limitations: nonNilStrings(value.Limitations),
		EvidenceCounts: evidenceCountsOutput{Supporting: len(value.Supporting), Contradicting: len(value.Contradicting), Neutral: len(value.Neutral)},
	}
}

func makeExplainFullResult(value inventory.Explanation) explainFullResult {
	entities := value.Entities
	if entities == nil {
		entities = map[string]string{}
	}
	result := explainFullResult{
		TargetID: value.TargetID, TargetType: value.TargetType, Conclusion: value.Conclusion,
		RelationType: value.RelationType, Confidence: value.Confidence, Score: value.Score, Algorithm: value.Algorithm,
		Entities: entities, Supporting: evidenceOutputs(value.Supporting), Contradicting: evidenceOutputs(value.Contradicting), Neutral: evidenceOutputs(value.Neutral),
		Missing: nonNilStrings(value.Missing), Sources: nonNilStrings(value.Sources), Freshness: value.ObservedAt.UTC().Format(time.RFC3339Nano), Limitations: nonNilStrings(value.Limitations),
		ScoreComponents: make([]scoreComponentOutput, 0, len(value.ScoreComponents)), HardCaps: make([]hardCapOutput, 0, len(value.HardCaps)),
		ResolutionAttempts: make([]resolutionAttemptOutput, 0, len(value.ResolutionAttempts)), SourceFreshness: make([]sourceFreshnessOutput, 0, len(value.SourceFreshness)),
	}
	for _, component := range value.ScoreComponents {
		result.ScoreComponents = append(result.ScoreComponents, scoreComponentOutput{Name: component.Name, Value: component.Value, Description: component.Description})
	}
	for _, cap := range value.HardCaps {
		result.HardCaps = append(result.HardCaps, hardCapOutput{Name: cap.Name, MaxLevel: cap.MaxLevel, Reason: cap.Reason})
	}
	for _, attempt := range value.ResolutionAttempts {
		result.ResolutionAttempts = append(result.ResolutionAttempts, resolutionAttemptOutput{
			State: attempt.State, Source: attempt.Source, Revision: pointer(attempt.Revision), ObservedAt: attempt.ObservedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	for _, freshness := range value.SourceFreshness {
		result.SourceFreshness = append(result.SourceFreshness, sourceFreshnessOutput{Source: freshness.Source, ObservedAt: freshness.ObservedAt.UTC().Format(time.RFC3339Nano)})
	}
	return result
}

func evidenceOutputs(values []inventory.PersistedEvidence) []evidenceOutput {
	result := make([]evidenceOutput, 0, len(values))
	for _, value := range values {
		details := value.Details
		if details == nil {
			details = map[string]string{}
		}
		result = append(result, evidenceOutput{
			ID: value.ID, Kind: value.Kind, Subject: value.Subject, Claim: value.Claim, Polarity: value.Polarity,
			Strength: value.Strength, Source: value.Source, ObservedAt: value.ObservedAt.UTC().Format(time.RFC3339Nano), Details: details,
		})
	}
	return result
}
