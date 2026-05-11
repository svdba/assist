// internal/secretary/storage_sqlite.go

package secretary

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/svdba/assist/internal/logging"
	_ "modernc.org/sqlite"
)

// SQLiteStorage handles persistent storage using SQLite
type SQLiteStorage struct {
	db *sql.DB
}

// NewSQLiteStorage creates a new SQLite storage instance
func NewSQLiteStorage(dataDir string) (*SQLiteStorage, error) {
	dbPath := dataDir + "/secretary.db"

	db, err := sql.Open("sqlite", dbPath+"?_journal=WAL&_fk=1")
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// SQLite works best with single writer
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	storage := &SQLiteStorage{db: db}

	if err := storage.migrate(); err != nil {
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	return storage, nil
}

// migrate creates tables if they don't exist
func (s *SQLiteStorage) migrate() error {
	// Check if we need to migrate
	var hasUniqueConstraint bool
	row := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master 
        WHERE type='index' AND name='sqlite_autoindex_tasks_1'`)
	row.Scan(&hasUniqueConstraint)

	if hasUniqueConstraint {
		// Recreate table without UNIQUE constraint
		queries := []string{
			`ALTER TABLE tasks RENAME TO tasks_old`,
			`CREATE TABLE tasks (
                id TEXT PRIMARY KEY,
                external_id TEXT,
                alias TEXT,
                name TEXT,
                created_at DATETIME NOT NULL,
                last_used DATETIME NOT NULL
            )`,
			`INSERT INTO tasks (id, external_id, alias, name, created_at, last_used)
             SELECT id, external_id, alias, name, created_at, last_used FROM tasks_old`,
			`DROP TABLE tasks_old`,
			`CREATE INDEX IF NOT EXISTS idx_tasks_alias ON tasks(alias)`,
		}

		for _, query := range queries {
			if _, err := s.db.Exec(query); err != nil {
				return fmt.Errorf("migration failed: %q: %w", query, err)
			}
		}
	}

	queries := []string{
		// Tasks table
		`CREATE TABLE IF NOT EXISTS tasks (
			id TEXT PRIMARY KEY,
			external_id TEXT,
			alias TEXT,
			name TEXT,
			created_at DATETIME NOT NULL,
			last_used DATETIME NOT NULL
		)`,

		// Sessions table
		`CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY,
			task_id TEXT NOT NULL,
			started_at DATETIME NOT NULL,
			ended_at DATETIME,
			timeout_at DATETIME,
			FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE
		)`,

		// Messages table
		`CREATE TABLE IF NOT EXISTS messages (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			timestamp DATETIME NOT NULL,
			sender_name TEXT NOT NULL,
			text TEXT NOT NULL,
			FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
		)`,

		// Events table (for future audit trail)
		`CREATE TABLE IF NOT EXISTS events (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			timestamp DATETIME NOT NULL,
			type TEXT NOT NULL,
			source TEXT NOT NULL,
			data_json TEXT,
			FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
		)`,

		// Indexes
		`CREATE INDEX IF NOT EXISTS idx_sessions_task_id ON sessions(task_id)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_started_at ON sessions(started_at)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_ended_at ON sessions(ended_at)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_session_id ON messages(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_alias ON tasks(alias)`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_last_used ON tasks(last_used)`,
	}

	for _, query := range queries {
		if _, err := s.db.Exec(query); err != nil {
			return fmt.Errorf("migration failed: %q: %w", query, err)
		}
	}

	// Add timeout_at column if not exists
	_, err := s.db.Exec(`ALTER TABLE sessions ADD COLUMN timeout_at DATETIME`)
	if err != nil {
		// Column might already exist, ignore error
		logging.Debug(context.Background(), "Adding timeout_at column (may already exist)", "error", err)
	}

	return nil
}

// Close closes the database connection
func (s *SQLiteStorage) Close() error {
	return s.db.Close()
}

// SaveTask inserts or updates a task
func (s *SQLiteStorage) SaveTask(task *Task) error {
	query := `INSERT INTO tasks (id, external_id, alias, name, created_at, last_used)
			  VALUES (?, ?, ?, ?, ?, ?)
			  ON CONFLICT(id) DO UPDATE SET
				external_id = excluded.external_id,
				alias = excluded.alias,
				name = excluded.name,
				last_used = excluded.last_used`

	_, err := s.db.Exec(query, task.ID, task.ExternalID, task.Alias, task.Name, task.CreatedAt, task.LastUsed)
	return err
}

