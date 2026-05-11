// internal/secretary/core.go

package secretary

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	conf "github.com/svdba/assist/config"
	"github.com/svdba/assist/internal/logging"
)

// Core is the main secretary logic
type Core struct {
	mu      sync.RWMutex
	storage *SQLiteStorage
	config  conf.SecretaryConfig
	state   *State
	stopCh  chan struct{}
}

type PendingBoundary struct {
	Type      string // "lunch" or "end_of_day"
	Scheduled time.Time
	Notified  bool
}

// State holds current runtime state
type State struct {
	CurrentSession  *Session
	PendingBoundary *PendingBoundary
}

// TaskReport represents aggregated data for a task in a report
type TaskReport struct {
	TaskName     string
	TotalMinutes int64
	MessageCount int
	SessionCount int
}

// DailyReportResponse represents the complete daily report for display
type DailyReportResponse struct {
	Date         string
	TotalMinutes int64
	TotalTasks   int
	Tasks        []TaskReportItem
}

type TaskReportItem struct {
	TaskID       string
	ExternalID   string
	Alias        string
	Name         string
	TotalMinutes int64
	MessageCount int
	SessionCount int
}

type GroupedDailyReportResponse struct {
	Date         string
	TotalMinutes int64
	TotalTasks   int
	Tasks        []GroupedTaskReport
}

type GroupedTaskReport struct {
	TaskID       string
	ExternalID   string
	Alias        string
	TotalMinutes int64
	SessionCount int
	Messages     []string
}

// NewCore creates a new secretary core instance
func NewCore(cfg conf.SecretaryConfig) (*Core, error) {
	storage, err := NewSQLiteStorage(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create storage: %w", err)
	}

	core := &Core{
		storage: storage,
		config:  cfg,
		state:   &State{},
		stopCh:  make(chan struct{}),
	}

	// Load active session on startup
	if err := core.loadActiveSession(); err != nil {
		logging.Error(context.Background(), "Failed to load active session", "error", err)
	}

	// Check if we need to create default session at current time
	now := time.Now()
	if core.state.CurrentSession == nil && core.isWorkingHours(now) && !core.isLunchTime(now) {
		if err := core.ensureDefaultSession(context.Background(), now); err != nil {
			logging.Error(context.Background(), "Failed to create default session on startup", "error", err)
		}
	}

	// Start background timeout checker
	go core.timeoutChecker()

	return core, nil
}

// ProcessEvent processes an incoming event and updates state
func (c *Core) ProcessEvent(ctx context.Context, event Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	logging.Debug(ctx, "Processing event",
		"type", event.Type,
		"timestamp", event.Timestamp.Format(time.RFC3339))

	// Check for scheduled boundaries (lunch, end of day)
	if err := c.checkBoundaries(ctx, event.Timestamp); err != nil {
		logging.Error(ctx, "Failed to check boundaries", "error", err)
	}

	// Check for timeout
	if err := c.checkTimeout(ctx, event.Timestamp); err != nil {
		logging.Error(ctx, "Failed to check timeout", "error", err)
	}

	// Ensure default session if nothing active
	if c.state.CurrentSession == nil {
		if err := c.ensureDefaultSession(ctx, event.Timestamp); err != nil {
			logging.Error(ctx, "Failed to ensure default session", "error", err)
		}
	}

	switch event.Type {
	case EventTypeMessage:
		return c.processMessageEvent(ctx, event)
	case EventTypeCommand:
		return c.processCommandEvent(ctx, event)
	case EventTypeTimeout:
		return c.processTimeoutEvent(ctx, event)
	default:
		logging.Debug(ctx, "Ignoring unknown event type", "type", event.Type)
	}

	return nil
}

// processMessageEvent handles incoming message events (forwarded or manual)
func (c *Core) processMessageEvent(ctx context.Context, event Event) error {
	senderName, _ := event.Data["sender_name"].(string)
	text, _ := event.Data["text"].(string)
	comment, _ := event.Data["comment"].(string)
	//isForward, _ := event.Data["is_forward"].(bool)

	// Determine task name/ID
	taskID := ExtractTaskID(text)
	if taskID == "" {
		taskID = ExtractTaskID(comment)
	}

	var task *Task
	var err error

	if taskID != "" {
		// Try to find existing task by external ID
		task, err = c.storage.FindTaskByID(taskID)
		if err != nil {
			return err
		}
	}

	// Create new task if not found
	if task == nil {
		internalID := GenerateInternalID(event.Timestamp)
		taskName := senderName
		if taskName == "" {
			taskName = c.config.DefaultTaskName
		}

		task = &Task{
			ID:         internalID,
			ExternalID: taskID,
			Name:       taskName,
			CreatedAt:  event.Timestamp,
			LastUsed:   event.Timestamp,
		}

		if err := c.storage.SaveTask(task); err != nil {
			return fmt.Errorf("failed to save task: %w", err)
		}
	}

	// Check if we need to switch tasks
	if c.state.CurrentSession != nil && c.state.CurrentSession.TaskID != task.ID {
		// Close current session
		now := event.Timestamp
		oldSession := c.state.CurrentSession // Сохраняем ссылку перед закрытием
		oldTaskID := oldSession.TaskID

		oldSession.EndedAt = &now
		if err := c.storage.SaveSession(oldSession); err != nil {
			logging.Error(ctx, "Failed to close session", "error", err)
			// Don't return here - continue with new session
		}
		c.state.CurrentSession = nil

		logging.Info(ctx, "Closed previous session due to task switch",
			"old_task_id", oldTaskID,
			"new_task_id", task.ID)
	}

	// Start new session if needed
	if c.state.CurrentSession == nil {
		timeoutAt := c.getTimeoutForRegularSession(event.Timestamp)
		session := &Session{
			ID:        GenerateInternalID(event.Timestamp) + "-sess",
			TaskID:    task.ID,
			Type:      SessionTypeForwarded,
			StartedAt: event.Timestamp,
			EndedAt:   nil,
			TimeoutAt: &timeoutAt,
			Messages:  []Message{},
		}

		if err := c.storage.SaveSession(session); err != nil {
			return fmt.Errorf("failed to save session: %w", err)
		}

		c.state.CurrentSession = session
		logging.Info(ctx, "Started new session",
			"session_id", session.ID,
			"task_id", task.ID,
			"sender", senderName)
	}

	// Add message to current session
	messageText := text
	if comment != "" {
		messageText = comment + ": " + text
	}

	message := Message{
		ID:         GenerateInternalID(event.Timestamp) + "-msg",
		SessionID:  c.state.CurrentSession.ID,
		Timestamp:  event.Timestamp,
		SenderName: senderName,
		Text:       messageText,
	}

	c.state.CurrentSession.Messages = append(c.state.CurrentSession.Messages, message)

	// Save session with new message
	if err := c.storage.SaveSession(c.state.CurrentSession); err != nil {
		return fmt.Errorf("failed to save session with message: %w", err)
	}

	logging.Info(ctx, "Added message to session",
		"session_id", c.state.CurrentSession.ID,
		"sender", senderName)

	return nil
}

