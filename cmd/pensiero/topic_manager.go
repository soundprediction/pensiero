package main

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"google.golang.org/grpc/metadata"
)

const (
	topicMetadataKey     = "pensiero-topic"
	defaultMaxOpenTopics = 8
)

var supportedTopicGraphExts = map[string]bool{
	".db":      true,
	".ladybug": true,
	".lbug":    true,
}

type topicServingSnapshot struct {
	SourceDir     string              `json:"source_dir,omitempty"`
	DefaultTopic  string              `json:"default_topic,omitempty"`
	MaxOpenTopics int                 `json:"max_open_topics,omitempty"`
	Available     []string            `json:"available,omitempty"`
	Open          []openTopicSnapshot `json:"open,omitempty"`
}

type openTopicSnapshot struct {
	Topic        string `json:"topic"`
	Path         string `json:"path"`
	GenerationID string `json:"generation_id,omitempty"`
}

type topicStatusProvider interface {
	TopicSnapshot() topicServingSnapshot
}

type topicDefinition struct {
	Key    string
	Name   string
	Path   string
	Tokens map[string]struct{}
}

type topicEntry struct {
	def      topicDefinition
	store    *generationStore
	reloader *snapshotReloader
	elem     *list.Element
}

type topicGenerationManager struct {
	ctx          context.Context
	dir          string
	defaultKey   string
	defaultTopic string
	maxOpen      int
	build        generationBuilder
	validate     generationValidator
	interval     time.Duration
	logger       *log.Logger

	topics []topicDefinition
	byKey  map[string]topicDefinition
	lookup map[string]string

	mu        sync.Mutex
	open      map[string]*topicEntry
	lru       *list.List
	closed    bool
	closeOnce sync.Once
	closeWG   sync.WaitGroup
	closeErr  error
}

func newTopicGenerationManager(ctx context.Context, dir string, defaultTopic string, maxOpen int, interval time.Duration, build generationBuilder, validate generationValidator, logger *log.Logger) (*topicGenerationManager, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, fmt.Errorf("--source-dir is required")
	}
	if build == nil {
		return nil, fmt.Errorf("topic generation manager: nil generation builder")
	}
	if maxOpen <= 0 {
		maxOpen = defaultMaxOpenTopics
	}
	topics, lookup, err := discoverTopicGraphs(dir)
	if err != nil {
		return nil, err
	}
	if len(topics) == 0 {
		return nil, fmt.Errorf("no topic graphs found in %s", dir)
	}
	defaultTopic = strings.TrimSpace(defaultTopic)
	defaultKey := ""
	if defaultTopic != "" {
		key, ok := lookup[topicLookupKey(defaultTopic)]
		if !ok {
			return nil, fmt.Errorf("--default-topic %q does not match a graph in %s", defaultTopic, dir)
		}
		defaultKey = key
		defaultTopic = topicsByKey(topics)[key].Name
	}
	return &topicGenerationManager{
		ctx:          ctx,
		dir:          dir,
		defaultKey:   defaultKey,
		defaultTopic: defaultTopic,
		maxOpen:      maxOpen,
		build:        build,
		validate:     validate,
		interval:     interval,
		logger:       logger,
		topics:       topics,
		byKey:        topicsByKey(topics),
		lookup:       lookup,
		open:         map[string]*topicEntry{},
		lru:          list.New(),
	}, nil
}

func discoverTopicGraphs(dir string) ([]topicDefinition, map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	topics := make([]topicDefinition, 0, len(entries))
	lookup := map[string]string{}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		ext := supportedTopicGraphExt(name)
		if ext == "" {
			continue
		}
		aliases := topicGraphAliases(name, ext)
		if len(aliases) == 0 {
			continue
		}
		topicName := aliases[0]
		key := topicLookupKey(topicName)
		if _, ok := lookup[key]; ok {
			return nil, nil, fmt.Errorf("duplicate topic graph %q in %s", topicName, dir)
		}
		path := filepath.Join(dir, name)
		tokens, err := topicDescriptorTokens(dir, aliases)
		if err != nil {
			return nil, nil, err
		}
		addTokens(tokens, topicName)
		def := topicDefinition{
			Key:    key,
			Name:   topicName,
			Path:   path,
			Tokens: tokens,
		}
		topics = append(topics, def)
		for _, alias := range aliases {
			aliasKey := topicLookupKey(alias)
			if aliasKey == "" {
				continue
			}
			if existing, ok := lookup[aliasKey]; ok && existing != key {
				return nil, nil, fmt.Errorf("topic alias %q is ambiguous in %s", alias, dir)
			}
			lookup[aliasKey] = key
		}
	}
	sort.Slice(topics, func(i, j int) bool { return topics[i].Name < topics[j].Name })
	return topics, lookup, nil
}

