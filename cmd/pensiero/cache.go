package main

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/soundprediction/pensiero/pkg/reasoning"
)

// proofCacheDebug logs each Entails (claim, assumed-fact count, cache status,
// verdict) when PENSIERO_ENTAILS_DEBUG=1 — for diagnosing rule firing.
var proofCacheDebug = os.Getenv("PENSIERO_ENTAILS_DEBUG") == "1"

const (
	defaultProofCacheMaxEntries = 1024
	defaultProofCacheMaxBytes   = 16 << 20
)

type proofCache struct {
	provider            generationProvider
	reg                 *reasoning.PredicateRegistry
	registryFingerprint string
	cfg                 proofCacheConfigKey
	maxEntries          int
	maxBytes            int

	mu      sync.Mutex
	lru     *list.List
	entries map[string]*list.Element
	calls   map[string]*proofCacheCall
	bytes   int
}

type proofCacheConfigKey struct {
	MaxHops        int     `json:"max_hops"`
	Decay          float64 `json:"decay"`
	MinConf        float64 `json:"min_conf"`
	Limit          int     `json:"limit"`
	ExcludeDeduced bool    `json:"exclude_deduced"`
}

type proofCacheEntry struct {
	key   string
	value proofCacheValue
	bytes int
}

type proofCacheCall struct {
	done  chan struct{}
	value proofCacheValue
	err   error
}

type proofCacheValue struct {
	method      string
	entail      reasoning.EntailResult
	derive      []reasoning.Proof
	contradicts proofCacheContradictsValue
}

type proofCacheContradictsValue struct {
	ok    bool
	proof *reasoning.Proof
}

type proofCacheKeyMaterial struct {
	Method              string               `json:"method"`
	GenerationID        string               `json:"generation_id"`
	RegistryFingerprint string               `json:"registry_fingerprint"`
	Backend             string               `json:"backend"`
	Config              proofCacheConfigKey  `json:"config"`
	Claim               *proofCacheClaimKey  `json:"claim,omitempty"`
	Derive              *proofCacheDeriveKey `json:"derive,omitempty"`
	// AssumedFacts are per-request ground facts (e.g. patient findings) that can
	// change the verdict for the same claim, so they MUST participate in the cache
	// key — otherwise different patients would share a cached result.
	AssumedFacts []proofCacheClaimKey `json:"assumed_facts,omitempty"`
}

type proofCacheClaimKey struct {
	Subject   string `json:"subject"`
	Predicate string `json:"predicate"`
	Object    string `json:"object"`
}

type proofCacheDeriveKey struct {
	Source         string   `json:"source"`
	Target         string   `json:"target"`
	Predicate      string   `json:"predicate"`
	Preds          []string `json:"preds,omitempty"`
	MaxHops        int      `json:"max_hops"`
	Decay          float64  `json:"decay"`
	MinConf        float64  `json:"min_conf"`
	Limit          int      `json:"limit"`
	IncludeInverse bool     `json:"include_inverse"`
}

type proofCacheKey struct {
	hash       string
	generation string
	predicate  string
	subject    string
	object     string
}

func newProofCache(provider generationProvider, reg *reasoning.PredicateRegistry, cfg reasoning.Config, maxEntries int, maxBytes int) *proofCache {
	if maxEntries <= 0 {
		maxEntries = defaultProofCacheMaxEntries
	}
	if maxBytes <= 0 {
		maxBytes = defaultProofCacheMaxBytes
	}
	return &proofCache{
		provider:            provider,
		reg:                 reg,
		registryFingerprint: reg.Fingerprint(),
		cfg: proofCacheConfigKey{
			MaxHops:        cfg.MaxHops,
			Decay:          cfg.Decay,
			MinConf:        cfg.MinConf,
			Limit:          cfg.Limit,
			ExcludeDeduced: cfg.ExcludeDeduced,
		},
		maxEntries: maxEntries,
		maxBytes:   maxBytes,
		lru:        list.New(),
		entries:    map[string]*list.Element{},
		calls:      map[string]*proofCacheCall{},
	}
}

func (c *proofCache) Derive(ctx context.Context, req reasoning.DeriveRequest) ([]reasoning.Proof, error) {
	normalized := c.normalizeDeriveRequest(req)
	req = normalized.deriveRequest()
	gen, release, err := c.acquire(ctx, generationRoute{Text: routeText(normalized.Source, normalized.Target)})
	if err != nil {
		setQueryCacheStatus(ctx, queryCacheStatusMiss)
		return nil, err
	}
	key := c.deriveKey(gen, req)
	setQueryCacheKey(ctx, key)
	value, status, err := c.loadOrCompute(ctx, key.hash, release, func() (proofCacheValue, error) {
		reasoner := reasoning.NewPredicateConstrained(gen.reasoner, c.reg)
		proofs, err := reasoner.Derive(ctx, req)
		return proofCacheValue{
			method: "Derive",
			derive: proofs,
		}, err
	})
	setQueryCacheStatus(ctx, status)
	if err != nil {
		return nil, err
	}
	return cloneProofs(value.derive), nil
}

