// internal/secretary/message_parser.go

package secretary

import (
	"regexp"
	"time"
)

// ParsedMessage contains extracted information from a Telegram message
type ParsedMessage struct {
	TaskID      string    // extracted task ID or generated internal ID
	StartTime   time.Time // when work started
	SenderName  string    // who sent the original message
	Comment     string    // optional user comment
	RawText     string    // original message text
	IsForwarded bool      // whether this is a forwarded message
}

// ParseMessage extracts task ID and other info from message
// Stub implementation for now - just logging
func ParseMessage(text string, forwardFrom string, receivedAt time.Time, userComment string) *ParsedMessage {
	parsed := &ParsedMessage{
		SenderName:  forwardFrom,
		StartTime:   receivedAt,
		Comment:     userComment,
		RawText:     text,
		IsForwarded: forwardFrom != "",
	}

	// TODO: Extract task ID using regex pattern
	// Pattern example: [A-Z]+-\d+ (e.g., "PROJ-123")
	taskID := extractTaskID(text)
	if taskID == "" {
		taskID = extractTaskID(userComment)
	}

	if taskID != "" {
		parsed.TaskID = taskID
	} else {
		// Generate internal ID
		parsed.TaskID = GenerateInternalID(receivedAt)
	}

	// TODO: Extract explicit time if present
	// Pattern example: "с 14:30" or "start=15:00"

	return parsed
}

// ExtractTaskID looks for pattern like PROJECT-123, TASK-42, etc.
// Exported for use from bot package
func ExtractTaskID(text string) string {
	if text == "" {
		return ""
	}

	// Pattern for Jira-like tickets: letters, hyphen, numbers
	pattern := regexp.MustCompile(`[A-Z][A-Z0-9]*-\d+`)
	return pattern.FindString(text)
}

// extractTaskID is now a wrapper (keep for internal use)
func extractTaskID(text string) string {
	return ExtractTaskID(text)
}

// generateInternalID creates an internal ID when no external ID found
func GenerateInternalID(t time.Time) string {
	return "int-" + t.Format("0102-1504")
}
