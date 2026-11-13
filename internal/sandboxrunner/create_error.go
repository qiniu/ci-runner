package sandboxrunner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"

	qnsandbox "github.com/qiniu/go-sdk/v7/sandbox"
)

// CreateFailureCategory never returns provider text, request URLs, headers or
// response bodies. SDK errors can contain credentials even after truncation.
func CreateFailureCategory(err error) string {
	var api *qnsandbox.APIError
	var dns *net.DNSError
	var network net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.As(err, &api) && api.StatusCode >= 100 && api.StatusCode <= 599:
		return fmt.Sprintf("http_%d", api.StatusCode)
	case errors.As(err, &dns):
		return "dns_error"
	case errors.As(err, &network) && network.Timeout():
		return "network_timeout"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "response_incomplete"
	case errors.As(err, &network):
		return "network_error"
	default:
		return "unclassified_error"
	}
}
