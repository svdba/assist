// internal/logging/jsonlogging.go
package logging

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type JSONLogEntry struct {
	Logger    string                 `json:"logger_name"`
	LevelName string                 `json:"level_name"`
	Message   interface{}            `json:"message"`
	Time      string                 `json:"time"`
	Context   map[string]interface{} `json:"context"`
}

// TryParseJSONLine tries to parse a line as JSON and detect its type
func TryParseJSONLine(line string) (*JSONLogEntry, bool, string) {
	line = strings.TrimSpace(line)
	if len(line) < 2 || (line[0] != '{' && line[0] != '[') {
		return nil, false, ""
	}

	var entry JSONLogEntry
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		// Not a JSON log entry
		return nil, false, ""
	}

	// Default JSON log
	return &entry, true, "json"
}

// HandleJSONLog processes JSON log entries
func HandleJSONLog(ctx context.Context, entry *JSONLogEntry, loggerName string, additionalFields ...map[string]interface{}) {
	fields := map[string]interface{}{
		"@version":    logVersion,
		"logger_name": loggerName,
	}

	// Merge additional fields if provided
	for _, extra := range additionalFields {
		for k, v := range extra {
			fields[k] = v
		}
	}

	// Add context from JSON entry
	for k, v := range entry.Context {
		fields[k] = v
	}

	// Handle message
	switch msg := entry.Message.(type) {
	case string:
		logWithLevel(ctx, entry.LevelName, msg, fields)
	case map[string]interface{}:
		// If message is a map, treat it as additional fields
		for k, v := range msg {
			fields[k] = v
		}
		// Use a default message or empty
		logWithLevel(ctx, entry.LevelName, "", fields)
	default:
		// Convert to string
		logWithLevel(ctx, entry.LevelName, fmt.Sprintf("%v", msg), fields)
	}
}

func logWithLevel(ctx context.Context, levelName, message string, fields map[string]interface{}) {
	switch strings.ToLower(levelName) {
	case "emergency", "alert", "critical", "error":
		if message == "" {
			message = "error"
		}
		instance.WithFields(fields).Error(message)
	case "warning", "warn":
		if message == "" {
			message = "warning"
		}
		instance.WithFields(fields).Warn(message)
	case "info", "notice":
		if message == "" {
			message = "info"
		}
		instance.WithFields(fields).Info(message)
	case "debug":
		if message == "" {
			message = "debug"
		}
		instance.WithFields(fields).Debug(message)
	default:
		if message == "" {
			message = "log"
		}
		instance.WithFields(fields).Info(message)
	}
}
