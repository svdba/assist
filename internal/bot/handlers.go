// internal/bot/handlers.go

package bot

import (
	"context"
	"encoding/base64"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/svdba/assist/internal/logging"
	"github.com/svdba/assist/internal/secretary"
)

// handleMessage processes incoming messages with stub logic
func (b *Bot) handleMessage(ctx context.Context, message *tgbotapi.Message) {
	// Log all incoming messages
	logging.Debug(ctx, "Received message",
		"chat_id", message.Chat.ID,
		"from", message.From.UserName,
		"text", message.Text,
		"has_entities", message.Entities != nil)

	// Check if this is a forwarded message (main scenario)
	if b.handleForwardedMessage(ctx, message) {
		return
	}

	// Handle direct commands
	if message.IsCommand() {
		b.handleCommand(ctx, message)
		return
	}

	// Handle direct text messages (possibly comments)
	b.handleDirectMessage(ctx, message)
}

// handleForwardedMessage processes forwarded messages from colleagues
func (b *Bot) handleForwardedMessage(ctx context.Context, message *tgbotapi.Message) bool {
	receivedAt := time.Now().Round(time.Minute) // Round to minute immediately

	// Extract sender info
	var senderName string
	var isHidden bool

	switch {
	case message.ForwardFrom != nil:
		logging.Debug(ctx, "case 1",
			"infa", fmt.Sprintf("%v", message.ForwardFrom))
		// Regular user account
		if message.ForwardFrom.UserName != "" {
			senderName = "@" + message.ForwardFrom.UserName
		} else if message.ForwardFrom.FirstName != "" {
			senderName = message.ForwardFrom.FirstName
		} else {
			senderName = "user_" + message.ForwardFrom.String()
		}
	case message.ForwardSenderName != "":
		logging.Debug(ctx, "case 2",
			"infa", fmt.Sprintf("%v", message.ForwardSenderName))
		// Hidden account (user has hidden their account)
		senderName = "🔒 " + message.ForwardSenderName
		isHidden = true
	case message.ForwardFromChat != nil:
		logging.Debug(ctx, "case 3",
			"infa", fmt.Sprintf("%v", message.ForwardFromChat))
		// Forward from channel or group
		senderName = "📢 " + message.ForwardFromChat.Title
	default:
		logging.Debug(ctx, "case d",
			"infa", fmt.Sprintf("%v", message.ForwardFromChat))
		senderName = "❓ unknown"
		logging.Warn(ctx, "Cannot determine forward source",
			"message_id", message.MessageID)
		return false
	}

	if isHidden {
		logging.Info(ctx, "Processing forwarded message from hidden account",
			"sender", senderName)
	}

	// Extract user comment from caption
	var userComment string
	if message.Caption != "" {
		userComment = message.Caption
	}

	// Parse message
	parsed := secretary.ParseMessage(
		message.Text,
		senderName,
		receivedAt,
		userComment,
	)

	// В handleForwardedMessage вместо логов:
	event := secretary.NewMessageEvent(
		receivedAt,
		senderName,
		message.Text,
		userComment,
		true,
	)

	if err := b.core.ProcessEvent(ctx, event); err != nil {
		logging.Error(ctx, "Failed to process event", "error", err)
		b.reply(message.Chat.ID, "❌ Failed to track work session")
		return true
	}

	// Send acknowledgment to user
	reply := b.buildForwardedAck(parsed)
	msg := tgbotapi.NewMessage(message.Chat.ID, reply)
	msg.ReplyToMessageID = message.MessageID
	b.api.Send(msg)
	return true
}

