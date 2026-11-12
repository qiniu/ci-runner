package sandboxrunner

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	qnsandbox "github.com/qiniu/go-sdk/v7/sandbox"
)

//go:embed terminal/helper.py
var terminalHelper []byte

const terminalProtocolPrefix = "\x1eRUNNERD_TERMINAL:"

type terminalOutputFrame struct {
	Ready uint32 `json:"ready"`
	Data  []byte `json:"data"`
}

// Commands.Start runs a login shell. Ignore its bounded startup banner, even
// without a trailing newline, but strictly decode the helper's framed stream.
type terminalOutputReader struct {
	pending []byte
	started bool
}

func (r *terminalOutputReader) feed(data []byte, visit func(terminalOutputFrame)) error {
	r.pending = append(r.pending, data...)
	if !r.started {
		i := bytes.Index(r.pending, []byte(terminalProtocolPrefix))
		if i < 0 {
			// A split marker is not part of the startup banner budget.
			partial := min(len(r.pending), len(terminalProtocolPrefix)-1)
			for partial > 0 && !bytes.HasSuffix(r.pending, []byte(terminalProtocolPrefix[:partial])) {
				partial--
			}
			if len(r.pending)-partial > 16384 {
				return errors.New("terminal startup output too large")
			}
			return nil
		}
		if i > 16384 {
			return errors.New("terminal startup output too large")
		}
		r.pending, r.started = r.pending[i:], true
	}
	for {
		i := bytes.IndexByte(r.pending, '\n')
		if i < 0 {
			if len(r.pending) > 8192 {
				return errors.New("terminal frame too large")
			}
			return nil
		}
		var frame terminalOutputFrame
		if i > 8192 || !bytes.HasPrefix(r.pending[:i], []byte(terminalProtocolPrefix)) || json.Unmarshal(r.pending[len(terminalProtocolPrefix):i], &frame) != nil {
			return errors.New("invalid terminal frame")
		}
		r.pending = r.pending[i+1:]
		visit(frame)
	}
}

func terminalCallConfirmed(stdout, nonce string) bool {
	marker := terminalProtocolPrefix + nonce + ":ok\n"
	i := bytes.Index([]byte(stdout), []byte(marker))
	return i >= 0 && i <= 16384 && stdout[i:] == marker
}

type TerminalStart struct {
	ID        string
	SandboxID string
	Size      PtySize
	ExpiresAt time.Time
}

type helperTerminal struct {
	sandbox   *qnsandbox.Sandbox
	directory string
	pid       uint32 // display evidence only; never used for remote operations
	done      chan struct{}
}