// FindTaskByID finds a task by its internal ID
func (s *SQLiteStorage) FindTaskByID(id string) (*Task, error) {
	var task Task
	query := `SELECT id, external_id, alias, name, created_at, last_used
			  FROM tasks WHERE id = ?`

	row := s.db.QueryRow(query, id)
	err := row.Scan(&task.ID, &task.ExternalID, &task.Alias, &task.Name, &task.CreatedAt, &task.LastUsed)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &task, nil
}

// FindTaskByAlias finds a task by its alias
func (s *SQLiteStorage) FindTaskByAlias(alias string) (*Task, error) {
	var task Task
	query := `SELECT id, external_id, alias, name, created_at, last_used
			  FROM tasks WHERE alias = ?`

	row := s.db.QueryRow(query, alias)
	err := row.Scan(&task.ID, &task.ExternalID, &task.Alias, &task.Name, &task.CreatedAt, &task.LastUsed)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &task, nil
}

// GetActiveTasks returns all tasks used recently (default 24 hours)
func (s *SQLiteStorage) GetActiveTasks(since time.Time) ([]Task, error) {
	query := `SELECT id, external_id, alias, name, created_at, last_used
			  FROM tasks WHERE last_used >= ? ORDER BY last_used DESC`

	rows, err := s.db.Query(query, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []Task
	for rows.Next() {
		var task Task
		if err := rows.Scan(&task.ID, &task.ExternalID, &task.Alias, &task.Name, &task.CreatedAt, &task.LastUsed); err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, nil
}

// SaveSession inserts or updates a session with its messages
func (s *SQLiteStorage) SaveSession(session *Session) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Insert or replace session with timeout_at
	query := `INSERT INTO sessions (id, task_id, started_at, ended_at, timeout_at)
              VALUES (?, ?, ?, ?, ?)
              ON CONFLICT(id) DO UPDATE SET
                task_id = excluded.task_id,
                started_at = excluded.started_at,
                ended_at = excluded.ended_at,
                timeout_at = excluded.timeout_at`

	_, err = tx.Exec(query, session.ID, session.TaskID, session.StartedAt, session.EndedAt, session.TimeoutAt)
	if err != nil {
		return err
	}

	// Save messages
	for _, msg := range session.Messages {
		msgQuery := `INSERT INTO messages (id, session_id, timestamp, sender_name, text)
					 VALUES (?, ?, ?, ?, ?)
					 ON CONFLICT(id) DO UPDATE SET
					   timestamp = excluded.timestamp,
					   sender_name = excluded.sender_name,
					   text = excluded.text`

		if _, err := tx.Exec(msgQuery, msg.ID, msg.SessionID, msg.Timestamp, msg.SenderName, msg.Text); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// GetActiveSessions returns all unfinished sessions
func (s *SQLiteStorage) GetActiveSessions() ([]Session, error) {
	query := `SELECT id, task_id, started_at, ended_at, timeout_at
              FROM sessions WHERE ended_at IS NULL ORDER BY started_at ASC`

	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []Session
	for rows.Next() {
		var sess Session
		var endedAt, timeoutAt sql.NullTime
		if err := rows.Scan(&sess.ID, &sess.TaskID, &sess.StartedAt, &endedAt, &timeoutAt); err != nil {
			return nil, err
		}
		if endedAt.Valid {
			sess.EndedAt = &endedAt.Time
		}
		if timeoutAt.Valid {
			sess.TimeoutAt = &timeoutAt.Time
		}
		sessions = append(sessions, sess)
	}
	return sessions, nil
}

// GetSessionsByTask returns all sessions for a specific task
func (s *SQLiteStorage) GetSessionsByTask(taskID string) ([]Session, error) {
	query := `SELECT id, task_id, started_at, ended_at
			  FROM sessions WHERE task_id = ? ORDER BY started_at ASC`

	rows, err := s.db.Query(query, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []Session
	for rows.Next() {
		var sess Session
		if err := rows.Scan(&sess.ID, &sess.TaskID, &sess.StartedAt, &sess.EndedAt); err != nil {
			return nil, err
		}
		sessions = append(sessions, sess)
	}
	return sessions, nil
}

// GetDailyReport returns aggregated time per task for a specific date (in minutes)
func (s *SQLiteStorage) GetDailyReport(date time.Time) (map[string]int64, error) {
	startOfDay := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, date.Location())
	endOfDay := startOfDay.Add(24 * time.Hour)

	query := `SELECT t.id, COALESCE(t.alias, t.id), 
					 SUM(CAST(strftime('%s', ended_at) AS INTEGER) - CAST(strftime('%s', started_at) AS INTEGER))
			  FROM sessions s
			  JOIN tasks t ON s.task_id = t.id
			  WHERE s.started_at >= ? AND s.started_at < ? AND s.ended_at IS NOT NULL
			  GROUP BY t.id`

	rows, err := s.db.Query(query, startOfDay, endOfDay)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	report := make(map[string]int64)
	for rows.Next() {
		var taskID, taskName string
		var seconds int64
		if err := rows.Scan(&taskID, &taskName, &seconds); err != nil {
			return nil, err
		}
		report[taskName] = seconds / 60 // Convert to minutes
	}
	return report, nil
}

// internal/secretary/storage_sqlite.go

// GetSessionsInRange returns all sessions within a time range
func (s *SQLiteStorage) GetSessionsInRange(ctx context.Context, start, end time.Time) ([]Session, error) {
	query := `SELECT id, task_id, started_at, ended_at, timeout_at
              FROM sessions 
              WHERE started_at >= ? AND started_at < ?
              ORDER BY started_at ASC`

	rows, err := s.db.Query(query, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []Session
	sessionIDs := make([]string, 0)

	for rows.Next() {
		var sess Session
		var endedAt, timeoutAt sql.NullTime
		if err := rows.Scan(&sess.ID, &sess.TaskID, &sess.StartedAt, &endedAt, &timeoutAt); err != nil {
			return nil, err
		}
		if endedAt.Valid {
			sess.EndedAt = &endedAt.Time
		}
		if timeoutAt.Valid {
			sess.TimeoutAt = &timeoutAt.Time
		}
		sessions = append(sessions, sess)
		sessionIDs = append(sessionIDs, sess.ID)
	}

	logging.Debug(ctx, "GetSessionsInRange: loaded sessions",
		"count", len(sessions),
		"session_ids", sessionIDs)

	// Load messages if there are sessions
	if len(sessionIDs) > 0 {
		logging.Debug(ctx, "GetSessionsInRange: loading messages for sessions",
			"session_count", len(sessionIDs))

		messagesMap, err := s.GetMessagesBySessionIDs(sessionIDs)
		if err != nil {
			logging.Error(ctx, "GetSessionsInRange: failed to load messages", "error", err)
			return nil, err
		}

		for i := range sessions {
			if msgs, ok := messagesMap[sessions[i].ID]; ok {
				sessions[i].Messages = msgs
			}
		}

		logging.Debug(ctx, "GetSessionsInRange: messages loaded",
			"total_messages", len(messagesMap))
	}

	logging.Debug(ctx, "GetSessionsInRange: done",
		"session_count", len(sessions))

	return sessions, nil
}

// GetMessagesBySession returns all messages for a specific session
func (s *SQLiteStorage) GetMessagesBySession(sessionID string) ([]Message, error) {
	query := `SELECT id, session_id, timestamp, sender_name, text
              FROM messages WHERE session_id = ?
              ORDER BY timestamp ASC`

	rows, err := s.db.Query(query, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var msg Message
		if err := rows.Scan(&msg.ID, &msg.SessionID, &msg.Timestamp, &msg.SenderName, &msg.Text); err != nil {
			return nil, err
		}
		messages = append(messages, msg)
	}
	return messages, nil
}

func (s *SQLiteStorage) GetMessagesBySessionIDs(sessionIDs []string) (map[string][]Message, error) {
	ctx := context.Background()
	logging.Debug(ctx, "GetMessagesBySessionIDs: start",
		"session_count", len(sessionIDs))

	if len(sessionIDs) == 0 {
		return make(map[string][]Message), nil
	}

	// Build query with placeholders
	placeholders := strings.Repeat("?,", len(sessionIDs))
	placeholders = placeholders[:len(placeholders)-1]

	query := fmt.Sprintf(`SELECT id, session_id, timestamp, sender_name, text
                          FROM messages 
                          WHERE session_id IN (%s)
                          ORDER BY timestamp ASC`, placeholders)

	logging.Debug(ctx, "GetMessagesBySessionIDs: executing query",
		"query", query,
		"args", sessionIDs)

	args := make([]interface{}, len(sessionIDs))
	for i, id := range sessionIDs {
		args[i] = id
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		logging.Error(ctx, "GetMessagesBySessionIDs: query failed", "error", err)
		return nil, err
	}
	defer rows.Close()

	messagesMap := make(map[string][]Message)
	rowCount := 0
	for rows.Next() {
		var msg Message
		if err := rows.Scan(&msg.ID, &msg.SessionID, &msg.Timestamp, &msg.SenderName, &msg.Text); err != nil {
			logging.Error(ctx, "GetMessagesBySessionIDs: scan failed", "error", err)
			return nil, err
		}
		messagesMap[msg.SessionID] = append(messagesMap[msg.SessionID], msg)
		rowCount++
	}

	logging.Debug(ctx, "GetMessagesBySessionIDs: done",
		"row_count", rowCount,
		"session_map_size", len(messagesMap))

	return messagesMap, nil
}

// DeleteSessionsInRange deletes all sessions within a time range
func (s *SQLiteStorage) DeleteSessionsInRange(start, end time.Time) (int64, error) {
	query := `DELETE FROM sessions WHERE started_at >= ? AND started_at < ?`
	result, err := s.db.Exec(query, start, end)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// DeleteAllSessions deletes all sessions
func (s *SQLiteStorage) DeleteAllSessions() (int64, error) {
	result, err := s.db.Exec(`DELETE FROM sessions`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// DeleteAllTasks deletes all tasks
func (s *SQLiteStorage) DeleteAllTasks() (int64, error) {
	result, err := s.db.Exec(`DELETE FROM tasks`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// DeleteOrphanedTasks deletes tasks that have no associated sessions
func (s *SQLiteStorage) DeleteOrphanedTasks() (int64, error) {
	query := `DELETE FROM tasks WHERE id NOT IN (SELECT DISTINCT task_id FROM sessions)`
	result, err := s.db.Exec(query)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// FindSessionByID finds a session by its ID
func (s *SQLiteStorage) FindSessionByID(id string) (*Session, error) {
	var sess Session
	var endedAt sql.NullTime
	query := `SELECT id, task_id, started_at, ended_at FROM sessions WHERE id = ?`

	err := s.db.QueryRow(query, id).Scan(&sess.ID, &sess.TaskID, &sess.StartedAt, &endedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if endedAt.Valid {
		sess.EndedAt = &endedAt.Time
	}
	return &sess, nil
}

// UpdateTaskExternalID updates the external_id of a task
func (s *SQLiteStorage) UpdateTaskExternalID(taskID, externalID string) error {
	query := `UPDATE tasks SET external_id = ? WHERE id = ?`
	_, err := s.db.Exec(query, externalID, taskID)
	if err != nil {
		return fmt.Errorf("failed to update task external_id: %w", err)
	}
	return nil
}

// GetTasksWithoutExternalID returns all tasks without external_id
func (s *SQLiteStorage) GetTasksWithoutExternalID() ([]Task, error) {
	query := `SELECT id, external_id, alias, name, created_at, last_used
              FROM tasks 
              WHERE (external_id IS NULL OR external_id = '')
              ORDER BY created_at DESC`

	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []Task
	for rows.Next() {
		var task Task
		if err := rows.Scan(&task.ID, &task.ExternalID, &task.Alias, &task.Name, &task.CreatedAt, &task.LastUsed); err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, nil
}

// GetTasksInRangeWithoutExternalID returns tasks created in time range without external_id
func (s *SQLiteStorage) GetTasksInRangeWithoutExternalID(start, end time.Time) ([]Task, error) {
	query := `SELECT id, external_id, alias, name, created_at, last_used
              FROM tasks 
              WHERE created_at >= ? AND created_at < ?
                AND (external_id IS NULL OR external_id = '')
              ORDER BY created_at DESC`

	rows, err := s.db.Query(query, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []Task
	for rows.Next() {
		var task Task
		if err := rows.Scan(&task.ID, &task.ExternalID, &task.Alias, &task.Name, &task.CreatedAt, &task.LastUsed); err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, nil
}