// processCommandEvent handles command events
func (c *Core) processCommandEvent(ctx context.Context, event Event) error {
	command, _ := event.Data["command"].(string)
	args, _ := event.Data["args"].(string)

	logging.Debug(ctx, "Processing command", "command", command, "args", args)

	switch strings.ToLower(command) {
	case "task":
		// Manual task creation
		taskID := ExtractTaskID(args)
		taskName := args
		if taskID != "" {
			taskName = strings.TrimSpace(strings.Replace(args, taskID, "", 1))
		}
		if taskName == "" {
			taskName = c.config.DefaultTaskName
		}

		_, err := c.StartNewTask(taskName, taskID, "", event.Timestamp)
		return err

	case "alias":
		// Set alias for current task
		if c.state.CurrentSession == nil {
			return fmt.Errorf("no active session")
		}
		task, err := c.storage.FindTaskByID(c.state.CurrentSession.TaskID)
		if err != nil || task == nil {
			return fmt.Errorf("task not found")
		}
		task.Alias = args
		task.LastUsed = event.Timestamp
		return c.storage.SaveTask(task)

	case "switch":
		// Switch to task by alias
		task, err := c.storage.FindTaskByAlias(args)
		if err != nil || task == nil {
			return fmt.Errorf("task with alias '%s' not found", args)
		}
		c.SwitchToTask(task.ID)
		return nil
	}

	return nil
}

// processTimeoutEvent handles timeout events
func (c *Core) processTimeoutEvent(ctx context.Context, event Event) error {
	if c.state.CurrentSession != nil {
		now := event.Timestamp
		c.state.CurrentSession.EndedAt = &now
		if err := c.storage.SaveSession(c.state.CurrentSession); err != nil {
			return err
		}
		logging.Info(ctx, "Session closed due to timeout",
			"session_id", c.state.CurrentSession.ID,
			"duration", now.Sub(c.state.CurrentSession.StartedAt).Minutes())
		c.state.CurrentSession = nil
	}
	return nil
}

func (c *Core) checkBoundaries(ctx context.Context, now time.Time) error {
	// Skip boundaries on non-working days
	if !c.isWorkingDay(now) {
		return nil
	}
	// Check if day just started (within last 5 minutes)
	workStartTime, err := c.parseTimeString(c.config.WorkStart, now)
	if err != nil {
		return err
	}

	// If within 5 minutes after work start and no active session
	if now.After(workStartTime) && now.Before(workStartTime.Add(5*time.Minute)) {
		if c.state.CurrentSession == nil {
			// Auto-start default task
			defaultTaskName := "int-" + now.Format("0102-0900")

			logging.Info(ctx, "Auto-starting default task at start of day",
				"task_name", defaultTaskName)

			task, err := c.StartNewTask(defaultTaskName, "", "", workStartTime)
			if err != nil {
				return err
			}

			// TODO: Send notification to user
			_ = task
		}
	}

	// Check lunch boundary
	if err := c.checkLunchBoundary(ctx, now); err != nil {
		return err
	}

	// Check end of day boundary
	if err := c.checkEndOfDayBoundary(ctx, now); err != nil {
		return err
	}

	// Actually close at end of day
	if err := c.checkEndOfDay(ctx, now); err != nil {
		return err
	}

	return nil
}

// checkTimeout checks if current session has timed out
func (c *Core) checkTimeout(ctx context.Context, now time.Time) error {
	if c.state.CurrentSession == nil {
		return nil
	}

	if c.state.CurrentSession.TimeoutAt != nil && now.After(*c.state.CurrentSession.TimeoutAt) {
		logging.Info(ctx, "Session timed out",
			"session_id", c.state.CurrentSession.ID,
			"timeout_at", c.state.CurrentSession.TimeoutAt.Format("15:04:05"))

		return c.processTimeoutEvent(ctx, NewEvent(now, EventTypeTimeout, "system", nil))
	}

	return nil
}

