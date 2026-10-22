package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
)

// Rows never pass through record constructors, hooks or automatic timestamps.
// Time values use PostgreSQL's microsecond precision on both sides of a check.
type migrationRow map[string]any

func (r migrationRow) String() string   { return "migration row [redacted]" }
func (r migrationRow) GoString() string { return r.String() }

func normalizeMigrationValue(column onlineMigrationColumn, value any) (any, error) {
	invalid := fmt.Errorf("%w: unrepresentable value in column %s", errOnlineMigrationNotReady, column.Name)
	if value == nil {
		if column.NotNull || column.PrimaryKey {
			return nil, invalid
		}
		return nil, nil
	}
	if raw, ok := value.([]byte); ok && column.Kind != "bytes" {
		value = string(raw)
	}
	switch column.Kind {
	case "string":
		text, ok := value.(string)
		if !ok || !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
			return nil, invalid
		}
		return text, nil
	case "int", "uint":
		var number int64
		switch v := value.(type) {
		case int64:
			number = v
		case json.Number:
			var err error
			number, err = v.Int64()
			if err != nil {
				return nil, invalid
			}
		default:
			return nil, invalid
		}
		if column.Kind == "uint" {
			if number < 0 || (column.Size > 0 && column.Size < 63 && uint64(number) >= uint64(1)<<column.Size) {
				return nil, invalid
			}
		} else if column.Size > 0 && column.Size < 64 {
			bound := int64(1) << (column.Size - 1)
			if number < -bound || number >= bound {
				return nil, invalid
			}
		}
		return number, nil
	case "bool":
		if flag, ok := value.(bool); ok {
			return flag, nil
		}
		var number int64
		switch v := value.(type) {
		case int64:
			number = v
		case json.Number:
			var err error
			number, err = v.Int64()
			if err != nil {
				return nil, invalid
			}
		default:
			return nil, invalid
		}
		if number != 0 && number != 1 {
			return nil, invalid
		}
		return number == 1, nil
	case "time":
		if parsed, ok := value.(time.Time); ok {
			return parsed.UTC().Truncate(time.Microsecond), nil
		}
		text, ok := value.(string)
		if !ok {
			return nil, invalid
		}
		for _, layout := range []string{
			time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07:00",
			"2006-01-02 15:04:05.999999999 -0700 MST",
			"2006-01-02 15:04:05.999999999", "2006-01-02T15:04:05.999999999",
			"2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02",
		} {
			if parsed, err := time.Parse(layout, text); err == nil {
				return parsed.UTC().Truncate(time.Microsecond), nil
			}
		}
		return nil, invalid
	case "float":
		var number float64
		switch v := value.(type) {
		case float64:
			number = v
		case json.Number:
			var err error
			number, err = strconv.ParseFloat(string(v), 64)
			if err != nil {
				return nil, invalid
			}
		default:
			return nil, invalid
		}
		if math.IsInf(number, 0) || math.IsNaN(number) {
			return nil, invalid
		}
		return number, nil
	default:
		// No current model has a byte column. JSON capture of SQLite BLOBs
		// needs a separately specified encoding before such a schema is usable.
		return nil, invalid
	}
}

func decodeMigrationRow(columns []onlineMigrationColumn, encoded string) (migrationRow, error) {
	if !utf8.ValidString(encoded) {
		return nil, fmt.Errorf("%w: invalid row encoding", errOnlineMigrationNotReady)
	}
	decoder := json.NewDecoder(strings.NewReader(encoded))
	decoder.UseNumber()
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, fmt.Errorf("%w: incomplete row encoding", errOnlineMigrationNotReady)
	}
	raw := make(map[string]any, len(columns))
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok {
			return nil, fmt.Errorf("%w: invalid row column", errOnlineMigrationNotReady)
		}
		if _, duplicate := raw[name]; duplicate {
			return nil, fmt.Errorf("%w: duplicate row column", errOnlineMigrationNotReady)
		}
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("%w: invalid row value", errOnlineMigrationNotReady)
		}
		raw[name] = value
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') || len(raw) != len(columns) {
		return nil, fmt.Errorf("%w: incomplete row encoding", errOnlineMigrationNotReady)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: trailing row encoding", errOnlineMigrationNotReady)
	}
	row := make(migrationRow, len(columns))
	for _, column := range columns {
		value, exists := raw[column.Name]
		if !exists {
			return nil, fmt.Errorf("%w: missing row column %s", errOnlineMigrationNotReady, column.Name)
		}
		normalized, err := normalizeMigrationValue(column, value)
		if err != nil {
			return nil, err
		}
		row[column.Name] = normalized
	}
	return row, nil
}

