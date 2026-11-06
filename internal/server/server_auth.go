package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/qiniu/ci-runner/internal/state"
)

func (s *Server) handleAuthSession(w http.ResponseWriter, r *http.Request) {
	session, ok := s.sessionFromRequest(r)
	response := map[string]any{
		"authenticated": ok,
		"oauth_enabled": s.cfg.GitHubOAuthEnabled(),
	}
	if ok {
		response["login"] = session.Login
		response["role"] = session.Role
		response["avatar_url"] = session.AvatarURL
		response["expires_at"] = time.Unix(session.ExpiresAt, 0).UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleGitHubOAuthLogin(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.GitHubOAuthEnabled() {
		http.NotFound(w, r)
		return
	}
	stateToken := newID()
	returnTo := sanitizeOAuthReturnTo(r.URL.Query().Get("return_to"))
	http.SetCookie(w, &http.Cookie{
		Name:     oauthStateCookieName,
		Value:    stateToken,
		Path:     "/auth/github",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   requestIsSecure(r),
		MaxAge:   int((10 * time.Minute).Seconds()),
	})
	if returnTo != "" {
		http.SetCookie(w, &http.Cookie{
			Name:     oauthReturnToCookieName,
			Value:    returnTo,
			Path:     "/auth/github",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			Secure:   requestIsSecure(r),
			MaxAge:   int((10 * time.Minute).Seconds()),
		})
	} else {
		clearCookie(w, oauthReturnToCookieName, "/auth/github", requestIsSecure(r))
	}
	values := url.Values{
		"client_id":    {s.cfg.GitHubOAuthClientID},
		"state":        {stateToken},
		"allow_signup": {"false"},
	}
	if redirectURL := s.githubOAuthRedirectURL(r); redirectURL != "" {
		values.Set("redirect_uri", redirectURL)
	}
	http.Redirect(w, r, githubOAuthAuthorizeURL+"?"+values.Encode(), http.StatusFound)
}

func (s *Server) handleGitHubOAuthCallback(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.GitHubOAuthEnabled() {
		http.NotFound(w, r)
		return
	}
	if s.isGitHubAppSetupCallback(r) {
		s.handleGitHubAppSetupRedirect(w, r)
		return
	}
	if problem := strings.TrimSpace(r.URL.Query().Get("error")); problem != "" {
		writeError(w, http.StatusUnauthorized, "github oauth rejected: "+problem)
		return
	}
	oauthState := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	cookie, err := r.Cookie(oauthStateCookieName)
	if err != nil || oauthState == "" || code == "" || subtle.ConstantTimeCompare([]byte(oauthState), []byte(cookie.Value)) != 1 {
		writeError(w, http.StatusUnauthorized, "invalid github oauth state")
		return
	}
	clearCookie(w, oauthStateCookieName, "/auth/github", requestIsSecure(r))

	token, err := s.exchangeGitHubOAuthCode(r.Context(), code, s.githubOAuthRedirectURL(r))
	if err != nil {
		s.logger.Warn("github oauth token exchange failed", "error", err)
		writeError(w, http.StatusUnauthorized, "github oauth token exchange failed")
		return
	}
	user, err := s.fetchGitHubOAuthUser(r.Context(), token)
	if err != nil {
		s.logger.Warn("github oauth user fetch failed", "error", err)
		writeError(w, http.StatusUnauthorized, "github oauth user fetch failed")
		return
	}
	account, _, err := s.store.EnsureAccountForOAuthIdentity(state.OAuthIdentity{
		OAuthProvider: "github",
		OAuthSubject:  user.Subject(),
		OAuthLogin:    user.Login,
	}, "user")
	if err != nil {
		s.logger.Warn("github oauth account save failed", "login", user.Login, "error", err)
		writeError(w, http.StatusInternalServerError, "save github oauth account")
		return
	}
	encryptedToken, err := encryptSecret(token, s.cfg.AuthEncryptionKey.Value())
	if err != nil {
		s.logger.Warn("github oauth token encrypt failed", "login", user.Login, "error", err)
		writeError(w, http.StatusInternalServerError, "save github oauth token")
		return
	}
	if _, err := s.store.UpsertAccountSecret(state.AccountSecret{
		ScopeType:      state.AccountScopeTypeAccount,
		ScopeID:        account.ID,
		KeyType:        state.AccountSecretTypeGitHubOAuthToken,
		EncryptedValue: encryptedToken,
	}); err != nil {
		s.logger.Warn("github oauth token save failed", "login", user.Login, "error", err)
		writeError(w, http.StatusInternalServerError, "save github oauth token")
		return
	}
	session := adminSession{
		Provider:  "github",
		Subject:   user.Subject(),
		Login:     user.Login,
		Role:      account.Role,
		AvatarURL: user.AvatarURL,
		ExpiresAt: time.Now().Add(s.cfg.AuthSessionTTL).Unix(),
	}
	value, err := s.encodeAdminSession(session)
	if err != nil {
		s.logger.Error("encode admin session failed", "error", err)
		writeError(w, http.StatusInternalServerError, "create admin session")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     adminSessionCookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   requestIsSecure(r),
		MaxAge:   int(s.cfg.AuthSessionTTL.Seconds()),
	})
	returnTo := "/"
	if cookie, err := r.Cookie(oauthReturnToCookieName); err == nil {
		clearCookie(w, oauthReturnToCookieName, "/auth/github", requestIsSecure(r))
		if sanitized := sanitizeOAuthReturnTo(cookie.Value); sanitized != "" {
			returnTo = sanitized
		}
	}
	http.Redirect(w, r, returnTo, http.StatusFound)
}

func (s *Server) isGitHubAppSetupCallback(r *http.Request) bool {
	return strings.TrimSpace(r.URL.Query().Get("installation_id")) != ""
}

func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	clearCookie(w, adminSessionCookieName, "/", requestIsSecure(r))
	clearCookie(w, oauthStateCookieName, "/auth/github", requestIsSecure(r))
	clearCookie(w, oauthReturnToCookieName, "/auth/github", requestIsSecure(r))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func sanitizeOAuthReturnTo(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.Contains(value, "\\") {
		return ""
	}
	if parsed, err := url.Parse(value); err != nil || parsed.IsAbs() || parsed.Host != "" {
		return ""
	}
	return value
}

type githubOAuthTokenResponse struct {
	AccessToken string `json:"access_token"`
	Error       string `json:"error"`
	Description string `json:"error_description"`
}

type githubOAuthUser struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url"`
}

