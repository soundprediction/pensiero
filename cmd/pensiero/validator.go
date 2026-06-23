package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/soundprediction/pensiero/pkg/reasoning"
)

type GoldenCase struct {
	Claim            reasoning.Claim
	WantVerdictClass reasoning.Verdict
}

type GoldenSet interface {
	Cases(ctx context.Context) ([]GoldenCase, error)
}

type fileGoldenSet struct {
	cases []GoldenCase
}

func (g fileGoldenSet) Cases(context.Context) ([]GoldenCase, error) {
	out := make([]GoldenCase, len(g.cases))
	copy(out, g.cases)
	return out, nil
}

type snapshotValidator struct {
	Golden   GoldenSet
	Registry *reasoning.PredicateRegistry
}

func (v snapshotValidator) Validate(ctx context.Context, candidate *generation) error {
	if candidate == nil {
		return fmt.Errorf("validate snapshot: nil generation")
	}
	if candidate.pool == nil {
		return fmt.Errorf("validate snapshot %s: nil pool", candidate.path)
	}
	rows, err := candidate.pool.Query(ctx, "MATCH (n:Entity) RETURN count(n) AS count", nil)
	if err != nil {
		return fmt.Errorf("validate snapshot %s: structural probe: %w", candidate.path, err)
	}
	if len(rows) == 0 || numericCount(rows[0]["count"]) <= 0 {
		return fmt.Errorf("validate snapshot %s: structural probe found no Entity nodes", candidate.path)
	}
	if v.Golden == nil {
		return nil
	}
	if candidate.reasoner == nil {
		return fmt.Errorf("validate snapshot %s: nil reasoner", candidate.path)
	}
	reasoner := candidate.reasoner
	if v.Registry != nil {
		reasoner = reasoning.NewPredicateConstrained(reasoner, v.Registry)
	}
	cases, err := v.Golden.Cases(ctx)
	if err != nil {
		return fmt.Errorf("validate snapshot %s: load golden set: %w", candidate.path, err)
	}
	for i, golden := range cases {
		want := normalizeVerdictClass(golden.WantVerdictClass)
		if want == "" {
			return fmt.Errorf("validate snapshot %s: golden case %d has empty wantVerdictClass", candidate.path, i)
		}
		got, err := reasoner.Entails(ctx, golden.Claim)
		if err != nil {
			return fmt.Errorf("validate snapshot %s: golden case %d: %w", candidate.path, i, err)
		}
		if !verdictClassMatches(want, got.Verdict) {
			return fmt.Errorf("validate snapshot %s: golden case %d verdict=%s want=%s", candidate.path, i, got.Verdict, want)
		}
	}
	return nil
}

func loadGoldenSet(path string) (GoldenSet, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cases []GoldenCase
	if err := json.Unmarshal(data, &cases); err == nil {
		return fileGoldenSet{cases: cases}, nil
	}
	var wrapped struct {
		Cases []GoldenCase `json:"cases"`
	}
	if err := json.Unmarshal(data, &wrapped); err != nil {
		return nil, err
	}
	return fileGoldenSet{cases: wrapped.Cases}, nil
}

func normalizeVerdictClass(v reasoning.Verdict) string {
	s := strings.ToLower(strings.TrimSpace(string(v)))
	s = strings.ReplaceAll(s, "-", "_")
	s = strings.ReplaceAll(s, " ", "_")
	return s
}

func verdictClassMatches(want string, got reasoning.Verdict) bool {
	gotClass := normalizeVerdictClass(got)
	switch want {
	case "non_entailed", "not_entailed", "negative":
		return gotClass == string(reasoning.VerdictUnsupported) ||
			gotClass == string(reasoning.VerdictContradicted)
	default:
		return gotClass == want
	}
}

func numericCount(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case int:
		return int64(t)
	case int32:
		return int64(t)
	case uint64:
		return int64(t)
	case uint:
		return int64(t)
	case float64:
		return int64(t)
	case float32:
		return int64(t)
	default:
		return 0
	}
}

func (c *GoldenCase) UnmarshalJSON(data []byte) error {
	var aux struct {
		Claim                 reasoning.Claim   `json:"claim"`
		Subject               string            `json:"subject"`
		Predicate             string            `json:"predicate"`
		Object                string            `json:"object"`
		WantVerdictClass      reasoning.Verdict `json:"wantVerdictClass"`
		WantVerdictClassSnake reasoning.Verdict `json:"want_verdict_class"`
		ExpectedVerdict       reasoning.Verdict `json:"expectedVerdict"`
		ExpectedVerdictSnake  reasoning.Verdict `json:"expected_verdict"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	claim := aux.Claim
	if claim.Subject == "" && claim.Predicate == "" && claim.Object == "" {
		claim = reasoning.Claim{
			Subject:   aux.Subject,
			Predicate: aux.Predicate,
			Object:    aux.Object,
		}
	}
	want := firstVerdict(aux.WantVerdictClass, aux.WantVerdictClassSnake, aux.ExpectedVerdict, aux.ExpectedVerdictSnake)
	c.Claim = claim
	c.WantVerdictClass = want
	return nil
}

func (c GoldenCase) MarshalJSON() ([]byte, error) {
	type goldenCaseJSON struct {
		Claim            reasoning.Claim   `json:"claim"`
		WantVerdictClass reasoning.Verdict `json:"wantVerdictClass"`
	}
	return json.Marshal(goldenCaseJSON{
		Claim:            c.Claim,
		WantVerdictClass: c.WantVerdictClass,
	})
}

func firstVerdict(values ...reasoning.Verdict) reasoning.Verdict {
	for _, value := range values {
		if strings.TrimSpace(string(value)) != "" {
			return value
		}
	}
	return ""
}
