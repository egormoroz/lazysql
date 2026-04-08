package main

import (
	"io"
	"log/slog"
	"os"
)

const defaultLogFile = "/tmp/pgviewer.log"

// InitLogger sets up the global slog logger to write JSON to a file.
// Returns a closer function that should be deferred.
func InitLogger(path string) (func(), error) {
	if path == "" {
		// Logging disabled — send to nowhere.
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
		return func() {}, nil
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}

	handler := slog.NewJSONHandler(f, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})
	slog.SetDefault(slog.New(handler))

	slog.Info("logger initialized", "file", path)
	return func() { f.Close() }, nil
}
