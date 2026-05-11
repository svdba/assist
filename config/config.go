// config/config.go

package config

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/svdba/assist/version"
)

type Config struct {
	TelegramToken  string
	TelegramAPIURL string // Optional: custom API endpoint for debugging
	LogLevel       string
	LogFormat      string
	Secretary      SecretaryConfig
}

type SecretaryConfig struct {
	DataDir               string
	TimeoutMinutes        int
	WorkStart             string
	WorkEnd               string
	LunchStart            string
	LunchEnd              string
	WorkDays              []string
	DefaultTaskName       string
	BoundaryNotifyMinutes int
}

var cfg Config

func Load() (*Config, error) {
	WorkDaysString := getEnvDefault("SECRETARY_WORK_DAYS", "monday,tuesday,wednesday,thursday,friday")
	cfg = Config{
		TelegramToken:  os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramAPIURL: os.Getenv("TELEGRAM_API_URL"),
		LogLevel:       os.Getenv("LOG_LEVEL"),
		LogFormat:      os.Getenv("LOG_FORMAT"),
		Secretary: SecretaryConfig{
			DataDir:         getEnvDefault("SECRETARY_DATA_DIR", "./data"),
			TimeoutMinutes:  getEnvIntDefault("SECRETARY_TIMEOUT_MINUTES", 45),
			WorkStart:       getEnvDefault("SECRETARY_WORK_START", "09:00"),
			WorkEnd:         getEnvDefault("SECRETARY_WORK_END", "18:00"),
			LunchStart:      getEnvDefault("SECRETARY_LUNCH_START", "13:00"),
			LunchEnd:        getEnvDefault("SECRETARY_LUNCH_END", "14:00"),
			WorkDays:        strings.Split(WorkDaysString, ","),
			DefaultTaskName: getEnvDefault("SECRETARY_DEFAULT_TASK", "misc"),
		},
	}

	showVersion := flag.Bool("version", false, "Show version and exit")

	// Command line flags
	flag.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "Log level (debug, info, warn, error)")
	flag.StringVar(&cfg.LogFormat, "log-format", cfg.LogFormat, "Log format (text, json)")
	flag.StringVar(&cfg.Secretary.DataDir, "data-dir", cfg.Secretary.DataDir, "Directory for persistent storage")
	flag.IntVar(&cfg.Secretary.TimeoutMinutes, "timeout", cfg.Secretary.TimeoutMinutes, "Inactivity timeout in minutes")
	flag.StringVar(&cfg.Secretary.WorkStart, "work-start", cfg.Secretary.WorkStart, "Working day start time (HH:MM)")
	flag.StringVar(&cfg.Secretary.WorkEnd, "work-end", cfg.Secretary.WorkEnd, "Working day end time (HH:MM)")
	flag.StringVar(&cfg.Secretary.LunchStart, "lunch-start", cfg.Secretary.LunchStart, "Lunch break start (HH:MM)")
	flag.StringVar(&cfg.Secretary.LunchEnd, "lunch-end", cfg.Secretary.LunchEnd, "Lunch break end (HH:MM)")
	flag.StringVar(&cfg.Secretary.DefaultTaskName, "default-task", cfg.Secretary.DefaultTaskName, "Default task name")

	flag.Parse()

	if *showVersion {
		fmt.Println(version.String())
		os.Exit(0)
	}

	// Set default API URL if not provided
	if cfg.TelegramAPIURL == "" {
		cfg.TelegramAPIURL = "https://api.telegram.org/"
	}

	// Note: Token validation is optional for now
	// Bot will be created only if token is provided

	return &cfg, nil
}

// Helper functions remain the same
func getEnvDefault(key, def string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return def
}

func getEnvIntDefault(key string, def int) int {
	if val := os.Getenv(key); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			return i
		}
	}
	return def
}