func (c *proofCache) Entails(ctx context.Context, claim reasoning.Claim) (reasoning.EntailResult, error) {
	claim = c.normalizeClaim(claim)
	assumed := reasoning.AssumedFactsFromContext(ctx)
	gen, release, err := c.acquire(ctx, generationRoute{Text: routeText(claim.Subject, claim.Object)})
	if err != nil {
		setQueryCacheStatus(ctx, queryCacheStatusMiss)
		return reasoning.EntailResult{}, err
	}
	key := c.claimKey(gen, "Entails", claim, assumed)
	setQueryCacheKey(ctx, key)
	value, status, err := c.loadOrCompute(ctx, key.hash, release, func() (proofCacheValue, error) {
		reasoner := reasoning.NewPredicateConstrained(gen.reasoner, c.reg)
		result, err := reasoner.Entails(ctx, claim)
		return proofCacheValue{
			method: "Entails",
			entail: result,
		}, err
	})
	setQueryCacheStatus(ctx, status)
	if err != nil {
		return reasoning.EntailResult{}, err
	}
	out := cloneEntailResult(value.entail)
	if proofCacheDebug {
		log.Printf("[pensiero-entails] gen=%s claim=(%q %q %q) assumed=%d cache=%s verdict=%s conf=%.3f",
			gen.id, claim.Subject, claim.Predicate, claim.Object, len(assumed), status, out.Verdict, out.Confidence)
	}
	return out, nil
}

func (c *proofCache) Contradicts(ctx context.Context, claim reasoning.Claim) (bool, *reasoning.Proof, error) {
	claim = c.normalizeClaim(claim)
	gen, release, err := c.acquire(ctx, generationRoute{Text: routeText(claim.Subject, claim.Object)})
	if err != nil {
		setQueryCacheStatus(ctx, queryCacheStatusMiss)
		return false, nil, err
	}
	key := c.claimKey(gen, "Contradicts", claim, reasoning.AssumedFactsFromContext(ctx))
	setQueryCacheKey(ctx, key)
	value, status, err := c.loadOrCompute(ctx, key.hash, release, func() (proofCacheValue, error) {
		reasoner := reasoning.NewPredicateConstrained(gen.reasoner, c.reg)
		ok, proof, err := reasoner.Contradicts(ctx, claim)
		return proofCacheValue{
			method: "Contradicts",
			contradicts: proofCacheContradictsValue{
				ok:    ok,
				proof: proof,
			},
		}, err
	})
	setQueryCacheStatus(ctx, status)
	if err != nil {
		return false, nil, err
	}
	return value.contradicts.ok, cloneProofPtr(value.contradicts.proof), nil
}

func (c *proofCache) Name() string {
	if c == nil || c.provider == nil {
		return "proof-cache"
	}
	return c.provider.ProviderName() + "+predicate-constrained+proof-cache"
}

func (c *proofCache) acquire(ctx context.Context, route generationRoute) (*generation, func(), error) {
	if c == nil || c.provider == nil {
		return nil, func() {}, errNoGeneration
	}
	return c.provider.AcquireGeneration(ctx, route)
}

func (c *proofCache) loadOrCompute(ctx context.Context, hash string, release func(), compute func() (proofCacheValue, error)) (proofCacheValue, queryCacheStatus, error) {
	for {
		c.mu.Lock()
		if elem, ok := c.entries[hash]; ok {
			c.lru.MoveToFront(elem)
			value := cloneCacheValue(elem.Value.(*proofCacheEntry).value)
			c.mu.Unlock()
			release()
			return value, queryCacheStatusHit, nil
		}
		if call, ok := c.calls[hash]; ok {
			c.mu.Unlock()
			select {
			case <-call.done:
				if call.err == nil {
					release()
					return cloneCacheValue(call.value), queryCacheStatusSingleflight, nil
				}
				if err := ctx.Err(); err != nil {
					release()
					return proofCacheValue{}, queryCacheStatusSingleflight, err
				}
				continue
			case <-ctx.Done():
				release()
				return proofCacheValue{}, queryCacheStatusSingleflight, ctx.Err()
			}
		}
		if err := ctx.Err(); err != nil {
			c.mu.Unlock()
			release()
			return proofCacheValue{}, queryCacheStatusMiss, err
		}
		call := &proofCacheCall{done: make(chan struct{})}
		c.calls[hash] = call
		c.mu.Unlock()

		var value proofCacheValue
		var err error
		var panicValue any
		defer func() {
			release()
			if panicValue != nil {
				panic(panicValue)
			}
		}()
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					panicValue = recovered
					err = fmt.Errorf("proof cache compute panic: %v", recovered)
				}
			}()
			value, err = compute()
		}()
		if err == nil {
			err = ctx.Err()
		}
		value = cloneCacheValue(value)
		if err == nil {
			c.insert(hash, value)
		}
		c.finishCall(hash, call, value, err)
		return cloneCacheValue(value), queryCacheStatusMiss, err
	}
}

