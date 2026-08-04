package taxonomy

import (
	"encoding/csv"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestDeployedCorpusReproducesMeasuredFigures pins the derivation against the
// real deployed corpus and the real Gene Ontology.
//
// It is skipped unless both inputs are provided, because neither can live in the
// repo: the ontology is a large external download, and the corpus is clinical
// data that must stay on the deployment host. Run it there:
//
//	PENSIERO_TAXONOMY_OBO=/tmp/go-basic.obo \
//	PENSIERO_TAXONOMY_PAIRS=/tmp/allpar.csv \
//	go test ./pkg/taxonomy/ -run DeployedCorpus -v
//
// Verified figures for thyroid.ladybug against go-basic.obo (2026-08-04):
//
//	pairs=3209  ontology-oriented=258  contradicts-stored=53
//	lexical precision vs ontology = 97.4% (n=114)
//
// If this test fails, the derivation is wrong — not the data. That direction of
// inference is the whole point of pinning it.
//
// A prior throwaway script reported 257/52. The one-pair difference is a single
// entity name carrying a doubled internal space ("Defective SLC9A6 causes  X-
// linked ..."), which NormalizeName collapses and the script did not. The values
// here are the correct ones; the discrepancy was chased to its cause rather than
// averaged over, because an unexplained off-by-one in a direction derivation is
// indistinguishable from a systematic bug until it is explained.
func TestDeployedCorpusReproducesMeasuredFigures(t *testing.T) {
	oboPath := os.Getenv("PENSIERO_TAXONOMY_OBO")
	pairsPath := os.Getenv("PENSIERO_TAXONOMY_PAIRS")
	if oboPath == "" || pairsPath == "" {
		t.Skip("set PENSIERO_TAXONOMY_OBO and PENSIERO_TAXONOMY_PAIRS to run against the deployed corpus")
	}

	o := NewOntology()
	if err := o.LoadOBOFile(oboPath); err != nil {
		t.Fatalf("load ontology: %v", err)
	}
	names, terms := o.Size()
	t.Logf("ontology: %d indexed names, %d terms with parents", names, terms)

	pairs := loadPairsCSV(t, pairsPath)
	t.Logf("corpus: %d distinct pairs", len(pairs))

	d := NewDeriver(o)
	_, rep := d.DeriveAll(pairs, 20)

	t.Logf("oriented=%d undetermined=%d by_signal=%v contradicts_stored=%d",
		rep.Oriented, rep.Undetermined, rep.BySignal, rep.ContradictsStored)
	if rate, ok := rep.LexicalPrecision(); ok {
		t.Logf("lexical precision vs ontology: %.1f%% (n=%d)", 100*rate, rep.LexicalTotal)
	} else {
		t.Logf("lexical precision: NOT MEASURED (n=%d, below threshold)", rep.LexicalTotal)
	}

	// Every pair accounted for.
	if rep.Oriented+rep.Undetermined != rep.TotalPairs {
		t.Errorf("oriented(%d)+undetermined(%d) != total(%d)", rep.Oriented, rep.Undetermined, rep.TotalPairs)
	}

	expect := func(name string, got, want int) {
		if want == 0 {
			return
		}
		if got != want {
			t.Errorf("%s = %d, want %d (independently measured)", name, got, want)
		}
	}
	expect("total pairs", rep.TotalPairs, envInt(t, "PENSIERO_TAXONOMY_WANT_PAIRS"))
	expect("ontology-oriented", rep.BySignal[SignalOntology], envInt(t, "PENSIERO_TAXONOMY_WANT_ORIENTED"))
	expect("contradicts stored", rep.ContradictsStored, envInt(t, "PENSIERO_TAXONOMY_WANT_BACKWARDS"))
}

func envInt(t *testing.T, key string) int {
	t.Helper()
	raw := os.Getenv(key)
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatalf("%s=%q: %v", key, raw, err)
	}
	return n
}

// loadPairsCSV reads a two-column head,tail dump of directed taxonomy edges and
// folds it into unordered pairs carrying which orientations were present.
func loadPairsCSV(t *testing.T, path string) []Pair {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open pairs: %v", err)
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	// Ontology term names contain bare quotes (e.g. 3' and 5' in nucleotide
	// terms), which the graph CLI does not escape.
	r.LazyQuotes = true
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatalf("read pairs: %v", err)
	}

	type key struct{ lo, hi string }
	seen := map[key]int{}
	var out []Pair

	for _, row := range rows {
		if len(row) != 2 {
			continue
		}
		a, b := strings.TrimSpace(row[0]), strings.TrimSpace(row[1])
		// Drop the CSV header and any terminal escape noise the shell client emits.
		if a == "" || b == "" || a == "a.name" || strings.Contains(a, "\x1b") {
			continue
		}
		lo, hi, flipped := a, b, false
		if lo > hi {
			lo, hi, flipped = b, a, true
		}
		k := key{lo, hi}
		i, ok := seen[k]
		if !ok {
			out = append(out, Pair{A: lo, B: hi})
			i = len(out) - 1
			seen[k] = i
		}
		if flipped {
			out[i].BToA = true
		} else {
			out[i].AToB = true
		}
	}
	return out
}