func (s *E2BService) StartTerminal(ctx context.Context, input TerminalStart, onData func([]byte)) (TerminalSession, error) {
	if b, err := hex.DecodeString(input.ID); err != nil || len(b) != 16 || len(input.ID) != 32 {
		return nil, errors.New("invalid terminal ID")
	}
	timeout := int32(30)
	if !input.ExpiresAt.IsZero() {
		seconds := math.Ceil(time.Until(input.ExpiresAt).Seconds())
		if seconds <= 0 || seconds > math.MaxInt32 {
			return nil, errors.New("invalid terminal lifetime")
		}
		timeout = int32(seconds)
	}
	sb, err := s.client.Connect(ctx, input.SandboxID, qnsandbox.ConnectParams{Timeout: timeout})
	if err != nil {
		return nil, errors.New("terminal connection failed")
	}
	term := &helperTerminal{sandbox: sb, directory: "/tmp/runnerd-terminal-" + input.ID, done: make(chan struct{})}
	// The private directory is created exactly once. Never overwrite another
	// session's supervisor or reopen a previously used identity.
	result, err := sb.Commands().Run(ctx, "umask 077; mkdir -- "+term.directory, qnsandbox.WithTimeout(5*time.Second))
	if err != nil || result.ExitCode != 0 {
		return nil, errors.New("terminal initialization failed")
	}
	if _, err = sb.Files().Write(ctx, term.directory+"/helper.py", terminalHelper); err != nil {
		return nil, errors.New("terminal upload failed")
	}
	size := input.Size
	if size.Cols == 0 {
		size.Cols = 100
	}
	if size.Rows == 0 {
		size.Rows = 28
	}
	if size.Cols > 500 || size.Rows > 500 {
		return nil, errors.New("invalid terminal size")
	}
	ready := make(chan uint32, 1)
	var mu sync.Mutex
	var reader terminalOutputReader
	streamCtx, cancel := context.WithCancel(ctx)
	handle, err := sb.Commands().Start(streamCtx,
		fmt.Sprintf("exec python3 %s/helper.py serve %d %d %d", term.directory, size.Rows, size.Cols, time.Now().Add(15*time.Second).Unix()),
		qnsandbox.WithOnStdout(func(data []byte) {
			mu.Lock()
			defer mu.Unlock()
			if err := reader.feed(data, func(frame terminalOutputFrame) {
				if frame.Ready != 0 {
					select {
					case ready <- frame.Ready:
					default:
					}
				}
				if len(frame.Data) > 0 && onData != nil {
					onData(frame.Data)
				}
			}); err != nil {
				cancel()
			}
		}))
	if err != nil {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = term.Close(closeCtx)
		closeCancel()
		cancel()
		return nil, errors.New("terminal start outcome unknown")
	}
	go func() { _, _ = handle.Wait(); close(term.done); cancel() }()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case term.pid = <-ready:
		return term, nil
	case <-term.done:
	case <-timer.C:
	case <-ctx.Done():
	}
	// Best effort identity-scoped close, including a tombstone before a delayed
	// start. Returning an error never asserts that unknown creation was cleaned.
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = term.Close(closeCtx)
	closeCancel()
	cancel()
	return nil, errors.New("terminal start outcome unknown")
}

func (s *helperTerminal) PID() uint32           { return s.pid }
func (s *helperTerminal) Done() <-chan struct{} { return s.done }
func (s *helperTerminal) KeepAlive(ctx context.Context) error {
	return s.call(ctx, map[string]any{"op": "ping"})
}

func (s *helperTerminal) SendInput(ctx context.Context, data []byte) error {
	if len(data) > 4096 {
		return errors.New("terminal input too large")
	}
	return s.call(ctx, map[string]any{"op": "input", "data": data})
}

func (s *helperTerminal) Resize(ctx context.Context, size PtySize) error {
	return s.call(ctx, map[string]any{"op": "resize", "rows": size.Rows, "cols": size.Cols})
}

func (s *helperTerminal) Close(ctx context.Context) error {
	err := s.call(ctx, map[string]any{"op": "close"})
	if err != nil {
		if _, infoErr := s.sandbox.GetInfo(ctx); isSandboxNotFound(infoErr) {
			return nil
		}
	}
	return err
}

func (s *helperTerminal) call(ctx context.Context, request map[string]any) error {
	request["expires"] = time.Now().Add(5 * time.Second).Unix()
	payload, err := json.Marshal(request)
	if err != nil {
		return err
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return err
	}
	name := hex.EncodeToString(nonce[:])
	path := s.directory + "/" + name
	// No command input is interpolated into shell syntax or logged by Commands.
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.sandbox.Files().Remove(cleanupCtx, path)
	}()
	if _, err = s.sandbox.Files().Write(ctx, path, payload); err != nil {
		return errors.New("terminal operation outcome unknown")
	}
	result, err := s.sandbox.Commands().Run(ctx, "exec python3 "+s.directory+"/helper.py call "+name, qnsandbox.WithTimeout(5*time.Second))
	if err != nil || result.ExitCode != 0 || !terminalCallConfirmed(result.Stdout, name) {
		return errors.New("terminal operation outcome unknown")
	}
	return nil
}
