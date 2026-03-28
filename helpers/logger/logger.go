package logger

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type logger struct {
	mu            sync.Mutex
	file          *os.File
	errorFile     *os.File
	level         slog.Level
	output        string
}

type logMessage struct {
	Timestamp string         `json:"timestamp"`
	Level     string         `json:"level"`
	Message   string         `json:"message"`
	Data      map[string]any `json:"additional_info,omitempty"`
}

var logInstance *logger

func init() {
	l := &logger{level: slog.LevelInfo}
	l.errorFile = openDefaultErrorFile()
	logInstance = l
}

// openDefaultErrorFile opens ~/.config/lazysql/errors.log, creating the
// directory if needed. Returns nil on failure (errors are silently ignored
// since we have no safe output channel in a TUI).
func openDefaultErrorFile() *os.File {
	configDir := os.Getenv("XDG_CONFIG_HOME")
	if configDir == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return nil
		}
		configDir = dir
	}
	dir := filepath.Join(configDir, "lazysql")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(dir, "errors.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY|os.O_SYNC, 0644)
	if err != nil {
		return nil
	}
	return f
}

func (l *logger) writeToFile(f *os.File, data []byte) {
	if f == nil {
		return
	}
	_, _ = f.Write(data)
	_, _ = f.Write([]byte("\n"))
}

func (l *logger) log(level slog.Level, msg string, data map[string]any) {
	if level < l.level && l.file != nil {
		return
	}
	// Errors are always logged regardless of configured level.
	if level < l.level && level < slog.LevelError {
		return
	}

	logMessage := logMessage{
		Timestamp: time.Now().Format(time.RFC3339),
		Level:     level.String(),
		Message:   msg,
		Data:      data,
	}

	logData, err := json.Marshal(logMessage)
	if err != nil {
		fmt.Println("Error marshaling log message:", err)
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.file != nil {
		// Explicit logfile: everything goes here.
		l.writeToFile(l.file, logData)
	} else if level >= slog.LevelError {
		// No explicit logfile: errors always go to the default error log.
		l.writeToFile(l.errorFile, logData)
	}
}

func (l *logger) SetFile(filename string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.file != nil {
		err := l.file.Close()
		if err != nil {
			return err
		}
	}

	file, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}

	l.file = file
	l.output = filename
	return nil
}

func (l *logger) SetLevel(level slog.Level) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.level = level
}

func SetLevel(level slog.Level) {
	logInstance.SetLevel(level)
}

func SetFile(filename string) error {
	return logInstance.SetFile(filename)
}

func Debug(msg string, data map[string]any) {
	logInstance.log(slog.LevelDebug, msg, data)
}

func Info(msg string, data map[string]any) {
	logInstance.log(slog.LevelInfo, msg, data)
}

func Warn(msg string, data map[string]any) {
	logInstance.log(slog.LevelWarn, msg, data)
}

func Error(msg string, data map[string]any) {
	logInstance.log(slog.LevelError, msg, data)
}

func ParseLogLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("unknown log level %q", s)
	}
}