// handleCommand processes bot commands
func (b *Bot) handleCommand(ctx context.Context, message *tgbotapi.Message) {
	args := message.CommandArguments()

	message_cmd := message.Command()

	logging.Debug(ctx, "handle Command",
		"cmd", message_cmd,
		"args", len(args))

	available_cmd := "Available: /help /task, /alias, /switch, /tasks, /status, /report, /purge, /stop, /link, /extend"
	switch message_cmd {
	case "help":
		b.reply(message.Chat.ID, available_cmd)

	case "alias":
		// /alias jenkins_down
		if args == "" {
			b.reply(message.Chat.ID, "Usage: /alias <name>")
			return
		}
		b.handleSetAlias(ctx, message.Chat.ID, args)
	case "switch":
		// /switch jenkins_down
		if args == "" {
			b.reply(message.Chat.ID, "Usage: /switch <alias>")
			return
		}
		b.handleSwitchAlias(ctx, message.Chat.ID, args)

	case "tasks":
		// /tasks - show all active tasks
		b.handleListTasks(ctx, message.Chat.ID)

	case "status":
		b.handleStatus(ctx, message.Chat.ID)

	case "report":
		b.handleReport(ctx, message.Chat.ID)

	case "purge":
		// /purge - deleting data for the entire day
		// /purge all - delete all
		// /purge 2026-04-20 - deleting data for the specific day
		b.handlePurge(ctx, message.Chat.ID, args)

	case "stop":
		// /stop - stop current session
		b.handleStop(ctx, message.Chat.ID)

	case "link":
		// /link MIG-12345
		// /link MIG-12345 int-0421-0919
		// /link MIG-12345 -today
		// /link MIG-12345 -all
		b.handleLink(ctx, message.Chat.ID, args)

	case "extend":
		// /extend 30 - extend current session by 30 minutes
		// /extend 2h - extend by 2 hours
		b.handleExtend(ctx, message.Chat.ID, args)

	default:
		b.reply(message.Chat.ID, fmt.Sprintf("Unknown command. %s", available_cmd))
	}
}

func escapeMarkdownV2(text string) string {
	var markdownEscapeRegex = regexp.MustCompile(`([_\[\]()~` + "`" + `>#+\-=|{}.!])`)
	return markdownEscapeRegex.ReplaceAllString(text, `\$1`)
}

func (b *Bot) reply(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, escapeMarkdownV2(text))
	msg.ParseMode = "MarkdownV2"

	ctx := context.Background()
	if _, err := b.api.Send(msg); err != nil {
		logging.Debug(ctx, "Failed message", "msg64", base64.StdEncoding.EncodeToString([]byte(text)))
		logging.Error(ctx, "Failed to send message", "error", err)
	}
}

func (b *Bot) handleSetAlias(ctx context.Context, chatID int64, alias string) {
	// Get current active session
	currentTask := b.core.GetCurrentTask()
	if currentTask.ID == "" {
		b.reply(chatID, "No active task. Start a task first with /task or forward a message.")
		return
	}

	// Set alias
	currentTask.Alias = alias
	b.core.SaveTask(currentTask)

	b.reply(chatID, fmt.Sprintf("✅ Alias '%s' set for task %s", alias, currentTask.ID))
}

func (b *Bot) handleSwitchAlias(ctx context.Context, chatID int64, alias string) {
	// Find task by alias
	task := b.core.FindTaskByAlias(alias)
	if task.ID == "" {
		b.reply(chatID, fmt.Sprintf("❌ No task found with alias '%s'", alias))
		return
	}

	// Close current session, start new session with this task
	b.core.SwitchToTask(task.ID)

	b.reply(chatID, fmt.Sprintf("✅ Switched to task '%s' (%s)", alias, task.ID))
}

func (b *Bot) handleListTasks(ctx context.Context, chatID int64) {
	tasks := b.core.GetActiveTasks()
	if len(tasks) == 0 {
		b.reply(chatID, "No active tasks.")
		return
	}

	reply := "📋 *Active tasks:*\n\n"

	for _, task := range tasks {
		displayName := task.ID
		if task.ExternalID != "" {
			displayName = task.ExternalID
		} else if task.Alias != "" {
			displayName = task.Alias
		}

		// Use task.Name as preview if available and not equal to displayName
		preview := ""
		if task.Name != "" && task.Name != "me" && task.Name != displayName {
			preview = truncateString(task.Name, 35)
		}

		line := fmt.Sprintf("• %s", displayName)
		if preview != "" {
			line += fmt.Sprintf(" - %s", preview)
		}
		reply += line + "\n"
	}

	b.reply(chatID, reply)
}

