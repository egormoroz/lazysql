package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const maxCellWidth = 40

// FormatCell converts an arbitrary pgx value to a display string.
func FormatCell(v any) string {
	if v == nil {
		return "NULL"
	}
	var s string
	switch val := v.(type) {
	case string:
		s = val
	case []byte:
		s = string(val)
	case int16, int32, int64, float32, float64:
		s = fmt.Sprintf("%v", val)
	case bool:
		if val {
			s = "true"
		} else {
			s = "false"
		}
	case time.Time:
		if val.Hour() == 0 && val.Minute() == 0 && val.Second() == 0 && val.Nanosecond() == 0 {
			s = val.Format("2006-01-02")
		} else {
			s = val.Format("2006-01-02 15:04:05")
		}
	case map[string]any:
		s = formatJSON(val)
	case []any:
		s = formatArray(val)
	default:
		s = fmt.Sprintf("%v", val)
	}
	return Truncate(s, maxCellWidth)
}

func formatJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

func formatArray(arr []any) string {
	parts := make([]string, len(arr))
	for i, v := range arr {
		parts[i] = fmt.Sprintf("%v", v)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// Truncate shortens s to maxLen, appending an ellipsis if truncated.
func Truncate(s string, maxLen int) string {
	// Replace newlines/tabs with spaces for single-line display.
	s = strings.NewReplacer("\n", "\\n", "\r", "", "\t", " ").Replace(s)
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-1]) + "…"
}

// ColumnWidth computes a reasonable display width for a column
// based on its name length and a sample of values.
func ColumnWidth(name string, samples []string, min, max int) int {
	w := len(name)
	for _, s := range samples {
		if len(s) > w {
			w = len(s)
		}
	}
	// Clamp.
	if w < min {
		w = min
	}
	if w > max {
		w = max
	}
	return w
}
