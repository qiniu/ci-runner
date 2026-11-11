package state

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/qiniu/ci-runner/internal/config"
)

func openPostgresPool(dsn string) (*sql.DB, error) {
	parsed, err := config.ParsePostgresDSN(config.Secret(dsn))
	if err != nil {
		return nil, err
	}
	var options []stdlib.OptionOpenDB
	// Preserve GORM's timestamp-without-time-zone scan location when supplying
	// our own pool, including its supported timezone parameter spellings.
	for _, key := range []string{"timezone", "TimeZone", "time_zone"} {
		zone, ok := parsed.RuntimeParams[key]
		if !ok {
			continue
		}
		delete(parsed.RuntimeParams, "TimeZone")
		delete(parsed.RuntimeParams, "time_zone")
		parsed.RuntimeParams["timezone"] = zone
		options = append(options, stdlib.OptionAfterConnect(func(ctx context.Context, conn *pgx.Conn) error {
			location, err := time.LoadLocation(zone)
			if err != nil {
				return errors.New("invalid PostgreSQL timezone")
			}
			conn.TypeMap().RegisterType(&pgtype.Type{
				Name: "timestamp", OID: pgtype.TimestampOID,
				Codec: &pgtype.TimestampCodec{ScanLocation: location},
			})
			return nil
		}))
		break
	}
	return stdlib.OpenDB(*parsed, options...), nil
}
