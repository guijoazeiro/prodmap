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
}

type explainResult struct {
	TargetID      string                   `json:"target_id"`
	TargetType    string                   `json:"target_type"`
	Conclusion    string                   `json:"conclusion"`
	RelationType  correlation.RelationType `json:"relation_type"`
	Confidence    correlation.Level        `json:"confidence"`
	Score         float64                  `json:"score"`
	Algorithm     string                   `json:"algorithm"`
	Entities      map[string]string        `json:"entities"`
	Supporting    []evidenceOutput         `json:"supporting_evidence"`
	Contradicting []evidenceOutput         `json:"contradicting_evidence"`
	Neutral       []evidenceOutput         `json:"neutral_evidence"`
	Missing       []string                 `json:"missing"`
	Sources       []string                 `json:"sources"`
	Freshness     string                   `json:"freshness"`
	Limitations   []string                 `json:"limitations"`
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
	result := makeExplainResult(explanation)
	if *common.jsonOutput {
		return WriteSuccess(a.Stdout, "explain", a.Now(), result, explanation.Warnings, nil)
	}
	fmt.Fprintf(a.Stdout, "Conclusion: %s\nRelation: %s\nConfidence: %s (%.2f)\nAlgorithm: %s\nFreshness: %s\n",
		result.Conclusion, result.RelationType, result.Confidence, result.Score, result.Algorithm, result.Freshness)
	printEvidence := func(label string, values []evidenceOutput) {
		fmt.Fprintf(a.Stdout, "%s:\n", label)
		if len(values) == 0 {
			fmt.Fprintln(a.Stdout, "  none")
			return
		}
		for _, evidence := range values {
			fmt.Fprintf(a.Stdout, "  %s [%s/%s %.2f] %s\n", evidence.ID, evidence.Source, evidence.Kind, evidence.Strength, evidence.Claim)
		}
	}
	printEvidence("Supporting evidence", result.Supporting)
	printEvidence("Contradicting evidence", result.Contradicting)
	printEvidence("Neutral evidence", result.Neutral)
	if len(result.Missing) > 0 {
		fmt.Fprintf(a.Stdout, "Missing: %v\n", result.Missing)
	}
	for _, limitation := range result.Limitations {
		fmt.Fprintf(a.Stdout, "Limitation: %s\n", limitation)
	}
	for _, warning := range explanation.Warnings {
		fmt.Fprintf(a.Stdout, "Warning: %s\n", warning)
	}
	return nil
}

func makeExplainResult(value inventory.Explanation) explainResult {
	return explainResult{
		TargetID: value.TargetID, TargetType: value.TargetType, Conclusion: value.Conclusion,
		RelationType: value.RelationType, Confidence: value.Confidence, Score: value.Score, Algorithm: value.Algorithm,
		Entities: value.Entities, Supporting: evidenceOutputs(value.Supporting), Contradicting: evidenceOutputs(value.Contradicting),
		Neutral: evidenceOutputs(value.Neutral), Missing: nonNilStrings(value.Missing), Sources: nonNilStrings(value.Sources),
		Freshness: value.ObservedAt.UTC().Format(time.RFC3339Nano), Limitations: nonNilStrings(value.Limitations),
	}
}

func evidenceOutputs(values []inventory.PersistedEvidence) []evidenceOutput {
	result := make([]evidenceOutput, 0, len(values))
	for _, value := range values {
		result = append(result, evidenceOutput{
			ID: value.ID, Kind: value.Kind, Subject: value.Subject, Claim: value.Claim, Polarity: value.Polarity,
			Strength: value.Strength, Source: value.Source, ObservedAt: value.ObservedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return result
}
