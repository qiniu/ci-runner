package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// RunnerJITConfig is bound to one server-created ephemeral Runner. Never log it.
type RunnerJITConfig struct {
	Runner        Runner `json:"runner"`
	EncodedConfig string `json:"encoded_jit_config"`
	// CleanupRequired is true once a create request may have reached GitHub.
	CleanupRequired bool `json:"-"`
}

func (c RunnerJITConfig) String() string   { return "[REDACTED JIT configuration]" }
func (c RunnerJITConfig) GoString() string { return c.String() }

// GitHub accepts at most 100 custom labels. Preserve every workflow label if
// the list is already full; without a marker, a lost reply requires numeric
// evidence instead of deleting an unrelated registration by name.
func JITAttemptLabels(labels []string, attempt string) []string {
	if attempt == "" || len(labels) == 0 || len(labels) >= 100 {
		return labels
	}
	return append(slices.Clone(labels), jitAttemptLabel(attempt))
}

func jitAttemptLabel(attempt string) string { return "runnerd-jit-" + strings.ToLower(attempt) }

func (c *Client) FindJITRunnerID(ctx context.Context, repository, group, name, attempt string) (int64, error) {
	if attempt == "" {
		return 0, errors.New("missing JIT attempt")
	}
	runners, err := c.listRunners(ctx, repository, group, name)
	if err != nil {
		return 0, err
	}
	var id int64
	for _, r := range runners {
		if r.Name != name || !slices.ContainsFunc(r.Labels, func(l RunnerLabel) bool { return strings.EqualFold(l.Name, jitAttemptLabel(attempt)) }) {
			continue
		}
		if r.ID <= 0 || (id != 0 && id != r.ID) {
			return 0, errors.New("ambiguous JIT Runner identity")
		}
		id = r.ID
	}
	return id, nil
}

// CreateRunnerJITConfig may return a Runner identity with an error. The caller
// must reconcile that identity (or the attempt label and name after a lost response).
func (c *Client) CreateRunnerJITConfig(ctx context.Context, repository, group, name string, labels []string, beforePost func() error) (RunnerJITConfig, error) {
	target, err := c.runnerTarget(repository, group)
	if err != nil {
		return RunnerJITConfig{}, err
	}
	groupID := int64(1) // Repository runners use GitHub's default group.
	if strings.TrimSpace(group) != "" {
		owner, repo, _ := strings.Cut(target.repositoryFullName, "/")
		groups, err := c.ListOrganizationRunnerGroups(ctx, 0, owner, repo)
		if err != nil {
			return RunnerJITConfig{}, err
		}
		groupID = 0
		for _, candidate := range groups {
			if strings.EqualFold(candidate.Name, strings.TrimSpace(group)) && !candidate.Inherited {
				groupID = candidate.ID
				break
			}
		}
		if groupID == 0 {
			return RunnerJITConfig{}, ErrRunnerGroupNotFound
		}
	}
	if strings.TrimSpace(name) == "" || len(labels) == 0 || len(labels) > 100 {
		return RunnerJITConfig{}, errors.New("invalid JIT Runner name or labels")
	}
	payload, err := json.Marshal(struct {
		Name       string   `json:"name"`
		GroupID    int64    `json:"runner_group_id"`
		Labels     []string `json:"labels"`
		WorkFolder string   `json:"work_folder"`
	}{name, groupID, labels, `..\work`})
	if err != nil {
		return RunnerJITConfig{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/"+target.apiPath+"/actions/runners/generate-jitconfig", bytes.NewReader(payload))
	if err != nil {
		return RunnerJITConfig{}, err
	}
	setGitHubHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	client := c.http
	if c.appAuth != nil {
		client, err = c.appAuth.clientForRepository(ctx, target.repositoryFullName)
		if err != nil {
			return RunnerJITConfig{}, err
		}
	}
	if beforePost != nil {
		if err := beforePost(); err != nil {
			return RunnerJITConfig{}, err
		}
	}
	// A redirected POST would create another operation outside the single
	// recorded dispatch. Classify a redirect as unconfirmed.
	postClient := *client
	postClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := postClient.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return RunnerJITConfig{CleanupRequired: true}, context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return RunnerJITConfig{CleanupRequired: true}, context.DeadlineExceeded
		}
		return RunnerJITConfig{CleanupRequired: true}, errors.New("github JIT Runner temporary network error")
	}
	defer resp.Body.Close()
	// Error bodies and parsing errors may contain credentials; never retain them.
	if resp.StatusCode != http.StatusCreated {
		category := "request rejected"
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if resp.StatusCode == http.StatusForbidden && strings.Contains(strings.ToLower(string(body)), "secondary rate limit") {
			category = "secondary rate limit"
		}
		uncertain := resp.StatusCode < 400 || resp.StatusCode >= 500 || resp.StatusCode == http.StatusRequestTimeout
		return RunnerJITConfig{CleanupRequired: uncertain}, &apiError{Operation: "github JIT Runner", StatusCode: resp.StatusCode, Body: category, RetryAfter: resp.Header.Get("Retry-After"), RateLimitRemaining: resp.Header.Get("X-RateLimit-Remaining")}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return RunnerJITConfig{CleanupRequired: true}, errors.New("invalid github JIT Runner response size")
	}
	out := RunnerJITConfig{CleanupRequired: true}
	if json.Unmarshal(body, &out) != nil {
		return RunnerJITConfig{CleanupRequired: true}, errors.New("invalid github JIT Runner response")
	}
	if out.Runner.ID <= 0 || out.Runner.Name != name || out.EncodedConfig == "" {
		return out, errors.New("invalid github JIT Runner configuration")
	}
	config, err := enableJITRunnerUpdate(out.EncodedConfig, name, out.Runner.ID)
	if err != nil {
		return out, err
	}
	out.EncodedConfig = config
	return out, nil
}

