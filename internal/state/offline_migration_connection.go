package state

import (
	"crypto/tls"
	"errors"
	"net"

	"github.com/jackc/pgx/v5/pgconn"
)

// Driver errors can embed the DSN, server text and credentials. Expose only
// fixed categories, never the original message or arbitrary SQLSTATE strings.
func offlinePostgresConnectionFailure(err error) string {
	var parseErr *pgconn.ParseConfigError
	if errors.As(err, &parseErr) {
		return "invalid DSN"
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "28P01", "28000":
			return "authentication rejected; check database.dsn credentials"
		case "3D000":
			return "database does not exist"
		case "53300":
			return "server connection limit reached"
		case "57P03":
			return "server not ready for connections"
		default:
			return "connection rejected by server"
		}
	}
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return "TLS certificate verification failed"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "DNS lookup failed"
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return "connection timed out"
		}
		return "network connection failed"
	}
	return "connection initialization failed; check DSN, credentials and TLS settings"
}