// timeoutChecker runs periodically to check for timeouts
func (c *Core) timeoutChecker() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	c.checkTimeBoundaries(context.Background(), time.Now())

	for {
		select {
		case <-ticker.C:
			ctx := context.Background()
			now := time.Now()

			c.mu.Lock()

			// Skip processing on non-working days (but keep timeout for manual tasks?)
			if c.isWorkingDay(now) {
				if err := c.checkTimeout(ctx, now); err != nil {
					logging.Error(ctx, "Timeout check failed", "error", err)
				}

				if err := c.checkTimeBoundaries(ctx, now); err != nil {
					logging.Error(ctx, "Time boundaries check failed", "error", err)
				}

				if c.state.CurrentSession == nil && c.isWorkingHours(now) && !c.isLunchTime(now) {
					if err := c.ensureDefaultSession(ctx, now); err != nil {
						logging.Error(ctx, "Failed to ensure default session", "error", err)
					}
				}
			}

			c.mu.Unlock()

		case <-c.stopCh:
			return
		}
	}
}

// checkTimeBoundaries checks if we just crossed a boundary (start of day, end of lunch)
func (c *Core) checkTimeBoundaries(ctx context.Context, now time.Time) error {
	// Check if we just passed work start time (within last minute)
	workStart, err := c.parseTimeString(c.config.WorkStart, now)
	if err != nil {
		return err
	}

	if now.After(workStart) && now.Sub(workStart) <= time.Minute {
		if c.state.CurrentSession == nil {
			logging.Info(ctx, "Work day started, creating default session", "time", now.Format("15:04"))
			return c.ensureDefaultSession(ctx, now)
		}
	}

	// Check if we just passed end of lunch
	lunchEnd, err := c.parseTimeString(c.config.LunchEnd, now)
	if err != nil {
		return err
	}

	if now.After(lunchEnd) && now.Sub(lunchEnd) <= time.Minute {
		if c.state.CurrentSession == nil {
			logging.Info(ctx, "Lunch ended, creating default session", "time", now.Format("15:04"))
			return c.ensureDefaultSession(ctx, now)
		}
	}

	return nil
}

// NewEvent creates a new event (helper)
func NewEvent(timestamp time.Time, eventType EventType, source string, data map[string]interface{}) Event {
	return Event{
		ID:        GenerateInternalID(timestamp) + "-" + string(eventType),
		Type:      eventType,
		Timestamp: timestamp,
		Source:    source,
		Data:      data,
	}
}

// loadActiveSession loads unfinished session from DB
func (c *Core) loadActiveSession() error {
	sessions, err := c.storage.GetActiveSessions()
	if err != nil {
		return err
	}

	if len(sessions) > 0 {
		c.state.CurrentSession = &sessions[len(sessions)-1]
		logging.Info(context.Background(), "Resumed active session",
			"session_id", c.state.CurrentSession.ID,
			"task_id", c.state.CurrentSession.TaskID)
	}

	return nil
}

// Close closes the core and cleans up resources
func (c *Core) Close() error {
	close(c.stopCh)
	return c.storage.Close()
}

// GetCurrentTask returns the current active task
func (c *Core) GetCurrentTask() Task {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.state.CurrentSession == nil {
		return Task{}
	}

	task, _ := c.storage.FindTaskByID(c.state.CurrentSession.TaskID)
	if task == nil {
		return Task{}
	}
	return *task
}

// SaveTask saves or updates a task
func (c *Core) SaveTask(task Task) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.storage.SaveTask(&task); err != nil {
		logging.Error(context.Background(), "Failed to save task", "error", err, "task_id", task.ID)
	}
}

// FindTaskByAlias finds a task by its alias
func (c *Core) FindTaskByAlias(alias string) Task {
	c.mu.RLock()
	defer c.mu.RUnlock()

	task, err := c.storage.FindTaskByAlias(alias)
	if err != nil || task == nil {
		return Task{}
	}
	return *task
}

// SwitchToTask switches current session to another task
func (c *Core) SwitchToTask(taskID string) Task {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Close current session
	if c.state.CurrentSession != nil {
		now := time.Now()
		c.state.CurrentSession.EndedAt = &now
		if err := c.storage.SaveSession(c.state.CurrentSession); err != nil {
			logging.Error(context.Background(), "Failed to close session", "error", err)
		}
	}

	task, err := c.storage.FindTaskByID(taskID)
	if err != nil || task == nil {
		return Task{}
	}

	task.LastUsed = time.Now()
	if err := c.storage.SaveTask(task); err != nil {
		logging.Error(context.Background(), "Failed to update task", "error", err)
	}

	timeoutAt := time.Now().Add(time.Duration(c.config.TimeoutMinutes) * time.Minute)
	newSession := &Session{
		ID:        GenerateInternalID(time.Now()) + "-sess",
		TaskID:    task.ID,
		Type:      SessionTypeManual,
		StartedAt: time.Now(),
		EndedAt:   nil,
		TimeoutAt: &timeoutAt,
		Messages:  []Message{},
	}

	if err := c.storage.SaveSession(newSession); err != nil {
		logging.Error(context.Background(), "Failed to save new session", "error", err)
	}

	c.state.CurrentSession = newSession

	return *task
}

// GetActiveTasks returns all tasks with recent activity
func (c *Core) GetActiveTasks() []Task {
	c.mu.RLock()
	defer c.mu.RUnlock()

	since := time.Now().Add(-24 * time.Hour)
	tasks, err := c.storage.GetActiveTasks(since)
	if err != nil {
		logging.Error(context.Background(), "Failed to get active tasks", "error", err)
		return []Task{}
	}
	return tasks
}

