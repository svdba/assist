// internal/logging/logging.go
package logging

import (
	"context"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	loggerWrapperDepth = 8
	// RequestIDKey is the key for request ID in context
	RequestIDKey ContextKey = "request_id"
	// LoggerKey is the key for logger in context
	LoggerKey ContextKey = "logger"
)

type ContextKey string

// Config holds logging configuration
type Config struct {
	Level      string
	Format     string
	Output     io.Writer
	AppName    string
	AppAsSfx   string
	WithCaller bool
	Verbose    bool
	Mock       bool
}

// Interface for structured logging
type StructuredLogger interface {
	Error(msg string, fields ...interface{})
	Info(msg string, fields ...interface{})
	Debug(msg string, fields ...interface{})
	Warn(msg string, fields ...interface{})
	WithFields(fields map[string]interface{}) StructuredLogger
}

const (
	logVersion = "1"
)

var (
	instance *log.Logger
	config   Config
)

// Init initializes the logging system
func Init(cfg Config) {
	config = cfg
	if config.AppName == "" {
		config.AppName = "poc-bin"
	}
	config.AppAsSfx = "/" + config.AppName + "/"

	// Initialize logrus
	instance = log.New()

	// Set output
	if config.Output != nil {
		instance.SetOutput(config.Output)
	}

	// Set level
	config.Mock = setLevel(cfg.Level) || cfg.Mock

	// Set format
	setFormat(cfg.Format)

}

func setLevel(level string) bool {
	var mock = false
	switch strings.ToLower(level) {
	case "trace":
		instance.SetLevel(log.TraceLevel)
	case "debug":
		instance.SetLevel(log.DebugLevel)
	case "info":
		instance.SetLevel(log.InfoLevel)
	case "warn", "warning":
		instance.SetLevel(log.WarnLevel)
	case "err", "error":
		instance.SetLevel(log.ErrorLevel)
	case "fatal":
		instance.SetLevel(log.FatalLevel)
	case "panic":
		instance.SetLevel(log.PanicLevel)
	case "off":
		mock = true
		instance.SetLevel(log.PanicLevel)
		instance.SetOutput(ioutil.Discard)
	default:
		instance.SetLevel(log.InfoLevel)
	}
	return mock
}

func setFormat(format string) {
	if strings.ToLower(format) == "json" {
		instance.SetFormatter(&log.JSONFormatter{
			TimestampFormat: time.RFC3339Nano,
			FieldMap: log.FieldMap{
				log.FieldKeyTime: "@timestamp",
				log.FieldKeyMsg:  "message",
			},
		})
	} else {
		instance.SetFormatter(&log.TextFormatter{
			TimestampFormat: time.RFC3339Nano,
			FullTimestamp:   true,
		})
	}
}

// NewRequestID generates a new request ID
func NewRequestID() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strconv.FormatInt(int64(os.Getpid()), 36)
}

// Helper function to get source location
func getCallerInfo(skip int) (thread string, source string) {

	pc, f, n, _ := runtime.Caller(skip)
	thread = runtime.FuncForPC(pc).Name()
	if threadIdx := strings.Index(thread, config.AppAsSfx); threadIdx != -1 {
		thread = thread[threadIdx+len(config.AppAsSfx):]
	}

	if fIdx := strings.Index(f, config.AppAsSfx); fIdx != -1 {
		f = f[fIdx+len(config.AppAsSfx):]
	}
	source = f + ":" + strconv.Itoa(n)

	return
}

// Structured logging methods with improved performance
func Error(ctx context.Context, msg string, fields ...interface{}) {
	if config.Mock {
		fieldMap := variadicToMap(fields...)
		mockLog(ctx, "error", msg, fieldMap)
		return
	}
	logWithContext(ctx, log.ErrorLevel, msg, fields...)
}
func Errorf(ctx context.Context, msg string, a ...interface{}) {
	if !config.Mock {
		logfWithContext(ctx, log.ErrorLevel, msg, a...)
	}
}

func Info(ctx context.Context, msg string, fields ...interface{}) {
	if config.Mock {
		fieldMap := variadicToMap(fields...)
		mockLog(ctx, "info", msg, fieldMap)
		return
	}
	logWithContext(ctx, log.InfoLevel, msg, fields...)
}
func Infof(ctx context.Context, msg string, a ...interface{}) {
	if !config.Mock {
		logfWithContext(ctx, log.InfoLevel, msg, a...)
	}
}

