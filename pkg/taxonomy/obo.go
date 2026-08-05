// Package taxonomy derives the direction of hierarchy edges from published
// ontology DAGs.
//
// Why this exists: the ingested corpus stores taxonomy edges (IS_PARENT_OF and
// friends) with NO usable direction. Measured on the deployed thyroid graph, of
// 5,029 IS_PARENT_OF edges 3,640 (72.4%) are stored in BOTH orientations, and of
// the unidirectional remainder that an independent signal can adjudicate, 72 are
// stored parent->child and 67 backwards — 51.8%, which is a coin flip. Instances
// like "AMP-thymidine kinase activity IS_PARENT_OF kinase activity" are flatly
// inverted.
//
// So there is nothing to "repair": a stored orientation is not evidence, not a
// tie-breaker, and not a default. Direction must be DERIVED from a source that
// actually knows it.
//
// The edges themselves are sound. Where Gene Ontology can adjudicate a pair it
// confirms a genuine ancestor relation in 257 of 264 cases (97.3%), so the
// information is recoverable rather than absent. That is what this package does:
// match both endpoints to ontology terms and read the direction off the
// ontology's own is_a DAG.
//
// Coverage, not precision, is the binding constraint. GO matches only 375 of
// 3,147 distinct entity names (11.9%) and orients 8.0% of pairs. Loading further
// ontologies (MONDO for diseases, HPO for phenotypes, CHEBI for chemicals) is how
// coverage grows; each brings the same authoritative direction.
package taxonomy

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

// Ontology is a name-indexed ontology hierarchy supporting ancestor queries.
//
// It is read-only after Load and safe for concurrent use.
type Ontology struct {
	// nameToID maps a normalized term name or EXACT synonym to its term ID.
	// First writer wins: an ontology's primary names are emitted before its
	// synonyms for a given term, and an earlier-loaded ontology outranks a later
	// one, so a name never silently changes meaning as more files are loaded.
	nameToID map[string]string
	// parents maps a term ID to its direct is_a parents.
	parents map[string][]string
	// sources records which files contributed, for the run report.
	sources []string
}

// oboSynonym captures the quoted synonym text and its scope. Only EXACT synonyms
// are indexed: NARROW/BROAD synonyms name a DIFFERENT concept than the term, so
// indexing them would attach an entity to the wrong node in the hierarchy and
// yield a confidently wrong parent — the precise failure this package exists to
// prevent.
var oboSynonym = regexp.MustCompile(`^synonym:\s+"((?:[^"\\]|\\.)*)"\s+([A-Z]+)`)

// NewOntology returns an empty ontology ready for Load.
func NewOntology() *Ontology {
	return &Ontology{
		nameToID: make(map[string]string),
		parents:  make(map[string][]string),
	}
}

// LoadOBOFile indexes an OBO-format ontology file.
//
// Ontologies are loaded in priority order: when two files name the same concept,
// the first one loaded wins. Load the most authoritative ontology for a domain
// first.
func (o *Ontology) LoadOBOFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open ontology %s: %w", path, err)
	}
	defer f.Close()
	if err := o.LoadOBO(f); err != nil {
		return fmt.Errorf("parse ontology %s: %w", path, err)
	}
	o.sources = append(o.sources, path)
	return nil
}

// LoadOBO indexes an OBO-format stream.
//
// Obsolete terms are skipped entirely: an obsolete term's name is frequently
// reused or redirected, and its is_a edges are stripped by the ontology, so
// indexing one would introduce an entity that can never be oriented while
// shadowing the live term of the same name.
func (o *Ontology) LoadOBO(r io.Reader) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	// A stanza is buffered before being committed, because "is_obsolete: true"
	// can appear after the name and is_a lines it invalidates.
	var (
		inTerm   bool
		id       string
		obsolete bool
		names    []string
		isA      []string
	)

	commit := func() {
		if !inTerm || id == "" || obsolete {
			return
		}
		for _, n := range names {
			key := NormalizeName(n)
			if key == "" {
				continue
			}
			if _, exists := o.nameToID[key]; !exists {
				o.nameToID[key] = id
			}
		}
		if len(isA) > 0 {
			o.parents[id] = append(o.parents[id], isA...)
		}
	}

	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			commit()
			inTerm = trimmed == "[Term]"
			id, obsolete, names, isA = "", false, nil, nil
			continue
		}
		if !inTerm {
			continue
		}

		switch {
		case strings.HasPrefix(trimmed, "id:"):
			if id == "" {
				id = strings.TrimSpace(trimmed[len("id:"):])
			}
		case strings.HasPrefix(trimmed, "name:"):
			names = append(names, strings.TrimSpace(trimmed[len("name:"):]))
		case strings.HasPrefix(trimmed, "is_obsolete:"):
			obsolete = strings.EqualFold(strings.TrimSpace(trimmed[len("is_obsolete:"):]), "true")
		case strings.HasPrefix(trimmed, "is_a:"):
			if p := oboRef(trimmed[len("is_a:"):]); p != "" {
				isA = append(isA, p)
			}
		case strings.HasPrefix(trimmed, "synonym:"):
			if m := oboSynonym.FindStringSubmatch(trimmed); m != nil && m[2] == "EXACT" {
				names = append(names, strings.ReplaceAll(m[1], `\"`, `"`))
			}
		}
	}
	commit()
	return sc.Err()
}

// oboRef extracts the identifier from an OBO reference, discarding the trailing
// "! human readable label" comment and any modifier braces.
func oboRef(s string) string {
	if i := strings.IndexByte(s, '!'); i >= 0 {
		s = s[:i]
	}
	if i := strings.IndexByte(s, '{'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// NormalizeName is the single normalization used for BOTH ontology terms and
// graph entity names. Matching is deliberately exact-after-normalization: a
// fuzzy ontology match would attach an entity to the wrong hierarchy node and
// produce a confidently wrong parent, which is worse than no match at all.
func NormalizeName(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), " ")
}

// Sources returns the ontology files loaded, in load order, for the run report.
// A derived graph is only reproducible if the ontology versions are recorded.
func (o *Ontology) Sources() []string {
	out := make([]string, len(o.sources))
	copy(out, o.sources)
	return out
}

// Size reports the indexed name and term counts, for the run report.
func (o *Ontology) Size() (names, terms int) {
	return len(o.nameToID), len(o.parents)
}

// lookup resolves an entity name to a term ID.
func (o *Ontology) lookup(name string) (string, bool) {
	id, ok := o.nameToID[NormalizeName(name)]
	return id, ok
}

// isAncestor reports whether anc is a proper ancestor of id.
//
// Walks the DAG breadth-first with a visited set. Real ontologies are acyclic,
// but a malformed file must not hang the builder, so a cycle is bounded by the
// visited set rather than assumed away.
func (o *Ontology) isAncestor(anc, id string) bool {
	if anc == id {
		return false
	}
	seen := map[string]bool{id: true}
	queue := append([]string(nil), o.parents[id]...)
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur == anc {
			return true
		}
		if seen[cur] {
			continue
		}
		seen[cur] = true
		queue = append(queue, o.parents[cur]...)
	}
	return false
}