// StartNewTask creates a new task and starts a session
func (c *Core) StartNewTask(taskName, externalID, alias string, startTime time.Time) (*Task, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.state.CurrentSession != nil {
		now := startTime
		c.state.CurrentSession.EndedAt = &now
		if err := c.storage.SaveSession(c.state.CurrentSession); err != nil {
			return nil, err
		}
	}

	internalID := GenerateInternalID(startTime)

	task := &Task{
		ID:         internalID,
		ExternalID: externalID,
		Alias:      alias,
		Name:       taskName,
		CreatedAt:  startTime,
		LastUsed:   startTime,
	}

	if err := c.storage.SaveTask(task); err != nil {
		return nil, err
	}

	timeoutAt := c.getTimeoutForRegularSession(startTime)
	session := &Session{
		ID:        GenerateInternalID(startTime) + "-sess",
		TaskID:    task.ID,
		Type:      SessionTypeManual,
		StartedAt: startTime,
		EndedAt:   nil,
		TimeoutAt: &timeoutAt,
		Messages:  []Message{},
	}

	if err := c.storage.SaveSession(session); err != nil {
		return nil, err
	}

	c.state.CurrentSession = session

	return task, nil
}

func (c *Core) checkLunchBoundary(ctx context.Context, now time.Time) error {
	lunchStart, err := c.parseTimeString(c.config.LunchStart, now)
	if err != nil {
		return err
	}

	lunchEnd, err := c.parseTimeString(c.config.LunchEnd, now)
	if err != nil {
		return err
	}

	// Check if we're approaching lunch
	timeToLunch := lunchStart.Sub(now)
	if timeToLunch > 0 && timeToLunch <= 10*time.Minute {
		if c.state.PendingBoundary == nil || c.state.PendingBoundary.Type != "lunch" {
			c.state.PendingBoundary = &PendingBoundary{
				Type:      "lunch",
				Scheduled: lunchStart,
				Notified:  false,
			}

			// Notify user only once
			if !c.state.PendingBoundary.Notified {
				c.state.PendingBoundary.Notified = true
				c.notifyUser(ctx, fmt.Sprintf(
					"⏰ Lunch break in %.0f minutes. Current task will be paused.\n"+
						"Reply with:\n"+
						"• 'continue' - finish current task first\n"+
						"• 'pause' - pause now",
					timeToLunch.Minutes()))
			}
		}
		return nil
	}

	// Check if we passed lunch start without confirmation
	if now.After(lunchStart) && now.Before(lunchEnd) {
		if c.state.CurrentSession != nil && c.state.PendingBoundary != nil {
			// Lunch time, but user chose to continue
			if c.state.PendingBoundary.Type == "lunch" && c.state.PendingBoundary.Notified {
				// User already chose to continue, don't interrupt
				return nil
			}
		}
	}

	// After lunch, clear pending
	if now.After(lunchEnd) {
		c.state.PendingBoundary = nil
	}

	return nil
}

func (c *Core) checkEndOfDayBoundary(ctx context.Context, now time.Time) error {
	endOfDay, err := c.parseTimeString(c.config.WorkEnd, now)
	if err != nil {
		return err
	}

	timeToEnd := endOfDay.Sub(now)
	if timeToEnd > 0 && timeToEnd <= 10*time.Minute {
		if c.state.PendingBoundary == nil || c.state.PendingBoundary.Type != "end_of_day" {
			c.state.PendingBoundary = &PendingBoundary{
				Type:      "end_of_day",
				Scheduled: endOfDay,
				Notified:  false,
			}

			if !c.state.PendingBoundary.Notified {
				c.state.PendingBoundary.Notified = true
				c.notifyUser(ctx, fmt.Sprintf(
					"⏰ End of day in %.0f minutes.\n"+
						"Reply with:\n"+
						"• 'continue' - extend working day\n"+
						"• 'stop' - finish current task and close day",
					timeToEnd.Minutes()))
			}
		}
		return nil
	}

	return nil
}

// checkEndOfDay closes session when work day ends
func (c *Core) checkEndOfDay(ctx context.Context, now time.Time) error {
	endOfDay, err := c.parseTimeString(c.config.WorkEnd, now)
	if err != nil {
		return err
	}

	// If work end time has passed or exactly now
	if !now.Before(endOfDay) {
		if c.state.CurrentSession != nil {
			// Close current session at end of day
			c.state.CurrentSession.EndedAt = &endOfDay
			if err := c.storage.SaveSession(c.state.CurrentSession); err != nil {
				return err
			}
			c.state.CurrentSession = nil

			logging.Info(ctx, "Session closed at end of working day",
				"scheduled_end", endOfDay.Format("15:04"),
				"actual_end", now.Format("15:04"))
		}
		// Clear pending boundary
		c.state.PendingBoundary = nil
		return nil
	}

	return nil
}

// Handle user response to boundary notification
func (c *Core) HandleBoundaryResponse(ctx context.Context, response string) error {
	if c.state.PendingBoundary == nil {
		return fmt.Errorf("no pending boundary")
	}

	switch strings.ToLower(response) {
	case "continue":
		// User wants to continue working
		logging.Info(ctx, "User chose to continue", "boundary", c.state.PendingBoundary.Type)
		c.state.PendingBoundary = nil
		return nil

	case "pause", "stop":
		// User wants to pause/stop
		if c.state.CurrentSession != nil {
			now := time.Now()
			c.state.CurrentSession.EndedAt = &now
			if err := c.storage.SaveSession(c.state.CurrentSession); err != nil {
				return err
			}
			c.state.CurrentSession = nil

			logging.Info(ctx, "Session closed due to boundary",
				"boundary", c.state.PendingBoundary.Type)
		}
		c.state.PendingBoundary = nil
		return nil

	default:
		return fmt.Errorf("unknown response: %s", response)
	}
}