func (c *proofCache) finishCall(hash string, call *proofCacheCall, value proofCacheValue, err error) {
	c.mu.Lock()
	call.value = value
	call.err = err
	delete(c.calls, hash)
	c.mu.Unlock()
	close(call.done)
}

func (c *proofCache) insert(hash string, value proofCacheValue) {
	entry := &proofCacheEntry{
		key:   hash,
		value: cloneCacheValue(value),
		bytes: cacheValueSize(value),
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.entries[hash]; ok {
		old := existing.Value.(*proofCacheEntry)
		c.bytes -= old.bytes
		existing.Value = entry
		c.bytes += entry.bytes
		c.lru.MoveToFront(existing)
	} else {
		elem := c.lru.PushFront(entry)
		c.entries[hash] = elem
		c.bytes += entry.bytes
	}
	for len(c.entries) > c.maxEntries || c.bytes > c.maxBytes {
		elem := c.lru.Back()
		if elem == nil {
			break
		}
		evicted := elem.Value.(*proofCacheEntry)
		delete(c.entries, evicted.key)
		c.bytes -= evicted.bytes
		c.lru.Remove(elem)
	}
}

func (c *proofCache) claimKey(gen *generation, method string, claim reasoning.Claim, assumed []reasoning.Claim) proofCacheKey {
	claim = c.normalizeClaim(claim)
	normalized := proofCacheClaimKey{
		Subject:   claim.Subject,
		Predicate: claim.Predicate,
		Object:    claim.Object,
	}
	material := proofCacheKeyMaterial{
		Method:              method,
		GenerationID:        gen.id,
		RegistryFingerprint: c.registryFingerprint,
		Backend:             gen.reasoner.Name(),
		Config:              c.cfg,
		Claim:               &normalized,
		AssumedFacts:        c.assumedFactsKey(assumed),
	}
	return proofCacheKey{
		hash:       hashCacheKey(material),
		generation: gen.id,
		predicate:  normalized.Predicate,
		subject:    normalized.Subject,
		object:     normalized.Object,
	}
}

// assumedFactsKey normalizes and deterministically orders per-request assumed
// facts so they contribute stably to the cache key.
func (c *proofCache) assumedFactsKey(facts []reasoning.Claim) []proofCacheClaimKey {
	if len(facts) == 0 {
		return nil
	}
	out := make([]proofCacheClaimKey, 0, len(facts))
	for _, f := range facts {
		f = c.normalizeClaim(f)
		out = append(out, proofCacheClaimKey{Subject: f.Subject, Predicate: f.Predicate, Object: f.Object})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Subject != out[j].Subject {
			return out[i].Subject < out[j].Subject
		}
		if out[i].Predicate != out[j].Predicate {
			return out[i].Predicate < out[j].Predicate
		}
		return out[i].Object < out[j].Object
	})
	return out
}

func (c *proofCache) normalizeClaim(claim reasoning.Claim) reasoning.Claim {
	return reasoning.Claim{
		Subject:   strings.TrimSpace(claim.Subject),
		Predicate: c.canonicalPredicate(claim.Predicate),
		Object:    strings.TrimSpace(claim.Object),
	}
}

func routeText(left string, right string) string {
	return strings.TrimSpace(strings.TrimSpace(left) + " " + strings.TrimSpace(right))
}

func (c *proofCache) deriveKey(gen *generation, req reasoning.DeriveRequest) proofCacheKey {
	normalized := c.normalizeDeriveRequest(req)
	material := proofCacheKeyMaterial{
		Method:              "Derive",
		GenerationID:        gen.id,
		RegistryFingerprint: c.registryFingerprint,
		Backend:             gen.reasoner.Name(),
		Config:              c.cfg,
		Derive:              &normalized,
	}
	return proofCacheKey{
		hash:       hashCacheKey(material),
		generation: gen.id,
		predicate:  normalized.Predicate,
		subject:    normalized.Source,
		object:     normalized.Target,
	}
}

