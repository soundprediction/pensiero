package reasoning

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// PredicatePack is a named, validated layer of predicate primitives.
type PredicatePack struct {
	Name         string
	Predicates   []PredicateMeta
	Compositions []CompositionRule
	Disjoints    []DisjointPair
	Aliases      map[string]string
}

type registeredPack struct {
	pack PredicatePack
}

type registryLayer struct {
	pack    PredicatePack
	name    string
	builtin bool
}

type predicateSource struct {
	layer   string
	builtin bool
}

var (
	packMu sync.RWMutex
	packs  = map[string]registeredPack{}
)

// RegisterPack registers a named predicate pack. It panics for empty or duplicate
// names because package-level packs are expected to be valid by construction.
func RegisterPack(p PredicatePack) {
	name := strings.TrimSpace(p.Name)
	if name == "" {
		panic("reasoning: predicate pack name is empty")
	}
	key := normKey(name)
	p.Name = name

	packMu.Lock()
	defer packMu.Unlock()
	if _, ok := packs[key]; ok {
		panic(fmt.Sprintf("reasoning: duplicate predicate pack %q", name))
	}
	packs[key] = registeredPack{pack: copyPredicatePack(p)}
}

// Packs returns the registered predicate pack names in deterministic order.
func Packs() []string {
	packMu.RLock()
	defer packMu.RUnlock()

	out := make([]string, 0, len(packs))
	for _, p := range packs {
		out = append(out, p.pack.Name)
	}
	sort.Slice(out, func(i, j int) bool {
		return normKey(out[i]) < normKey(out[j])
	})
	return out
}

// BuildRegistry builds a validated registry by layering the general pack, then
// each named pack, then any operator-provided extras. Extras may override earlier
// predicates; registered pack collisions are treated as shipped-data errors.
func BuildRegistry(names []string, extra ...PredicatePack) (*PredicateRegistry, error) {
	return BuildRegistryWithTypes(names, emptyTypeRegistry(), extra...)
}

// BuildRegistryWithTypes builds a predicate registry and validates advisory
// domain/range declarations against an explicit type registry. Unknown types are
// returned as registry warnings, not as hard build errors.
func BuildRegistryWithTypes(names []string, types *TypeRegistry, extra ...PredicatePack) (*PredicateRegistry, error) {
	general, ok := lookupPack("general")
	if !ok {
		return nil, fmt.Errorf("predicate pack %q is not registered", "general")
	}

	layers := []registryLayer{{
		pack:    general,
		name:    general.Name,
		builtin: true,
	}}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || normKey(name) == "general" {
			continue
		}
		p, ok := lookupPack(name)
		if !ok {
			return nil, fmt.Errorf("predicate pack %q is not registered", name)
		}
		layers = append(layers, registryLayer{
			pack:    p,
			name:    p.Name,
			builtin: true,
		})
	}
	for i, p := range extra {
		name := strings.TrimSpace(p.Name)
		if name == "" {
			name = fmt.Sprintf("extra[%d]", i)
		}
		p.Name = name
		layers = append(layers, registryLayer{
			pack:    copyPredicatePack(p),
			name:    name,
			builtin: false,
		})
	}

	merged := newPackMerge()
	for _, layer := range layers {
		if err := merged.merge(layer); err != nil {
			return nil, err
		}
	}
	if err := merged.validate(); err != nil {
		return nil, err
	}
	warnings := append(types.Warnings(), merged.domainRangeWarnings(types)...)

	return newPredicateRegistry(merged.metas(), merged.compositions(), merged.disjointPairs(), warnings, types), nil
}

type packMerge struct {
	preds        map[string]PredicateMeta
	predSources  map[string]predicateSource
	aliases      map[string]string
	aliasRaws    map[string]string
	aliasSources map[string]predicateSource
	comps        []CompositionRule
	compIndex    map[string]int
	disjoints    []DisjointPair
	disjIndex    map[string]int
}

func newPackMerge() *packMerge {
	return &packMerge{
		preds:        map[string]PredicateMeta{},
		predSources:  map[string]predicateSource{},
		aliases:      map[string]string{},
		aliasRaws:    map[string]string{},
		aliasSources: map[string]predicateSource{},
		compIndex:    map[string]int{},
		disjIndex:    map[string]int{},
	}
}