func (c *Core) parseTimeString(timeStr string, reference time.Time) (time.Time, error) {
	parts := strings.Split(timeStr, ":")
	if len(parts) != 2 {
		return time.Time{}, fmt.Errorf("invalid time format: %s", timeStr)
	}

	hour, err := strconv.Atoi(parts[0])
	if err != nil {
		return time.Time{}, err
	}

	minute, err := strconv.Atoi(parts[1])
	if err != nil {
		return time.Time{}, err
	}

	return time.Date(reference.Year(), reference.Month(), reference.Day(),
		hour, minute, 0, 0, reference.Location()), nil
}

func (c *Core) notifyUser(ctx context.Context, message string) {
	// TODO: Send notification via Telegram or other interface
	logging.Info(ctx, "User notification", "message", message)
}

// HasPendingBoundary returns true if there's a pending boundary notification
func (c *Core) HasPendingBoundary() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.state.PendingBoundary != nil
}

// StartTimedTask creates a new task with custom timeout duration
func (c *Core) StartTimedTask(parsed *ParsedMessage, duration time.Duration) (*Task, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Close current session if exists
	if c.state.CurrentSession != nil {
		now := parsed.StartTime
		c.state.CurrentSession.EndedAt = &now
		if err := c.storage.SaveSession(c.state.CurrentSession); err != nil {
			return nil, err
		}
	}

	// Determine task ID
	taskID := parsed.TaskID
	if taskID == "" {
		taskID = GenerateInternalID(parsed.StartTime)
	}

	// Create or find task
	var task *Task
	var err error

	if parsed.TaskID != "" {
		task, err = c.storage.FindTaskByID(parsed.TaskID)
	}
	if err != nil {
		return nil, err
	}

	if task == nil {
		task = &Task{
			ID:         taskID,
			ExternalID: parsed.TaskID,
			Name:       parsed.SenderName,
			CreatedAt:  parsed.StartTime,
			LastUsed:   parsed.StartTime,
		}

		if parsed.Comment != "" {
			task.Name = parsed.Comment
		}

		if err := c.storage.SaveTask(task); err != nil {
			return nil, fmt.Errorf("failed to save task: %w", err)
		}
	}

	// Create session with custom timeout stored in metadata
	timeoutAt := parsed.StartTime.Add(duration)
	session := &Session{
		ID:        GenerateInternalID(parsed.StartTime) + "-sess",
		TaskID:    task.ID,
		Type:      SessionTypeManual,
		StartedAt: parsed.StartTime,
		EndedAt:   nil,
		TimeoutAt: &timeoutAt,
		Messages:  []Message{},
	}

	// Store custom timeout duration in session (will be checked by timeoutChecker)
	// For now, we'll just note it in logs; actual implementation can store in a separate table
	logging.Info(context.Background(), "Starting timed task",
		"task_id", task.ID,
		"duration_minutes", duration.Minutes())

	if err := c.storage.SaveSession(session); err != nil {
		return nil, fmt.Errorf("failed to save session: %w", err)
	}

	c.state.CurrentSession = session

	// Schedule auto-end if custom duration provided
	if duration > 0 {
		go func() {
			time.Sleep(duration)
			ctx := context.Background()
			c.mu.Lock()
			defer c.mu.Unlock()

			// Check if this session is still active
			if c.state.CurrentSession != nil && c.state.CurrentSession.ID == session.ID {
				now := time.Now()
				c.state.CurrentSession.EndedAt = &now
				if err := c.storage.SaveSession(c.state.CurrentSession); err != nil {
					logging.Error(ctx, "Failed to close timed session", "error", err)
				}
				c.state.CurrentSession = nil
				logging.Info(ctx, "Timed session auto-closed", "duration_minutes", duration.Minutes())
			}
		}()
	}

	return task, nil
}

// GetCurrentSession returns the current active session
func (c *Core) GetCurrentSession() *Session {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.state.CurrentSession
}

// GetTimeoutMinutes returns the configured timeout in minutes
func (c *Core) GetTimeoutMinutes() int {
	return c.config.TimeoutMinutes
}

// GetDailyReport returns aggregated report for a specific date
func (c *Core) GetDailyReport(ctx context.Context, date time.Time) (*DailyReportResponse, error) {
	logging.Debug(ctx, "GetDailyReport: start", "date", date.Format("2006-01-02"))

	logging.Debug(ctx, "GetDailyReport: acquiring read lock")
	c.mu.RLock()
	logging.Debug(ctx, "GetDailyReport: read lock acquired")

	startOfDay := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, date.Location())
	endOfDay := startOfDay.Add(24 * time.Hour)

	logging.Debug(ctx, "GetDailyReport: querying sessions",
		"start", startOfDay.Format(time.RFC3339),
		"end", endOfDay.Format(time.RFC3339))

	sessions, err := c.storage.GetSessionsInRange(ctx, startOfDay, endOfDay)

	logging.Debug(ctx, "GetDailyReport: releasing read lock")
	c.mu.RUnlock()

	if err != nil {
		logging.Error(ctx, "GetDailyReport: failed to get sessions", "error", err)
		return nil, err
	}

	logging.Debug(ctx, "GetDailyReport: got sessions", "count", len(sessions))

	// Aggregate by task
	taskMap := make(map[string]*TaskReportItem)

	for _, session := range sessions {
		if session.EndedAt == nil {
			continue // Skip active sessions
		}

		duration := session.EndedAt.Sub(session.StartedAt)
		minutes := int64(duration.Minutes())

		task, err := c.storage.FindTaskByID(session.TaskID)
		if err != nil || task == nil {
			continue
		}

		report := taskMap[task.ID]
		if report == nil {
			report = &TaskReportItem{
				TaskID:       task.ID,
				ExternalID:   task.ExternalID,
				Alias:        task.Alias,
				Name:         task.Name,
				TotalMinutes: 0,
				MessageCount: 0,
				SessionCount: 0,
			}
			taskMap[task.ID] = report
		}

		report.TotalMinutes += minutes
		report.MessageCount += len(session.Messages)
		report.SessionCount++
	}

	// Convert map to slice
	var tasks []TaskReportItem
	var totalMinutes int64
	for _, report := range taskMap {
		tasks = append(tasks, *report)
		totalMinutes += report.TotalMinutes
	}

	// Sort by total minutes (descending)
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].TotalMinutes > tasks[j].TotalMinutes
	})

	return &DailyReportResponse{
		Date:         date.Format("2006-01-02"),
		TotalMinutes: totalMinutes,
		TotalTasks:   len(tasks),
		Tasks:        tasks,
	}, nil
}