func (k proofCacheDeriveKey) deriveRequest() reasoning.DeriveRequest {
	return reasoning.DeriveRequest{
		Source:         k.Source,
		Target:         k.Target,
		Predicate:      k.Predicate,
		Preds:          append([]string{}, k.Preds...),
		MaxHops:        k.MaxHops,
		Decay:          k.Decay,
		MinConf:        k.MinConf,
		Limit:          k.Limit,
		IncludeInverse: k.IncludeInverse,
	}
}

func (c *proofCache) normalizeDeriveRequest(req reasoning.DeriveRequest) proofCacheDeriveKey {
	preds := make([]string, 0, len(req.Preds))
	seen := map[string]bool{}
	for _, pred := range req.Preds {
		canon := c.canonicalPredicate(pred)
		if canon == "" || seen[canon] {
			continue
		}
		seen[canon] = true
		preds = append(preds, canon)
	}
	sort.Strings(preds)
	maxHops := req.MaxHops
	if maxHops <= 0 {
		maxHops = c.cfg.MaxHops
	}
	decay := req.Decay
	if decay <= 0 || decay > 1 {
		decay = c.cfg.Decay
	}
	minConf := req.MinConf
	if minConf <= 0 {
		minConf = c.cfg.MinConf
	}
	limit := req.Limit
	if limit <= 0 {
		limit = c.cfg.Limit
	}
	return proofCacheDeriveKey{
		Source:         strings.TrimSpace(req.Source),
		Target:         strings.TrimSpace(req.Target),
		Predicate:      c.canonicalPredicate(req.Predicate),
		Preds:          preds,
		MaxHops:        maxHops,
		Decay:          decay,
		MinConf:        minConf,
		Limit:          limit,
		IncludeInverse: req.IncludeInverse,
	}
}

func (c *proofCache) canonicalPredicate(pred string) string {
	if c != nil && c.reg != nil {
		meta, _ := c.reg.Canonical(pred)
		return strings.TrimSpace(meta.Canonical)
	}
	return strings.TrimSpace(pred)
}

func hashCacheKey(material proofCacheKeyMaterial) string {
	data, err := json.Marshal(material)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func cloneCacheValue(value proofCacheValue) proofCacheValue {
	return proofCacheValue{
		method: value.method,
		entail: cloneEntailResult(value.entail),
		derive: cloneProofs(value.derive),
		contradicts: proofCacheContradictsValue{
			ok:    value.contradicts.ok,
			proof: cloneProofPtr(value.contradicts.proof),
		},
	}
}

func cloneEntailResult(result reasoning.EntailResult) reasoning.EntailResult {
	return reasoning.EntailResult{
		Best:       cloneProofPtr(result.Best),
		Verdict:    result.Verdict,
		All:        cloneProofs(result.All),
		Confidence: result.Confidence,
	}
}

func cloneProofPtr(proof *reasoning.Proof) *reasoning.Proof {
	if proof == nil {
		return nil
	}
	cloned := cloneProof(*proof)
	return &cloned
}

func cloneProofs(proofs []reasoning.Proof) []reasoning.Proof {
	if len(proofs) == 0 {
		return nil
	}
	out := make([]reasoning.Proof, len(proofs))
	for i := range proofs {
		out[i] = cloneProof(proofs[i])
	}
	return out
}

func cloneProof(proof reasoning.Proof) reasoning.Proof {
	proof.Steps = append([]reasoning.ProofStep(nil), proof.Steps...)
	return proof
}

func cacheValueSize(value proofCacheValue) int {
	size := len(value.method) + 128
	size += entailResultSize(value.entail)
	size += proofsSize(value.derive)
	if value.contradicts.proof != nil {
		size += proofSize(*value.contradicts.proof)
	}
	return size
}

func entailResultSize(result reasoning.EntailResult) int {
	size := len(result.Verdict) + 64
	if result.Best != nil {
		size += proofSize(*result.Best)
	}
	return size + proofsSize(result.All)
}

func proofsSize(proofs []reasoning.Proof) int {
	size := 0
	for _, proof := range proofs {
		size += proofSize(proof)
	}
	return size
}

func proofSize(proof reasoning.Proof) int {
	size := len(proof.Source) + len(proof.Target) + len(proof.Predicate) + len(proof.RuleClass) + 64
	for _, step := range proof.Steps {
		size += len(step.EdgeID) + len(step.Rule) + len(step.Predicate) + len(step.Source) + len(step.Target) + 32
	}
	return size
}
