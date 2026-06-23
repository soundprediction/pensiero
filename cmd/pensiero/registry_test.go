package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRegistryExtendsPredicatePack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	data := []byte(`{
		"extends": ["medical"],
		"predicates": [
			{
				"canonical": "operator_relation",
				"sub_property_of": ["associated_with"]
			}
		],
		"aliases": {
			"operator relation": "operator_relation"
		}
	}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	reg, err := loadRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Canonical("prevents"); !ok {
		t.Fatal("medical pack predicate prevents was not loaded")
	}
	meta, ok := reg.Canonical("operator relation")
	if !ok {
		t.Fatal("operator alias was not loaded")
	}
	if meta.Canonical != "operator_relation" {
		t.Fatalf("canonical=%q, want operator_relation", meta.Canonical)
	}
}

func TestLoadRegistryComposesPredicatePackArgumentWithFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	data := []byte(`{
		"predicates": [
			{
				"canonical": "file_relation",
				"sub_property_of": ["associated_with"]
			}
		],
		"aliases": {
			"file relation": "file_relation"
		}
	}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	reg, err := loadRegistry(path, "medical")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Canonical("prevents"); !ok {
		t.Fatal("predicate pack argument was not loaded with registry file")
	}
	meta, ok := reg.Canonical("file relation")
	if !ok {
		t.Fatal("file alias was not loaded")
	}
	if meta.Canonical != "file_relation" {
		t.Fatalf("canonical=%q, want file_relation", meta.Canonical)
	}
}

func TestLoadRegistryDefaultAndUnknownPack(t *testing.T) {
	reg, err := loadRegistry("")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Canonical("is a"); !ok {
		t.Fatal("empty registry spec did not default to general")
	}

	_, err = loadRegistry("general", "missing_pack")
	if err == nil {
		t.Fatal("loadRegistry returned nil error")
	}
	if !strings.Contains(err.Error(), `predicate pack "missing_pack" is not registered`) {
		t.Fatalf("error %q does not clearly report unknown pack", err)
	}
}

func TestBuildGeneralizationAcceptsPredicatePacksFlag(t *testing.T) {
	err := runBuildGeneralization([]string{"--predicate-packs", "medical"})
	if err == nil {
		t.Fatal("runBuildGeneralization returned nil error")
	}
	if !strings.Contains(err.Error(), "--source is required") {
		t.Fatalf("error %q indicates --predicate-packs was not parsed before validation", err)
	}
}