func supportedTopicGraphExt(name string) string {
	ext := filepath.Ext(name)
	if supportedTopicGraphExts[strings.ToLower(ext)] {
		return ext
	}
	return ""
}

func topicGraphAliases(name string, ext string) []string {
	base := strings.TrimSuffix(name, ext)
	primary := strings.TrimSuffix(base, ".g_g")
	aliases := []string{primary}
	if base != primary {
		aliases = append(aliases, base)
	}
	return uniqueNonEmpty(aliases)
}

func topicDescriptorTokens(dir string, aliases []string) (map[string]struct{}, error) {
	tokens := map[string]struct{}{}
	for _, alias := range aliases {
		for _, ext := range []string{".topic", ".desc", ".descriptor", ".keywords", ".txt", ".md", ".json"} {
			path := filepath.Join(dir, alias+ext)
			data, err := os.ReadFile(path)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, fmt.Errorf("read topic descriptor %s: %w", path, err)
			}
			addTokens(tokens, string(data))
			return tokens, nil
		}
	}
	return tokens, nil
}

func (m *topicGenerationManager) AcquireGeneration(ctx context.Context, route generationRoute) (*generation, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	key, err := m.resolveTopic(ctx, route)
	if err != nil {
		return nil, func() {}, err
	}
	return m.acquireTopic(ctx, key)
}

func (m *topicGenerationManager) Acquire() (*generation, func()) {
	if m == nil {
		return nil, func() {}
	}
	m.mu.Lock()
	elem := m.lru.Front()
	if m.closed || elem == nil {
		m.mu.Unlock()
		return nil, func() {}
	}
	entry := m.open[elem.Value.(string)]
	gen, release := entry.store.Acquire()
	m.mu.Unlock()
	if gen == nil || gen.reasoner == nil {
		release()
		return nil, func() {}
	}
	return gen, release
}

func (m *topicGenerationManager) ProviderName() string {
	gen, release := m.Acquire()
	if gen == nil || gen.reasoner == nil {
		release()
		return "multi-topic"
	}
	defer release()
	return gen.reasoner.Name() + "+multi-topic"
}

func (m *topicGenerationManager) TopicSnapshot() topicServingSnapshot {
	if m == nil {
		return topicServingSnapshot{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	available := make([]string, 0, len(m.topics))
	for _, topic := range m.topics {
		available = append(available, topic.Name)
	}
	open := make([]openTopicSnapshot, 0, len(m.open))
	for elem := m.lru.Front(); elem != nil; elem = elem.Next() {
		entry := m.open[elem.Value.(string)]
		if entry == nil {
			continue
		}
		item := openTopicSnapshot{
			Topic: entry.def.Name,
			Path:  entry.def.Path,
		}
		gen, release := entry.store.Acquire()
		if gen != nil {
			item.GenerationID = gen.id
		}
		release()
		open = append(open, item)
	}
	return topicServingSnapshot{
		SourceDir:     m.dir,
		DefaultTopic:  m.defaultTopic,
		MaxOpenTopics: m.maxOpen,
		Available:     available,
		Open:          open,
	}
}

func (m *topicGenerationManager) Close() error {
	if m == nil {
		return nil
	}
	m.closeOnce.Do(func() {
		var entries []*topicEntry
		m.mu.Lock()
		m.closed = true
		for key, entry := range m.open {
			entries = append(entries, entry)
			delete(m.open, key)
		}
		m.lru.Init()
		m.mu.Unlock()
		var err error
		for _, entry := range entries {
			err = errors.Join(err, closeTopicEntry(entry))
		}
		m.closeWG.Wait()
		m.closeErr = err
	})
	return m.closeErr
}

func (m *topicGenerationManager) resolveTopic(ctx context.Context, route generationRoute) (string, error) {
	if m == nil {
		return "", errNoGeneration
	}
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		for _, raw := range md.Get(topicMetadataKey) {
			topic := strings.TrimSpace(raw)
			if topic == "" {
				continue
			}
			if key, ok := m.lookup[topicLookupKey(topic)]; ok {
				return key, nil
			}
		}
	}
	if key := m.keywordTopic(route.Text); key != "" {
		return key, nil
	}
	if m.defaultKey != "" {
		return m.defaultKey, nil
	}
	if len(m.topics) == 0 {
		return "", errNoGeneration
	}
	return m.topics[0].Key, nil
}

func (m *topicGenerationManager) keywordTopic(text string) string {
	queryTokens := tokenizeTopicText(text)
	if len(queryTokens) == 0 {
		return ""
	}
	bestScore := 0
	bestKey := ""
	bestName := ""
	for _, topic := range m.topics {
		score := tokenOverlap(queryTokens, topic.Tokens)
		if score == 0 {
			continue
		}
		if score > bestScore || (score == bestScore && (bestName == "" || topic.Name < bestName)) {
			bestScore = score
			bestKey = topic.Key
			bestName = topic.Name
		}
	}
	return bestKey
}

func (m *topicGenerationManager) acquireTopic(ctx context.Context, key string) (*generation, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, func() {}, err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, func() {}, errNoGeneration
	}
	if entry := m.open[key]; entry != nil {
		m.lru.MoveToFront(entry.elem)
		gen, release := entry.store.Acquire()
		m.mu.Unlock()
		if gen == nil || gen.reasoner == nil {
			release()
			return nil, func() {}, errNoGeneration
		}
		return gen, release, nil
	}
	def, ok := m.byKey[key]
	if !ok {
		m.mu.Unlock()
		return nil, func() {}, fmt.Errorf("topic %q not found", key)
	}
	gen, err := m.build(ctx, def.Path)
	if err != nil {
		m.mu.Unlock()
		return nil, func() {}, fmt.Errorf("open topic %s: %w", def.Name, err)
	}
	if gen == nil {
		m.mu.Unlock()
		return nil, func() {}, fmt.Errorf("open topic %s: nil generation", def.Name)
	}
	swapped := false
	defer func() {
		if !swapped {
			closeGeneration(gen)
		}
	}()
	if m.validate != nil {
		if err := m.validate(ctx, gen); err != nil {
			m.mu.Unlock()
			return nil, func() {}, err
		}
	}
	store := newGenerationStore(gen)
	entry := &topicEntry{
		def:   def,
		store: store,
	}
	entry.elem = m.lru.PushFront(key)
	m.open[key] = entry
	reloader := newSnapshotReloader(def.Path, m.interval, m.build, m.validate, store, m.logger)
	if fp, err := snapshotFingerprintForPath(def.Path); err == nil {
		reloader.setLast(fp)
	}
	reloader.Start(m.ctx)
	entry.reloader = reloader
	evicted := m.evictLocked(key)
	if len(evicted) > 0 {
		m.closeWG.Add(len(evicted))
	}
	gen, release := store.Acquire()
	swapped = true
	m.mu.Unlock()
	m.closeEntriesAsync(evicted)
	if gen == nil || gen.reasoner == nil {
		release()
		return nil, func() {}, errNoGeneration
	}
	if m.logger != nil {
		m.logger.Printf("topic open topic=%s path=%s generation=%s open=%d max_open=%d", def.Name, def.Path, gen.id, m.openCount(), m.maxOpen)
	}
	return gen, release, nil
}

