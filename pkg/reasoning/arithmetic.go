package reasoning

import (
	"fmt"
	"math"
	"strings"
)

// Arithmetic / computational primitives — the third primitive category, alongside
// the general LOGICAL relational primitives (predicates.go) and the DOMAIN
// primitives (predicate_primitives.go). These operate on numeric VALUES rather
// than the graph, so the reasoner can actually *compute*: evaluate clinical
// formulas, compare measurements to thresholds/ranges, and — crucially — bridge
// quantities into symbolic findings (e.g. "potassium HIGH") that then drive the
// relational graph reasoning. They are evaluated inline during reasoning, not
// traversed.

// Quantity is a numeric value with an optional unit.
type Quantity struct {
	Unit  string
	Value float64
}

func Q(v float64, unit string) Quantity { return Quantity{Value: v, Unit: unit} }

// --- arithmetic primitives ---------------------------------------------------

// BinOp is a named binary arithmetic primitive.
type BinOp string

const (
	OpAdd BinOp = "add"
	OpSub BinOp = "sub"
	OpMul BinOp = "mul"
	OpDiv BinOp = "div"
	OpMin BinOp = "min"
	OpMax BinOp = "max"
	OpPow BinOp = "pow"
)

// Apply evaluates a binary arithmetic primitive on raw values.
func (op BinOp) Apply(a, b float64) (float64, error) {
	switch op {
	case OpAdd:
		return a + b, nil
	case OpSub:
		return a - b, nil
	case OpMul:
		return a * b, nil
	case OpDiv:
		if b == 0 {
			return 0, fmt.Errorf("division by zero")
		}
		return a / b, nil
	case OpMin:
		return math.Min(a, b), nil
	case OpMax:
		return math.Max(a, b), nil
	case OpPow:
		return math.Pow(a, b), nil
	}
	return 0, fmt.Errorf("unknown arithmetic op %q", op)
}

// UnOp is a named unary arithmetic primitive.
type UnOp string

const (
	OpNeg   UnOp = "neg"
	OpAbs   UnOp = "abs"
	OpRound UnOp = "round"
	OpFloor UnOp = "floor"
	OpCeil  UnOp = "ceil"
	OpSqrt  UnOp = "sqrt"
	OpLn    UnOp = "ln"
)

func (op UnOp) Apply(a float64) (float64, error) {
	switch op {
	case OpNeg:
		return -a, nil
	case OpAbs:
		return math.Abs(a), nil
	case OpRound:
		return math.Round(a), nil
	case OpFloor:
		return math.Floor(a), nil
	case OpCeil:
		return math.Ceil(a), nil
	case OpSqrt:
		return math.Sqrt(a), nil
	case OpLn:
		return math.Log(a), nil
	}
	return 0, fmt.Errorf("unknown arithmetic op %q", op)
}

// --- comparison primitives ---------------------------------------------------

// CmpOp is a named comparison primitive.
type CmpOp string

const (
	CmpLt CmpOp = "lt"
	CmpLe CmpOp = "le"
	CmpEq CmpOp = "eq"
	CmpNe CmpOp = "ne"
	CmpGe CmpOp = "ge"
	CmpGt CmpOp = "gt"
)

// Test applies a comparison primitive (eq/ne use an epsilon for float safety).
func (op CmpOp) Test(a, b float64) bool {
	const eps = 1e-9
	switch op {
	case CmpLt:
		return a < b
	case CmpLe:
		return a <= b
	case CmpEq:
		return math.Abs(a-b) <= eps
	case CmpNe:
		return math.Abs(a-b) > eps
	case CmpGe:
		return a >= b
	case CmpGt:
		return a > b
	}
	return false
}

// --- quantity → symbolic finding bridge --------------------------------------

// RefRange is a reference interval for a measured quantity.
type RefRange struct {
	Unit      string
	Low, High float64
}

// Classification is the qualitative result of comparing a quantity to a range.
type Classification string

