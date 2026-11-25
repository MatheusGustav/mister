// Package memory is the long-term memory: markdown notes in a folder, linked
// by [[name]], found by meaning through an embedding index.
package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Document is a piece of a note handed to the embedder.
type Document struct {
	Title string
	Text  string
}

// Embedder turns text into vectors. Implementations own whatever the model
// needs (task prefixes, normalization) and are swappable.
type Embedder interface {
	// ID names the model; the index is rebuilt when it changes.
	ID() string
	EmbedQuery(ctx context.Context, query string) ([]float32, error)
	EmbedDocuments(ctx context.Context, docs []Document) ([][]float32, error)
}

// ErrNoEmbedder is returned by searches when no embedding provider is set.
var ErrNoEmbedder = errors.New("a memória está sem provedor de embedding configurado")

// Thresholds decide what counts as related. They depend on the model.
type Thresholds struct {
	// Entry is the least similarity for the best note to be recalled.
	Entry float64
	// Walk is the least similarity for a linked note to be followed.
	Walk float64
}

type Store struct {
	dir        string
	indexFile  string
	embedder   Embedder
	thresholds Thresholds

	mu sync.Mutex
}

// Open uses dir for the notes and indexFile for the embedding index. A nil
// embedder leaves writing on and searching off.
func Open(dir, indexFile string, embedder Embedder, thresholds Thresholds) *Store {
	return &Store{dir: dir, indexFile: indexFile, embedder: embedder, thresholds: thresholds}
}

type Note struct {
	Name  string
	Body  string
	Score float64
}

var slugRe = regexp.MustCompile(`[a-z0-9]+`)

// accents maps the accented letters of Latin languages to their base letter.
var accents = strings.NewReplacer(
	"á", "a", "à", "a", "â", "a", "ã", "a", "ä", "a", "å", "a",
	"é", "e", "è", "e", "ê", "e", "ë", "e",
	"í", "i", "ì", "i", "î", "i", "ï", "i",
	"ó", "o", "ò", "o", "ô", "o", "õ", "o", "ö", "o",
	"ú", "u", "ù", "u", "û", "u", "ü", "u",
	"ç", "c", "ñ", "n",
)

// Slug is the file name of a note: no accents, lowercase, hyphens. Writing a
// note and resolving a [[link]] go through it, so both reach the same file.
func Slug(title string) string {
	plain := accents.Replace(strings.ToLower(title))
	return strings.Join(slugRe.FindAllString(plain, -1), "-")
}

func (s *Store) path(name string) string {
	return filepath.Join(s.dir, Slug(name)+".md")
}

