package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// Deployment source reads always use the authorized target repository's App.
// Never follow a fork-provided clone URL or send target credentials to a fork.
func (c *Client) deploymentSourceJSON(ctx context.Context, repository, resource string, result any) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/repos/"+repository+"/"+resource, nil)
	if err != nil {
		return false, err
	}
	setGitHubHeaders(req)
	resp, err := c.do(req, repository)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil {
		return false, err
	}
	if len(data) > 2<<20 {
		return false, errors.New("deployment source response exceeds limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, &apiError{Operation: "deployment source", StatusCode: resp.StatusCode, RetryAfter: resp.Header.Get("Retry-After"), RateLimitRemaining: resp.Header.Get("X-RateLimit-Remaining")}
	}
	if err := json.Unmarshal(data, result); err != nil {
		return false, err
	}
	return nextLink(resp.Header.Get("Link")) != "", nil
}

func (c *Client) ListDeploymentPullRequests(ctx context.Context, repository string, page int) ([]string, bool, error) {
	if page <= 0 || page > 1000 {
		return nil, false, errors.New("invalid pull request page")
	}
	var entries []PullRequest
	more, err := c.deploymentSourceJSON(ctx, repository, fmt.Sprintf("pulls?state=open&per_page=100&page=%d", page), &entries)
	if err != nil {
		return nil, false, err
	}
	if len(entries) > 100 {
		return nil, false, errors.New("pull request page exceeds limit")
	}
	refs := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Number > 0 {
			refs = append(refs, strconv.FormatInt(entry.Number, 10))
		}
	}
	return refs, more && page < 1000, nil
}

// PullRequestContainsCommit has a fixed 250-commit bound, matching GitHub's API.
// An unavailable/force-pushed commit fails closed rather than deploying a new head.
func (c *Client) PullRequestContainsCommit(ctx context.Context, repository string, number int64, sha string) (bool, error) {
	for page := 1; page <= 3; page++ {
		var entries []struct {
			SHA string `json:"sha"`
		}
		more, err := c.deploymentSourceJSON(ctx, repository, fmt.Sprintf("pulls/%d/commits?per_page=100&page=%d", number, page), &entries)
		if err != nil {
			return false, err
		}
		if len(entries) > 100 {
			return false, errors.New("pull request commit page exceeds limit")
		}
		for i, entry := range entries {
			if (page-1)*100+i >= 250 {
				break
			}
			if entry.SHA == sha {
				return true, nil
			}
		}
		if !more {
			break
		}
	}
	return false, nil
}

func (c *Client) IsDeploymentAncestor(ctx context.Context, repository, sha, head string) (bool, error) {
	var comparison struct {
		MergeBase struct {
			SHA string `json:"sha"`
		} `json:"merge_base_commit"`
	}
	_, err := c.deploymentSourceJSON(ctx, repository, "compare/"+url.PathEscape(sha+"..."+head)+"?per_page=1", &comparison)
	return err == nil && comparison.MergeBase.SHA == sha, err
}
