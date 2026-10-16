package worker

import (
	"context"
	"net/url"

	deploy "github.com/miclle/deployd"
)

// applicationRuntime supplies the application-facing Host policy. Keep this
// provider integration out of the immutable repository spec and deployd kernel.
type applicationRuntime struct {
	RuntimeHandle
	port int
}

func (r *applicationRuntime) Start(ctx context.Context, command deploy.Command, output deploy.Output) (deploy.ProcessRef, error) {
	endpoint, err := r.RuntimeHandle.Endpoint(ctx, r.port)
	if err != nil {
		return deploy.ProcessRef{}, err
	}
	origin, err := url.Parse(endpoint)
	if err != nil || (origin.Scheme != "https" && origin.Scheme != "http") || origin.Hostname() == "" || origin.User != nil || (origin.Path != "" && origin.Path != "/") || origin.RawQuery != "" || origin.Fragment != "" {
		return deploy.ProcessRef{}, deploy.ErrInvalidInput
	}
	env := make(map[string]string, len(command.Env)+1)
	for key, value := range command.Env {
		env[key] = value
	}
	// Vite rejects dynamic ingress hosts by default. Allow only this Sandbox's
	// exact application hostname, as in the existing deployment integration.
	env["__VITE_ADDITIONAL_SERVER_ALLOWED_HOSTS"] = origin.Hostname()
	command.Env = env
	return r.RuntimeHandle.Start(ctx, command, output)
}