// Write saves a fact. A new note starts with its title; an existing one gets
// the content at the end, nothing is erased.
func (s *Store) Write(title, content string) (string, error) {
	name := Slug(title)
	content = strings.TrimSpace(content)
	switch {
	case name == "":
		return "", errors.New("o título precisa ter pelo menos uma letra ou número")
	case content == "":
		return "", errors.New("a nota veio sem conteúdo")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return "", fmt.Errorf("criando a pasta da memória: %w", err)
	}
	path := s.path(name)
	current, err := os.ReadFile(path)
	var body string
	switch {
	case err == nil:
		body = strings.TrimRight(string(current), "\n") + "\n\n" + content + "\n"
	case errors.Is(err, fs.ErrNotExist):
		body = "# " + strings.TrimSpace(title) + "\n\n" + content + "\n"
	default:
		return "", fmt.Errorf("lendo a nota %s: %w", name, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return "", fmt.Errorf("gravando a nota %s: %w", name, err)
	}
	return name, nil
}

// Read returns the whole text of a note.
func (s *Store) Read(name string) (string, error) {
	data, err := os.ReadFile(s.path(name))
	if err != nil {
		return "", fmt.Errorf("lendo a nota %s: %w", Slug(name), err)
	}
	return strings.TrimSpace(string(data)), nil
}

var headingRe = regexp.MustCompile(`^#{1,6} `)

// Chunk splits a note by markdown heading, so a long note is matched by its
// best section instead of by a blur of all of them.
func Chunk(text string) []string {
	var chunks []string
	var current []string
	flush := func() {
		if c := strings.TrimSpace(strings.Join(current, "\n")); c != "" {
			chunks = append(chunks, c)
		}
		current = nil
	}
	for _, line := range strings.Split(text, "\n") {
		if headingRe.MatchString(line) {
			flush()
		}
		current = append(current, line)
	}
	flush()
	return chunks
}

type indexEntry struct {
	// Mark is the note's mtime and size when it was embedded.
	Mark     string      `json:"marca"`
	Sections [][]float32 `json:"secoes"`
}

type index struct {
	Model string                `json:"modelo"`
	Notes map[string]indexEntry `json:"notas"`
}

// sync brings the index up to date with the folder: only new or changed notes
// are embedded again, deleted ones leave.
func (s *Store) sync(ctx context.Context) (index, error) {
	old := index{Notes: map[string]indexEntry{}}
	if data, err := os.ReadFile(s.indexFile); err == nil {
		var loaded index
		if json.Unmarshal(data, &loaded) == nil && loaded.Model == s.embedder.ID() && loaded.Notes != nil {
			old = loaded
		}
	}

	files, err := filepath.Glob(filepath.Join(s.dir, "*.md"))
	if err != nil {
		return index{}, fmt.Errorf("listando as notas: %w", err)
	}
	sort.Strings(files)

	fresh := index{Model: s.embedder.ID(), Notes: make(map[string]indexEntry, len(files))}
	type pending struct {
		name   string
		mark   string
		chunks []string
	}
	var todo []pending
	var docs []Document
	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil {
			continue
		}
		name := strings.TrimSuffix(filepath.Base(file), ".md")
		mark := fmt.Sprintf("%d:%d", info.ModTime().UnixNano(), info.Size())
		if entry, ok := old.Notes[name]; ok && entry.Mark == mark {
			fresh.Notes[name] = entry
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		chunks := Chunk(string(data))
		if len(chunks) == 0 {
			continue
		}
		todo = append(todo, pending{name: name, mark: mark, chunks: chunks})
		for _, c := range chunks {
			docs = append(docs, Document{Title: name, Text: c})
		}
	}

	changed := len(todo) > 0 || len(fresh.Notes) != len(old.Notes)
	if len(docs) > 0 {
		vectors, err := s.embedder.EmbedDocuments(ctx, docs)
		if err != nil {
			return index{}, fmt.Errorf("gerando os embeddings das notas: %w", err)
		}
		if len(vectors) != len(docs) {
			return index{}, fmt.Errorf("o provedor de embedding devolveu %d vetores para %d textos", len(vectors), len(docs))
		}
		cursor := 0
		for _, p := range todo {
			fresh.Notes[p.name] = indexEntry{Mark: p.mark, Sections: vectors[cursor : cursor+len(p.chunks)]}
			cursor += len(p.chunks)
		}
	}
	if changed {
		// The index is derived data: failing to save it only costs time later.
		if data, err := json.Marshal(fresh); err == nil {
			_ = os.WriteFile(s.indexFile, data, 0o644)
		}
	}
	return fresh, nil
}

// Search ranks the notes by how close their best section is to the query.
func (s *Store) Search(ctx context.Context, query string) ([]Note, error) {
	if s.embedder == nil {
		return nil, ErrNoEmbedder
	}
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, err := s.sync(ctx)
	if err != nil {
		return nil, err
	}
	if len(idx.Notes) == 0 {
		return nil, nil
	}
	qv, err := s.embedder.EmbedQuery(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("gerando o embedding da busca: %w", err)
	}
	notes := make([]Note, 0, len(idx.Notes))
	for name, entry := range idx.Notes {
		best := -1.0
		for _, section := range entry.Sections {
			best = math.Max(best, cosine(qv, section))
		}
		notes = append(notes, Note{Name: name, Score: best})
	}
	sort.Slice(notes, func(i, j int) bool {
		if notes[i].Score != notes[j].Score {
			return notes[i].Score > notes[j].Score
		}
		return notes[i].Name < notes[j].Name
	})
	return notes, nil
}

var linkRe = regexp.MustCompile(`\[\[([^\[\]]+)\]\]`)

// Recall returns the notes related to a text: the closest one, when it is
// close enough, and then the notes it links to that are still on the subject.
// Nothing related returns nothing: a quiet memory beats a nosy one.
func (s *Store) Recall(ctx context.Context, text string) ([]Note, error) {
	ranked, err := s.Search(ctx, text)
	if err != nil {
		return nil, err
	}
	if len(ranked) == 0 || ranked[0].Score < s.thresholds.Entry {
		return nil, nil
	}
	scores := make(map[string]float64, len(ranked))
	for _, n := range ranked {
		scores[n.Name] = n.Score
	}
	queue := []string{ranked[0].Name}
	seen := map[string]bool{ranked[0].Name: true}
	var notes []Note
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		body, err := s.Read(name)
		if err != nil {
			continue
		}
		notes = append(notes, Note{Name: name, Body: body, Score: scores[name]})
		for _, link := range linkRe.FindAllStringSubmatch(body, -1) {
			target := Slug(link[1])
			if score, ok := scores[target]; ok && !seen[target] && score >= s.thresholds.Walk {
				seen[target] = true
				queue = append(queue, target)
			}
		}
	}
	return notes, nil
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
