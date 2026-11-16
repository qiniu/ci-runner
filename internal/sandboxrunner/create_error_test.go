package sandboxrunner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"testing"

	qnsandbox "github.com/qiniu/go-sdk/v7/sandbox"
)

func TestCreateFailureCategoryDoesNotExposeProviderDetails(t *testing.T) {
	const secret = "credential-canary"
	for _, tc := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("%w: %w", ErrCreateOutcomeUnknown, context.DeadlineExceeded), "deadline_exceeded"},
		{context.Canceled, "canceled"},
		{&url.Error{Op: "POST", URL: "https://user:" + secret + "@example.invalid/?token=" + secret, Err: io.ErrUnexpectedEOF}, "response_incomplete"},
		{&qnsandbox.APIError{StatusCode: 503, Message: secret}, "http_503"},
		{&net.DNSError{Err: secret, Name: secret}, "dns_error"},
		{errors.New(secret), "unclassified_error"},
	} {
		got := CreateFailureCategory(tc.err)
		if got != tc.want || strings.Contains(got, secret) {
			t.Fatalf("category = %q, want %q", got, tc.want)
		}
	}
}
