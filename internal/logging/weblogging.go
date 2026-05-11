// internal/logging/weblogging.go
package logging

import (
	"net"
	"net/http"
	"time"
)

// WebLogger creates a middleware that logs HTTP requests
func WebLogger(skipper func(r *http.Request) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip logging if skipper returns true
			if skipper != nil && skipper(r) {
				next.ServeHTTP(w, r)
				return
			}

			start := time.Now()

			// Wrap the response writer to capture status code
			ww := &responseWriterWrapper{ResponseWriter: w}

			defer func() {
				ip, _, err := net.SplitHostPort(r.RemoteAddr)
				if err != nil {
					ip = "0.0.0.0"
				}
				stop := time.Now()
				latency := stop.Sub(start)

				// Log the request details
				fields := map[string]interface{}{
					"@version":    logVersion,
					"logger_name": config.AppName,
					"component":   "http_handler",
					"method":      r.Method,
					"uri":         r.RequestURI,
					"status":      ww.status,
					"latency":     latency.String(),
					"latency_ms":  latency.Milliseconds(),
					"remote_addr": r.RemoteAddr,
					"user_agent":  r.UserAgent(),
					"referer":     r.Referer(),
					"ip":          ip,
				}

				// Log with appropriate level based on status code
				switch {
				case ww.status >= 500:
					instance.WithFields(fields).Error("Server error")
				case ww.status >= 400:
					instance.WithFields(fields).Warn("Client error")
				case ww.status >= 300:
					instance.WithFields(fields).Info("Redirection")
				default:
					instance.WithFields(fields).Info("Request processed")
				}

			}()

			next.ServeHTTP(ww, r)
		})
	}
}

// responseWriterWrapper wraps http.ResponseWriter to capture status code
type responseWriterWrapper struct {
	http.ResponseWriter
	status int
}

func (w *responseWriterWrapper) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseWriterWrapper) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func CreateSkipper() func(r *http.Request) bool {
	return func(r *http.Request) bool {
		return (r.URL.Path == "/health" || r.URL.Path == "/ready" || r.URL.Path == "/metrics")
	}
}