func Debug(ctx context.Context, msg string, fields ...interface{}) {
	if config.Mock {
		fieldMap := variadicToMap(fields...)
		mockLog(ctx, "debug", msg, fieldMap)
		return
	}
	logWithContext(ctx, log.DebugLevel, msg, fields...)
}
func Debugf(ctx context.Context, msg string, a ...interface{}) {
	if !config.Mock {
		logfWithContext(ctx, log.DebugLevel, msg, a...)
	}
}

func Warn(ctx context.Context, msg string, fields ...interface{}) {
	if config.Mock {
		fieldMap := variadicToMap(fields...)
		mockLog(ctx, "warn", msg, fieldMap)
		return
	}
	logWithContext(ctx, log.WarnLevel, msg, fields...)
}
func Warnf(ctx context.Context, msg string, a ...interface{}) {
	if !config.Mock {
		logfWithContext(ctx, log.WarnLevel, msg, a...)
	}
}

func Fatal(ctx context.Context, msg string, fields ...interface{}) {
	if !config.Mock {
		logWithContext(ctx, log.FatalLevel, msg, fields...)
	}
}
func Fatalf(ctx context.Context, msg string, a ...interface{}) {
	if !config.Mock {
		logfWithContext(ctx, log.FatalLevel, msg, a...)
	}
}

func logEntryWithContext(ctx context.Context) *log.Entry {
	entry := instance.WithTime(time.Now())

	// Add context fields
	if ctx != nil {
		if reqID := ctx.Value("request_id"); reqID != nil {
			entry = entry.WithField("request_id", reqID)
		}
	}

	// Add caller info for debug/trace levels
	if config.WithCaller {
		thread, source := getCallerInfo(4)
		entry = entry.WithFields(log.Fields{
			"@version":    logVersion,
			"logger_name": config.AppName,
			"thread":      thread,
			"source":      source,
		})
	} else {
		entry = entry.WithFields(log.Fields{
			"@version":    logVersion,
			"logger_name": config.AppName,
		})
	}

	return entry

}

func logfWithContext(ctx context.Context, level log.Level, msg string, a ...interface{}) {

	entry := logEntryWithContext(ctx)

	// Log the message
	switch level {
	case log.PanicLevel:
		entry.Panicf(msg, a...)
	case log.FatalLevel:
		entry.Fatalf(msg, a...)
	case log.ErrorLevel:
		entry.Errorf(msg, a...)
	case log.WarnLevel:
		entry.Warnf(msg, a...)
	case log.InfoLevel:
		entry.Infof(msg, a...)
	case log.DebugLevel:
		entry.Debugf(msg, a...)
	case log.TraceLevel:
		entry.Tracef(msg, a...)
	}
}

func logWithContext(ctx context.Context, level log.Level, msg string, fields ...interface{}) {
	entry := logEntryWithContext(ctx)

	// Convert variadic fields to map
	if len(fields) > 0 {
		fieldMap := make(log.Fields)
		for i := 0; i < len(fields); i += 2 {
			if i+1 < len(fields) {
				key, ok := fields[i].(string)
				if ok {
					fieldMap[key] = fields[i+1]
				}
			}
		}
		entry = entry.WithFields(fieldMap)
	}

	// Log the message
	switch level {
	case log.PanicLevel:
		entry.Panic(msg)
	case log.FatalLevel:
		entry.Fatal(msg)
	case log.ErrorLevel:
		entry.Error(msg)
	case log.WarnLevel:
		entry.Warn(msg)
	case log.InfoLevel:
		entry.Info(msg)
	case log.DebugLevel:
		entry.Debug(msg)
	case log.TraceLevel:
		entry.Trace(msg)
	}
}

// HTTP middleware for request logging
type RequestLogger struct {
	Logger *log.Entry
}

func (l *RequestLogger) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Create response wrapper to capture status code
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		// Process request
		next.ServeHTTP(rw, r)

		// Log request
		duration := time.Since(start)
		l.Logger.WithFields(log.Fields{
			"method":     r.Method,
			"path":       r.URL.Path,
			"query":      r.URL.RawQuery,
			"ip":         r.RemoteAddr,
			"user_agent": r.UserAgent(),
			"status":     rw.statusCode,
			"duration":   duration.Seconds(),
			"bytes":      rw.bytesWritten,
		}).Info("http_request")
	})
}

type responseWriter struct {
	http.ResponseWriter
	statusCode   int
	bytesWritten int64
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	n, err := rw.ResponseWriter.Write(b)
	rw.bytesWritten += int64(n)
	return n, err
}
