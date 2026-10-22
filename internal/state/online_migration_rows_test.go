package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

func TestOnlineMigrationRowsStrictEncodingAndRedaction(t *testing.T) {
	columns := []onlineMigrationColumn{
		{Name: "id", Kind: "int", Size: 64, PrimaryKey: true},
		{Name: "enabled", Kind: "bool", NotNull: true},
		{Name: "updated_at", Kind: "time"},
		{Name: "secret", Kind: "string"},
	}
	valid := `{"id":9007199254740993,"enabled":0,"updated_at":"2026-10-22 12:30:01.123456789+08:00","secret":null}`
	row, err := decodeMigrationRow(columns, valid)
	if err != nil {
		t.Fatal(err)
	}
	if row["id"] != int64(9007199254740993) || row["enabled"] != false || row["secret"] != nil ||
		!row["updated_at"].(time.Time).Equal(time.Date(2026, 10, 22, 4, 30, 1, 123456000, time.UTC)) {
		t.Fatal("integer, boolean, timezone, precision or NULL was changed incorrectly")
	}
	for _, invalid := range []string{
		valid + " broken", valid + " {}", valid + " 0", valid + " null",
		strings.Replace(valid, `"id":9007199254740993`, `"id":null`, 1),
		strings.Replace(valid, `9007199254740993`, `9223372036854775808`, 1),
		strings.Replace(valid, `9007199254740993`, `9.1`, 1),
		strings.Replace(valid, `"enabled":0`, `"enabled":2`, 1),
		strings.Replace(valid, `"secret":null`, `"secret":"sensitive\u0000value"`, 1),
		strings.Replace(valid, `"secret":null`, `"secret":"sensitive","secret":"duplicate"`, 1),
		strings.Replace(valid, `"secret":null`, `"unknown":null`, 1),
		strings.Replace(valid, `"updated_at":"2026-10-22 12:30:01.123456789+08:00"`, `"updated_at":"sensitive invalid time"`, 1),
		"null", "[]", "{}", string([]byte{'{', '"', 0xff, '"', '}'}),
	} {
		if _, err := decodeMigrationRow(columns, invalid); !errors.Is(err, errOnlineMigrationNotReady) || strings.Contains(err.Error(), "sensitive") {
			t.Fatalf("invalid row accepted or leaked: %v", err)
		}
	}
	for _, test := range []struct {
		column onlineMigrationColumn
		value  any
	}{
		{onlineMigrationColumn{Name: "pid", Kind: "uint", Size: 32}, json.Number("4294967296")},
		{onlineMigrationColumn{Name: "pid", Kind: "uint", Size: 32}, int64(-1)},
		{onlineMigrationColumn{Name: "code", Kind: "int", Size: 8}, int64(128)},
		{onlineMigrationColumn{Name: "number", Kind: "float"}, math.Inf(1)},
		{onlineMigrationColumn{Name: "secret", Kind: "string"}, []byte{0xff}},
	} {
		if _, err := normalizeMigrationValue(test.column, test.value); !errors.Is(err, errOnlineMigrationNotReady) {
			t.Fatalf("unrepresentable column %s accepted: %v", test.column.Name, err)
		}
	}
	row["secret"] = "sensitive-value"
	for _, rendered := range []string{fmt.Sprint(row), fmt.Sprintf("%#v", row), fmt.Sprint(migrationBaselineRecord{LastKey: "sensitive-value"})} {
		if strings.Contains(rendered, "sensitive") {
			t.Fatal("row or resume key leaked through formatting")
		}
	}
}