func (m *packMerge) merge(layer registryLayer) error {
	for _, pred := range layer.pack.Predicates {
		meta := normalizePredicateMeta(pred)
		if meta.Canonical == "" {
			return fmt.Errorf("predicate pack %q declares a predicate with empty canonical", layer.name)
		}
		key := normKey(meta.Canonical)
		if prev, ok := m.predSources[key]; ok && prev.builtin && layer.builtin {
			return fmt.Errorf("predicate pack %q duplicates built-in predicate %q from pack %q", layer.name, meta.Canonical, prev.layer)
		}
		m.preds[key] = meta
		m.predSources[key] = predicateSource{layer: layer.name, builtin: layer.builtin}
	}

	aliasKeys := make([]string, 0, len(layer.pack.Aliases))
	for raw := range layer.pack.Aliases {
		aliasKeys = append(aliasKeys, raw)
	}
	sort.Slice(aliasKeys, func(i, j int) bool {
		ik, jk := normKey(aliasKeys[i]), normKey(aliasKeys[j])
		if ik == jk {
			return aliasKeys[i] < aliasKeys[j]
		}
		return ik < jk
	})
	seenAliases := map[string]string{}
	for _, raw := range aliasKeys {
		canon := layer.pack.Aliases[raw]
		raw = strings.TrimSpace(raw)
		canon = strings.TrimSpace(canon)
		if raw == "" {
			return fmt.Errorf("predicate pack %q declares an alias with empty raw predicate", layer.name)
		}
		rawKey := normKey(raw)
		if prev, ok := seenAliases[rawKey]; ok {
			return fmt.Errorf("predicate pack %q declares duplicate alias %q (also %q)", layer.name, raw, prev)
		}
		seenAliases[rawKey] = raw
		if prev, ok := m.aliasSources[rawKey]; ok && prev.builtin && layer.builtin && normKey(m.aliases[rawKey]) != normKey(canon) {
			return fmt.Errorf("predicate pack %q duplicates built-in alias %q from pack %q", layer.name, raw, prev.layer)
		}
		m.aliases[rawKey] = canon
		m.aliasRaws[rawKey] = raw
		m.aliasSources[rawKey] = predicateSource{layer: layer.name, builtin: layer.builtin}
	}

	for _, comp := range layer.pack.Compositions {
		key := compositionKey(comp)
		if idx, ok := m.compIndex[key]; ok {
			m.comps[idx] = comp
			continue
		}
		m.compIndex[key] = len(m.comps)
		m.comps = append(m.comps, comp)
	}

	for _, disjoint := range layer.pack.Disjoints {
		key := disjointKey(disjoint)
		if idx, ok := m.disjIndex[key]; ok {
			m.disjoints[idx] = disjoint
			continue
		}
		m.disjIndex[key] = len(m.disjoints)
		m.disjoints = append(m.disjoints, disjoint)
	}

	return nil
}

func (m *packMerge) validate() error {
	aliasKeys := make([]string, 0, len(m.aliases))
	for raw := range m.aliases {
		aliasKeys = append(aliasKeys, raw)
	}
	sort.Strings(aliasKeys)
	for _, raw := range aliasKeys {
		canon := m.aliases[raw]
		if _, ok := m.preds[normKey(canon)]; !ok {
			alias := raw
			if original, ok := m.aliasRaws[raw]; ok {
				alias = original
			}
			return fmt.Errorf("predicate pack %q alias %q references undeclared canonical predicate %q", m.aliasSources[raw].layer, alias, canon)
		}
	}

	canonKeys := make([]string, 0, len(m.preds))
	for key := range m.preds {
		canonKeys = append(canonKeys, key)
	}
	sort.Strings(canonKeys)
	for _, key := range canonKeys {
		meta := m.preds[key]
		if err := m.validateRef("predicate "+meta.Canonical, "inverse_of", meta.InverseOf); err != nil {
			return err
		}
		for _, parent := range meta.SubPropertyOf {
			if err := m.validateRef("predicate "+meta.Canonical, "sub_property_of", parent); err != nil {
				return err
			}
		}
	}

	for _, comp := range m.comps {
		offender := fmt.Sprintf("composition %s,%s=>%s", comp.First, comp.Second, comp.Result)
		if err := m.validateRef(offender, "first", comp.First); err != nil {
			return err
		}
		if err := m.validateRef(offender, "second", comp.Second); err != nil {
			return err
		}
		if err := m.validateRef(offender, "result", comp.Result); err != nil {
			return err
		}
	}

	for _, disjoint := range m.disjoints {
		offender := fmt.Sprintf("disjoint %s,%s", disjoint.A, disjoint.B)
		if err := m.validateRef(offender, "a", disjoint.A); err != nil {
			return err
		}
		if err := m.validateRef(offender, "b", disjoint.B); err != nil {
			return err
		}
	}

	return nil
}

