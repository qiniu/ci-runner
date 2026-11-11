package sandboxrunner

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	qnsandbox "github.com/qiniu/go-sdk/v7/sandbox"
)

type (
	TerminalReference = qnsandbox.PtyReference
	TerminalEvent     = qnsandbox.PtyReplayEvent
)

type TerminalStream interface {
	Receive() (TerminalEvent, error)
	Close() error
}

// SharedTerminalService performs each operation directly against the sandbox.
// It owns no process handle or callback between runnerd requests.
type SharedTerminalService interface {
	CreatePTY(context.Context, string, string, PtySize) (TerminalReference, uint64, error)
	ConnectPTY(context.Context, string, TerminalReference, uint64) (TerminalStream, error)
	InputPTY(context.Context, string, TerminalReference, []byte) error
	ResizePTY(context.Context, string, TerminalReference, PtySize) error
	CleanupPTY(context.Context, string, string, TerminalReference) (bool, error)
}

var ErrTerminalReplayUnsupported = qnsandbox.ErrPtyReplayUnsupported

func (s *E2BService) CreatePTY(ctx context.Context, id, tag string, size PtySize) (TerminalReference, uint64, error) {
	sb, err := s.client.Attach(ctx, id)
	if err != nil {
		return TerminalReference{}, 0, err
	}
	if size.Cols == 0 {
		size.Cols = 100
	}
	if size.Rows == 0 {
		size.Rows = 28
	}
	stream, err := sb.Pty().CreateReplay(ctx, qnsandbox.PtySize{Cols: size.Cols, Rows: size.Rows}, qnsandbox.WithTag(tag))
	if err != nil {
		return TerminalReference{}, 0, err
	}
	defer stream.Close()
	return stream.Reference, stream.Offset, nil
}

func (s *E2BService) ConnectPTY(ctx context.Context, id string, ref TerminalReference, after uint64) (TerminalStream, error) {
	sb, err := s.client.Attach(ctx, id)
	if err != nil {
		return nil, err
	}
	return sb.Pty().ConnectReplay(ctx, ref, &after)
}

func (s *E2BService) InputPTY(ctx context.Context, id string, ref TerminalReference, data []byte) error {
	sb, err := s.client.Attach(ctx, id)
	if err != nil {
		return err
	}
	return sb.Pty().SendReplayInput(ctx, ref, data)
}

func (s *E2BService) ResizePTY(ctx context.Context, id string, ref TerminalReference, size PtySize) error {
	sb, err := s.client.Attach(ctx, id)
	if err != nil {
		return err
	}
	return sb.Pty().ResizeReplay(ctx, ref, qnsandbox.PtySize{Cols: size.Cols, Rows: size.Rows})
}

func (s *E2BService) CleanupPTY(ctx context.Context, id, tag string, ref TerminalReference) (bool, error) {
	sb, err := s.client.Attach(ctx, id)
	if isSandboxNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	refs := []TerminalReference{ref}
	if ref.PID == 0 {
		refs, err = sb.Pty().FindReplay(ctx, tag)
		if errors.Is(err, ErrTerminalReplayUnsupported) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		// A missing tag cannot disprove an in-flight Start. Retain cleanup intent
		// until a PTY is found or the owning Sandbox is definitively gone.
		if len(refs) == 0 {
			return false, nil
		}
	}
	for _, r := range refs {
		err = sb.Pty().KillReplay(ctx, r)
		if err != nil && connect.CodeOf(err) != connect.CodeNotFound {
			return false, err
		}
	}
	return true, nil
}
