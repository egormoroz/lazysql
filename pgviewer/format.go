package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
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

// Truncate shortens s to maxLen display cells, appending an ellipsis if truncated.
func Truncate(s string, maxLen int) string {
	s = strings.NewReplacer("\n", "\\n", "\r", "", "\t", " ").Replace(s)
	if runewidth.StringWidth(s) <= maxLen {
		return s
	}
	return runewidth.Truncate(s, maxLen, "…")
}

// ColumnWidth computes a reasonable display width (in terminal cells) for a
// column based on its name and a sample of values.
func ColumnWidth(name string, samples []string, minW, maxW int) int {
	w := runewidth.StringWidth(name)
	for _, s := range samples {
		if sw := runewidth.StringWidth(s); sw > w {
			w = sw
		}
	}
	if w < minW {
		w = minW
	}
	if w > maxW {
		w = maxW
	}
	return w
}