func (b *Bot) handleStatus(ctx context.Context, chatID int64) {
	currentTask := b.core.GetCurrentTask()

	if currentTask.ID == "" {
		b.reply(chatID, "📭 No active work session.\n\n"+
			"Start one by:\n"+
			"• Forwarding a message from a colleague\n"+
			"• Sending /task <description>\n"+
			"• Sending /task PROJ-123 description")
		return
	}

	session := b.core.GetCurrentSession()
	if session == nil {
		b.reply(chatID, "⚠️ No active session")
		return
	}

	// Calculate duration
	duration := time.Since(session.StartedAt)
	hours := int(duration.Hours())
	minutes := int(duration.Minutes()) % 60

	// Get remaining timeout
	remainingMinutes := b.core.GetRemainingTimeout()

	// Get last message preview (if any)
	lastMessage := ""
	if len(session.Messages) > 0 {
		lastMsgText := session.Messages[len(session.Messages)-1].Text
		lastMessage = truncateString(lastMsgText, 50)
	}

	// Build display name
	taskDisplay := currentTask.ID
	if currentTask.ExternalID != "" {
		taskDisplay = currentTask.ExternalID
	} else if currentTask.Alias != "" {
		taskDisplay = currentTask.Alias
	}

	// Build message
	reply := fmt.Sprintf("📊 *Current Work Session*\n\n")
	reply += fmt.Sprintf("📋 *Task:* %s\n", taskDisplay)
	if currentTask.Name != "" && currentTask.Name != "me" && currentTask.Name != taskDisplay {
		reply += fmt.Sprintf("📝 *Description:* %s\n", truncateString(currentTask.Name, 50))
	}
	reply += fmt.Sprintf("\n⏱️ *Started:* %s\n", session.StartedAt.Format("15:04:05"))
	reply += fmt.Sprintf("⌛ *Duration:* %dh %dm\n", hours, minutes)

	if lastMessage != "" {
		reply += fmt.Sprintf("💬 *Last:* %s\n", lastMessage)
	} else {
		reply += fmt.Sprintf("💬 *Messages:* %d\n", len(session.Messages))
	}

	// Auto-end info with actual time
	if remainingMinutes > 0 {
		endTime := time.Now().Add(time.Duration(remainingMinutes) * time.Minute)
		reply += fmt.Sprintf("\n⏰ Auto-end at %s (in %d minutes)",
			endTime.Format("15:04"), remainingMinutes)
	} else {
		reply += "\n⚠️ Session will timeout very soon"
	}

	b.reply(chatID, reply)
}

// truncateString truncates a string to maxRunes characters
func truncateString(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes-3]) + "..."
}

func (b *Bot) handleReport(ctx context.Context, chatID int64) {
	report, err := b.core.GetGroupedDailyReport(ctx, time.Now())
	if err != nil {
		logging.Error(ctx, "Failed to get daily report", "error", err)
		b.reply(chatID, "❌ Failed to generate report")
		return
	}

	if report.TotalTasks == 0 {
		b.reply(chatID, "📭 No completed activity recorded today.\n\n"+
			"Note: Active sessions are not included in the report.\n"+
			"Complete a session by:\n"+
			"• Starting a new task (auto-closes previous)\n"+
			"• Waiting for timeout\n"+
			"• Using /stop")
		return
	}

	reply := fmt.Sprintf("📅 *Daily Report - %s*\n\n", report.Date)

	for _, task := range report.Tasks {
		// Display name priority: ExternalID > Alias > TaskID
		displayName := task.TaskID
		if task.ExternalID != "" {
			displayName = task.ExternalID
		} else if task.Alias != "" {
			displayName = task.Alias
		}

		sessionInfo := ""
		if task.SessionCount > 1 {
			sessionInfo = fmt.Sprintf(" (%d sessions)", task.SessionCount)
		}

		reply += fmt.Sprintf("*%s* - %s%s\n",
			displayName,
			formatDuration(task.TotalMinutes),
			sessionInfo)

		// Show messages (up to 5 to avoid flooding)
		messageCount := len(task.Messages)
		if messageCount > 0 {
			maxShow := 5
			for i, msgText := range task.Messages {
				if i >= maxShow {
					remaining := messageCount - maxShow
					reply += fmt.Sprintf("  • ... and %d more message(s)\n", remaining)
					break
				}
				// Truncate long messages
				if len([]rune(msgText)) > 100 {
					msgText = truncateString(msgText, 100)
				}
				reply += fmt.Sprintf("  • %s\n", msgText)
			}
		}
		reply += "\n"
	}

	reply += fmt.Sprintf("---\n*Total:* %s (%d tasks)",
		formatDuration(report.TotalMinutes), report.TotalTasks)

	// Add note about active session if any
	currentTask := b.core.GetCurrentTask()
	if currentTask.ID != "" {
		reply += "\n\n⚠️ *Active session in progress* - not included in report"
	}

	b.reply(chatID, reply)
}