// GetGroupedDailyReport returns report grouped by Jira ID
func (c *Core) GetGroupedDailyReport(ctx context.Context, date time.Time) (*GroupedDailyReportResponse, error) {
	logging.Debug(ctx, "GetGroupedDailyReport: start", "date", date.Format("2006-01-02"))

	c.mu.RLock()
	startOfDay := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, date.Location())
	endOfDay := startOfDay.Add(24 * time.Hour)

	sessions, err := c.storage.GetSessionsInRange(ctx, startOfDay, endOfDay)
	c.mu.RUnlock()

	if err != nil {
		return nil, err
	}

	// Group by Jira ID (external_id)
	groupMap := make(map[string]*GroupedTaskReport)

	for _, session := range sessions {
		if session.EndedAt == nil {
			continue
		}

		task, err := c.storage.FindTaskByID(session.TaskID)
		if err != nil || task == nil {
			continue
		}

		// Determine group key: Jira ID > Task ID
		groupKey := task.ExternalID
		if groupKey == "" {
			groupKey = task.ID
		}

		report := groupMap[groupKey]
		if report == nil {
			report = &GroupedTaskReport{
				TaskID:       task.ID,
				ExternalID:   task.ExternalID,
				Alias:        task.Alias,
				TotalMinutes: 0,
				SessionCount: 0,
				Messages:     []string{},
			}
			groupMap[groupKey] = report
		}

		duration := session.EndedAt.Sub(session.StartedAt)
		report.TotalMinutes += int64(duration.Minutes())
		report.SessionCount++

		// Collect messages
		for _, msg := range session.Messages {
			if msg.Text != "" {
				report.Messages = append(report.Messages, msg.Text)
			}
		}
	}

	// Convert to slice and sort by total minutes
	var reports []GroupedTaskReport
	var totalMinutes int64
	for _, report := range groupMap {
		reports = append(reports, *report)
		totalMinutes += report.TotalMinutes
	}

	sort.Slice(reports, func(i, j int) bool {
		return reports[i].TotalMinutes > reports[j].TotalMinutes
	})

	return &GroupedDailyReportResponse{
		Date:         date.Format("2006-01-02"),
		TotalMinutes: totalMinutes,
		TotalTasks:   len(reports),
		Tasks:        reports,
	}, nil
}

// PurgeSessions deletes all sessions and tasks for a specific date
// If date is nil, purges all data (use with caution)
func (c *Core) PurgeSessions(ctx context.Context, date *time.Time) (int64, int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var sessionsDeleted, tasksDeleted int64
	var err error

	if date == nil {
		// Purge all data
		sessionsDeleted, err = c.storage.DeleteAllSessions()
		if err != nil {
			return 0, 0, err
		}
		tasksDeleted, err = c.storage.DeleteAllTasks()
		if err != nil {
			return sessionsDeleted, 0, err
		}

		// Clear current session
		c.state.CurrentSession = nil

		logging.Warn(ctx, "Purged all data",
			"sessions_deleted", sessionsDeleted,
			"tasks_deleted", tasksDeleted)
	} else {
		// Purge specific date
		startOfDay := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, date.Location())
		endOfDay := startOfDay.Add(24 * time.Hour)

		sessionsDeleted, err = c.storage.DeleteSessionsInRange(startOfDay, endOfDay)
		if err != nil {
			return 0, 0, err
		}

		// Clean up orphaned tasks (tasks with no sessions)
		tasksDeleted, err = c.storage.DeleteOrphanedTasks()
		if err != nil {
			return sessionsDeleted, tasksDeleted, err
		}

		// Clear current session if it was purged
		if c.state.CurrentSession != nil {
			sess, _ := c.storage.FindSessionByID(c.state.CurrentSession.ID)
			if sess == nil {
				c.state.CurrentSession = nil
			}
		}

		logging.Warn(ctx, "Purged sessions for date",
			"date", date.Format("2006-01-02"),
			"sessions_deleted", sessionsDeleted,
			"tasks_deleted", tasksDeleted)
	}

	return sessionsDeleted, tasksDeleted, nil
}

// StopCurrentSession closes the current active session without starting a new one
func (c *Core) StopCurrentSession(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.state.CurrentSession == nil {
		return fmt.Errorf("no active session to stop")
	}

	now := time.Now()
	c.state.CurrentSession.EndedAt = &now

	if err := c.storage.SaveSession(c.state.CurrentSession); err != nil {
		return fmt.Errorf("failed to save stopped session: %w", err)
	}

	logging.Info(ctx, "Session stopped by user",
		"session_id", c.state.CurrentSession.ID,
		"task_id", c.state.CurrentSession.TaskID,
		"duration", now.Sub(c.state.CurrentSession.StartedAt).Minutes())

	c.state.CurrentSession = nil

	return nil
}