func (m *packMerge) validateRef(offender, field, ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil
	}
	if _, ok := m.preds[normKey(ref)]; ok {
		return nil
	}
	return fmt.Errorf("%s %s references undeclared canonical predicate %q", offender, field, ref)
}

func (m *packMerge) domainRangeWarnings(types *TypeRegistry) []string {
	if types == nil {
		return nil
	}
	keys := make([]string, 0, len(m.preds))
	for key := range m.preds {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var warnings []string
	for _, key := range keys {
		meta := m.preds[key]
		source := m.predSources[key]
		for _, typ := range meta.Domain {
			if _, ok := types.Get(typ); !ok {
				warnings = append(warnings, fmt.Sprintf("predicate pack %q predicate %q domain type %q is undeclared", source.layer, meta.Canonical, typ))
			}
		}
		for _, typ := range meta.Range {
			if _, ok := types.Get(typ); !ok {
				warnings = append(warnings, fmt.Sprintf("predicate pack %q predicate %q range type %q is undeclared", source.layer, meta.Canonical, typ))
			}
		}
	}
	return warnings
}

func (m *packMerge) metas() []PredicateMeta {
	keys := make([]string, 0, len(m.preds))
	for key := range m.preds {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	metas := make([]PredicateMeta, 0, len(m.preds)+len(m.aliases))
	for _, key := range keys {
		metas = append(metas, m.preds[key])
	}

	aliasKeys := make([]string, 0, len(m.aliases))
	for key := range m.aliases {
		aliasKeys = append(aliasKeys, key)
	}
	sort.Strings(aliasKeys)
	for _, rawKey := range aliasKeys {
		raw := rawKey
		canon := m.aliases[rawKey]
		base := m.preds[normKey(canon)]
		if original, ok := m.aliasRaws[rawKey]; ok {
			raw = original
		}
		base.Raw = raw
		metas = append(metas, base)
	}

	return metas
}

func (m *packMerge) compositions() []CompositionRule {
	return append([]CompositionRule{}, m.comps...)
}

func (m *packMerge) disjointPairs() []DisjointPair {
	return append([]DisjointPair{}, m.disjoints...)
}

func lookupPack(name string) (PredicatePack, bool) {
	packMu.RLock()
	defer packMu.RUnlock()
	p, ok := packs[normKey(name)]
	if !ok {
		return PredicatePack{}, false
	}
	return copyPredicatePack(p.pack), true
}

func normalizePredicateMeta(meta PredicateMeta) PredicateMeta {
	meta.Raw = strings.TrimSpace(meta.Raw)
	meta.Canonical = strings.TrimSpace(meta.Canonical)
	meta.InverseOf = strings.TrimSpace(meta.InverseOf)
	if meta.Canonical == "" {
		meta.Canonical = meta.Raw
	}
	if meta.Raw == "" {
		meta.Raw = meta.Canonical
	}
	meta.SubPropertyOf = trimmedStrings(meta.SubPropertyOf)
	meta.Domain = trimmedStrings(meta.Domain)
	meta.Range = trimmedStrings(meta.Range)
	return meta
}

func copyPredicatePack(p PredicatePack) PredicatePack {
	cp := PredicatePack{
		Name:         strings.TrimSpace(p.Name),
		Predicates:   copyPredicateMetas(p.Predicates),
		Compositions: append([]CompositionRule{}, p.Compositions...),
		Disjoints:    append([]DisjointPair{}, p.Disjoints...),
	}
	if p.Aliases != nil {
		cp.Aliases = make(map[string]string, len(p.Aliases))
		for raw, canon := range p.Aliases {
			cp.Aliases[raw] = canon
		}
	}
	return cp
}

func copyPredicateMetas(values []PredicateMeta) []PredicateMeta {
	if len(values) == 0 {
		return nil
	}
	out := make([]PredicateMeta, len(values))
	for i, value := range values {
		out[i] = value
		out[i].SubPropertyOf = append([]string{}, value.SubPropertyOf...)
		out[i].Domain = append([]string{}, value.Domain...)
		out[i].Range = append([]string{}, value.Range...)
	}
	return out
}

func trimmedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func compositionKey(comp CompositionRule) string {
	return strings.Join([]string{normKey(comp.First), normKey(comp.Second), normKey(comp.Result)}, "\x00")
}

func disjointKey(pair DisjointPair) string {
	a, b := normKey(pair.A), normKey(pair.B)
	if b < a {
		a, b = b, a
	}
	return a + "\x00" + b
}
