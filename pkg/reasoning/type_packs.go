package reasoning

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// EntityType is an explicit advisory entity type. Type names correspond to graph
// entity labels; there is no closed-world type inference here.
type EntityType struct {
	Name       string   `json:"name"`
	Supertypes []string `json:"supertypes"`
}

// TypePack is a named bundle of explicit entity type declarations.
type TypePack struct {
	Name  string       `json:"name"`
	Types []EntityType `json:"types"`
}

type registeredTypePack struct {
	pack TypePack
}

type typeLayer struct {
	pack TypePack
	name string
}

type typeSource struct {
	layer string
}

var (
	typePackMu sync.RWMutex
	typePacks  = map[string]registeredTypePack{}
)

// RegisterTypePack registers a named entity type pack. It panics for empty or
// duplicate names because package-level packs are expected to be valid.
func RegisterTypePack(p TypePack) {
	name := strings.TrimSpace(p.Name)
	if name == "" {
		panic("reasoning: type pack name is empty")
	}
	key := normKey(name)
	p.Name = name

	typePackMu.Lock()
	defer typePackMu.Unlock()
	if _, ok := typePacks[key]; ok {
		panic(fmt.Sprintf("reasoning: duplicate type pack %q", name))
	}
	typePacks[key] = registeredTypePack{pack: copyTypePack(p)}
}

// TypePacks returns registered entity type pack names in deterministic order.
func TypePacks() []string {
	typePackMu.RLock()
	defer typePackMu.RUnlock()

	out := make([]string, 0, len(typePacks))
	for _, p := range typePacks {
		out = append(out, p.pack.Name)
	}
	sort.Slice(out, func(i, j int) bool {
		return normKey(out[i]) < normKey(out[j])
	})
	return out
}

// BuildTypeRegistry builds an explicit type registry by layering named packs and
// optional operator extras. Unknown supertype references are advisory warnings,
// not hard errors.
func BuildTypeRegistry(names []string, extra ...TypePack) (*TypeRegistry, error) {
	var layers []typeLayer
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		p, ok := lookupTypePack(name)
		if !ok {
			return nil, fmt.Errorf("type pack %q is not registered", name)
		}
		layers = append(layers, typeLayer{
			pack: p,
			name: p.Name,
		})
	}
	for i, p := range extra {
		name := strings.TrimSpace(p.Name)
		if name == "" {
			name = fmt.Sprintf("extra[%d]", i)
		}
		p.Name = name
		layers = append(layers, typeLayer{
			pack: copyTypePack(p),
			name: name,
		})
	}

	types := map[string]EntityType{}
	sources := map[string]typeSource{}
	for _, layer := range layers {
		for _, typ := range layer.pack.Types {
			typ = normalizeEntityType(typ)
			if typ.Name == "" {
				return nil, fmt.Errorf("type pack %q declares a type with empty name", layer.name)
			}
			key := normKey(typ.Name)
			types[key] = typ
			sources[key] = typeSource{layer: layer.name}
		}
	}

	reg := &TypeRegistry{
		byName:  types,
		sources: sources,
	}
	reg.warnings = reg.supertypeWarnings()
	return reg, nil
}

// TypeRegistry is the explicit, advisory entity type registry.
type TypeRegistry struct {
	byName   map[string]EntityType
	sources  map[string]typeSource
	warnings []string
}

func emptyTypeRegistry() *TypeRegistry {
	return &TypeRegistry{
		byName:  map[string]EntityType{},
		sources: map[string]typeSource{},
	}
}

// Get returns an explicit entity type by name.
func (r *TypeRegistry) Get(name string) (EntityType, bool) {
	if r == nil {
		return EntityType{}, false
	}
	typ, ok := r.byName[normKey(name)]
	if !ok {
		return EntityType{}, false
	}
	return copyEntityType(typ), true
}

// IsA reports whether child is the same type as, or has a transitive is_a path
// to, ancestor. Cycles are handled safely.
func (r *TypeRegistry) IsA(child, ancestor string) bool {
	if r == nil {
		return false
	}
	childKey := normKey(child)
	ancestorKey := normKey(ancestor)
	if childKey == "" || ancestorKey == "" {
		return false
	}
	if _, ok := r.byName[childKey]; !ok {
		return false
	}
	if _, ok := r.byName[ancestorKey]; !ok {
		return false
	}
	if childKey == ancestorKey {
		return true
	}

	seen := map[string]bool{}
	var visit func(string) bool
	visit = func(key string) bool {
		if seen[key] {
			return false
		}
		seen[key] = true
		typ, ok := r.byName[key]
		if !ok {
			return false
		}
		for _, parent := range typ.Supertypes {
			parentKey := normKey(parent)
			if parentKey == ancestorKey {
				return true
			}
			if visit(parentKey) {
				return true
			}
		}
		return false
	}
	return visit(childKey)
}

// Warnings returns non-fatal advisory validation warnings gathered while building
// the type registry.
func (r *TypeRegistry) Warnings() []string {
	if r == nil {
		return nil
	}
	return append([]string{}, r.warnings...)
}

func (r *TypeRegistry) supertypeWarnings() []string {
	if r == nil {
		return nil
	}
	keys := make([]string, 0, len(r.byName))
	for key := range r.byName {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var warnings []string
	for _, key := range keys {
		typ := r.byName[key]
		source := r.sources[key]
		for _, parent := range typ.Supertypes {
			if _, ok := r.byName[normKey(parent)]; !ok {
				warnings = append(warnings, fmt.Sprintf("type pack %q type %q supertype %q is undeclared", source.layer, typ.Name, parent))
			}
		}
	}
	return warnings
}

func lookupTypePack(name string) (TypePack, bool) {
	typePackMu.RLock()
	defer typePackMu.RUnlock()
	p, ok := typePacks[normKey(name)]
	if !ok {
		return TypePack{}, false
	}
	return copyTypePack(p.pack), true
}

func normalizeEntityType(typ EntityType) EntityType {
	typ.Name = strings.TrimSpace(typ.Name)
	typ.Supertypes = trimmedStrings(typ.Supertypes)
	return typ
}

func copyTypePack(p TypePack) TypePack {
	cp := TypePack{
		Name:  strings.TrimSpace(p.Name),
		Types: make([]EntityType, 0, len(p.Types)),
	}
	for _, typ := range p.Types {
		cp.Types = append(cp.Types, copyEntityType(typ))
	}
	return cp
}

func copyEntityType(typ EntityType) EntityType {
	return EntityType{
		Name:       strings.TrimSpace(typ.Name),
		Supertypes: append([]string{}, typ.Supertypes...),
	}
}