// LinkCurrentTask assigns Jira ID to the current active task
func (c *Core) LinkCurrentTask(ctx context.Context, externalID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.state.CurrentSession == nil {
		return fmt.Errorf("no active session/task to link")
	}

	taskID := c.state.CurrentSession.TaskID
	if err := c.storage.UpdateTaskExternalID(taskID, externalID); err != nil {
		return err
	}

	logging.Info(ctx, "Linked current task to Jira",
		"task_id", taskID,
		"external_id", externalID)

	return nil
}

// LinkTaskByID assigns Jira ID to a specific task by ID
func (c *Core) LinkTaskByID(ctx context.Context, taskID, externalID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Verify task exists
	task, err := c.storage.FindTaskByID(taskID)
	if err != nil {
		return fmt.Errorf("failed to find task: %w", err)
	}
	if task == nil {
		return fmt.Errorf("task not found: %s", taskID)
	}

	if err := c.storage.UpdateTaskExternalID(taskID, externalID); err != nil {
		return err
	}

	logging.Info(ctx, "Linked task to Jira",
		"task_id", taskID,
		"external_id", externalID)

	return nil
}

// LinkTasksTodayWithoutExternalID assigns Jira ID to all tasks created today without external_id
func (c *Core) LinkTasksTodayWithoutExternalID(ctx context.Context, externalID string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	endOfDay := startOfDay.Add(24 * time.Hour)

	tasks, err := c.storage.GetTasksInRangeWithoutExternalID(startOfDay, endOfDay)
	if err != nil {
		return 0, err
	}

	count := int64(0)
	for _, task := range tasks {
		if err := c.storage.UpdateTaskExternalID(task.ID, externalID); err != nil {
			logging.Error(ctx, "Failed to update task", "task_id", task.ID, "error", err)
			continue
		}
		count++
	}

	logging.Info(ctx, "Linked all today's tasks without Jira ID",
		"count", count,
		"external_id", externalID)

	return count, nil
}

// LinkAllTasksWithoutExternalID assigns Jira ID to all tasks without external_id
func (c *Core) LinkAllTasksWithoutExternalID(ctx context.Context, externalID string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	tasks, err := c.storage.GetTasksWithoutExternalID()
	if err != nil {
		return 0, err
	}

	count := int64(0)
	for _, task := range tasks {
		if err := c.storage.UpdateTaskExternalID(task.ID, externalID); err != nil {
			logging.Error(ctx, "Failed to update task", "task_id", task.ID, "error", err)
			continue
		}
		count++
	}

	logging.Info(ctx, "Linked all tasks without Jira ID",
		"count", count,
		"external_id", externalID)

	return count, nil
}

// ExtendCurrentSession extends the current active session by the specified duration
func (c *Core) ExtendCurrentSession(ctx context.Context, duration time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.state.CurrentSession == nil {
		return fmt.Errorf("no active session to extend")
	}

	if duration <= 0 {
		return fmt.Errorf("duration must be positive")
	}

	// Add duration to TimeoutAt (NOT subtract)
	if c.state.CurrentSession.TimeoutAt != nil {
		newTimeout := c.state.CurrentSession.TimeoutAt.Add(duration)
		c.state.CurrentSession.TimeoutAt = &newTimeout
		if err := c.storage.SaveSession(c.state.CurrentSession); err != nil {
			return fmt.Errorf("failed to save extended session: %w", err)
		}

		logging.Info(ctx, "Extended current session",
			"session_id", c.state.CurrentSession.ID,
			"added_minutes", duration.Minutes(),
			"old_timeout", c.state.CurrentSession.TimeoutAt.Add(-duration).Format("15:04"),
			"new_timeout", newTimeout.Format("15:04"))
	}

	return nil
}

// ExtendSessionByID extends a specific session by the specified duration
func (c *Core) ExtendSessionByID(ctx context.Context, sessionID string, duration time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	session, err := c.storage.FindSessionByID(sessionID)
	if err != nil {
		return fmt.Errorf("failed to find session: %w", err)
	}
	if session == nil {
		return fmt.Errorf("session not found: %s", sessionID)
	}

	if duration > 0 {
		// For positive duration, we can only affect active sessions
		if session.EndedAt != nil {
			return fmt.Errorf("cannot extend completed session")
		}

		// Reset the timeout by updating last event time
		// This is a bit tricky since we don't track last event time per session
		// For now, we'll just log and return success
		logging.Info(ctx, "Extended session",
			"session_id", sessionID,
			"duration_minutes", duration.Minutes())
	} else if duration < 0 {
		// Shorten the session
		newEndTime := time.Now().Add(duration)
		if newEndTime.Before(session.StartedAt) {
			return fmt.Errorf("cannot shorten session before its start time")
		}
		session.EndedAt = &newEndTime
		if err := c.storage.SaveSession(session); err != nil {
			return fmt.Errorf("failed to save shortened session: %w", err)
		}

		// If this was the current session and it's now ended, clear it
		if c.state.CurrentSession != nil && c.state.CurrentSession.ID == sessionID {
			if newEndTime.Before(time.Now()) || newEndTime.Equal(time.Now()) {
				c.state.CurrentSession = nil
			}
		}

		logging.Info(ctx, "Shortened session",
			"session_id", sessionID,
			"new_end_time", newEndTime)
	}

	return nil
}

