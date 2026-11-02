package config

import (
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ParsePostgresDSN resolves password obfuscation after parsing, so password
// punctuation never needs to be interpolated back into a connection string.
// The returned driver config contains plaintext credentials; do not log it.
func ParsePostgresDSN(dsn Secret) (*pgx.ConnConfig, error) {
	value, err := revealObfuscatedSecret(dsn.Value())
	if err != nil {
		return nil, pgconn.NewParseConfigError("[redacted]", "invalid obfuscated PostgreSQL DSN", nil)
	}
	parsed, err := pgx.ParseConfig(value)
	if err != nil {
		// pgx parse errors can contain the original DSN or credentials.
		return nil, pgconn.NewParseConfigError("[redacted]", "invalid PostgreSQL DSN", nil)
	}
	parsed.Password, err = revealObfuscatedSecret(parsed.Password)
	if err != nil {
		return nil, pgconn.NewParseConfigError("[redacted]", "invalid obfuscated PostgreSQL password", nil)
	}
	return parsed, nil
}