// formatDuration formats minutes into "Xh Ym"
func formatDuration(minutes int64) string {
	hours := minutes / 60
	mins := minutes % 60

	if hours > 0 && mins > 0 {
		return fmt.Sprintf("%dh %dm", hours, mins)
	} else if hours > 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dm", mins)
}

func (b *Bot) handlePurge(ctx context.Context, chatID int64, args string) {
	// Parse arguments
	args = strings.TrimSpace(args)

	var date *time.Time
	var confirmMsg string

	switch args {
	case "":
		// Purge today's data
		now := time.Now()
		date = &now
		confirmMsg = fmt.Sprintf("⚠️ This will delete ALL sessions for *%s*.\n\n", now.Format("2006-01-02")) +
			"This action cannot be undone.\n\n" +
			"Type *CONFIRM* to proceed."

	case "all":
		// Purge all data
		confirmMsg = "⚠️⚠️⚠️ *DANGER* ⚠️⚠️⚠️\n\n" +
			"This will delete *ALL* sessions and tasks from the database.\n" +
			"This action cannot be undone.\n\n" +
			"Type *CONFIRM ALL* to proceed."
		date = nil

	default:
		// Try to parse date
		parsedDate, err := time.Parse("2006-01-02", args)
		if err != nil {
			b.reply(chatID, "❌ Invalid date format. Use YYYY-MM-DD or 'all'")
			return
		}
		date = &parsedDate
		confirmMsg = fmt.Sprintf("⚠️ This will delete ALL sessions for *%s*.\n\n", parsedDate.Format("2006-01-02")) +
			"This action cannot be undone.\n\n" +
			"Type *CONFIRM* to proceed."
	}

	// Store pending purge request
	b.pendingPurge = &pendingPurge{
		chatID: chatID,
		date:   date,
	}

	b.reply(chatID, confirmMsg)
}

func (b *Bot) handleDirectMessage(ctx context.Context, message *tgbotapi.Message) {
	// Check for pending purge confirmation
	if b.pendingPurge != nil && b.pendingPurge.chatID == message.Chat.ID {
		response := strings.ToUpper(strings.TrimSpace(message.Text))

		var confirmed bool
		if b.pendingPurge.date == nil && response == "CONFIRM ALL" {
			confirmed = true
		} else if b.pendingPurge.date != nil && response == "CONFIRM" {
			confirmed = true
		}

		if confirmed {
			sessionsDeleted, tasksDeleted, err := b.core.PurgeSessions(ctx, b.pendingPurge.date)
			if err != nil {
				logging.Error(ctx, "Failed to purge", "error", err)
				b.reply(message.Chat.ID, "❌ Failed to purge data")
			} else {
				b.reply(message.Chat.ID, fmt.Sprintf(
					"✅ Purge completed\n📋 Sessions deleted: %d\n📋 Tasks deleted: %d",
					sessionsDeleted, tasksDeleted))
			}
			b.pendingPurge = nil
			return
		} else {
			b.reply(message.Chat.ID, "❌ Purge cancelled (confirmation mismatch)")
			b.pendingPurge = nil
			return
		}
	}

	receivedAt := time.Now().Round(time.Minute)
	text := message.Text

	// If it's a command, let handleCommand process it
	if message.IsCommand() {
		b.handleCommand(ctx, message)
		return
	}

	// Treat as manual task start
	if text != "" {
		b.handleManualTask(ctx, message.Chat.ID, text, receivedAt)
		return
	}

	// Voice message
	if message.Voice != nil {
		b.handleManualTask(ctx, message.Chat.ID, "[voice message]", receivedAt)
		return
	}

	// Default fallback
	reply := "Message received. To track work, forward a message from a colleague or send /task <description>"
	b.reply(message.Chat.ID, reply)
}