func (u githubOAuthUser) Subject() string {
	if u.ID <= 0 {
		return ""
	}
	return strconv.FormatInt(u.ID, 10)
}

func (s *Server) exchangeGitHubOAuthCode(ctx context.Context, code, redirectURL string) (string, error) {
	values := url.Values{
		"client_id":     {s.cfg.GitHubOAuthClientID},
		"client_secret": {s.cfg.GitHubOAuthClientSecret.Value()},
		"code":          {code},
	}
	if redirectURL != "" {
		values.Set("redirect_uri", redirectURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubOAuthTokenURL, strings.NewReader(values.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.oauth.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var decoded githubOAuthTokenResponse
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<10)).Decode(&decoded)
		if decoded.Error != "" {
			return "", fmt.Errorf("github oauth token status %d: %s", resp.StatusCode, decoded.Error)
		}
		return "", fmt.Errorf("github oauth token status %d", resp.StatusCode)
	}
	var decoded githubOAuthTokenResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&decoded); err != nil {
		return "", err
	}
	if decoded.Error != "" {
		return "", fmt.Errorf("github oauth token error: %s", decoded.Error)
	}
	if strings.TrimSpace(decoded.AccessToken) == "" {
		return "", fmt.Errorf("github oauth token response missing access_token")
	}
	return decoded.AccessToken, nil
}

func (s *Server) fetchGitHubOAuthUser(ctx context.Context, token string) (githubOAuthUser, error) {
	endpoint := strings.TrimRight(s.cfg.GitHubAPIBaseURL, "/") + "/user"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return githubOAuthUser{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := s.oauth.Do(req)
	if err != nil {
		return githubOAuthUser{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return githubOAuthUser{}, fmt.Errorf("github user status %d", resp.StatusCode)
	}
	var user githubOAuthUser
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&user); err != nil {
		return githubOAuthUser{}, err
	}
	if strings.TrimSpace(user.Login) == "" {
		return githubOAuthUser{}, fmt.Errorf("github user response missing login")
	}
	if user.ID <= 0 {
		return githubOAuthUser{}, fmt.Errorf("github user response missing id")
	}
	return user, nil
}
