package agent

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/MatheusGustav/mister/internal/provider"
)

// Status of an agent inside a session.
const (
	StatusIdle        = "parado"
	StatusRunning     = "rodando"
	StatusDone        = "terminou"
	StatusFailed      = "falhou"
	StatusInterrupted = "interrompido"
)

// AgentMeta is what the session file keeps about an agent.
type AgentMeta struct {
	ID     string `json:"id"`
	Role   string `json:"agente"`
	Model  string `json:"modelo"`
	Effort string `json:"esforco,omitempty"`
	// Root is the directory the agent works in.
	Root   string `json:"pasta"`
	Branch string `json:"branch,omitempty"`
	Task   string `json:"tarefa,omitempty"`
	Status string `json:"status"`
	Report string `json:"relatorio,omitempty"`
	// Reported says the host already received the report.
	Reported bool `json:"entregue,omitempty"`
}

// SessionMeta is the content of a session file.
type SessionMeta struct {
	ID      string      `json:"id"`
	Project string      `json:"projeto"`
	Created time.Time   `json:"criada"`
	Updated time.Time   `json:"atualizada"`
	Host    AgentMeta   `json:"host"`
	Threads []AgentMeta `json:"threads"`
}

// Store keeps one session on disk: the session file and one append-only
// history file per agent.
type Store struct {
	dir string
	mu  sync.Mutex
}

const sessionFile = "sessao.json"

// OpenStore opens (creating if needed) the directory of a session.
func OpenStore(sessionsDir, id string) (*Store, error) {
	dir := filepath.Join(sessionsDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("criando a pasta da sessão: %w", err)
	}
	return &Store{dir: dir}, nil
}

func (s *Store) Dir() string { return s.dir }

type record struct {
	// Type is "msg" (one message appended) or "reset" (history replaced, after
	// a compaction).
	Type     string             `json:"t"`
	Message  *provider.Message  `json:"msg,omitempty"`
	Messages []provider.Message `json:"msgs,omitempty"`
}

// Append adds one message to the history of an agent.
func (s *Store) Append(agentID string, msg provider.Message) error {
	return s.write(agentID, record{Type: "msg", Message: &msg})
}

// Reset records that the history of an agent was replaced.
func (s *Store) Reset(agentID string, msgs []provider.Message) error {
	return s.write(agentID, record{Type: "reset", Messages: msgs})
}

func (s *Store) historyPath(agentID string) string {
	return filepath.Join(s.dir, agentID+".jsonl")
}

func (s *Store) write(agentID string, rec record) error {
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("serializando o histórico de %s: %w", agentID, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.historyPath(agentID), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("abrindo o histórico de %s: %w", agentID, err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return fmt.Errorf("gravando o histórico de %s: %w", agentID, err)
	}
	// The history must survive a crash right after the write.
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("gravando o histórico de %s: %w", agentID, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("fechando o histórico de %s: %w", agentID, err)
	}
	return nil
}

// Load rebuilds the history of an agent. A line cut by a crash is ignored.
func (s *Store) Load(agentID string) ([]provider.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.Open(s.historyPath(agentID))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("abrindo o histórico de %s: %w", agentID, err)
	}
	defer f.Close()

	var history []provider.Message
	reader := bufio.NewReader(f)
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			var rec record
			if json.Unmarshal(line, &rec) == nil {
				switch {
				case rec.Type == "msg" && rec.Message != nil:
					history = append(history, *rec.Message)
				case rec.Type == "reset":
					history = append([]provider.Message(nil), rec.Messages...)
				}
			}
		}
		if readErr != nil {
			break
		}
	}
	return history, nil
}

// SaveMeta writes the session file atomically.
func (s *Store) SaveMeta(meta SessionMeta) error {
	meta.Updated = time.Now()
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("serializando a sessão: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tmp := filepath.Join(s.dir, sessionFile+".tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("gravando a sessão: %w", err)
	}
	if err := os.Rename(tmp, filepath.Join(s.dir, sessionFile)); err != nil {
		return fmt.Errorf("gravando a sessão: %w", err)
	}
	return nil
}

func (s *Store) LoadMeta() (SessionMeta, error) {
	return readMeta(filepath.Join(s.dir, sessionFile))
}

func readMeta(path string) (SessionMeta, error) {
	var meta SessionMeta
	data, err := os.ReadFile(path)
	if err != nil {
		return meta, fmt.Errorf("lendo a sessão: %w", err)
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return meta, fmt.Errorf("sessão ilegível em %s: %w", path, err)
	}
	return meta, nil
}

// ListSessions returns the saved sessions, most recent first. Sessions whose
// file cannot be read are skipped.
func ListSessions(sessionsDir string) ([]SessionMeta, error) {
	entries, err := os.ReadDir(sessionsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("listando as sessões: %w", err)
	}
	var sessions []SessionMeta
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		meta, err := readMeta(filepath.Join(sessionsDir, e.Name(), sessionFile))
		if err != nil {
			continue
		}
		sessions = append(sessions, meta)
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].Updated.After(sessions[j].Updated) })
	return sessions, nil
}

// NewSessionID names a session after the moment it starts.
func NewSessionID(now time.Time) string {
	return now.Format("2006-01-02-150405")
}
