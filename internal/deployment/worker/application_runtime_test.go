package worker

import (
	"context"
	"errors"
	"testing"

	deploy "github.com/miclle/deployd"
)

func TestApplicationRuntimeUsesExactCurrentHostWithoutMutatingEnvironment(t *testing.T) {
	for _, host := range []string{"4173-first.region.example.com", "4173-second.region.example.com"} {
		t.Run(host, func(t *testing.T) {
			underlying := &fakeRuntime{id: "sandbox", url: "https://" + host}
			runtime := &applicationRuntime{RuntimeHandle: underlying, port: 4173}
			env := map[string]string{"PORT": "4173", "__VITE_ADDITIONAL_SERVER_ALLOWED_HOSTS": ".example.com"}
			command := deploy.Command{Script: "npm start", Directory: "/workspace", Tag: "attempt-1", Env: env}
			ref, err := runtime.Start(context.Background(), command, nil)
			if err != nil || ref.Tag != command.Tag || ref.RuntimeID != underlying.ID() {
				t.Fatalf("start identity = %#v, error = %v", ref, err)
			}
			got := underlying.startCommand
			if got.Env["__VITE_ADDITIONAL_SERVER_ALLOWED_HOSTS"] != host || got.Env["PORT"] != "4173" || got.Script != command.Script || got.Directory != command.Directory {
				t.Fatalf("unexpected application command: %#v", got)
			}
			if env["__VITE_ADDITIONAL_SERVER_ALLOWED_HOSTS"] != ".example.com" {
				t.Fatal("caller environment was mutated")
			}
		})
	}
}

func TestApplicationRuntimeRejectsInvalidEndpointBeforeStarting(t *testing.T) {
	for _, endpoint := range []string{"", "file:///tmp/app", "https://user:pass@example.com", "https://example.com/path", "https://example.com?token=value", "https://example.com#fragment"} {
		t.Run(endpoint, func(t *testing.T) {
			underlying := &fakeRuntime{id: "sandbox", url: endpoint}
			runtime := &applicationRuntime{RuntimeHandle: underlying, port: 4173}
			ref, err := runtime.Start(context.Background(), deploy.Command{Script: "npm start", Tag: "attempt-1"}, nil)
			if !errors.Is(err, deploy.ErrInvalidInput) || ref.ID != "" || underlying.startCommand.Script != "" {
				t.Fatalf("invalid endpoint reached application start: ref=%#v err=%v", ref, err)
			}
		})
	}
}

type applicationEndpointFailure struct {
	fakeRuntime
	err error
}

func (r *applicationEndpointFailure) Endpoint(context.Context, int) (string, error) {
	return "", r.err
}

func TestApplicationRuntimeEndpointFailureDoesNotStartProcess(t *testing.T) {
	err := errors.New("endpoint unavailable")
	underlying := &applicationEndpointFailure{err: err}
	runtime := &applicationRuntime{RuntimeHandle: underlying, port: 4173}
	ref, got := runtime.Start(context.Background(), deploy.Command{Script: "npm start", Tag: "attempt-1"}, nil)
	if !errors.Is(got, err) || ref.ID != "" || underlying.startCommand.Script != "" {
		t.Fatalf("endpoint failure reached application start: ref=%#v err=%v", ref, got)
	}
}
