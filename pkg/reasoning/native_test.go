package reasoning

import "testing"

// The reasoning extension emits a proof as a JSON array of steps; parseProofJSON
// must decode that into a Proof (deriving Source/Target/RuleClass/Hops) so callers
// receive a populated proof rather than a silently-empty one.
func TestParseProofArrayForm(t *testing.T) {
	s := `[{"edge_id":"gg-1","rule":"composition","predicate":"is_parent_of","source":"A","target":"B","confidence":0.8},` +
		`{"edge_id":"x","rule":"composition","predicate":"has_phenotype","source":"B","target":"C","confidence":0.8}]`
	p, ok := parseProofJSON(s)
	if !ok {
		t.Fatal("expected ok")
	}
	if len(p.Steps) != 2 {
		t.Fatalf("steps=%d", len(p.Steps))
	}
	if p.Source != "A" || p.Target != "C" {
		t.Fatalf("src=%q tgt=%q", p.Source, p.Target)
	}
	if p.RuleClass != "composition" || p.Hops != 2 {
		t.Fatalf("ruleClass=%q hops=%d", p.RuleClass, p.Hops)
	}
	if p.Steps[1].Predicate != "has_phenotype" {
		t.Fatalf("pred=%q", p.Steps[1].Predicate)
	}
}

// Object form (other backends) and empty/null inputs are also handled.
func TestParseProofObjectAndEmpty(t *testing.T) {
	obj := `{"source":"A","target":"C","rule_class":"composition","steps":[{"predicate":"p","source":"A","target":"C"}]}`
	if p, ok := parseProofJSON(obj); !ok || p.Source != "A" || len(p.Steps) != 1 {
		t.Fatalf("object form: ok=%v p=%+v", ok, p)
	}
	for _, s := range []string{"", "null", "[]", "  "} {
		if _, ok := parseProofJSON(s); ok {
			t.Fatalf("expected !ok for %q", s)
		}
	}
}
