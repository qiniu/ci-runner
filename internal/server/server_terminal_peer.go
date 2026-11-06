package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	terminalRoutePrefix      = "tr1."
	terminalPeerHeader       = "X-Runnerd-Terminal-Request"
	terminalRouteKeyDomain   = "\x00terminal-route-v1"
	terminalRequestKeyDomain = "\x00terminal-request-v1"
)

type terminalRoute struct {
	Origin  string `json:"origin"`
	Account int64  `json:"account"`
	Nonce   string `json:"nonce"`
}

// A route expires with its in-memory PTY. An address reused after restart
// cannot claim an old session because its new hub has different session IDs.
func (s *Server) newTerminalSessionID(accountID int64) (string, error) {
	if s.cfg.TerminalPeerOrigin == "" {
		return newID(), nil
	}
	raw, err := json.Marshal(terminalRoute{s.cfg.TerminalPeerOrigin, accountID, newID()})
	if err != nil {
		return "", err
	}
	sealed, err := encryptSecret(string(raw), s.cfg.AuthEncryptionKey.Value()+terminalRouteKeyDomain)
	if err != nil {
		return "", err
	}
	return terminalRoutePrefix + strings.TrimPrefix(sealed, encryptedSecretPrefix), nil
}

type terminalPeerRequest struct {
	Method  string
	Path    string
	Expires int64
	Cookie  string
	Payload [sha256.Size]byte
	Cursor  string
}

// Terminal operations fit the existing one-MiB input limit. Bind the exact
// bytes to the short-lived peer credential before passing them to the owner.
func terminalPayload(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid terminal payload")
		return nil, false
	}
	return raw, true
}

func newTerminalPeerClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.ResponseHeaderTimeout = 10 * time.Second
	return &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Only the eight terminal follow-up routes use this handler. The peer can
