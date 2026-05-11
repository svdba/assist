// internal/secretary/task.go

package secretary

import (
	"sync"
	"time"
)

// internal/secretary/task.go

const (
	SessionTypeDefault   SessionType = "default"
	SessionTypeForwarded SessionType = "forwarded"
	SessionTypeManual    SessionType = "manual"
	SessionTypeTimed     SessionType = "timed"
)

type SessionType string

// Task represents a work task (Jira ticket or internal task)
type Task struct {
	ID         string    `json:"id"`          // internal ID (int-0420-1505)
	ExternalID string    `json:"external_id"` // Jira ID if exists (PROJ-123)
	Alias      string    `json:"alias"`       // user-defined short name (jenkins_down)
	Name       string    `json:"name"`        // description or first message
	CreatedAt  time.Time `json:"created_at"`
	LastUsed   time.Time `json:"last_used"`
}

// Session represents a continuous work period
type Session struct {
	ID        string      `json:"id"`
	TaskID    string      `json:"task_id"`
	Type      SessionType `json:"type"`
	StartedAt time.Time   `json:"started_at"`
	EndedAt   *time.Time  `json:"ended_at,omitempty"`
	TimeoutAt *time.Time  `json:"timeout_at,omitempty"`
	Messages  []Message   `json:"messages"`
}

// Message represents a forwarded or manual message
type Message struct {
	ID         string    `json:"id"`
	SessionID  string    `json:"session_id"`
	Timestamp  time.Time `json:"timestamp"`
	SenderName string    `json:"sender_name"`
	Text       string    `json:"text"`
}

// TaskRegistry manages in-memory task storage (for future use)
type TaskRegistry struct {
	tasks      map[string]*Task  // key: internal ID
	aliasIndex map[string]string // alias -> internal ID
	mu         sync.RWMutex
}

// NewTaskRegistry creates a new task registry
func NewTaskRegistry() *TaskRegistry {
	return &TaskRegistry{
		tasks:      make(map[string]*Task),
		aliasIndex: make(map[string]string),
	}
}

// GetOrCreateByAlias returns task by alias, creates new if not exists
func (r *TaskRegistry) GetOrCreateByAlias(alias string, startTime time.Time) *Task {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Check if alias exists
	if internalID, ok := r.aliasIndex[alias]; ok {
		if task, exists := r.tasks[internalID]; exists {
			task.LastUsed = startTime
			return task
		}
	}

	// Create new task with alias
	internalID := GenerateInternalID(startTime)
	task := &Task{
		ID:        internalID,
		Alias:     alias,
		CreatedAt: startTime,
		LastUsed:  startTime,
	}
	r.tasks[internalID] = task
	r.aliasIndex[alias] = internalID

	return task
}