const (
	ClassLow    Classification = "low"
	ClassNormal Classification = "normal"
	ClassHigh   Classification = "high"
)

// Classify turns a measurement into a symbolic finding via the comparison
// primitives — the bridge from arithmetic to the relational reasoner. The returned
// Classification (and a finding label like "potassium high") can then be fed to the
// graph reasoning as a presenting finding.
func (r RefRange) Classify(q Quantity) Classification {
	switch {
	case CmpLt.Test(q.Value, r.Low):
		return ClassLow
	case CmpGt.Test(q.Value, r.High):
		return ClassHigh
	default:
		return ClassNormal
	}
}

// FindingLabel renders e.g. "potassium high" / "sodium normal" for the relational
// layer to consume as a symbolic finding.
func FindingLabel(analyte string, c Classification) string {
	return strings.ToLower(strings.TrimSpace(analyte)) + " " + string(c)
}

// --- derived clinical quantities (composed from the primitives) --------------

// Formula computes a named derived quantity from named numeric inputs. It is the
// general mechanism; the clinical formulas below are instances. Returns an error if
// a required input is missing.
type Formula struct {
	Compute func(in map[string]float64) (float64, error)
	Name    string
	Unit    string
	Inputs  []string
}

func need(in map[string]float64, keys ...string) error {
	for _, k := range keys {
		if _, ok := in[k]; !ok {
			return fmt.Errorf("missing input %q", k)
		}
	}
	return nil
}

// ClinicalFormulas are common derived quantities built from arithmetic primitives.
// (Examples — extend as needed; values are inputs in standard units.)
var ClinicalFormulas = map[string]Formula{
	"bmi": {Name: "bmi", Unit: "kg/m2", Inputs: []string{"weight_kg", "height_m"},
		Compute: func(in map[string]float64) (float64, error) {
			if err := need(in, "weight_kg", "height_m"); err != nil {
				return 0, err
			}
			return OpDiv.Apply(in["weight_kg"], in["height_m"]*in["height_m"])
		}},
	"anion_gap": {Name: "anion_gap", Unit: "mmol/L", Inputs: []string{"na", "cl", "hco3"},
		Compute: func(in map[string]float64) (float64, error) {
			if err := need(in, "na", "cl", "hco3"); err != nil {
				return 0, err
			}
			return in["na"] - (in["cl"] + in["hco3"]), nil
		}},
	"map": {Name: "map", Unit: "mmHg", Inputs: []string{"sbp", "dbp"}, // mean arterial pressure
		Compute: func(in map[string]float64) (float64, error) {
			if err := need(in, "sbp", "dbp"); err != nil {
				return 0, err
			}
			return (in["sbp"] + 2*in["dbp"]) / 3, nil
		}},
	"corrected_na": {Name: "corrected_na", Unit: "mmol/L", Inputs: []string{"na", "glucose_mgdl"},
		Compute: func(in map[string]float64) (float64, error) {
			if err := need(in, "na", "glucose_mgdl"); err != nil {
				return 0, err
			}
			return in["na"] + 0.016*(in["glucose_mgdl"]-100), nil
		}},
	"dose_by_weight": {Name: "dose_by_weight", Unit: "mg", Inputs: []string{"mg_per_kg", "weight_kg"},
		Compute: func(in map[string]float64) (float64, error) {
			if err := need(in, "mg_per_kg", "weight_kg"); err != nil {
				return 0, err
			}
			return OpMul.Apply(in["mg_per_kg"], in["weight_kg"])
		}},
}

// Evaluate runs a named clinical formula.
func EvaluateFormula(name string, in map[string]float64) (Quantity, error) {
	f, ok := ClinicalFormulas[strings.ToLower(name)]
	if !ok {
		return Quantity{}, fmt.Errorf("unknown formula %q", name)
	}
	v, err := f.Compute(in)
	if err != nil {
		return Quantity{}, err
	}
	return Quantity{Value: v, Unit: f.Unit}, nil
}