func encodeMigrationKey(table onlineMigrationTable, row migrationRow) (string, error) {
	key := make(map[string]any)
	for _, column := range migrationKeyColumns(table) {
		key[column.Name] = row[column.Name]
	}
	encoded, err := json.Marshal(key)
	return string(encoded), err
}

func canonicalMigrationRow(table onlineMigrationTable, row migrationRow) ([]byte, error) {
	values := make([]any, 0, len(table.Columns))
	for _, column := range table.Columns {
		values = append(values, row[column.Name])
	}
	return json.Marshal(values)
}

func migrationKeyExpression(column onlineMigrationColumn, backend string) string {
	expression := migrationQuoted(column.Name)
	if column.Kind == "string" {
		if backend == BackendPostgres {
			return expression + ` COLLATE "C"`
		}
		return expression + " COLLATE BINARY"
	}
	return expression
}

// readMigrationRows returns a bounded keyset page. Binary text collation keeps
// cross-dialect pages aligned even with mixed case or non-ASCII primary keys.
func readMigrationRows(db *gorm.DB, table onlineMigrationTable, after string, limit int) ([]migrationRow, bool, error) {
	if limit < 1 || limit > 1000 {
		return nil, false, fmt.Errorf("%w: invalid row batch size", errOnlineMigrationNotReady)
	}
	var names, order []string
	for _, column := range table.Columns {
		names = append(names, column.Name)
	}
	keys := migrationKeyColumns(table)
	for _, key := range keys {
		order = append(order, migrationKeyExpression(key, db.Dialector.Name()))
	}
	query := db.Table(table.Name).Select(names).Order(strings.Join(order, ",")).Limit(limit + 1)
	if after != "" {
		key, err := decodeMigrationRow(keys, after)
		if err != nil {
			return nil, false, err
		}
		var alternatives []string
		var args []any
		for i, column := range keys {
			var parts []string
			for _, previous := range keys[:i] {
				parts = append(parts, migrationKeyExpression(previous, db.Dialector.Name())+" = ?")
				args = append(args, key[previous.Name])
			}
			parts = append(parts, migrationKeyExpression(column, db.Dialector.Name())+" > ?")
			args = append(args, key[column.Name])
			alternatives = append(alternatives, "("+strings.Join(parts, " AND ")+")")
		}
		query = query.Where("("+strings.Join(alternatives, " OR ")+")", args...)
	}
	rows, err := query.Rows()
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var result []migrationRow
	for rows.Next() {
		if len(result) == limit {
			return result, true, nil
		}
		values := make([]any, len(table.Columns))
		pointers := make([]any, len(values))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, false, err
		}
		row := make(migrationRow, len(values))
		for i, column := range table.Columns {
			normalized, err := normalizeMigrationValue(column, values[i])
			if err != nil {
				return nil, false, err
			}
			row[column.Name] = normalized
		}
		result = append(result, row)
	}
	return result, false, rows.Err()
}

func equalMigrationKeys(table onlineMigrationTable, key, row migrationRow) bool {
	first, err := encodeMigrationKey(table, key)
	if err != nil {
		return false
	}
	second, err := encodeMigrationKey(table, row)
	return err == nil && bytes.Equal([]byte(first), []byte(second))
}
