// internal/logging/logging_mock.go - Additional mock functions
package logging

import (
	"context"
	"strings"
	"sync"
	"time"
)

// MockLogger captures log calls for testing
type MockLogger struct {
	mu        sync.RWMutex
	Logs      []LogEntry
	CallCount map[string]int
	Enabled   bool
	LastError error
}

type LogEntry struct {
	Level   string
	Message string
	Fields  map[string]interface{}
	Context context.Context
	Time    time.Time
}

var (
	mockLogger *MockLogger
)

// InitMock initializes the mock logger for testing
func InitMock() *MockLogger {
	mockLogger = &MockLogger{
		Logs:      make([]LogEntry, 0),
		CallCount: make(map[string]int),
		Enabled:   true,
	}

	// Set global config to mock mode
	config.Mock = true

	return mockLogger
}

// Clear resets the mock logger
func (m *MockLogger) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.Logs = make([]LogEntry, 0)
	m.CallCount = make(map[string]int)
	m.LastError = nil
}

// ClearMock is a convenience function that clears the global mock logger
func ClearMock() {
	if mockLogger != nil {
		mockLogger.Clear()
	}
}

// GetMock returns the mock logger instance
func GetMock() *MockLogger {
	if mockLogger == nil {
		mockLogger = &MockLogger{
			Logs:      make([]LogEntry, 0),
			CallCount: make(map[string]int),
			Enabled:   true,
		}
	}
	return mockLogger
}

// SetMock enables or disables mock mode
func SetMock(enabled bool) {
	config.Mock = enabled
	if mockLogger != nil {
		mockLogger.Enabled = enabled
	}
}

// mockLog records a log entry in the mock logger
func mockLog(ctx context.Context, level, msg string, fields map[string]interface{}) {
	if mockLogger == nil || !mockLogger.Enabled {
		return
	}

	mockLogger.mu.Lock()
	defer mockLogger.mu.Unlock()

	entry := LogEntry{
		Level:   level,
		Message: msg,
		Fields:  fields,
		Context: ctx,
		Time:    time.Now(),
	}

	mockLogger.Logs = append(mockLogger.Logs, entry)
	mockLogger.CallCount[level]++
}

// Helper to convert variadic fields to map
func variadicToMap(fields ...interface{}) map[string]interface{} {
	if len(fields) == 0 {
		return nil
	}

	fieldMap := make(map[string]interface{})
	for i := 0; i < len(fields)-1; i += 2 {
		if key, ok := fields[i].(string); ok {
			fieldMap[key] = fields[i+1]
		}
	}
	return fieldMap
}

// GetLogs returns all captured log entries
func (m *MockLogger) GetLogs() []LogEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	logs := make([]LogEntry, len(m.Logs))
	copy(logs, m.Logs)
	return logs
}

// GetLogCount returns the number of logs for a specific level
func (m *MockLogger) GetLogCount(level string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.CallCount[level]
}

// ContainsMessage checks if any log contains the given message
func (m *MockLogger) ContainsMessage(msg string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, entry := range m.Logs {
		if strings.Contains(entry.Message, msg) {
			return true
		}
	}
	return false
}

// GetLastLog returns the most recent log entry
func (m *MockLogger) GetLastLog() *LogEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.Logs) == 0 {
		return nil
	}
	return &m.Logs[len(m.Logs)-1]
}

// FindLogsByLevel returns all logs of a specific level
func (m *MockLogger) FindLogsByLevel(level string) []LogEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []LogEntry
	for _, entry := range m.Logs {
		if entry.Level == level {
			result = append(result, entry)
		}
	}
	return result
}

// FindLogsByMessage returns all logs containing the given message
func (m *MockLogger) FindLogsByMessage(substring string) []LogEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []LogEntry
	for _, entry := range m.Logs {
		if strings.Contains(entry.Message, substring) {
			result = append(result, entry)
		}
	}
	return result
}

// GetTotalLogs returns the total number of captured logs
func (m *MockLogger) GetTotalLogs() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return len(m.Logs)
}

// SetLastError sets an error for testing error scenarios
func (m *MockLogger) SetLastError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.LastError = err
}

// GetLastError returns the last set error
func (m *MockLogger) GetLastError() error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.LastError
}
