package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/soundprediction/pensiero/pkg/reasoning"
)

type registryFile struct {
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

func loadRegistry(spec string) (*reasoning.PredicateRegistry, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" || spec == "general" {
		return reasoning.DefaultGeneralRegistry(), nil
	}
	data, err := os.ReadFile(spec)
	if err != nil {
		return nil, err
	}
	var file registryFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, err
	}
	metas := make([]reasoning.PredicateMeta, 0, len(file.Predicates)+len(file.Aliases))
	base := map[string]reasoning.PredicateMeta{}
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
		base[strings.ToLower(strings.TrimSpace(meta.Canonical))] = meta
	}
	for raw, canon := range file.Aliases {
		meta, ok := base[strings.ToLower(strings.TrimSpace(canon))]
		if !ok {
			return nil, fmt.Errorf("registry alias %q references unknown canonical %q", raw, canon)
		}
		meta.Raw = raw
		metas = append(metas, meta)
	}
	return reasoning.NewPredicateRegistry(metas, file.Compositions, file.Disjoint), nil
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
