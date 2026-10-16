package server

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/qiniu/ci-runner/internal/github"
)

var errDeploymentSourceChanged = errors.New("selected deployment source changed or commit is unavailable")

type deploymentSourceSnapshot struct {
	CommitSHA          string
	RepositoryID       int64
	RepositoryFullName string
	Ref                string
	PullRequestTitle   string
}

func deploymentPullRequestNumber(ref string) int64 {
	if !strings.HasPrefix(ref, "refs/pull/") || !strings.HasSuffix(ref, "/head") {
		return 0
	}
	value := strings.TrimSuffix(strings.TrimPrefix(ref, "refs/pull/"), "/head")
	number, err := strconv.ParseInt(value, 10, 64)
	if err != nil || number <= 0 || fmt.Sprintf("refs/pull/%d/head", number) != ref {
		return 0
	}
	return number
}

func validDeploymentSHA(sha string) bool {
	if len(sha) != 40 || sha != strings.ToLower(sha) {
		return false
	}
	_, err := hex.DecodeString(sha)
	return err == nil
}

// Resolve PR heads through the authorized base repository's pull ref. Source
// repository identity is evidence only; it never chooses credentials or URLs.
func (s *Server) resolveDeploymentSource(ctx context.Context, repository github.Repository, ref, requestedSHA string) (deploymentSourceSnapshot, error) {
	var source deploymentSourceSnapshot
	number := deploymentPullRequestNumber(ref)
	if number > 0 {
		pull, err := s.gh.GetPullRequest(ctx, repository.FullName, number)
		if err != nil {
			return source, err
		}
		if pull.Number != number || pull.Base.Repository.ID != repository.ID || !strings.EqualFold(pull.Base.Repository.FullName, repository.FullName) || pull.Head.Repository.ID <= 0 || !validDeploymentSHA(pull.Head.SHA) || pull.Head.Ref == "" {
			return source, errors.New("pull request source identity is unavailable")
		}
		parts := strings.Split(pull.Head.Repository.FullName, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" || len(pull.Head.Repository.FullName) > 256 || strings.ContainsAny(pull.Head.Repository.FullName, "\\?#%\r\n") {
			return source, errors.New("invalid pull request source repository")
		}
		source = deploymentSourceSnapshot{CommitSHA: pull.Head.SHA, RepositoryID: pull.Head.Repository.ID, RepositoryFullName: pull.Head.Repository.FullName, Ref: pull.Head.Ref, PullRequestTitle: strings.TrimSpace(pull.Title)}
	}
	head, err := s.gh.ResolveCommit(ctx, repository.FullName, ref)
	if err != nil {
		return source, err
	}
	if number > 0 && source.CommitSHA != head {
		return source, errDeploymentSourceChanged
	}
	source.CommitSHA = head
	if requestedSHA == "" || requestedSHA == head {
		return source, nil
	}
	var allowed bool
	if number > 0 {
		allowed, err = s.gh.PullRequestContainsCommit(ctx, repository.FullName, number, requestedSHA)
	} else if strings.HasPrefix(ref, "refs/heads/") {
		allowed, err = s.gh.IsDeploymentAncestor(ctx, repository.FullName, requestedSHA, head)
	}
	if err != nil {
		return source, err
	}
	if !allowed {
		return source, errDeploymentSourceChanged
	}
	source.CommitSHA = requestedSHA
	return source, nil
}
