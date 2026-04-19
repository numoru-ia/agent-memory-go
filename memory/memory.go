// Package memory implements the tiered memory pattern for production LLM agents.
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Turn is a single conversational unit.
type Turn struct {
	Role      string    `json:"role"` // "user" | "assistant" | "tool"
	Content   string    `json:"content"`
	Tokens    int       `json:"tokens"`
	CreatedAt time.Time `json:"created_at"`
}

// SessionState holds the working memory for one conversation.
type SessionState struct {
	SessionID   string            `json:"session_id"`
	UserID      string            `json:"user_id"`
	TenantID    string            `json:"tenant_id"`
	Turns       []Turn            `json:"turns"`
	Summary     string            `json:"summary"`
	Slots       map[string]string `json:"slots"`
	TokenBudget int               `json:"token_budget"`
}

// Fact is a consolidated piece of semantic memory about a user.
type Fact struct {
	Content string  `json:"content"`
	Score   float64 `json:"score"`
	Source  string  `json:"source,omitempty"`
}

// Summarizer condenses older turns into a short summary.
type Summarizer interface {
	Summarize(ctx context.Context, in SummarizeInput) (string, error)
}

type SummarizeInput struct {
	PreviousSummary string
	NewTurns        []Turn
}

// WorkingOpts configures WorkingMemory.
type WorkingOpts struct {
	TTL        time.Duration
	WindowSize int
	Summarizer Summarizer
	KeyPrefix  string
}

// WorkingMemory stores per-session state in Redis.
type WorkingMemory struct {
	rdb  *redis.Client
	opts WorkingOpts
}

func NewWorking(rdb *redis.Client, opts WorkingOpts) *WorkingMemory {
	if opts.TTL == 0 {
		opts.TTL = time.Hour
	}
	if opts.WindowSize == 0 {
		opts.WindowSize = 6
	}
	if opts.KeyPrefix == "" {
		opts.KeyPrefix = "mem:session:"
	}
	return &WorkingMemory{rdb: rdb, opts: opts}
}

func (w *WorkingMemory) key(sessionID string) string {
	return w.opts.KeyPrefix + sessionID
}

// Load returns the state for a session, or a fresh one if none exists.
func (w *WorkingMemory) Load(ctx context.Context, sessionID string) (*SessionState, error) {
	data, err := w.rdb.Get(ctx, w.key(sessionID)).Bytes()
	if err == redis.Nil {
		return &SessionState{SessionID: sessionID, Slots: map[string]string{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var s SessionState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	if s.Slots == nil {
		s.Slots = map[string]string{}
	}
	return &s, nil
}

// Append adds a turn and, if the window is exceeded, compacts older turns.
func (w *WorkingMemory) Append(ctx context.Context, s *SessionState, t Turn) error {
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now()
	}
	s.Turns = append(s.Turns, t)

	if len(s.Turns) > w.opts.WindowSize && w.opts.Summarizer != nil {
		if err := w.compact(ctx, s); err != nil {
			return fmt.Errorf("compact: %w", err)
		}
	}

	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return w.rdb.Set(ctx, w.key(s.SessionID), b, w.opts.TTL).Err()
}

func (w *WorkingMemory) compact(ctx context.Context, s *SessionState) error {
	oldest := s.Turns[:len(s.Turns)-w.opts.WindowSize]
	s.Turns = s.Turns[len(s.Turns)-w.opts.WindowSize:]
	summary, err := w.opts.Summarizer.Summarize(ctx, SummarizeInput{
		PreviousSummary: s.Summary,
		NewTurns:        oldest,
	})
	if err != nil {
		return err
	}
	s.Summary = summary
	return nil
}

// Delete wipes working memory for a session.
func (w *WorkingMemory) Delete(ctx context.Context, sessionID string) error {
	return w.rdb.Del(ctx, w.key(sessionID)).Err()
}

// Semantic is a semantic long-term memory backend (Mem0, Zep, or custom).
type Semantic interface {
	Search(ctx context.Context, userID, query string, limit int) ([]Fact, error)
	AddAsync(ctx context.Context, userID string, conv []Turn)
	Delete(ctx context.Context, userID, factID string) error
}

// Audit writes traces to an external observability backend (Langfuse).
type Audit interface {
	TraceTurn(ctx context.Context, s *SessionState, input, output string) error
}

// Manager coordinates the three layers.
type Manager struct {
	Working  *WorkingMemory
	Semantic Semantic
	Audit    Audit
}

// BuildPrompt assembles a turn prompt from the three memory layers.
// The caller is responsible for feeding the result to the LLM.
func (m *Manager) BuildPrompt(systemPrompt string, state *SessionState, facts []Fact, userMessage string) string {
	var b strings.Builder
	b.WriteString(systemPrompt)
	if len(facts) > 0 {
		b.WriteString("\n\n## Hechos sobre el usuario\n")
		for _, f := range facts {
			fmt.Fprintf(&b, "- %s (confianza %.2f)\n", f.Content, f.Score)
		}
	}
	if state.Summary != "" {
		b.WriteString("\n\n## Resumen de la conversación previa\n")
		b.WriteString(state.Summary)
	}
	if len(state.Turns) > 0 {
		b.WriteString("\n\n## Últimos turnos\n")
		for _, t := range state.Turns {
			fmt.Fprintf(&b, "[%s] %s\n", t.Role, t.Content)
		}
	}
	fmt.Fprintf(&b, "\n[user] %s", userMessage)
	return b.String()
}