// handleManualTask processes tasks started manually (voice, text, etc.)
func (b *Bot) handleManualTask(ctx context.Context, chatID int64, description string, startTime time.Time) {
	logging.Info(ctx, "Processing manual task",
		"description", description,
		"start_time", startTime.Format(time.RFC3339))

	// Parse or generate task ID
	parsed := &secretary.ParsedMessage{
		TaskID:      secretary.GenerateInternalID(startTime),
		SenderName:  "me",
		StartTime:   startTime,
		Comment:     description,
		RawText:     description,
		IsForwarded: false,
	}

	// Try to extract task ID from description
	if taskID := secretary.ExtractTaskID(description); taskID != "" {
		parsed.TaskID = taskID
	}

	// Create event and process through core
	event := secretary.NewMessageEvent(
		startTime,
		"me",
		description,
		"",
		false,
	)

	if err := b.core.ProcessEvent(ctx, event); err != nil {
		logging.Error(ctx, "Failed to process manual task", "error", err)
		b.reply(chatID, "❌ Failed to start manual task")
		return
	}

	// Send acknowledgment
	reply := b.buildManualTaskAck(parsed)
	b.reply(chatID, reply)
}

func (b *Bot) parseDuration(text string) time.Duration {
	// Patterns: "на 2 часа", "на 30 мин", "1.5h", "90m"
	patterns := []struct {
		regex *regexp.Regexp
		unit  time.Duration
	}{
		{regexp.MustCompile(`на\s+(\d+(?:\.\d+)?)\s*часа?`), time.Hour},
		{regexp.MustCompile(`на\s+(\d+(?:\.\d+)?)\s*мин`), time.Minute},
		{regexp.MustCompile(`(\d+(?:\.\d+)?)\s*h`), time.Hour},
		{regexp.MustCompile(`(\d+(?:\.\d+)?)\s*m`), time.Minute},
	}

	for _, p := range patterns {
		if matches := p.regex.FindStringSubmatch(text); len(matches) > 1 {
			if val, err := strconv.ParseFloat(matches[1], 64); err == nil {
				return time.Duration(val * float64(p.unit))
			}
		}
	}
	return 0
}

func (b *Bot) handleTimedTask(ctx context.Context, chatID int64, description string, startTime time.Time, duration time.Duration) {
	// Create task with custom timeout
	parsed := &secretary.ParsedMessage{
		TaskID:      secretary.GenerateInternalID(startTime),
		SenderName:  "me",
		StartTime:   startTime,
		Comment:     description,
		RawText:     description,
		IsForwarded: false,
	}

	if taskID := secretary.ExtractTaskID(description); taskID != "" {
		parsed.TaskID = taskID
	}

	// Save to core with custom timeout
	// Store duration in session metadata
	b.core.StartTimedTask(parsed, duration)

	reply := fmt.Sprintf("✅ Task started for %s (will auto-end after that time)",
		duration.String())
	b.reply(chatID, reply)
}

// buildManualTaskAck creates acknowledgment for manual tasks
func (b *Bot) buildManualTaskAck(parsed *secretary.ParsedMessage) string {
	ack := "✅ Manual work session started\n\n"
	ack += "📋 Task: " + parsed.TaskID + "\n"
	ack += "📝 Description: " + parsed.Comment + "\n"
	ack += "⏰ Started: " + parsed.StartTime.Format("15:04") + "\n"
	ack += "⏱️ Will auto-end after " + fmt.Sprintf("%d", b.core.GetTimeoutMinutes()) + " minutes of inactivity"
	return ack
}

// buildForwardedAck creates acknowledgment message for forwarded messages
func (b *Bot) buildForwardedAck(parsed *secretary.ParsedMessage) string {
	roundedStart := parsed.StartTime.Round(time.Minute)

	ack := "✅ Work session started\n\n"
	ack += "📋 Task: " + parsed.TaskID + "\n"
	ack += "👤 From: " + parsed.SenderName + "\n"
	ack += "⏰ Started: " + roundedStart.Format("15:04") + "\n"
	ack += "⏱️ Will auto-end after " + fmt.Sprintf("%d", b.core.GetTimeoutMinutes()) + " minutes of inactivity"

	if parsed.Comment != "" {
		ack += "\n📝 Comment: " + parsed.Comment
	}

	return ack
}

func (b *Bot) handleStop(ctx context.Context, chatID int64) {
	// Check if there's an active session
	currentTask := b.core.GetCurrentTask()
	if currentTask.ID == "" {
		b.reply(chatID, "❌ No active session to stop")
		return
	}

	// Stop the current session
	if err := b.core.StopCurrentSession(ctx); err != nil {
		logging.Error(ctx, "Failed to stop session", "error", err)
		b.reply(chatID, "❌ Failed to stop current session")
		return
	}

	// Get session info for response
	session := b.core.GetCurrentSession()

	reply := "✅ Session stopped\n\n"
	if session != nil {
		// Session was just stopped, get it from DB
		reply = "✅ Session stopped successfully"
	} else {
		reply = "✅ Session stopped successfully"
	}

	b.reply(chatID, reply)
}