func (m *topicGenerationManager) evictLocked(protectedKey string) []*topicEntry {
	var evicted []*topicEntry
	for len(m.open) > m.maxOpen {
		elem := m.lru.Back()
		if elem == nil {
			break
		}
		key, _ := elem.Value.(string)
		if key == protectedKey && m.lru.Len() == 1 {
			break
		}
		entry := m.open[key]
		delete(m.open, key)
		m.lru.Remove(elem)
		if entry != nil {
			entry.elem = nil
			evicted = append(evicted, entry)
		}
	}
	return evicted
}

func (m *topicGenerationManager) closeEntriesAsync(entries []*topicEntry) {
	for _, entry := range entries {
		go func(entry *topicEntry) {
			defer m.closeWG.Done()
			if err := closeTopicEntry(entry); err != nil && m.logger != nil {
				m.logger.Printf("topic close topic=%s error=%v", entry.def.Name, err)
			}
		}(entry)
	}
}

func (m *topicGenerationManager) openCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.open)
}

func closeTopicEntry(entry *topicEntry) error {
	if entry == nil {
		return nil
	}
	if entry.reloader != nil {
		entry.reloader.Close()
	}
	if entry.store != nil {
		return entry.store.Close()
	}
	return nil
}

func topicsByKey(topics []topicDefinition) map[string]topicDefinition {
	out := make(map[string]topicDefinition, len(topics))
	for _, topic := range topics {
		out[topic.Key] = topic
	}
	return out
}

func topicLookupKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func tokenizeTopicText(text string) map[string]struct{} {
	tokens := map[string]struct{}{}
	addTokens(tokens, text)
	return tokens
}

func addTokens(tokens map[string]struct{}, text string) {
	for _, token := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		tokens[token] = struct{}{}
	}
}

func tokenOverlap(a map[string]struct{}, b map[string]struct{}) int {
	score := 0
	for token := range a {
		if _, ok := b[token]; ok {
			score++
		}
	}
	return score
}

func uniqueNonEmpty(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := topicLookupKey(value)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
	}
	return out
}

type generationGraphQuerier struct {
	source generationAcquirer
}

func (g generationGraphQuerier) Query(ctx context.Context, query string, params map[string]any) ([]map[string]any, error) {
	if g.source == nil {
		return nil, errNoGeneration
	}
	gen, release := g.source.Acquire()
	if gen == nil || gen.pool == nil {
		release()
		return nil, errNoGeneration
	}
	defer release()
	return gen.pool.Query(ctx, query, params)
}
