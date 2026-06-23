package main

import (
	"encoding/json"
	"os"
	"strings"

	"github.com/soundprediction/pensiero/pkg/reasoning"
)

type registryFile struct {
	Extends      []string                    `json:"extends"`
	Aliases      map[string]string           `json:"aliases"`
	Predicates   []registryPredicate         `json:"predicates"`
	Compositions []reasoning.CompositionRule `json:"compositions"`
	Disjoint     []reasoning.DisjointPair    `json:"disjoint"`
}

type registryPredicate struct {
	Raw             string                   `json:"raw"`
	Canonical       string                   `json:"canonical"`
	InverseOf       string                   `json:"inverse_of"`
	SubPropertyOf   []string                 `json:"sub_property_of"`
	Characteristics []string                 `json:"characteristics"`
	Chars           reasoning.Characteristic `json:"chars"`
}

func loadRegistry(spec string, packs ...string) (*reasoning.PredicateRegistry, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" || spec == "general" {
		return reasoning.BuildRegistry(packs)
	}
	data, err := os.ReadFile(spec)
	if err != nil {
		return nil, err
	}
	var file registryFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, err
	}
	metas := make([]reasoning.PredicateMeta, 0, len(file.Predicates))
	for _, pred := range file.Predicates {
		meta := reasoning.PredicateMeta{
			Raw:           pred.Raw,
			Canonical:     pred.Canonical,
			InverseOf:     pred.InverseOf,
			SubPropertyOf: pred.SubPropertyOf,
			Chars:         pred.Chars | parseCharacteristics(pred.Characteristics),
		}
		if meta.Canonical == "" {
			meta.Canonical = meta.Raw
		}
		if meta.Raw == "" {
			meta.Raw = meta.Canonical
		}
		metas = append(metas, meta)
	}
	extends := append([]string{}, file.Extends...)
	extends = append(extends, packs...)
	return reasoning.BuildRegistry(extends, reasoning.PredicatePack{
		Name:         spec,
		Predicates:   metas,
		Compositions: file.Compositions,
		Disjoints:    file.Disjoint,
		Aliases:      file.Aliases,
	})
}

func parseCharacteristics(values []string) reasoning.Characteristic {
	var out reasoning.Characteristic
	for _, value := range values {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "transitive":
			out |= reasoning.Transitive
		case "symmetric":
			out |= reasoning.Symmetric
		case "asymmetric":
			out |= reasoning.Asymmetric
		case "reflexive":
			out |= reasoning.Reflexive
		case "irreflexive":
			out |= reasoning.Irreflexive
		case "functional":
			out |= reasoning.Functional
		case "inverse_functional":
			out |= reasoning.InverseFunctional
		}
	}
	return out
}
