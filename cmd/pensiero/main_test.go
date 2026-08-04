package main

import "testing"

// Direction derivation is opt-in: with no --ontology the build keeps its
// historical behaviour rather than silently changing every existing pipeline.
func TestBuildDirectionSourceNilWithoutOntology(t *testing.T) {
	src, err := buildDirectionSource(nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if src != nil {
		t.Error("expected nil DirectionSource when no ontology is supplied")
	}
}

// The lexical signal is only trustworthy once measured against an
// ontology-adjudicable subset, so enabling it alone must fail loudly rather
// than quietly become the sole source of direction.
func TestBuildDirectionSourceRejectsLexicalWithoutOntology(t *testing.T) {
	if _, err := buildDirectionSource(nil, true); err == nil {
		t.Fatal("expected an error when --ontology-lexical is set without --ontology")
	}
}

// A named ontology that cannot be read must be an error: continuing would build
// a graph with far less direction evidence than asked for, and it would look
// like it succeeded.
func TestBuildDirectionSourceFailsOnUnreadableOntology(t *testing.T) {
	if _, err := buildDirectionSource([]string{"/nonexistent/does-not-exist.obo"}, false); err == nil {
		t.Fatal("expected an error for an unreadable ontology file")
	}
}
