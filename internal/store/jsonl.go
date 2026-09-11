package store

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// appendJSONL appends v as a JSON line to path, creating the file and its
// directory if needed. Shared by AuditStore and RunStore, the two
// append-only JSONL logs in this package.
func appendJSONL(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer func() { _ = f.Close() }()

	line, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}

// readJSONL reads every JSON line in path as a T. A missing file returns an
// empty slice, not an error. A malformed line is skipped with a warning
// rather than failing the whole read — one bad line shouldn't hide the rest
// of the log.
func readJSONL[T any](path string) ([]T, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []T{}, nil
		}
		return nil, fmt.Errorf("open: %w", err)
	}
	defer func() { _ = f.Close() }()

	var items []T
	scanner := bufio.NewScanner(f)
	line := 0
	for scanner.Scan() {
		line++
		var v T
		if err := json.Unmarshal(scanner.Bytes(), &v); err != nil {
			slog.Warn("skipping malformed line", "path", path, "line", line, "err", err)
			continue
		}
		items = append(items, v)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	return items, nil
}