// GitHub's JIT defaults disable self-update. Keep runnerd's existing update
// contract without modifying the Runner identity, credentials, or ephemeral flag.
func enableJITRunnerUpdate(encoded, name string, runnerID int64) (string, error) {
	invalid := errors.New("invalid github JIT Runner settings")
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", invalid
	}
	var files map[string]string
	if json.Unmarshal(decoded, &files) != nil {
		return "", invalid
	}
	for key := range files {
		if key != ".runner" && key != ".credentials" && key != ".credentials_rsaparams" {
			return "", invalid
		}
	}
	settingsBytes, err := base64.StdEncoding.DecodeString(files[".runner"])
	if err != nil {
		return "", invalid
	}
	var settings map[string]json.RawMessage
	if json.Unmarshal(settingsBytes, &settings) != nil {
		return "", invalid
	}
	var agentName, ephemeral, agentID, workFolder string
	if json.Unmarshal(settings["AgentName"], &agentName) != nil || agentName != name || json.Unmarshal(settings["Ephemeral"], &ephemeral) != nil || !strings.EqualFold(ephemeral, "true") {
		return "", invalid
	}
	if json.Unmarshal(settings["AgentId"], &agentID) != nil || agentID != strconv.FormatInt(runnerID, 10) || json.Unmarshal(settings["WorkFolder"], &workFolder) != nil || workFolder != `..\work` || files[".credentials"] == "" || files[".credentials_rsaparams"] == "" {
		return "", invalid
	}
	for _, value := range files {
		if _, err := base64.StdEncoding.DecodeString(value); err != nil {
			return "", invalid
		}
	}
	settings["DisableUpdate"] = json.RawMessage(`"False"`)
	settingsBytes, err = json.Marshal(settings)
	if err != nil {
		return "", invalid
	}
	files[".runner"] = base64.StdEncoding.EncodeToString(settingsBytes)
	decoded, err = json.Marshal(files)
	if err != nil {
		return "", invalid
	}
	return base64.StdEncoding.EncodeToString(decoded), nil
}