func (b *Bot) handleLink(ctx context.Context, chatID int64, args string) {
	args = strings.TrimSpace(args)
	if args == "" {
		b.reply(chatID, "Usage:\n"+
			"• /link <JIRA-ID> - link current task\n"+
			"• /link <JIRA-ID> <task-id> - link specific task\n"+
			"• /link <JIRA-ID> -today - link all today's tasks without Jira ID\n"+
			"• /link <JIRA-ID> -all - link all tasks without Jira ID")
		return
	}

	parts := strings.Fields(args)
	if len(parts) < 1 {
		b.reply(chatID, "❌ Invalid format. Usage: /link <JIRA-ID> [task-id|--today|--all]")
		return
	}

	externalID := parts[0]

	// Validate Jira ID format
	if !regexp.MustCompile(`^[A-Z][A-Z0-9]*-\d+$`).MatchString(externalID) {
		b.reply(chatID, fmt.Sprintf("❌ Invalid Jira ID format: %s", externalID))
		return
	}

	if len(parts) == 1 {
		// Link current task
		if err := b.core.LinkCurrentTask(ctx, externalID); err != nil {
			logging.Error(ctx, "Failed to link current task", "error", err)
			b.reply(chatID, fmt.Sprintf("❌ Failed to link current task: %v", err))
			return
		}
		b.reply(chatID, fmt.Sprintf("✅ Current task linked to Jira: %s", externalID))
		return
	}

	target := parts[1]

	switch target {
	case "-today":
		count, err := b.core.LinkTasksTodayWithoutExternalID(ctx, externalID)
		if err != nil {
			logging.Error(ctx, "Failed to link today's tasks", "error", err)
			b.reply(chatID, fmt.Sprintf("❌ Failed to link today's tasks: %v", err))
			return
		}
		b.reply(chatID, fmt.Sprintf("✅ Linked %d task(s) from today to Jira: %s", count, externalID))

	case "-all":
		count, err := b.core.LinkAllTasksWithoutExternalID(ctx, externalID)
		if err != nil {
			logging.Error(ctx, "Failed to link all tasks", "error", err)
			b.reply(chatID, fmt.Sprintf("❌ Failed to link all tasks: %v", err))
			return
		}
		b.reply(chatID, fmt.Sprintf("✅ Linked %d task(s) to Jira: %s", count, externalID))

	default:
		// Link specific task by ID
		taskID := target
		if err := b.core.LinkTaskByID(ctx, taskID, externalID); err != nil {
			logging.Error(ctx, "Failed to link task", "task_id", taskID, "error", err)
			b.reply(chatID, fmt.Sprintf("❌ Failed to link task %s: %v", taskID, err))
			return
		}
		b.reply(chatID, fmt.Sprintf("✅ Task %s linked to Jira: %s", taskID, externalID))
	}
}

func (b *Bot) handleExtend(ctx context.Context, chatID int64, args string) {
	args = strings.TrimSpace(args)
	if args == "" {
		b.reply(chatID, "Usage: /extend <duration> (e.g., /extend 30, /extend 1h, /extend 90m)")
		return
	}

	// Parse duration
	duration, err := parseDuration(args)
	if err != nil {
		b.reply(chatID, fmt.Sprintf("❌ Invalid duration: %s", err))
		return
	}

	if duration <= 0 {
		b.reply(chatID, "❌ Duration must be positive")
		return
	}

	// Extend current session
	if err := b.core.ExtendCurrentSession(ctx, duration); err != nil {
		logging.Error(ctx, "Failed to extend session", "error", err)
		b.reply(chatID, fmt.Sprintf("❌ Failed to extend session: %v", err))
		return
	}

	b.reply(chatID, fmt.Sprintf("✅ Session extended by %s", formatDurationSimple(duration)))
}

func formatDurationSimple(d time.Duration) string {
	minutes := int(d.Minutes())
	if minutes >= 60 {
		hours := minutes / 60
		mins := minutes % 60
		if mins > 0 {
			return fmt.Sprintf("%dh %dm", hours, mins)
		}
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dm", minutes)
}
