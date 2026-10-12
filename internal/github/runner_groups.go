package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	ErrRunnerGroupNotFound         = errors.New("GitHub Runner group does not exist in the repository owner's organization")
	ErrRunnerGroupInherited        = errors.New("an inherited enterprise Runner group cannot register an organization Runner")
	ErrRunnerGroupRepositoryDenied = errors.New("GitHub Runner group does not allow the target repository")
)

type RunnerGroup struct {
	ID                       int64  `json:"id"`
	Name                     string `json:"name"`
	Default                  bool   `json:"default"`
	Inherited                bool   `json:"inherited"`
	Visibility               string `json:"visibility"`
	AllowsPublicRepositories bool   `json:"allows_public_repositories"`
	RestrictedToWorkflows    bool   `json:"restricted_to_workflows"`
}

// ListOrganizationRunnerGroups uses the selected installation's credentials,
// never a browser-supplied organization or the signed-in user's token.
// Pagination is constructed locally so upstream Link URLs cannot redirect credentials.
func (c *Client) ListOrganizationRunnerGroups(ctx context.Context, installationID int64, organization, repository string) ([]RunnerGroup, error) {
	if organization == "" || strings.ContainsAny(organization, "/?#\\") || strings.ContainsAny(repository, "/?#\\") {
		return nil, errors.New("invalid Runner group organization or repository")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	client := c.http
	if c.appAuth != nil {
		var err error
		if installationID > 0 {
			client, err = c.appAuth.clientForInstallation(ctx, installationID)
		} else if repository != "" {
			client, err = c.appAuth.clientForRepository(ctx, organization+"/"+repository)
		} else {
			return nil, errors.New("organization installation is required")
		}
		if err != nil {
			return nil, err
		}
	}
	groups := make([]RunnerGroup, 0)
	for page := 1; page <= 10; page++ {
		query := url.Values{"per_page": {"100"}, "page": {fmt.Sprint(page)}}
		if repository != "" {
			query.Set("visible_to_repository", repository)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/orgs/"+url.PathEscape(organization)+"/actions/runner-groups?"+query.Encode(), nil)
		if err != nil {
			return nil, err
		}
		setGitHubHeaders(req)
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if resp.StatusCode != http.StatusOK {
			return nil, &apiError{Operation: "github runner groups", StatusCode: resp.StatusCode, Body: string(body), RetryAfter: resp.Header.Get("Retry-After"), RateLimitRemaining: resp.Header.Get("X-RateLimit-Remaining")}
		}
		if len(body) > 1<<20 {
			return nil, errors.New("GitHub Runner group response exceeds limit")
		}
		var out struct {
			TotalCount *int          `json:"total_count"`
			Groups     []RunnerGroup `json:"runner_groups"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, err
		}
		if out.TotalCount == nil || *out.TotalCount < 0 || out.Groups == nil {
			return nil, errors.New("invalid GitHub Runner group response")
		}
		if len(out.Groups) > 100 || *out.TotalCount > 1000 {
			return nil, errors.New("GitHub Runner group catalog exceeds limit")
		}
		for _, group := range out.Groups {
			if group.ID <= 0 || strings.TrimSpace(group.Name) == "" {
				return nil, errors.New("invalid GitHub Runner group")
			}
		}
		groups = append(groups, out.Groups...)
		if nextLink(resp.Header.Get("Link")) == "" && len(groups) >= *out.TotalCount {
			return groups, nil
		}
		if len(out.Groups) == 0 {
			return nil, errors.New("incomplete GitHub Runner group response")
		}
	}
	return nil, errors.New("GitHub Runner group pagination exceeds limit")
}

// ValidateRunnerGroup checks the organization-owned group and repository access.
// Workflow-specific restrictions remain GitHub's final scheduling decision.
func (c *Client) ValidateRunnerGroup(ctx context.Context, installationID int64, repositoryFullName, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	organization, repository, ok := strings.Cut(repositoryFullName, "/")
	if !ok || repository == "" {
		return errors.New("repository full name is required")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// Manual requests can omit the installation ID; preserve the existing
	// registration path's static or repository-based App installation lookup.
	if c.appAuth != nil && installationID <= 0 {
		var err error
		installationID, err = c.appAuth.installationID(ctx, repositoryFullName)
		if err != nil {
			return err
		}
	}
	visible, err := c.ListOrganizationRunnerGroups(ctx, installationID, organization, repository)
	if err != nil {
		return err
	}
	for _, group := range visible {
		if strings.EqualFold(group.Name, name) {
			if group.Inherited {
				return ErrRunnerGroupInherited
			}
			return nil
		}
	}
	// Only rejected starts need the full catalog to distinguish a missing group
	// from an existing group that does not allow this repository.
	groups, err := c.ListOrganizationRunnerGroups(ctx, installationID, organization, "")
	if err != nil {
		return err
	}
	for _, group := range groups {
		if strings.EqualFold(group.Name, name) {
			if group.Inherited {
				return ErrRunnerGroupInherited
			}
			return ErrRunnerGroupRepositoryDenied
		}
	}
	return ErrRunnerGroupNotFound
}