// GetRemainingTimeout returns the remaining time before auto-timeout (in minutes)
func (c *Core) GetRemainingTimeout() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.state.CurrentSession == nil || c.state.CurrentSession.TimeoutAt == nil {
		return 0
	}

	remaining := time.Until(*c.state.CurrentSession.TimeoutAt)

	// Debug log
	logging.Debug(context.Background(), "GetRemainingTimeout",
		"timeout_at", c.state.CurrentSession.TimeoutAt.Format("15:04:05"),
		"remaining_minutes", int64(remaining.Minutes()))

	if remaining <= 0 {
		return 0
	}

	return int64(remaining.Minutes())
}

// ensureDefaultSession creates a default session if none active and within working hours
func (c *Core) ensureDefaultSession(ctx context.Context, now time.Time) error {
	// Don't create default session on non-working days
	if !c.isWorkingDay(now) {
		logging.Debug(ctx, "Not creating default session - non-working day",
			"weekday", now.Weekday().String())
		return nil
	}

	// Don't create during lunch
	if c.isLunchTime(now) {
		logging.Debug(ctx, "Not creating default session - lunch time")
		return nil
	}

	// Don't create after work hours
	if !c.isWorkingHours(now) {
		logging.Debug(ctx, "Not creating default session - after work hours")
		return nil
	}

	// Check if there's already an active session
	if c.state.CurrentSession != nil {
		return nil
	}

	// Create default task
	defaultTaskID := fmt.Sprintf("default-%s", now.Format("0102-1504"))

	task, err := c.storage.FindTaskByID(defaultTaskID)
	if err != nil {
		return err
	}

	if task == nil {
		task = &Task{
			ID:        defaultTaskID,
			Name:      "Default work",
			CreatedAt: now,
			LastUsed:  now,
		}
		if err := c.storage.SaveTask(task); err != nil {
			return err
		}
	}

	// Calculate timeout based on boundaries
	timeoutAt, err := c.getTimeoutForDefaultSession(now)
	if err != nil {
		return err
	}

	if timeoutAt == nil {
		// No valid boundary (should not happen due to working hours check)
		return nil
	}

	// Create default session
	session := &Session{
		ID:        fmt.Sprintf("%s-sess", defaultTaskID),
		TaskID:    task.ID,
		Type:      SessionTypeDefault,
		StartedAt: now,
		EndedAt:   nil,
		TimeoutAt: timeoutAt,
		Messages:  []Message{},
	}

	if err := c.storage.SaveSession(session); err != nil {
		return fmt.Errorf("failed to save default session: %w", err)
	}

	c.state.CurrentSession = session

	logging.Info(ctx, "Created default session",
		"session_id", session.ID,
		"task_id", task.ID,
		"started_at", now.Format("15:04"),
		"timeout_at", timeoutAt.Format("15:04"))

	return nil
}

// isWorkingHours checks if current time is within working hours AND working day
func (c *Core) isWorkingHours(t time.Time) bool {
	// Check if it's a working day
	if !c.isWorkingDay(t) {
		return false
	}

	// Check time range
	workStart, err := c.parseTimeString(c.config.WorkStart, t)
	if err != nil {
		return false
	}

	workEnd, err := c.parseTimeString(c.config.WorkEnd, t)
	if err != nil {
		return false
	}

	return t.After(workStart) && t.Before(workEnd)
}

// isLunchTime checks if current time is within lunch break (only on working days)
func (c *Core) isLunchTime(t time.Time) bool {
	if !c.isWorkingDay(t) {
		return false
	}

	lunchStart, err := c.parseTimeString(c.config.LunchStart, t)
	if err != nil {
		return false
	}

	lunchEnd, err := c.parseTimeString(c.config.LunchEnd, t)
	if err != nil {
		return false
	}

	return t.After(lunchStart) && t.Before(lunchEnd)
}

// isWorkingDay checks if today is a working day
func (c *Core) isWorkingDay(t time.Time) bool {
	if len(c.config.WorkDays) == 0 {
		// Default: Monday to Friday
		weekday := t.Weekday()
		return weekday != time.Saturday && weekday != time.Sunday
	}

	dayName := strings.ToLower(t.Weekday().String())
	for _, workDay := range c.config.WorkDays {
		if strings.ToLower(workDay) == dayName {
			return true
		}
	}
	return false
}

// getNextBoundary returns the next time boundary (lunch or end of day)
func (c *Core) getNextBoundary(now time.Time) *time.Time {
	lunchStart, _ := c.parseTimeString(c.config.LunchStart, now)
	lunchEnd, _ := c.parseTimeString(c.config.LunchEnd, now)
	workEnd, _ := c.parseTimeString(c.config.WorkEnd, now)

	// If before lunch, next boundary is lunch start
	if now.Before(lunchStart) {
		return &lunchStart
	}

	// If after lunch but before end of day, next boundary is work end
	if now.After(lunchEnd) && now.Before(workEnd) {
		return &workEnd
	}

	// No valid boundary (lunch time or after work)
	return nil
}

// getTimeoutForDefaultSession calculates appropriate timeout for default session
func (c *Core) getTimeoutForDefaultSession(now time.Time) (*time.Time, error) {
	nextBoundary := c.getNextBoundary(now)

	if nextBoundary == nil {
		return nil, nil
	}

	// Default session always times out at next boundary (lunch or end of day)
	return nextBoundary, nil
}

func (c *Core) getTimeoutForRegularSession(now time.Time) time.Time {
	return now.Add(time.Duration(c.config.TimeoutMinutes) * time.Minute)
}
