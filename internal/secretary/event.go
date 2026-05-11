// internal/secretary/event.go

package secretary

import (
	"time"
)

// EventType defines possible event types
type EventType string

const (
	EventTypeMessage    EventType = "message"
	EventTypeCommand    EventType = "command"
	EventTypeTimeout    EventType = "timeout"
	EventTypeDayStart   EventType = "day_start"
	EventTypeDayEnd     EventType = "day_end"
	EventTypeLunchStart EventType = "lunch_start"
	EventTypeLunchEnd   EventType = "lunch_end"
)

// Event represents an incoming event from any source
type Event struct {
	ID        string                 `json:"id"`
	Type      EventType              `json:"type"`
	Timestamp time.Time              `json:"timestamp"`
	Source    string                 `json:"source"` // "telegram", "system", "cli"
	Data      map[string]interface{} `json:"data"`
}

// MessageEventData carries message-specific data
type MessageEventData struct {
	SenderName string
	Text       string
	Comment    string
	IsForward  bool
}

// CommandEventData carries command-specific data
type CommandEventData struct {
	Command string
	Args    string
}

// NewMessageEvent creates a new message event
func NewMessageEvent(timestamp time.Time, senderName, text, comment string, isForward bool) Event {
	return Event{
		ID:        GenerateInternalID(timestamp) + "-msg",
		Type:      EventTypeMessage,
		Timestamp: timestamp,
		Source:    "telegram",
		Data: map[string]interface{}{
			"sender_name": senderName,
			"text":        text,
			"comment":     comment,
			"is_forward":  isForward,
		},
	}
}

// NewCommandEvent creates a new command event
func NewCommandEvent(timestamp time.Time, command, args string) Event {
	return Event{
		ID:        GenerateInternalID(timestamp) + "-cmd",
		Type:      EventTypeCommand,
		Timestamp: timestamp,
		Source:    "telegram",
		Data: map[string]interface{}{
			"command": command,
			"args":    args,
		},
	}
}