// present a user's cookie only on the specific original terminal operation.
func (s *Server) routeTerminal(local http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("sessionID")
		if !strings.HasPrefix(id, terminalRoutePrefix) {
			local(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if s.cfg.TerminalPeerOrigin == "" || len(id) > 4096 {
			writeError(w, http.StatusNotFound, "terminal session not found")
			return
		}
		raw, err := decryptSecret(encryptedSecretPrefix+strings.TrimPrefix(id, terminalRoutePrefix), s.cfg.AuthEncryptionKey.Value()+terminalRouteKeyDomain)
		var route terminalRoute
		if err != nil || json.Unmarshal([]byte(raw), &route) != nil || route.Account <= 0 || route.Origin == "" || route.Nonce == "" {
			writeError(w, http.StatusNotFound, "terminal session not found")
			return
		}
		if route.Origin == s.cfg.TerminalPeerOrigin {
			if proof := r.Header.Get(terminalPeerHeader); proof != "" {
				raw, err = decryptSecret(proof, s.cfg.AuthEncryptionKey.Value()+terminalRequestKeyDomain)
				var grant terminalPeerRequest
				if err != nil || json.Unmarshal([]byte(raw), &grant) != nil || grant.Method != r.Method ||
					grant.Path != r.URL.EscapedPath() || grant.Expires <= time.Now().Unix() {
					writeError(w, http.StatusUnauthorized, "invalid terminal peer request")
					return
				}
				payload, ok := terminalPayload(w, r)
				if !ok {
					return
				}
				if sha256.Sum256(payload) != grant.Payload || r.Header.Get("Last-Event-ID") != grant.Cursor {
					writeError(w, http.StatusUnauthorized, "invalid terminal peer request")
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(payload))
				r.Header.Del("Cookie")
				r.AddCookie(&http.Cookie{Name: adminSessionCookieName, Value: grant.Cookie})
			}
			local(w, r)
			return
		}
		if r.Header.Get(terminalPeerHeader) != "" {
			writeError(w, http.StatusServiceUnavailable, "terminal owner is unavailable")
			return
		}
		_, account, ok := s.requireUserSession(w, r)
		if !ok {
			return
		}
		if account.ID != route.Account {
			writeError(w, http.StatusNotFound, "terminal session not found")
			return
		}
		s.forwardTerminal(w, r, route.Origin)
	}
}

func (s *Server) forwardTerminal(w http.ResponseWriter, r *http.Request, origin string) {
	base, err := url.Parse(origin)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "terminal owner is unavailable")
		return
	}
	base.Path, base.RawPath = r.URL.Path, r.URL.RawPath
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	payload, ok := terminalPayload(w, r)
	if !ok {
		return
	}
	out, err := http.NewRequestWithContext(ctx, r.Method, base.String(), bytes.NewReader(payload))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid terminal request")
		return
	}
	// Never ask the transport to replay an input operation.
	out.GetBody = nil
	cookie, err := r.Cookie(adminSessionCookieName)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "missing terminal session cookie")
		return
	}
	grant, err := json.Marshal(terminalPeerRequest{
		Method: r.Method, Path: r.URL.EscapedPath(), Expires: time.Now().Add(20 * time.Second).Unix(),
		Cookie: cookie.Value, Payload: sha256.Sum256(payload), Cursor: r.Header.Get("Last-Event-ID"),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cannot authorize terminal request")
		return
	}
	proof, err := encryptSecret(string(grant), s.cfg.AuthEncryptionKey.Value()+terminalRequestKeyDomain)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cannot authorize terminal request")
		return
	}
	out.Header.Set(terminalPeerHeader, proof)
	out.Header.Set("Content-Type", "application/json")
	if cursor := r.Header.Get("Last-Event-ID"); cursor != "" {
		out.Header.Set("Last-Event-ID", cursor)
	}
	// The owner sends a heartbeat every five seconds. Bound a silent peer
	// even after it returned headers so EventSource can reconnect.
	controller := http.NewResponseController(w)
	// An idle owner may flush headers and emit no replay bytes until its
	// next heartbeat. Protect the downstream stream while awaiting both.
	if r.Method == http.MethodGet && !prepareTerminalStreamWrite(controller) {
		return
	}
	idle := time.AfterFunc(20*time.Second, cancel)
	defer idle.Stop()
	resp, err := s.terminalPeerHTTP.Do(out)
	if err != nil {
		if r.Method == http.MethodGet {
			writeTerminalPeerReconnect(w)
		} else {
			writeError(w, http.StatusServiceUnavailable, "terminal owner is unavailable")
		}
		return
	}
	defer resp.Body.Close()
	if r.Method == http.MethodGet && (resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusGatewayTimeout) {
		writeTerminalPeerReconnect(w)
		return
	}
	contentType := resp.Header.Get("Content-Type")
	if resp.StatusCode/100 == 3 || (contentType != "text/event-stream" && !strings.HasPrefix(contentType, "application/json")) {
		writeError(w, http.StatusBadGateway, "invalid terminal owner response")
		return
	}
	w.Header().Set("Content-Type", contentType)
	stream := contentType == "text/event-stream"
	if stream {
		if !prepareTerminalStreamWrite(controller) {
			return
		}
		w.Header().Set("X-Accel-Buffering", "no")
	}
	w.WriteHeader(resp.StatusCode)
	if stream && controller.Flush() != nil {
		return
	}
	buf := make([]byte, 16<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			idle.Reset(20 * time.Second)
			if stream && !prepareTerminalStreamWrite(controller) {
				return
			}
			if _, err := w.Write(buf[:n]); err != nil || (stream && controller.Flush() != nil) {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// A native EventSource closes permanently on an HTTP error. A completed SSE
// response containing only a comment lets it reconnect with its last cursor
// without resetting the UI's bounded recovery timer.
func writeTerminalPeerReconnect(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	_, _ = io.WriteString(w, ": terminal owner unavailable\n\n")
}
