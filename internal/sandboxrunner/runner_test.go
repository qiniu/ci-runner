package sandboxrunner

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	qnsandbox "github.com/qiniu/go-sdk/v7/sandbox"
)

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func writeExecutable(t *testing.T, name, contents string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
}

func runCommand(t *testing.T, command string, args []string, env ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(command, args...)
	cmd.Dir = repositoryRoot(t)
	cmd.Env = append(os.Environ(), env...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func TestStartScriptEncodesRunnerArguments(t *testing.T) {
	input := StartInput{
		RequestID:         `req$(touch /tmp/request)`,
		RepositoryURL:     `https://github.com/o/r$(touch /tmp/pwn)`,
		RegistrationToken: `tok"$(touch /tmp/token)"`,
		RunnerName:        "runner`touch /tmp/name`",
		Labels:            []string{"self-hosted", `e2b$(touch /tmp/label)`},
		RunnerGroup:       `group$(touch /tmp/group)`,
	}
	sandboxID := `sandbox$(touch /tmp/sandbox)`
	script := startScript(input, sandboxID)

	for _, raw := range []string{
		input.RequestID,
		input.RepositoryURL,
		input.RegistrationToken,
		input.RunnerName,
		strings.Join(input.Labels, ","),
		input.RunnerGroup,
		sandboxID,
	} {
		if strings.Contains(script, raw) {
			t.Fatalf("script contains raw argument %q:\n%s", raw, script)
		}
		if !strings.Contains(script, base64.StdEncoding.EncodeToString([]byte(raw))) {
			t.Fatalf("script does not contain encoded argument %q:\n%s", raw, script)
		}
	}
}

func TestStartScriptExportsScopedCachePrefixes(t *testing.T) {
	input := StartInput{
		CacheS3Bucket:       "cache-bucket",
		CacheS3AccessKeyID:  "access-key",
		CacheS3SecretKey:    "secret-key",
		CacheS3ReadPrefixes: `["cache/o/r/scopes/pr-7","cache/o/r/scopes/branch-main"]`,
		CacheS3WritePrefix:  "cache/o/r/scopes/pr-7",
	}
	script := startScript(input, "sandbox-1")
	for _, want := range []string{
		"RUNS_ON_S3_CACHE_READ_PREFIXES",
		"RUNS_ON_S3_CACHE_WRITE_PREFIX",
		base64.StdEncoding.EncodeToString([]byte(input.CacheS3ReadPrefixes)),
		base64.StdEncoding.EncodeToString([]byte(input.CacheS3WritePrefix)),
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "RUNS_ON_S3_CACHE_REPO_PREFIX") {
		t.Fatalf("script contains obsolete cache prefix environment variable:\n%s", script)
	}
}

func TestRunnerBootstrapRequiresRootUser(t *testing.T) {
	if runnerBootstrapUser != "root" {
		t.Fatalf("runner bootstrap user = %q, want root", runnerBootstrapUser)
	}
}

func TestStartScriptRunsCleanupAfterRunnerExit(t *testing.T) {
	script := startScript(StartInput{
		RepositoryURL:     "https://github.com/o/r",
		RegistrationToken: "token",
		RunnerName:        "runner",
		Labels:            []string{"self-hosted", "e2b"},
	}, "sandbox-1")

	if strings.Contains(script, "exec ./run.sh") {
		t.Fatalf("script must not exec run.sh because that bypasses the EXIT cleanup trap:\n%s", script)
	}
	if !strings.Contains(script, "trap cleanup EXIT") || !strings.Contains(script, "./run.sh") {
		t.Fatalf("script does not preserve cleanup trap and runner execution:\n%s", script)
	}
}

func TestStartScriptReportsSandboxIDInJobStartedHook(t *testing.T) {
	script := startScript(StartInput{
		RequestID:         "78433740691",
		RepositoryURL:     "https://github.com/o/r",
		RegistrationToken: "token",
		RunnerName:        "e2b-78433740691",
		Labels:            []string{"self-hosted", "e2b"},
	}, "sb-78433740691")

	for _, want := range []string{
		`export ACTIONS_RUNNER_HOOK_JOB_STARTED="$hook_root/job-started.sh"`,
		"RUNNERD_JOB_STARTED",
		"::notice title=Qiniu sandbox::sandbox_id=${RUNNERD_SANDBOX_ID} runner_request_id=${RUNNERD_REQUEST_ID} runner_name=${RUNNERD_RUNNER_NAME}",
		"Qiniu sandbox id: ${RUNNERD_SANDBOX_ID}",
		"Runner request id: ${RUNNERD_REQUEST_ID}",
		"Runner name: ${RUNNERD_RUNNER_NAME}",
		base64.StdEncoding.EncodeToString([]byte("78433740691")),
		base64.StdEncoding.EncodeToString([]byte("sb-78433740691")),
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q:\n%s", want, script)
		}
	}
}

func TestStartScriptUsesHostedRunnerFilesystemContract(t *testing.T) {
	fixture := t.TempDir()
	actionsRunnerRoot := filepath.Join(fixture, "actions-runner")
	workdir := filepath.Join(fixture, "home", "runner", "work", "_runner")
	runnerJobWork := filepath.Join(fixture, "home", "runner", "work")
	runnerHome := filepath.Join(fixture, "home", "runner")
	hookRoot := filepath.Join(fixture, "hooks")
	logPath := filepath.Join(fixture, "runner.log")
	environmentPath := filepath.Join(fixture, "environment")
	if err := os.MkdirAll(actionsRunnerRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(environmentPath, []byte("ImageVersion=\"$TEMPLATE_VERSION\"\nIMAGE_VERSION=\"$TEMPLATE_VERSION\"\nCUSTOM_RUNNER_ENV=\"$HOME/from-environment\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(actionsRunnerRoot, "config.sh"), `#!/usr/bin/env bash
set -euo pipefail
printf 'config HOME=%s PWD=%s TOOL_CACHE=%s AGENT_TOOLS=%s IMAGE_VERSION=%s CUSTOM_RUNNER_ENV=%s\n' "$HOME" "$PWD" "$RUNNER_TOOL_CACHE" "$AGENT_TOOLSDIRECTORY" "${IMAGE_VERSION:-}" "${CUSTOM_RUNNER_ENV:-}" >>"$RUNNER_TEST_LOG"
`)
	writeExecutable(t, filepath.Join(actionsRunnerRoot, "run.sh"), `#!/usr/bin/env bash
set -euo pipefail
printf 'run HOME=%s PWD=%s RUNASROOT=%s\n' "$HOME" "$PWD" "${RUNNER_ALLOW_RUNASROOT:-}" >>"$RUNNER_TEST_LOG"
`)

	script := startScript(StartInput{
		RequestID:         "request-1",
		RepositoryURL:     "https://github.com/o/r",
		RegistrationToken: "token",
		RunnerName:        "runner",
		Labels:            []string{"self-hosted", "linux", "x64"},
	}, "sandbox-1")
	scriptPath := filepath.Join(fixture, "start-runner.sh")
	writeExecutable(t, scriptPath, script)

	output, err := runCommand(
		t,
		"bash",
		[]string{scriptPath},
		"ACTIONS_RUNNER_ROOT="+actionsRunnerRoot,
		"RUNNER_WORKDIR="+workdir,
		"RUNNER_JOB_WORK="+runnerJobWork,
		"RUNNER_HOME="+runnerHome,
		"RUNNER_HOOK_ROOT="+hookRoot,
		"RUNNER_TEST_LOG="+logPath,
		"RUNNER_ENVIRONMENT_FILE="+environmentPath,
		"ENSURE_DOCKER=/bin/true",
	)
	if err != nil {
		t.Fatalf("start script failed: %v\n%s", err, output)
	}
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(logBytes)
	for _, want := range []string{
		"config HOME=" + runnerHome + " PWD=" + workdir,
		"TOOL_CACHE=/opt/hostedtoolcache AGENT_TOOLS=/opt/hostedtoolcache",
		"IMAGE_VERSION= CUSTOM_RUNNER_ENV=" + runnerHome + "/from-environment",
		"run HOME=" + runnerHome + " PWD=" + workdir + " RUNASROOT=",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("runner execution log missing %q:\n%s", want, log)
		}
	}
}

func TestStartScriptDockerBootstrapPolicy(t *testing.T) {
	tests := []struct {
		name                string
		requireDocker       bool
		installDockerHelper bool
		wantSuccess         bool
	}{
		{name: "custom runner continues when Docker bootstrap fails", installDockerHelper: true, wantSuccess: true},
		{name: "managed runner requires working Docker", requireDocker: true, installDockerHelper: true},
		{name: "custom runner continues when Docker helper is missing", wantSuccess: true},
		{name: "managed runner requires Docker helper", requireDocker: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := t.TempDir()
			actionsRunnerRoot := filepath.Join(fixture, "actions-runner")
			ensureDockerPath := filepath.Join(fixture, "ensure-docker")
			workdir := filepath.Join(fixture, "workdir")
			logPath := filepath.Join(fixture, "runner.log")
			if err := os.MkdirAll(actionsRunnerRoot, 0o755); err != nil {
				t.Fatal(err)
			}
			writeExecutable(t, filepath.Join(actionsRunnerRoot, "config.sh"), `#!/usr/bin/env bash
set -euo pipefail
printf 'config %s\n' "$*" >>"$RUNNER_TEST_LOG"
`)
			writeExecutable(t, filepath.Join(actionsRunnerRoot, "run.sh"), `#!/usr/bin/env bash
set -euo pipefail
printf 'run\n' >>"$RUNNER_TEST_LOG"
`)
			if tt.installDockerHelper {
				writeExecutable(t, ensureDockerPath, `#!/usr/bin/env bash
exit 1
`)
			}

			script := startScript(StartInput{
				RequestID:         "request-1",
				RepositoryURL:     "https://github.com/o/r",
				RegistrationToken: "token",
				RunnerName:        "runner",
				Labels:            []string{"self-hosted", "linux", "x64"},
				RequireDocker:     tt.requireDocker,
			}, "sandbox-1")
			scriptPath := filepath.Join(fixture, "start-runner.sh")
			writeExecutable(t, scriptPath, script)

			output, err := runCommand(
				t,
				"bash",
				[]string{scriptPath},
				"ACTIONS_RUNNER_ROOT="+actionsRunnerRoot,
				"RUNNER_WORKDIR="+workdir,
				"RUNNER_JOB_WORK="+filepath.Join(fixture, "job-work"),
				"RUNNER_HOME="+filepath.Join(fixture, "home"),
				"RUNNER_HOOK_ROOT="+filepath.Join(fixture, "hooks"),
				"RUNNER_ENVIRONMENT_FILE="+filepath.Join(fixture, "missing-environment"),
				"RUNNER_TEST_LOG="+logPath,
				"RUNNERD_AS_RUNNER=1",
				"ENSURE_DOCKER="+ensureDockerPath,
			)
			if tt.wantSuccess && err != nil {
				t.Fatalf("custom runner start failed: %v\n%s", err, output)
			}
			if !tt.wantSuccess && err == nil {
				t.Fatalf("managed runner start succeeded without Docker:\n%s", output)
			}

			logBytes, readErr := os.ReadFile(logPath)
			if tt.wantSuccess {
				if readErr != nil {
					t.Fatal(readErr)
				}
				if !strings.Contains(string(logBytes), "run\n") {
					t.Fatalf("custom runner did not execute after Docker failure: %q", logBytes)
				}
			} else if readErr == nil && strings.Contains(string(logBytes), "run\n") {
				t.Fatalf("managed runner executed after Docker failure: %q", logBytes)
			}
		})
	}
}

func TestRunnerTemplateMatrixGateAcceptsManagedCatalog(t *testing.T) {
	output, err := runCommand(t, "bash", []string{"scripts/check-runner-template-matrix.sh"})
	if err != nil {
		t.Fatalf("matrix gate failed: %v\n%s", err, output)
	}
}

func TestRunnerTemplateMatrixGateAcceptsLifecycleStatesAndRejectsUnknownState(t *testing.T) {
	root := repositoryRoot(t)
	readmeBytes, err := os.ReadFile(filepath.Join(root, "templates", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(t.TempDir(), "templates-readme.md")
	verified := strings.ReplaceAll(string(readmeBytes), "| development |", "| verified |")
	if err := os.WriteFile(fixture, []byte(verified), 0o644); err != nil {
		t.Fatal(err)
	}
	output, err := runCommand(
		t,
		"bash",
		[]string{"scripts/check-runner-template-matrix.sh"},
		"RUNNER_TEMPLATES_README="+fixture,
	)
	if err != nil {
		t.Fatalf("verified publication state must pass: %v\n%s", err, output)
	}

	invalid := strings.Replace(verified, "| verified |", "| released |", 1)
	if err := os.WriteFile(fixture, []byte(invalid), 0o644); err != nil {
		t.Fatal(err)
	}
	output, err = runCommand(
		t,
		"bash",
		[]string{"scripts/check-runner-template-matrix.sh"},
		"RUNNER_TEMPLATES_README="+fixture,
	)
	if err == nil || !strings.Contains(output, "unsupported publication state") {
		t.Fatalf("unsupported publication state must fail, got err=%v\n%s", err, output)
	}
}

func TestRunnerTemplateQshellBuildUsesTemporaryConfigAndRequiresReady(t *testing.T) {
	fixture := t.TempDir()
	templateDir := filepath.Join(fixture, "template")
	if err := os.MkdirAll(templateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(templateDir, "qshell.sandbox.toml")
	const trackedConfig = "name = \"fixture-template\"\n"
	if err := os.WriteFile(configPath, []byte(trackedConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	qshellPath := filepath.Join(fixture, "qshell")
	qshellLog := filepath.Join(fixture, "qshell.log")
	writeExecutable(t, qshellPath, `#!/usr/bin/env bash
set -euo pipefail
if [ "${1:-}" = version ]; then
  printf 'v2.19.10\n'
  exit 0
fi
printf 'pwd=%s args=%s\n' "$PWD" "$*" >>"$QSHELL_TEST_LOG"
if [ "$QSHELL_TEST_MODE" = ready ]; then
  printf 'Status:       ready\n'
else
  printf 'Error: fixture build failed\n' >&2
fi
`)
	commonEnv := []string{
		"QSHELL=" + qshellPath,
		"QSHELL_TEST_LOG=" + qshellLog,
		"QINIU_SANDBOX_API_URL=https://sandbox.example.test",
		"QINIU_API_KEY=test-api-key",
	}
	output, err := runCommand(
		t,
		"bash",
		[]string{"scripts/run-runner-template-operation.sh", "build", templateDir},
		append(commonEnv, "QSHELL_TEST_MODE=ready")...,
	)
	if err != nil {
		t.Fatalf("ready qshell build failed: %v\n%s", err, output)
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(configBytes) != trackedConfig {
		t.Fatalf("tracked qshell config changed:\n%s", configBytes)
	}
	temporaryConfigs, err := filepath.Glob(filepath.Join(templateDir, ".qshell-sandbox.*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temporaryConfigs) != 0 {
		t.Fatalf("temporary qshell configs were not removed: %v", temporaryConfigs)
	}
	logBytes, err := os.ReadFile(qshellLog)
	if err != nil {
		t.Fatal(err)
	}
	log := string(logBytes)
	if !strings.Contains(log, "pwd="+templateDir) ||
		!strings.Contains(log, "args=sandbox template build --wait --config .qshell-sandbox.") ||
		strings.Contains(log, "--config qshell.sandbox.toml") {
		t.Fatalf("build did not use a temporary config from the template directory:\n%s", log)
	}

	output, err = runCommand(
		t,
		"bash",
		[]string{"scripts/run-runner-template-operation.sh", "build", templateDir},
		append(commonEnv, "QSHELL_TEST_MODE=failed")...,
	)
	if err == nil || !strings.Contains(output, "did not report terminal Status: ready") {
		t.Fatalf("zero-exit qshell failure must fail closed, got err=%v\n%s", err, output)
	}
}

func TestRunnerTemplateQshellResolvesRelativeTemplateFromScriptCheckout(t *testing.T) {
	root := repositoryRoot(t)
	fixture := t.TempDir()
	qshellPath := filepath.Join(fixture, "qshell")
	qshellLog := filepath.Join(fixture, "qshell.log")
	writeExecutable(t, qshellPath, `#!/usr/bin/env bash
set -euo pipefail
if [ "${1:-}" = version ]; then
  printf 'v2.19.10\n'
  exit 0
fi
printf 'pwd=%s\n' "$PWD" >"$QSHELL_TEST_LOG"
printf 'Status:       ready\n'
`)
	scriptPath := filepath.Join(root, "scripts", "run-runner-template-operation.sh")
	cmd := exec.Command(
		"bash",
		scriptPath,
		"build",
		"templates/github-runner-ubuntu-slim",
	)
	cmd.Dir = fixture
	cmd.Env = append(
		os.Environ(),
		"QSHELL="+qshellPath,
		"QSHELL_TEST_LOG="+qshellLog,
		"QINIU_SANDBOX_API_URL=https://sandbox.example.test",
		"QINIU_API_KEY=test-api-key",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("relative template build failed: %v\n%s", err, output)
	}
	logBytes, err := os.ReadFile(qshellLog)
	if err != nil {
		t.Fatal(err)
	}
	want := "pwd=" + filepath.Join(root, "templates", "github-runner-ubuntu-slim")
	if !strings.Contains(string(logBytes), want) {
		t.Fatalf("relative template resolved outside the script checkout; want %q, got:\n%s", want, logBytes)
	}
}

func TestRunnerTemplateQshellPublicationRunsFromTemplateDirectory(t *testing.T) {
	fixture := t.TempDir()
	templateDir := filepath.Join(fixture, "template")
	if err := os.MkdirAll(templateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(templateDir, "qshell.sandbox.toml"),
		[]byte("name = \"fixture-template\"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	qshellPath := filepath.Join(fixture, "qshell")
	qshellLog := filepath.Join(fixture, "qshell.log")
	writeExecutable(t, qshellPath, `#!/usr/bin/env bash
set -euo pipefail
if [ "${1:-}" = version ]; then
  printf 'v2.19.10\n'
  exit 0
fi
printf 'pwd=%s args=%s\n' "$PWD" "$*" >>"$QSHELL_TEST_LOG"
case "$3" in
  publish) printf 'Template tmpl-fixture published\n' ;;
  unpublish) printf 'Template tmpl-fixture unpublished\n' ;;
  *) exit 64 ;;
esac
`)
	commonEnv := []string{
		"QSHELL=" + qshellPath,
		"QSHELL_TEST_LOG=" + qshellLog,
		"QINIU_SANDBOX_API_URL=https://sandbox.example.test",
		"QINIU_API_KEY=test-api-key",
	}
	for _, operation := range []string{"publish", "unpublish"} {
		output, err := runCommand(
			t,
			"bash",
			[]string{"scripts/run-runner-template-operation.sh", operation, templateDir},
			commonEnv...,
		)
		if err != nil {
			t.Fatalf("%s failed: %v\n%s", operation, err, output)
		}
	}
	logBytes, err := os.ReadFile(qshellLog)
	if err != nil {
		t.Fatal(err)
	}
	log := string(logBytes)
	for _, operation := range []string{"publish", "unpublish"} {
		want := "pwd=" + templateDir + " args=sandbox template " + operation + " -y"
		if !strings.Contains(log, want) {
			t.Fatalf("missing qshell publication invocation %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "--config") {
		t.Fatalf("publish/unpublish must not use build-only --config:\n%s", log)
	}
}

func TestDefaultTemplateCatalogCheckRequiresUniqueRunnablePublicTemplates(t *testing.T) {
	templates := []map[string]any{
		{
			"templateID":  "tmpl-slim",
			"names":       []string{"qiniu/github-runner-ubuntu-slim"},
			"public":      true,
			"buildStatus": "ready",
		},
		{
			"templateID":  "tmpl-22",
			"names":       []string{"github-runner-ubuntu-22-04"},
			"public":      true,
			"buildStatus": "uploaded",
		},
		{
			"templateID":  "tmpl-24",
			"names":       []string{"github-runner-ubuntu-24-04"},
			"public":      true,
			"buildStatus": "ready",
		},
		{
			"templateID":  "tmpl-26",
			"names":       []string{"github-runner-ubuntu-26-04"},
			"public":      true,
			"buildStatus": "ready",
		},
		{
			"templateID":  "tmpl-slim-large",
			"names":       []string{"github-runner-ubuntu-slim-large"},
			"public":      true,
			"buildStatus": "ready",
		},
		{
			"templateID":  "tmpl-22-large",
			"names":       []string{"github-runner-ubuntu-22-04-large"},
			"public":      true,
			"buildStatus": "ready",
		},
		{
			"templateID":  "tmpl-24-large",
			"names":       []string{"github-runner-ubuntu-24-04-large"},
			"public":      true,
			"buildStatus": "ready",
		},
		{
			"templateID":  "tmpl-26-large",
			"names":       []string{"github-runner-ubuntu-26-04-large"},
			"public":      true,
			"buildStatus": "ready",
		},
	}
	initialResponseBody, err := json.Marshal(templates)
	if err != nil {
		t.Fatal(err)
	}
	var responseBody atomic.Value
	responseBody.Store(initialResponseBody)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/default-templates" {
			http.NotFound(response, request)
			return
		}
		if request.Header.Get("X-API-Key") != "test-api-key" {
			http.Error(response, "missing API key", http.StatusUnauthorized)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write(responseBody.Load().([]byte))
	}))
	defer server.Close()

	commonEnv := []string{
		"QINIU_SANDBOX_API_URL=" + server.URL,
		"QINIU_API_KEY=test-api-key",
	}
	output, err := runCommand(
		t,
		"bash",
		[]string{"scripts/check-default-template-catalog.sh"},
		commonEnv...,
	)
	if err != nil {
		t.Fatalf("valid default-template catalog failed: %v\n%s", err, output)
	}
	for _, want := range []string{
		"github-runner-ubuntu-slim\ttmpl-slim\tready",
		"github-runner-ubuntu-22-04\ttmpl-22\tuploaded",
		"github-runner-ubuntu-24-04\ttmpl-24\tready",
		"github-runner-ubuntu-26-04\ttmpl-26\tready",
		"github-runner-ubuntu-slim-large\ttmpl-slim-large\tready",
		"github-runner-ubuntu-22-04-large\ttmpl-22-large\tready",
		"github-runner-ubuntu-24-04-large\ttmpl-24-large\tready",
		"github-runner-ubuntu-26-04-large\ttmpl-26-large\tready",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("catalog output missing %q:\n%s", want, output)
		}
	}

	templates = append(templates, map[string]any{
		"templateID":  "tmpl-24-duplicate",
		"names":       []string{"team/github-runner-ubuntu-24-04"},
		"public":      true,
		"buildStatus": "ready",
	})
	duplicateResponseBody, err := json.Marshal(templates)
	if err != nil {
		t.Fatal(err)
	}
	responseBody.Store(duplicateResponseBody)
	output, err = runCommand(
		t,
		"bash",
		[]string{"scripts/check-default-template-catalog.sh"},
		commonEnv...,
	)
	if err == nil || !strings.Contains(
		output,
		"expected exactly one default template name match for github-runner-ubuntu-24-04; found 2",
	) {
		t.Fatalf("duplicate catalog entry must fail, got err=%v\n%s", err, output)
	}

	privateDuplicateTemplates := append(
		templates[:len(templates)-1],
		map[string]any{
			"templateID":  "tmpl-24-private",
			"names":       []string{"team/github-runner-ubuntu-24-04"},
			"public":      false,
			"buildStatus": "ready",
		},
	)
	privateDuplicateResponseBody, err := json.Marshal(privateDuplicateTemplates)
	if err != nil {
		t.Fatal(err)
	}
	responseBody.Store(privateDuplicateResponseBody)
	output, err = runCommand(
		t,
		"bash",
		[]string{"scripts/check-default-template-catalog.sh"},
		commonEnv...,
	)
	if err == nil || !strings.Contains(
		output,
		"expected exactly one default template name match for github-runner-ubuntu-24-04; found 2",
	) {
		t.Fatalf("private duplicate catalog entry must fail, got err=%v\n%s", err, output)
	}
}

func TestCompatibilityGateRejectsMissingAndUnexplainedCoverage(t *testing.T) {
	fixture := t.TempDir()
	reportDir := filepath.Join(fixture, "reports")
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		t.Fatal(err)
	}
	report := `# Fixture
- OS Version: 24.04

## Installed Software

### Tools
- Git 2.0
- Docker Client 28.0
`
	for _, name := range []string{
		"Ubuntu2204-Readme.md",
		"Ubuntu2404-Readme.md",
		"Ubuntu2604-Readme.md",
		"ubuntu-slim-Readme.md",
	} {
		if err := os.WriteFile(filepath.Join(reportDir, name), []byte(report), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	lockPath := filepath.Join(fixture, "lock.json")
	lock := `{
  "repository": "actions/runner-images",
  "commit": "e986db797519f06a2e5e53701a715cfa4c1545e8",
  "reports": {
    "ubuntu-slim": "images/ubuntu-slim/ubuntu-slim-Readme.md",
    "ubuntu-22.04": "images/ubuntu/Ubuntu2204-Readme.md",
    "ubuntu-24.04": "images/ubuntu/Ubuntu2404-Readme.md",
    "ubuntu-26.04": "images/ubuntu/Ubuntu2604-Readme.md"
  }
}`
	if err := os.WriteFile(lockPath, []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(fixture, "compatibility.json")
	missingManifest := `{
  "upstream_commit": "e986db797519f06a2e5e53701a715cfa4c1545e8",
  "images": {
    "ubuntu-slim": {"entries": []},
    "ubuntu-22.04": {"entries": []},
    "ubuntu-24.04": {"entries": []},
    "ubuntu-26.04": {"entries": []}
  }
}`
	if err := os.WriteFile(manifestPath, []byte(missingManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	commonEnv := []string{
		"RUNNER_IMAGES_REPORT_DIR=" + reportDir,
		"RUNNER_IMAGES_LOCK=" + lockPath,
		"RUNNER_IMAGES_MANIFEST=" + manifestPath,
	}
	output, err := runCommand(t, "bash", []string{"scripts/check-runner-image-compatibility.sh"}, commonEnv...)
	if err == nil || !strings.Contains(output, "missing compatibility entry") {
		t.Fatalf("missing coverage must fail with a specific error, got err=%v\n%s", err, output)
	}

	entries := make([]map[string]string, 0, 12)
	for _, image := range []string{"ubuntu-slim", "ubuntu-22.04", "ubuntu-24.04", "ubuntu-26.04"} {
		for _, item := range []string{"Image metadata|OS Version", "Tools|Git", "Tools|Docker Client"} {
			parts := strings.Split(item, "|")
			entries = append(entries, map[string]string{
				"image":         image,
				"category":      parts[0],
				"upstream_name": parts[1],
				"status":        "excluded",
				"verification":  "false",
				"reason":        "",
			})
		}
	}
	images := map[string]map[string]any{}
	for _, image := range []string{"ubuntu-slim", "ubuntu-22.04", "ubuntu-24.04", "ubuntu-26.04"} {
		imageEntries := make([]map[string]string, 0, 3)
		for _, entry := range entries {
			if entry["image"] == image {
				delete(entry, "image")
				imageEntries = append(imageEntries, entry)
			}
		}
		images[image] = map[string]any{"entries": imageEntries}
	}
	unexplained, err := json.Marshal(map[string]any{
		"upstream_commit": "e986db797519f06a2e5e53701a715cfa4c1545e8",
		"images":          images,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, unexplained, 0o644); err != nil {
		t.Fatal(err)
	}
	output, err = runCommand(t, "bash", []string{"scripts/check-runner-image-compatibility.sh"}, commonEnv...)
	if err == nil || !strings.Contains(output, "Sandbox-specific reason") {
		t.Fatalf("unexplained exclusion must fail with a specific error, got err=%v\n%s", err, output)
	}
}

func TestDockerConformanceStopsOnFirstFailureAndRecordsJSON(t *testing.T) {
	fixture := t.TempDir()
	binDir := filepath.Join(fixture, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(binDir, "docker"), `#!/usr/bin/env bash
set -euo pipefail
if [ "$1" != run ]; then
  exit 64
fi
shift
while [ "$#" -gt 0 ] && [ "$1" != bash ]; do
  shift
done
shift
test "$1" = -lc
shift
exec bash -lc "$1"
`)
	manifestPath := filepath.Join(fixture, "compatibility.json")
	manifest := `{
  "images": {
    "ubuntu-24.04": {
      "entries": [
        {"category":"Tools","upstream_name":"first","status":"provided","verification":"printf first"},
        {"category":"Tools","upstream_name":"second","status":"provided","verification":"printf second >&2; exit 17"},
        {"category":"Tools","upstream_name":"third","status":"provided","verification":"printf third"}
      ]
    }
  }
}`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(fixture, "result.json")
	output, err := runCommand(
		t,
		"bash",
		[]string{
			"scripts/run-runner-image-conformance.sh",
			"--image", "ubuntu-24.04",
			"--executor", "docker",
			"--target", "fixture-image",
			"--output", resultPath,
		},
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"RUNNER_IMAGES_MANIFEST="+manifestPath,
	)
	if err == nil {
		t.Fatalf("conformance must fail on the failing assertion:\n%s", output)
	}
	resultBytes, readErr := os.ReadFile(resultPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	var result struct {
		Completed bool `json:"completed"`
		Passed    bool `json:"passed"`
		Results   []struct {
			Name       string `json:"name"`
			Stdout     string `json:"stdout"`
			Stderr     string `json:"stderr"`
			ExitStatus int    `json:"exit_status"`
		} `json:"results"`
	}
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		t.Fatalf("invalid result JSON: %v\n%s", err, resultBytes)
	}
	if !result.Completed || result.Passed || len(result.Results) != 2 {
		t.Fatalf("result must contain the two executed assertions and fail: %#v", result)
	}
	if result.Results[0].Stdout != "first" || result.Results[1].Stderr != "second" || result.Results[1].ExitStatus != 17 {
		t.Fatalf("unexpected command evidence: %#v", result.Results)
	}
}

func TestSandboxConformanceKillsSandboxOnFailure(t *testing.T) {
	fixture := t.TempDir()
	binDir := filepath.Join(fixture, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	qshellLog := filepath.Join(fixture, "qshell.log")
	writeExecutable(t, filepath.Join(binDir, "qshell"), `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$QSHELL_TEST_LOG"
case "$1 $2" in
  "sandbox create")
    printf '{"sandbox_id":"sb-fixture"}\n'
    ;;
  "sandbox exec")
    shift 2
    test "$1" = sb-fixture
    shift
    test "$1" = -u
    test "$2" = runner
    shift 2
    test "$1" = --
    shift
    exec "$@"
    ;;
  "sandbox kill")
    test "$3" = sb-fixture
    printf 'Killed sandbox sb-fixture\n'
    ;;
  *)
    exit 64
    ;;
esac
`)
	manifestPath := filepath.Join(fixture, "compatibility.json")
	manifest := `{
  "images": {
    "ubuntu-slim": {
      "entries": [
        {"category":"Tools","upstream_name":"failure","status":"provided","verification":"exit 23"}
      ]
    }
  }
}`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(fixture, "result.json")
	output, err := runCommand(
		t,
		"bash",
		[]string{
			"scripts/run-runner-image-conformance.sh",
			"--image", "ubuntu-slim",
			"--executor", "sandbox",
			"--target", "template-fixture",
			"--output", resultPath,
		},
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"RUNNER_IMAGES_MANIFEST="+manifestPath,
		"QSHELL_TEST_LOG="+qshellLog,
		"QINIU_SANDBOX_API_URL=https://sandbox.example.test",
		"QINIU_API_KEY=test-api-key",
	)
	if err == nil {
		t.Fatalf("sandbox conformance must fail on the assertion:\n%s", output)
	}
	logBytes, readErr := os.ReadFile(qshellLog)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(logBytes), "sandbox kill sb-fixture") {
		t.Fatalf("temporary sandbox was not killed:\n%s", logBytes)
	}
	if _, statErr := os.Stat(resultPath); errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("failure result JSON was not written")
	}
	resultBytes, readErr := os.ReadFile(resultPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	var result struct {
		Completed bool `json:"completed"`
		Passed    bool `json:"passed"`
		Cleanup   struct {
			Attempted bool `json:"attempted"`
			Passed    bool `json:"passed"`
		} `json:"cleanup"`
	}
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		t.Fatalf("invalid result JSON: %v\n%s", err, resultBytes)
	}
	if !result.Completed || result.Passed || !result.Cleanup.Attempted || !result.Cleanup.Passed {
		t.Fatalf("failed assertion must retain successful Sandbox cleanup evidence: %#v", result)
	}
}

func TestSandboxConformanceParsesOpaqueQshellSandboxID(t *testing.T) {
	fixture := t.TempDir()
	binDir := filepath.Join(fixture, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	qshellLog := filepath.Join(fixture, "qshell.log")
	writeExecutable(t, filepath.Join(binDir, "qshell"), `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$QSHELL_TEST_LOG"
case "$1 $2" in
  "sandbox create")
    printf 'Sandbox ID:   opaque-fixture-42\n'
    ;;
  "sandbox exec")
    if IFS= read -r unexpected_input; then
      printf 'unexpected stdin: %s\n' "$unexpected_input" >&2
      exit 88
    fi
    shift 2
    test "$1" = opaque-fixture-42
    shift
    test "$1" = -u
    test "$2" = runner
    shift 2
    test "$1" = --
    shift
    exec "$@"
    ;;
  "sandbox kill")
    test "$3" = opaque-fixture-42
    printf 'Killed sandbox opaque-fixture-42\n'
    ;;
  *)
    exit 64
    ;;
esac
`)
	manifestPath := filepath.Join(fixture, "compatibility.json")
	manifest := `{
  "images": {
    "ubuntu-slim": {
      "entries": [
        {"category":"Tools","upstream_name":"success","status":"provided","verification":"printf verified"}
      ]
    }
  }
}`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(fixture, "result.json")
	output, err := runCommand(
		t,
		"bash",
		[]string{
			"scripts/run-runner-image-conformance.sh",
			"--image", "ubuntu-slim",
			"--executor", "sandbox",
			"--target", "template-fixture",
			"--output", resultPath,
		},
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"RUNNER_IMAGES_MANIFEST="+manifestPath,
		"QSHELL_TEST_LOG="+qshellLog,
		"QINIU_SANDBOX_API_URL=https://sandbox.example.test",
		"QINIU_API_KEY=test-api-key",
	)
	if err != nil {
		t.Fatalf("sandbox conformance rejected an opaque qshell Sandbox ID: %v\n%s", err, output)
	}
	logBytes, err := os.ReadFile(qshellLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{
		"sandbox exec opaque-fixture-42 -u runner",
		"sandbox kill opaque-fixture-42",
	} {
		if !strings.Contains(string(logBytes), command) {
			t.Fatalf("qshell did not receive %q:\n%s", command, logBytes)
		}
	}
}

func TestSandboxConformanceRejectsQshellTransportErrorsWithZeroExitStatus(t *testing.T) {
	fixture := t.TempDir()
	binDir := filepath.Join(fixture, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	qshellLog := filepath.Join(fixture, "qshell.log")
	writeExecutable(t, filepath.Join(binDir, "qshell"), `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$QSHELL_TEST_LOG"
case "$1 $2" in
  "sandbox create")
    printf 'Sandbox ID:   sb-fixture\n'
    ;;
  "sandbox exec")
    printf 'Error: connect to sandbox sb-fixture failed: fixture transport failure\n' >&2
    exit 0
    ;;
  "sandbox kill")
    test "$3" = sb-fixture
    printf 'Killed sandbox sb-fixture\n'
    ;;
  *)
    exit 64
    ;;
esac
`)
	manifestPath := filepath.Join(fixture, "compatibility.json")
	manifest := `{
  "images": {
    "ubuntu-slim": {
      "entries": [
        {"category":"Tools","upstream_name":"transport","status":"provided","verification":"printf should-not-run"}
      ]
    }
  }
}`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(fixture, "result.json")
	output, err := runCommand(
		t,
		"bash",
		[]string{
			"scripts/run-runner-image-conformance.sh",
			"--image", "ubuntu-slim",
			"--executor", "sandbox",
			"--target", "template-fixture",
			"--output", resultPath,
		},
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"RUNNER_IMAGES_MANIFEST="+manifestPath,
		"QSHELL_TEST_LOG="+qshellLog,
		"QINIU_SANDBOX_API_URL=https://sandbox.example.test",
		"QINIU_API_KEY=test-api-key",
	)
	if err == nil {
		t.Fatalf("sandbox conformance must reject a qshell transport error even when qshell exits zero:\n%s", output)
	}
	resultBytes, readErr := os.ReadFile(resultPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	var result struct {
		Passed  bool `json:"passed"`
		Results []struct {
			Stdout     string `json:"stdout"`
			Stderr     string `json:"stderr"`
			ExitStatus int    `json:"exit_status"`
		} `json:"results"`
	}
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		t.Fatalf("invalid result JSON: %v\n%s", err, resultBytes)
	}
	if result.Passed || len(result.Results) != 1 {
		t.Fatalf("transport failure must be retained as one failed result: %#v", result)
	}
	if result.Results[0].ExitStatus == 0 ||
		!strings.Contains(result.Results[0].Stderr, "fixture transport failure") ||
		strings.Contains(result.Results[0].Stdout, "should-not-run") {
		t.Fatalf("transport failure evidence was not preserved: %#v", result.Results[0])
	}
	logBytes, readErr := os.ReadFile(qshellLog)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(logBytes), "sandbox kill sb-fixture") {
		t.Fatalf("temporary sandbox was not killed after transport failure:\n%s", logBytes)
	}
}

func TestSandboxConformanceFailsWhenCleanupCannotBeConfirmed(t *testing.T) {
	fixture := t.TempDir()
	binDir := filepath.Join(fixture, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(binDir, "qshell"), `#!/usr/bin/env bash
set -euo pipefail
case "$1 $2" in
  "sandbox create")
    printf 'Sandbox ID:   sb-fixture\n'
    ;;
  "sandbox exec")
    shift 2
    test "$1" = sb-fixture
    shift
    test "$1" = -u
    test "$2" = runner
    shift 2
    test "$1" = --
    shift
    exec "$@"
    ;;
  "sandbox kill")
    printf 'Error: kill sandbox sb-fixture failed: fixture cleanup failure\n' >&2
    exit 0
    ;;
  *)
    exit 64
    ;;
esac
`)
	manifestPath := filepath.Join(fixture, "compatibility.json")
	manifest := `{
  "images": {
    "ubuntu-slim": {
      "entries": [
        {"category":"Tools","upstream_name":"success","status":"provided","verification":"printf verified"}
      ]
    }
  }
}`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(fixture, "result.json")
	output, err := runCommand(
		t,
		"bash",
		[]string{
			"scripts/run-runner-image-conformance.sh",
			"--image", "ubuntu-slim",
			"--executor", "sandbox",
			"--target", "template-fixture",
			"--output", resultPath,
		},
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"RUNNER_IMAGES_MANIFEST="+manifestPath,
		"QINIU_SANDBOX_API_URL=https://sandbox.example.test",
		"QINIU_API_KEY=test-api-key",
	)
	if err == nil {
		t.Fatalf("sandbox conformance must fail when cleanup cannot be confirmed:\n%s", output)
	}
	resultBytes, readErr := os.ReadFile(resultPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	var result struct {
		Passed  bool `json:"passed"`
		Cleanup struct {
			Attempted  bool   `json:"attempted"`
			Passed     bool   `json:"passed"`
			ExitStatus int    `json:"exit_status"`
			Stderr     string `json:"stderr"`
		} `json:"cleanup"`
	}
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		t.Fatalf("invalid result JSON: %v\n%s", err, resultBytes)
	}
	if result.Passed ||
		!result.Cleanup.Attempted ||
		result.Cleanup.Passed ||
		result.Cleanup.ExitStatus == 0 ||
		!strings.Contains(result.Cleanup.Stderr, "fixture cleanup failure") {
		t.Fatalf("unconfirmed cleanup must be retained as failed release evidence: %#v", result)
	}
}

func TestTemplateReleaseSmokeRunsUsabilityAndIdentityContract(t *testing.T) {
	fixture := t.TempDir()
	binDir := filepath.Join(fixture, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(binDir, "qshell"), `#!/usr/bin/env bash
set -euo pipefail
if [ "${1:-}" = version ]; then
  printf 'v2.19.10\n'
  exit 0
fi
case "$1 $2" in
  "sandbox create")
    printf 'Sandbox ID:   sb-smoke\n'
    ;;
  "sandbox exec")
    printf '__QINIU_RUNNER_CONFORMANCE_REMOTE_STARTED__\n'
    ;;
  "sandbox kill")
    test "$3" = sb-smoke
    printf 'Killed sandbox sb-smoke\n'
    ;;
  *)
    exit 64
    ;;
esac
`)
	manifestPath := filepath.Join(fixture, "compatibility.json")
	manifest := `{
  "images": {
    "ubuntu-26.04": {
      "entries": [
        {
          "category": "Full inventory",
          "upstream_name": "expensive diagnostic",
          "status": "provided",
          "verification": "exit 99"
        }
      ]
    }
  }
}`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	outputDir := filepath.Join(fixture, "smoke")
	output, err := runCommand(
		t,
		"bash",
		[]string{"scripts/smoke-runner-template.sh", "ubuntu-26.04", "template-fixture"},
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"RUNNER_IMAGES_MANIFEST="+manifestPath,
		"RUNNER_SMOKE_OUTPUT_DIR="+outputDir,
		"QINIU_SANDBOX_API_URL=https://sandbox.example.test",
		"QINIU_API_KEY=test-api-key",
	)
	if err != nil {
		t.Fatalf("release smoke failed: %v\n%s", err, output)
	}
	resultFiles, err := filepath.Glob(filepath.Join(outputDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(resultFiles) != 1 {
		t.Fatalf("release smoke result files = %#v, want one", resultFiles)
	}
	resultBytes, err := os.ReadFile(resultFiles[0])
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Passed         bool   `json:"passed"`
		SupportChannel string `json:"support_channel"`
		Results        []struct {
			Category string `json:"category"`
			Name     string `json:"name"`
			Command  string `json:"command"`
		} `json:"results"`
		Cleanup struct {
			Passed bool `json:"passed"`
		} `json:"cleanup"`
	}
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		t.Fatalf("invalid release smoke JSON: %v\n%s", err, resultBytes)
	}
	if !result.Passed ||
		result.SupportChannel != "preview" ||
		!result.Cleanup.Passed ||
		len(result.Results) != 9 {
		t.Fatalf("release smoke result = %#v, want nine passing usability and identity checks plus cleanup", result)
	}
	runnerVersionChecked := false
	runtimeMetadataChecked := false
	nvmHomeChecked := false
	cloudflareDNSChecked := false
	for _, check := range result.Results {
		if check.Category != "Release smoke" {
			t.Fatalf("release smoke unexpectedly ran full inventory check %#v", check)
		}
		if check.Name == "preinstalled Actions runner" {
			runnerVersionChecked = strings.Contains(check.Command, "Runner.Listener --version") &&
				strings.Contains(check.Command, `= "2.336.0"`)
		}
		if check.Name == "runtime image metadata" {
			runtimeMetadataChecked = strings.Contains(check.Command, `"$IMAGE_TEMPLATE" = "github-runner-ubuntu-26-04"`) &&
				strings.Contains(check.Command, `"$ImageVersion" = "20260817.1"`) &&
				strings.Contains(check.Command, `"$IMAGE_VERSION" = "20260817.1"`)
		}
		if check.Name == "Cloudflare DNS" {
			cloudflareDNSChecked = strings.Contains(check.Command, `nameserver 1.1.1.1`) &&
				strings.Contains(check.Command, `nameserver 1.0.0.1`) &&
				strings.Contains(check.Command, `/etc/resolv.conf`)
			resolvConfPath := filepath.Join(fixture, "resolv.conf")
			if err := os.WriteFile(
				resolvConfPath,
				[]byte("nameserver 1.1.1.1\nnameserver 1.0.0.1\n"),
				0o644,
			); err != nil {
				t.Fatal(err)
			}
			resolverCommand := strings.ReplaceAll(
				check.Command,
				"/etc/resolv.conf",
				fmt.Sprintf("%q", resolvConfPath),
			)
			if output, err := runCommand(t, "bash", []string{"-c", resolverCommand}); err != nil {
				t.Fatalf("Cloudflare DNS verification command failed: %v\n%s", err, output)
			}
		}
		if check.Name == "runner writable NVM home" {
			nvmHomeChecked = strings.Contains(check.Command, "sudo -H -u runner") &&
				strings.Contains(check.Command, `test -s "$HOME/.nvm/nvm.sh"`) &&
				strings.Contains(check.Command, `test -w "$HOME/.nvm"`) &&
				strings.Contains(check.Command, `nvm --version`)
		}
		if check.Name == "Docker daemon" {
			if !strings.Contains(check.Command, "sudo -H -u runner") {
				t.Fatalf("Docker smoke does not reproduce the runnerd group context: %#v", check)
			}
			if strings.Contains(check.Command, "hello-world") || strings.Contains(check.Command, "docker.io") {
				t.Fatalf("Docker daemon smoke must not depend on Docker Hub availability: %#v", check)
			}
			if !strings.Contains(check.Command, "docker import") || !strings.Contains(check.Command, "docker run") {
				t.Fatalf("Docker daemon smoke must execute a local container without a registry pull: %#v", check)
			}
		}
	}
	if !runnerVersionChecked {
		t.Fatalf("release smoke did not pin the Actions runner version: %#v", result.Results)
	}
	if !runtimeMetadataChecked {
		t.Fatalf("release smoke did not pin runtime template metadata: %#v", result.Results)
	}
	if !nvmHomeChecked {
		t.Fatalf("release smoke did not verify the runner-owned NVM home: %#v", result.Results)
	}
	if !cloudflareDNSChecked {
		t.Fatalf("release smoke did not verify Cloudflare DNS: %#v", result.Results)
	}
}

func TestEnsureDockerIsIdempotentAndKeepsSocketNonRootAccessible(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the non-root socket contract requires a non-root test process")
	}
	root := repositoryRoot(t)
	for _, image := range []string{
		"ubuntu-slim",
		"ubuntu-22.04",
		"ubuntu-24.04",
		"ubuntu-26.04",
	} {
		t.Run(image, func(t *testing.T) {
			fixture, err := os.MkdirTemp("", "runnerd-docker-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.RemoveAll(fixture); err != nil {
					t.Errorf("remove fixture: %v", err)
				}
			})
			binDir := filepath.Join(fixture, "bin")
			if err := os.MkdirAll(binDir, 0o755); err != nil {
				t.Fatal(err)
			}
			socketPath := filepath.Join(fixture, "docker.sock")
			listener, err := net.Listen("unix", socketPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = listener.Close()
			})
			if err := os.Chmod(socketPath, 0o600); err != nil {
				t.Fatal(err)
			}
			sudoLog := filepath.Join(fixture, "sudo.log")
			dockerdLog := filepath.Join(fixture, "dockerd.log")
			socketFixedMarker := filepath.Join(fixture, "docker-socket.fixed")
			writeExecutable(t, filepath.Join(binDir, "docker"), `#!/usr/bin/env bash
set -euo pipefail
test "$1" = info
test -f "$DOCKER_SOCKET_FIXED_MARKER"
`)
			writeExecutable(t, filepath.Join(binDir, "dockerd"), `#!/usr/bin/env bash
set -euo pipefail
printf 'unexpected dockerd start\n' >>"$DOCKERD_TEST_LOG"
: >"$DOCKER_SOCKET_FIXED_MARKER"
exit 1
`)
			writeExecutable(t, filepath.Join(binDir, "sudo"), `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$SUDO_TEST_LOG"
if [ "$1" = chmod ]; then
  : >"$DOCKER_SOCKET_FIXED_MARKER"
  exec "$@"
fi
if [ "$1" = chgrp ]; then
  exit 0
fi
exec "$@"
`)
			script := filepath.Join(
				root,
				"templates",
				"github-runner-"+image,
				"scripts",
				"ensure-docker",
			)
			env := []string{
				"PATH=" + binDir + ":" + os.Getenv("PATH"),
				"DOCKER_BIN=" + filepath.Join(binDir, "docker"),
				"DOCKERD_BIN=" + filepath.Join(binDir, "dockerd"),
				"DOCKER_SOCKET=" + socketPath,
				"DOCKER_PID_FILE=" + filepath.Join(fixture, "docker.pid"),
				"DOCKER_LOG_FILE=" + dockerdLog,
				"DOCKERD_TEST_LOG=" + dockerdLog,
				"DOCKER_SOCKET_FIXED_MARKER=" + socketFixedMarker,
				"SUDO_TEST_LOG=" + sudoLog,
			}
			for run := 0; run < 2; run++ {
				output, err := runCommand(t, "bash", []string{script}, env...)
				if err != nil {
					t.Fatalf("ensure-docker run %d failed: %v\n%s", run+1, err, output)
				}
			}
			info, err := os.Stat(socketPath)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != 0o660 {
				t.Fatalf("socket mode = %04o, want 0660", got)
			}
			if contents, err := os.ReadFile(dockerdLog); err == nil && len(contents) > 0 {
				t.Fatalf("idempotent ready path started dockerd:\n%s", contents)
			}
			sudoBytes, err := os.ReadFile(sudoLog)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(string(sudoBytes), "chmod 0660 "+socketPath) != 2 {
				t.Fatalf("both runs must preserve group-only socket access:\n%s", sudoBytes)
			}
		})
	}
}

func TestEnsureDockerReplacesStalePIDOwnedByAnotherProcess(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the stale PID contract requires the non-root sudo path")
	}
	root := repositoryRoot(t)
	for _, image := range []string{
		"ubuntu-slim",
		"ubuntu-22.04",
		"ubuntu-24.04",
		"ubuntu-26.04",
	} {
		t.Run(image, func(t *testing.T) {
			fixture := t.TempDir()
			binDir := filepath.Join(fixture, "bin")
			if err := os.MkdirAll(binDir, 0o755); err != nil {
				t.Fatal(err)
			}
			readyPath := filepath.Join(fixture, "docker.ready")
			startMarker := filepath.Join(fixture, "dockerd.started")
			pidPath := filepath.Join(fixture, "docker.pid")
			socketPath := filepath.Join(fixture, "docker.sock")
			if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(socketPath, []byte("stale socket placeholder"), 0o644); err != nil {
				t.Fatal(err)
			}
			writeExecutable(t, filepath.Join(binDir, "docker"), `#!/usr/bin/env bash
set -euo pipefail
test "$1" = info
test -f "$DOCKER_READY_FILE"
`)
			writeExecutable(t, filepath.Join(binDir, "dockerd"), `#!/usr/bin/env bash
set -euo pipefail
pid_file=""
for argument in "$@"; do
  case "$argument" in
    --pidfile=*) pid_file="${argument#--pidfile=}" ;;
  esac
done
test -n "$pid_file"
printf '%s\n' "$$" >"$pid_file"
: >"$DOCKER_READY_FILE"
: >"$DOCKER_START_MARKER"
`)
			writeExecutable(t, filepath.Join(binDir, "sudo"), `#!/usr/bin/env bash
set -euo pipefail
if [ "$1" = mkdir ]; then
  exit 0
fi
exec "$@"
`)
			script := filepath.Join(root, "templates", "github-runner-"+image, "scripts", "ensure-docker")
			output, err := runCommand(
				t, "bash", []string{script},
				"PATH="+binDir+":"+os.Getenv("PATH"),
				"DOCKER_BIN="+filepath.Join(binDir, "docker"),
				"DOCKERD_BIN="+filepath.Join(binDir, "dockerd"),
				"DOCKER_SOCKET="+socketPath,
				"DOCKER_PID_FILE="+pidPath,
				"DOCKER_LOG_FILE="+filepath.Join(fixture, "dockerd.log"),
				"DOCKER_READY_FILE="+readyPath,
				"DOCKER_START_MARKER="+startMarker,
			)
			if err != nil {
				t.Fatalf("ensure-docker did not replace the stale PID: %v\n%s", err, output)
			}
			if _, err := os.Stat(startMarker); err != nil {
				t.Fatalf("dockerd was not started after stale PID detection: %v", err)
			}
			if _, err := os.Stat(socketPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("stale socket was not removed: %v", err)
			}
		})
	}
}

func TestEnsureDockerRestartsUnreadyDockerdProcess(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the unready dockerd contract requires the non-root sudo path")
	}
	root := repositoryRoot(t)
	for _, image := range []string{
		"ubuntu-slim",
		"ubuntu-22.04",
		"ubuntu-24.04",
		"ubuntu-26.04",
	} {
		t.Run(image, func(t *testing.T) {
			fixture := t.TempDir()
			binDir := filepath.Join(fixture, "bin")
			if err := os.MkdirAll(binDir, 0o755); err != nil {
				t.Fatal(err)
			}

			staleDockerd := exec.Command("sleep", "60")
			if err := staleDockerd.Start(); err != nil {
				t.Fatal(err)
			}
			staleDockerdWait := make(chan error, 1)
			go func() {
				staleDockerdWait <- staleDockerd.Wait()
			}()
			staleDockerdExited := false
			t.Cleanup(func() {
				if staleDockerdExited {
					return
				}
				_ = staleDockerd.Process.Kill()
				<-staleDockerdWait
			})

			procDir := filepath.Join(fixture, "proc", strconv.Itoa(staleDockerd.Process.Pid))
			if err := os.MkdirAll(procDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(procDir, "comm"), []byte("dockerd\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			readyPath := filepath.Join(fixture, "docker.ready")
			startMarker := filepath.Join(fixture, "dockerd.started")
			pidPath := filepath.Join(fixture, "docker.pid")
			socketPath := filepath.Join(fixture, "docker.sock")
			if err := os.WriteFile(pidPath, []byte(strconv.Itoa(staleDockerd.Process.Pid)+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			writeExecutable(t, filepath.Join(binDir, "docker"), `#!/usr/bin/env bash
set -euo pipefail
test "$1" = info
test -f "$DOCKER_READY_FILE"
`)
			writeExecutable(t, filepath.Join(binDir, "dockerd"), `#!/usr/bin/env bash
set -euo pipefail
pid_file=""
for argument in "$@"; do
  case "$argument" in
    --pidfile=*) pid_file="${argument#--pidfile=}" ;;
  esac
done
test -n "$pid_file"
printf '%s\n' "$$" >"$pid_file"
: >"$DOCKER_READY_FILE"
: >"$DOCKER_START_MARKER"
`)
			writeExecutable(t, filepath.Join(binDir, "sudo"), `#!/usr/bin/env bash
set -euo pipefail
if [ "$1" = mkdir ]; then
  exit 0
fi
exec "$@"
`)
			script := filepath.Join(root, "templates", "github-runner-"+image, "scripts", "ensure-docker")
			output, err := runCommand(
				t, "bash", []string{script},
				"PATH="+binDir+":"+os.Getenv("PATH"),
				"DOCKER_BIN="+filepath.Join(binDir, "docker"),
				"DOCKERD_BIN="+filepath.Join(binDir, "dockerd"),
				"DOCKER_SOCKET="+socketPath,
				"DOCKER_PID_FILE="+pidPath,
				"DOCKER_LOG_FILE="+filepath.Join(fixture, "dockerd.log"),
				"DOCKER_PROC_ROOT="+filepath.Join(fixture, "proc"),
				"DOCKER_READY_FILE="+readyPath,
				"DOCKER_START_MARKER="+startMarker,
			)
			if err != nil {
				t.Fatalf("ensure-docker did not restart the unready daemon: %v\n%s", err, output)
			}
			if _, err := os.Stat(startMarker); err != nil {
				t.Fatalf("dockerd was not restarted after the unready daemon was stopped: %v", err)
			}
			select {
			case err := <-staleDockerdWait:
				staleDockerdExited = true
				if err == nil {
					t.Fatal("unready dockerd exited successfully; want termination by ensure-docker")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("ensure-docker left the unready dockerd process running")
			}
		})
	}
}

func TestTemplateCurlAddsAuthenticationOnlyForGitHubAPI(t *testing.T) {
	root := repositoryRoot(t)
	for _, image := range []string{
		"ubuntu-slim",
		"ubuntu-22.04",
		"ubuntu-24.04",
		"ubuntu-26.04",
	} {
		t.Run(image, func(t *testing.T) {
			fixture := t.TempDir()
			curlLog := filepath.Join(fixture, "curl.log")
			fakeCurl := filepath.Join(fixture, "curl")
			writeExecutable(t, fakeCurl, `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$CURL_TEST_LOG"
if [[ "$*" == *"https://api.github.com/"* ]]; then
  printf '[]\n'
fi
`)
			wrapper := filepath.Join(
				root,
				"templates",
				"github-runner-"+image,
				"scripts",
				"curl",
			)
			env := []string{
				"RUNNER_TEMPLATE_CURL_BIN=" + fakeCurl,
				"GITHUB_TOKEN=fixture-token",
				"CURL_TEST_LOG=" + curlLog,
			}
			for _, url := range []string{
				"https://api.github.com/repos/actions/runner/releases",
				"https://download.docker.com/linux/ubuntu/gpg",
			} {
				output, err := runCommand(t, wrapper, []string{"-fsSL", url}, env...)
				if err != nil {
					t.Fatalf("curl wrapper failed for %s: %v\n%s", url, err, output)
				}
			}
			logBytes, err := os.ReadFile(curlLog)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(logBytes)), "\n")
			if len(lines) != 2 {
				t.Fatalf("curl invocation log = %q", logBytes)
			}
			if !strings.Contains(lines[0], "Authorization: Bearer fixture-token") {
				t.Fatalf("GitHub API request was not authenticated: %s", lines[0])
			}
			if strings.Contains(lines[1], "Authorization:") {
				t.Fatalf("non-GitHub request received the GitHub credential: %s", lines[1])
			}
			for lineNumber, line := range lines {
				for _, option := range []string{
					"--http1.1",
					"--connect-timeout 15",
					"--speed-limit 1024",
					"--speed-time 60",
					"--retry 5",
					"--retry-all-errors",
					"--retry-delay 2",
				} {
					if !strings.Contains(line, option) {
						t.Fatalf(
							"curl invocation %d missing resilient transport option %q: %s",
							lineNumber+1,
							option,
							line,
						)
					}
				}
			}
		})
	}
}

func TestTemplateCurlRetriesStalledAWSCLIArchiveDownloads(t *testing.T) {
	root := repositoryRoot(t)
	for _, image := range []string{
		"ubuntu-slim",
		"ubuntu-22.04",
		"ubuntu-24.04",
		"ubuntu-26.04",
	} {
		t.Run(image, func(t *testing.T) {
			fixture := t.TempDir()
			curlLog := filepath.Join(fixture, "curl.log")
			fakeCurl := filepath.Join(fixture, "curl")
			writeExecutable(t, fakeCurl, `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >"$CURL_TEST_LOG"
`)
			wrapper := filepath.Join(
				root,
				"templates",
				"github-runner-"+image,
				"scripts",
				"curl",
			)
			output, err := runCommand(
				t, wrapper, []string{
					"-fsSL",
					"https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip",
					"-o",
					filepath.Join(fixture, "awscli.zip"),
				},
				"RUNNER_TEMPLATE_CURL_BIN="+fakeCurl,
				"CURL_TEST_LOG="+curlLog,
			)
			if err != nil {
				t.Fatalf("curl wrapper failed: %v\n%s", err, output)
			}
			logBytes, err := os.ReadFile(curlLog)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(logBytes), "--speed-limit 65536 --speed-time 60") {
				t.Fatalf("AWS CLI download does not reject a stalled CDN connection: %s", logBytes)
			}
		})
	}
}

func TestTemplateCurlRetriesTruncatedGitHubAPIResponses(t *testing.T) {
	root := repositoryRoot(t)
	for _, image := range []string{
		"ubuntu-slim",
		"ubuntu-22.04",
		"ubuntu-24.04",
		"ubuntu-26.04",
	} {
		t.Run(image, func(t *testing.T) {
			fixture := t.TempDir()
			curlLog := filepath.Join(fixture, "curl.log")
			attemptFile := filepath.Join(fixture, "attempt")
			fakeCurl := filepath.Join(fixture, "curl")
			writeExecutable(t, fakeCurl, `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$CURL_TEST_LOG"
attempt=0
if [ -f "$CURL_TEST_ATTEMPT" ]; then
  attempt="$(cat "$CURL_TEST_ATTEMPT")"
fi
attempt=$((attempt + 1))
printf '%s\n' "$attempt" >"$CURL_TEST_ATTEMPT"
if [ "$attempt" -eq 1 ]; then
  printf '{"truncated":'
else
  printf '[{"tag_name":"v1.2.3"}]\n'
fi
`)
			wrapper := filepath.Join(
				root,
				"templates",
				"github-runner-"+image,
				"scripts",
				"curl",
			)
			output, err := runCommand(
				t, wrapper, []string{
					"-fsSL",
					"https://api.github.com/repos/actions/runner/releases",
				},
				"RUNNER_TEMPLATE_CURL_BIN="+fakeCurl,
				"RUNNER_TEMPLATE_GITHUB_API_RETRY_DELAY=0",
				"CURL_TEST_LOG="+curlLog,
				"CURL_TEST_ATTEMPT="+attemptFile,
			)
			if err != nil {
				t.Fatalf("curl wrapper failed: %v\n%s", err, output)
			}
			outputLines := strings.Split(strings.TrimSpace(string(output)), "\n")
			if got := outputLines[len(outputLines)-1]; got != `[{"tag_name":"v1.2.3"}]` {
				t.Fatalf("curl wrapper output = %q", strings.TrimSpace(string(output)))
			}
			if !strings.Contains(string(output), "GitHub API returned an incomplete response") {
				t.Fatalf("curl wrapper did not report the invalid response retry: %q", output)
			}
			logBytes, err := os.ReadFile(curlLog)
			if err != nil {
				t.Fatal(err)
			}
			if got := len(strings.Split(strings.TrimSpace(string(logBytes)), "\n")); got != 2 {
				t.Fatalf("curl invocation count = %d, want 2\n%s", got, logBytes)
			}
		})
	}
}

func TestTemplateCurlCachesReleaseAPIAndSeedsTagMetadata(t *testing.T) {
	root := repositoryRoot(t)
	for _, image := range []string{
		"ubuntu-slim",
		"ubuntu-22.04",
		"ubuntu-24.04",
		"ubuntu-26.04",
	} {
		t.Run(image, func(t *testing.T) {
			fixture := t.TempDir()
			curlLog := filepath.Join(fixture, "curl.log")
			fakeCurl := filepath.Join(fixture, "curl")
			writeExecutable(t, fakeCurl, `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$CURL_TEST_LOG"
if [[ "$*" == *"/repos/cli/cli/releases/latest"* ]]; then
  printf '%s\n' '{"tag_name":"v2.97.0","assets":[{"name":"gh_2.97.0_checksums.txt","url":"https://api.github.com/repos/cli/cli/releases/assets/1"}]}'
fi
`)
			wrapper := filepath.Join(
				root,
				"templates",
				"github-runner-"+image,
				"scripts",
				"curl",
			)
			env := []string{
				"RUNNER_TEMPLATE_CURL_BIN=" + fakeCurl,
				"RUNNER_TEMPLATE_GITHUB_RELEASE_CACHE=" + filepath.Join(fixture, "release-cache"),
				"RUNNER_TEMPLATE_GITHUB_API_RETRY_DELAY=0",
				"CURL_TEST_LOG=" + curlLog,
			}
			latestArgs := []string{
				"-fsSL",
				"https://api.github.com/repos/cli/cli/releases/latest",
			}
			for attempt := 1; attempt <= 2; attempt++ {
				output, err := runCommand(t, wrapper, latestArgs, env...)
				if err != nil {
					t.Fatalf("release API attempt %d failed: %v\n%s", attempt, err, output)
				}
				if !strings.Contains(string(output), `"tag_name":"v2.97.0"`) {
					t.Fatalf("release API attempt %d returned %q", attempt, output)
				}
			}
			output, err := runCommand(t, wrapper, []string{
				"-fsSL",
				"https://github.com/cli/cli/releases/download/v2.97.0/gh_2.97.0_checksums.txt",
			}, env...)
			if err != nil {
				t.Fatalf("release asset download failed: %v\n%s", err, output)
			}

			logBytes, err := os.ReadFile(curlLog)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(logBytes)), "\n")
			if len(lines) != 2 {
				t.Fatalf("curl invocation count = %d, want one release lookup and one asset download\n%s", len(lines), logBytes)
			}
			if !strings.Contains(lines[0], "/repos/cli/cli/releases/latest") {
				t.Fatalf("first request is not the release lookup: %s", lines[0])
			}
			if !strings.Contains(lines[1], "/repos/cli/cli/releases/assets/1") {
				t.Fatalf("tag cache did not route the asset through the API: %s", lines[1])
			}
			if strings.Contains(string(logBytes), "/releases/tags/v2.97.0") {
				t.Fatalf("latest response did not seed the tag cache: %s", logBytes)
			}
		})
	}
}

func TestTemplateCurlDownloadsGitHubReleaseAssetsThroughAPI(t *testing.T) {
	root := repositoryRoot(t)
	for _, image := range []string{
		"ubuntu-slim",
		"ubuntu-22.04",
		"ubuntu-24.04",
		"ubuntu-26.04",
	} {
		t.Run(image, func(t *testing.T) {
			fixture := t.TempDir()
			curlLog := filepath.Join(fixture, "curl.log")
			fakeCurl := filepath.Join(fixture, "curl")
			writeExecutable(t, fakeCurl, `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$CURL_TEST_LOG"
if [[ "$*" == *"/repos/aws/aws-sam-cli/releases/tags/v1.165.0"* ]]; then
  printf '%s\n' '{"assets":[{"name":"aws-sam-cli-linux-x86_64.zip","url":"https://api.github.com/repos/aws/aws-sam-cli/releases/assets/497055647"}]}'
fi
`)
			wrapper := filepath.Join(
				root,
				"templates",
				"github-runner-"+image,
				"scripts",
				"curl",
			)
			args := []string{
				"-fsSL",
				"https://github.com/aws/aws-sam-cli/releases/download/v1.165.0/aws-sam-cli-linux-x86_64.zip",
				"-o",
				filepath.Join(fixture, "sam.zip"),
			}
			env := []string{
				"RUNNER_TEMPLATE_CURL_BIN=" + fakeCurl,
				"RUNNER_TEMPLATE_GITHUB_RELEASE_CACHE=" + filepath.Join(fixture, "release-cache"),
				"CURL_TEST_LOG=" + curlLog,
			}
			for attempt := 1; attempt <= 2; attempt++ {
				output, err := runCommand(t, wrapper, args, env...)
				if err != nil {
					t.Fatalf("curl wrapper attempt %d failed: %v\n%s", attempt, err, output)
				}
			}
			logBytes, err := os.ReadFile(curlLog)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(logBytes)), "\n")
			if len(lines) != 3 {
				t.Fatalf("curl invocation count = %d, want 3 (one cached metadata lookup and two downloads)\n%s", len(lines), logBytes)
			}
			if !strings.Contains(lines[0], "/releases/tags/v1.165.0") {
				t.Fatalf("release metadata request missing: %s", lines[0])
			}
			for _, required := range []string{
				"https://api.github.com/repos/aws/aws-sam-cli/releases/assets/497055647",
				"Accept: application/octet-stream",
				"X-GitHub-Api-Version: 2022-11-28",
			} {
				if !strings.Contains(lines[1], required) {
					t.Fatalf("release asset request missing %q: %s", required, lines[1])
				}
			}
			if strings.Contains(lines[1], "https://github.com/aws/aws-sam-cli/releases/download/") {
				t.Fatalf("release asset request still uses the browser download endpoint: %s", lines[1])
			}
			if !strings.Contains(lines[2], "https://api.github.com/repos/aws/aws-sam-cli/releases/assets/497055647") {
				t.Fatalf("cached release asset request did not use the asset API: %s", lines[2])
			}
		})
	}
}

func TestTemplateCurlCanDownloadPinnedGitHubReleaseAssetsDirectly(t *testing.T) {
	root := repositoryRoot(t)
	for _, image := range []string{
		"ubuntu-slim",
		"ubuntu-22.04",
		"ubuntu-24.04",
		"ubuntu-26.04",
	} {
		t.Run(image, func(t *testing.T) {
			fixture := t.TempDir()
			curlLog := filepath.Join(fixture, "curl.log")
			fakeCurl := filepath.Join(fixture, "curl")
			writeExecutable(t, fakeCurl, `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$CURL_TEST_LOG"
`)
			wrapper := filepath.Join(
				root,
				"templates",
				"github-runner-"+image,
				"scripts",
				"curl",
			)
			assetURL := "https://github.com/mgoltzsche/podman-static/releases/download/v5.8.4/podman-linux-amd64.tar.gz"
			output, err := runCommand(
				t, wrapper, []string{"-fsSL", assetURL},
				"RUNNER_TEMPLATE_CURL_BIN="+fakeCurl,
				"RUNNER_TEMPLATE_DIRECT_GITHUB_ASSETS=1",
				"CURL_TEST_LOG="+curlLog,
			)
			if err != nil {
				t.Fatalf("direct release asset download failed: %v\n%s", err, output)
			}
			logBytes, err := os.ReadFile(curlLog)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(logBytes)), "\n")
			if len(lines) != 1 {
				t.Fatalf("curl invocation count = %d, want one direct download\n%s", len(lines), logBytes)
			}
			if !strings.Contains(lines[0], assetURL) {
				t.Fatalf("direct download did not retain the browser asset URL: %s", lines[0])
			}
			if strings.Contains(lines[0], "api.github.com") {
				t.Fatalf("direct download unexpectedly queried the GitHub API: %s", lines[0])
			}
		})
	}
}

func TestUbuntu2204TemplateWgetBoundsStalledTransfers(t *testing.T) {
	root := repositoryRoot(t)
	fixture := t.TempDir()
	wgetLog := filepath.Join(fixture, "wget.log")
	fakeWget := filepath.Join(fixture, "wget")
	writeExecutable(t, fakeWget, `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >"$WGET_TEST_LOG"
`)
	wrapper := filepath.Join(
		root,
		"templates",
		"github-runner-ubuntu-22.04",
		"scripts",
		"wget",
	)
	output, err := runCommand(
		t, wrapper, []string{
			"-O",
			"/tmp/output",
			"https://raw.githubusercontent.com/example/file",
		},
		"RUNNER_TEMPLATE_WGET_BIN="+fakeWget,
		"WGET_TEST_LOG="+wgetLog,
	)
	if err != nil {
		t.Fatalf("wget wrapper failed: %v\n%s", err, output)
	}
	logBytes, err := os.ReadFile(wgetLog)
	if err != nil {
		t.Fatal(err)
	}
	invocation := string(logBytes)
	for _, option := range []string{
		"--timeout=15",
		"--read-timeout=60",
		"--tries=6",
		"--waitretry=2",
		"--retry-connrefused",
	} {
		if !strings.Contains(invocation, option) {
			t.Fatalf("wget wrapper missing resilient transport option %q: %s", option, invocation)
		}
	}
}

func TestUbuntu2204TemplateUsesCurlRetriesForAptFastRawFiles(t *testing.T) {
	root := repositoryRoot(t)
	fixture := t.TempDir()
	curlLog := filepath.Join(fixture, "curl.log")
	fakeCurl := filepath.Join(fixture, "curl")
	writeExecutable(t, fakeCurl, `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >"$CURL_TEST_LOG"
`)
	wrapper := filepath.Join(
		root,
		"templates",
		"github-runner-ubuntu-22.04",
		"scripts",
		"wget",
	)
	output, err := runCommand(
		t, wrapper, []string{
			"https://raw.githubusercontent.com/ilikenwf/apt-fast/master/apt-fast",
			"-O",
			"/usr/local/bin/apt-fast",
		},
		"RUNNER_TEMPLATE_CURL_BIN="+fakeCurl,
		"CURL_TEST_LOG="+curlLog,
	)
	if err != nil {
		t.Fatalf("apt-fast curl fallback failed: %v\n%s", err, output)
	}
	logBytes, err := os.ReadFile(curlLog)
	if err != nil {
		t.Fatal(err)
	}
	invocation := string(logBytes)
	for _, option := range []string{
		"--http1.1",
		"--retry 8",
		"--retry-all-errors",
		"--output /usr/local/bin/apt-fast",
		"https://raw.githubusercontent.com/ilikenwf/apt-fast/master/apt-fast",
	} {
		if !strings.Contains(invocation, option) {
			t.Fatalf("apt-fast curl fallback missing %q: %s", option, invocation)
		}
	}
}

func TestRunnerTemplateDockerfilesUseQshellCompatibleRunInstructions(t *testing.T) {
	root := repositoryRoot(t)
	for _, image := range []string{
		"ubuntu-slim",
		"ubuntu-22.04",
		"ubuntu-24.04",
		"ubuntu-26.04",
	} {
		t.Run(image, func(t *testing.T) {
			dockerfilePath := filepath.Join(
				root,
				"templates",
				"github-runner-"+image,
				"Dockerfile",
			)
			dockerfileBytes, err := os.ReadFile(dockerfilePath)
			if err != nil {
				t.Fatal(err)
			}
			dockerfile := string(dockerfileBytes)
			for _, unsupported := range []string{
				"RUN --mount=",
				"/run/secrets/github_token",
			} {
				if strings.Contains(dockerfile, unsupported) {
					t.Fatalf(
						"qshell v2.19.10 passes RUN arguments to the remote shell and cannot execute BuildKit-only %q",
						unsupported,
					)
				}
			}
			if !strings.Contains(dockerfile, "RUN TEMPLATE_FLAVOR=") {
				t.Fatal("template setup must remain a plain qshell-compatible RUN instruction")
			}
		})
	}
}

func TestRunnerTemplateDockerfilesSplitSetupIntoCacheablePhases(t *testing.T) {
	root := repositoryRoot(t)
	for _, image := range []string{
		"ubuntu-slim",
		"ubuntu-22.04",
		"ubuntu-24.04",
		"ubuntu-26.04",
	} {
		t.Run(image, func(t *testing.T) {
			templateRoot := filepath.Join(root, "templates", "github-runner-"+image)
			dockerfileBytes, err := os.ReadFile(filepath.Join(templateRoot, "Dockerfile"))
			if err != nil {
				t.Fatal(err)
			}
			dockerfile := string(dockerfileBytes)
			flavor := "versioned"
			if image == "ubuntu-slim" {
				flavor = "slim"
			}
			phases := []string{"bootstrap", "platform", "toolchain", "runtime"}
			if image != "ubuntu-slim" {
				phases = []string{"bootstrap", "platform", "node", "toolchain", "runtime"}
			}
			previous := -1
			for _, phase := range phases {
				instruction := "TEMPLATE_FLAVOR=" + flavor +
					" RUNNER_TEMPLATE_PHASE=" + phase +
					" bash /usr/local/share/qiniu-sandbox-runner-template/setup-template.sh"
				index := strings.Index(dockerfile, instruction)
				if index < 0 {
					t.Fatalf("template must expose a cacheable %s setup layer", phase)
				}
				if index <= previous {
					t.Fatalf("template setup phases must preserve order; %s index=%d previous=%d", phase, index, previous)
				}
				previous = index
			}
			if strings.Count(dockerfile, "RUNNER_TEMPLATE_PHASE=") != len(phases) {
				t.Fatalf("template must have exactly %d cacheable setup layers; got %d", len(phases), strings.Count(dockerfile, "RUNNER_TEMPLATE_PHASE="))
			}

			scriptBytes, err := os.ReadFile(filepath.Join(templateRoot, "scripts", "setup-template.sh"))
			if err != nil {
				t.Fatal(err)
			}
			script := string(scriptBytes)
			if strings.Contains(script, "/tmp/runner-images/") ||
				strings.Contains(script, "mkdir -p /tmp/runner-images") ||
				strings.Contains(script, "test -d /tmp/runner-images") {
				t.Fatal("runner-images source must survive qshell cache layers outside /tmp")
			}
			if !strings.Contains(script, "mkdir -p /opt/qiniu-runner-images") ||
				!strings.Contains(script, "test -d /opt/qiniu-runner-images") {
				t.Fatal("runner-images source must use the durable cross-phase directory /opt/qiniu-runner-images")
			}
			required := []string{
				`runner_template_phase="${RUNNER_TEMPLATE_PHASE:-all}"`,
				`phase_selected() {`,
				`if phase_selected runtime; then`,
			}
			if image == "ubuntu-slim" {
				required = append(required, `all | bootstrap | platform | toolchain | runtime)`)
			} else {
				required = append(
					required,
					`all | bootstrap | platform | node | toolchain | runtime)`,
					`install-nvm.sh | install-nodejs.sh)`,
					`echo node`,
				)
			}
			for _, required := range required {
				if !strings.Contains(script, required) {
					t.Fatalf("phase-aware setup is missing %q", required)
				}
			}
		})
	}
}

func TestUbuntu2604DockerfilePreinstallsAptCommonInCacheableChunks(t *testing.T) {
	dockerfileBytes, err := os.ReadFile(filepath.Join(
		repositoryRoot(t),
		"templates",
		"github-runner-ubuntu-26.04",
		"Dockerfile",
	))
	if err != nil {
		t.Fatal(err)
	}
	dockerfile := string(dockerfileBytes)
	bootstrap := strings.Index(
		dockerfile,
		"RUN TEMPLATE_FLAVOR=versioned RUNNER_TEMPLATE_PHASE=bootstrap ",
	)
	platform := strings.Index(
		dockerfile,
		"TEMPLATE_FLAVOR=versioned RUNNER_TEMPLATE_PHASE=platform ",
	)
	if bootstrap < 0 || platform < 0 || bootstrap >= platform {
		t.Fatalf("Ubuntu 26.04 setup phases are not ordered: bootstrap=%d platform=%d", bootstrap, platform)
	}

	previous := bootstrap
	for _, packageRange := range []string{
		"0:7",
		"7:8",
		"8:14",
		"14:15",
		"15:18",
		"18:21",
		"21:22",
		"22:23",
		"23:24",
		"24:27",
		"27:28",
		"28:30",
		"30:32",
		"32:33",
		"33:45",
		"45:56",
		"56:57",
		"57:",
	} {
		chunk := "[.apt.common_packages[], .apt.cmd_packages[]] | .[" +
			packageRange + "][]"
		index := strings.Index(dockerfile, chunk)
		if index < 0 {
			t.Fatalf("Ubuntu 26.04 Dockerfile must preinstall apt package chunk %s", packageRange)
		}
		if index <= previous || index >= platform {
			t.Fatalf(
				"Ubuntu 26.04 apt package chunk %s must be cacheable between bootstrap and platform: index=%d previous=%d platform=%d",
				packageRange,
				index,
				previous,
				platform,
			)
		}
		previous = index
	}
	if count := strings.Count(
		dockerfile,
		`packages="$(jq -r '[.apt.common_packages[], .apt.cmd_packages[]]`,
	); count != 18 {
		t.Fatalf("Ubuntu 26.04 must expose exactly 18 cacheable apt package chunks; got %d", count)
	}

	if !strings.Contains(
		dockerfile,
		`map(if . == "netcat" then "netcat-openbsd" else . end)`,
	) {
		t.Fatal("Ubuntu 26.04 apt preinstall must resolve the virtual netcat package to netcat-openbsd")
	}
	restoreVirtualPackage := strings.Index(
		dockerfile,
		`map(if . == "netcat-openbsd" then "netcat" else . end)`,
	)
	patchUpstreamInstaller := strings.Index(
		dockerfile,
		`${package/netcat/netcat-openbsd}`,
	)
	if restoreVirtualPackage <= previous || restoreVirtualPackage >= platform {
		t.Fatalf(
			"Ubuntu 26.04 must restore netcat for the upstream command test after preinstalling every package: restore=%d last_chunk=%d platform=%d",
			restoreVirtualPackage,
			previous,
			platform,
		)
	}
	if patchUpstreamInstaller <= restoreVirtualPackage || patchUpstreamInstaller >= platform {
		t.Fatalf(
			"Ubuntu 26.04 must map netcat only for the upstream apt install before platform tests: patch=%d restore=%d platform=%d",
			patchUpstreamInstaller,
			restoreVirtualPackage,
			platform,
		)
	}
}

func TestVersionedTemplateBuildStagesPinnedUpstreamTests(t *testing.T) {
	root := repositoryRoot(t)
	for _, image := range []string{
		"ubuntu-slim",
		"ubuntu-22.04",
		"ubuntu-24.04",
		"ubuntu-26.04",
	} {
		t.Run(image, func(t *testing.T) {
			scriptPath := filepath.Join(
				root,
				"templates",
				"github-runner-"+image,
				"scripts",
				"setup-template.sh",
			)
			scriptBytes, err := os.ReadFile(scriptPath)
			if err != nil {
				t.Fatal(err)
			}
			script := string(scriptBytes)
			stage := `cp -a "$HELPER_SCRIPTS/../tests" /imagegeneration/tests`
			helperStage := `cp -a "$HELPER_SCRIPTS" /imagegeneration/helpers`
			validationStart := `for installer in \`
			cleanup := `find /imagegeneration -mindepth 1 -delete`
			stageIndex := strings.Index(script, stage)
			helperStageIndex := strings.Index(script, helperStage)
			validationIndex := strings.LastIndex(script, validationStart)
			cleanupIndex := strings.Index(script, cleanup)
			if stageIndex < 0 || helperStageIndex < 0 || validationIndex < 0 || cleanupIndex < 0 {
				t.Fatalf(
					"setup must stage and clean pinned upstream Pester tests and helpers: tests=%d helpers=%d validation=%d cleanup=%d",
					stageIndex,
					helperStageIndex,
					validationIndex,
					cleanupIndex,
				)
			}
			if stageIndex > validationIndex || helperStageIndex > validationIndex || cleanupIndex < validationIndex {
				t.Fatalf(
					"upstream tests and helpers must exist for installer validation and be removed afterward: tests=%d helpers=%d validation=%d cleanup=%d",
					stageIndex,
					helperStageIndex,
					validationIndex,
					cleanupIndex,
				)
			}
		})
	}
}

func TestDiskBoundedTemplatesSkipOnlyCMakeDependentNinjaAssertions(t *testing.T) {
	root := repositoryRoot(t)
	for _, image := range []string{
		"ubuntu-22.04",
		"ubuntu-24.04",
		"ubuntu-26.04",
	} {
		t.Run(image, func(t *testing.T) {
			scriptPath := filepath.Join(
				root,
				"templates",
				"github-runner-"+image,
				"scripts",
				"setup-template.sh",
			)
			scriptBytes, err := os.ReadFile(scriptPath)
			if err != nil {
				t.Fatal(err)
			}
			script := string(scriptBytes)
			for _, assertion := range []string{
				`It "Make a simple ninja project" -Skip {`,
				`It "build.ninja file should exist" -Skip {`,
				`It "Ninja" {`,
			} {
				if !strings.Contains(script, assertion) {
					t.Fatalf("setup must pin the disk-bounded Ninja test adjustment %q", assertion)
				}
			}
			if strings.Index(script, `It "Ninja" {`) > strings.LastIndex(script, `install-ninja.sh`) {
				t.Fatal("pinned Ninja test adjustment must be staged before the installer runs")
			}
		})
	}
}

func TestPublicTemplatesUsePinnedMicrosoftAzCopyWithoutActionPrewarm(t *testing.T) {
	root := repositoryRoot(t)
	for _, image := range []string{
		"ubuntu-slim",
		"ubuntu-22.04",
		"ubuntu-24.04",
		"ubuntu-26.04",
	} {
		t.Run(image, func(t *testing.T) {
			directory := filepath.Join(root, "templates", "github-runner-"+image)
			dockerfileBytes, err := os.ReadFile(filepath.Join(directory, "Dockerfile"))
			if err != nil {
				t.Fatal(err)
			}
			dockerfile := string(dockerfileBytes)
			for _, pinned := range []string{
				"ARG AZCOPY_VERSION=10.32.6",
				"ARG AZCOPY_DEB_SHA256=1a5078a8260ba7524a4400c602519d36905e592cd87a1db91a30af0da528fc86",
			} {
				if !strings.Contains(dockerfile, pinned) {
					t.Fatalf("Dockerfile must pin the Microsoft AzCopy package with %q", pinned)
				}
			}

			scriptBytes, err := os.ReadFile(filepath.Join(directory, "scripts", "setup-template.sh"))
			if err != nil {
				t.Fatal(err)
			}
			script := string(scriptBytes)
			for _, required := range []string{
				"install_azcopy_from_microsoft_package",
				"https://packages.microsoft.com/ubuntu/24.04/prod/pool/main/a/azcopy/",
				"/etc/apt/preferences.d/qiniu-azcopy",
				"Pin: version ${AZCOPY_VERSION}",
				"Pin-Priority: 1001",
				`test "$(azcopy --version)" = "azcopy version $AZCOPY_VERSION"`,
			} {
				if !strings.Contains(script, required) {
					t.Fatalf("setup must use the pinned official AzCopy package with %q", required)
				}
			}
			if strings.Contains(script, "install-actions-cache.sh") {
				t.Fatal("public templates must not prewarm the nonessential GitHub action archive cache")
			}
			if image != "ubuntu-slim" {
				invokeIndex := strings.LastIndex(script, `invoke-tests.sh" Tools azcopy`)
				pesterIndex := strings.LastIndex(script, "install_pester_for_upstream_tests")
				if invokeIndex < 0 {
					t.Fatal("versioned templates must retain the pinned upstream AzCopy test")
				}
				if pesterIndex < 0 || invokeIndex < pesterIndex {
					t.Fatalf("AzCopy Pester must run after pinned Pester is installed: pester=%d invoke=%d", pesterIndex, invokeIndex)
				}
			}
		})
	}
}

func TestVersionedTemplatesInstallPinnedPesterPackage(t *testing.T) {
	root := repositoryRoot(t)
	for _, image := range []string{
		"ubuntu-22.04",
		"ubuntu-24.04",
		"ubuntu-26.04",
	} {
		t.Run(image, func(t *testing.T) {
			templateRoot := filepath.Join(
				root,
				"templates",
				"github-runner-"+image,
			)
			dockerfileBytes, err := os.ReadFile(filepath.Join(templateRoot, "Dockerfile"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(dockerfileBytes), "ARG PESTER_NUPKG_SHA256=5a0fd80b361600bf4bbd4c307c1fd01b17f11668bab19e657add41b00ad22ab9") {
				t.Fatal("Dockerfile must pin the Pester 5.9.0 package checksum")
			}

			scriptBytes, err := os.ReadFile(filepath.Join(templateRoot, "scripts", "setup-template.sh"))
			if err != nil {
				t.Fatal(err)
			}
			script := string(scriptBytes)
			functionStart := strings.Index(script, "install_pester_for_upstream_tests() {")
			if functionStart < 0 {
				t.Fatal("setup must define the Pester installation helper")
			}
			functionEnd := strings.Index(script[functionStart:], "\n}\n\nstop_validated_service()")
			if functionEnd < 0 {
				t.Fatal("setup must define the Pester installation helper")
			}
			functionBody := script[functionStart : functionStart+functionEnd]
			for _, required := range []string{
				`"https://www.powershellgallery.com/api/v2/package/Pester/${pester_version}"`,
				`"$PESTER_NUPKG_SHA256"`,
				`/usr/local/share/powershell/Modules/Pester/${pester_version}`,
				`unzip -q "$package" -d "$module_dir"`,
				"Import-Module Pester -RequiredVersion $env:PESTER_VERSION -Force",
			} {
				if !strings.Contains(functionBody, required) {
					t.Fatalf("Pester installation must use the pinned package with %q", required)
				}
			}
			if strings.Contains(functionBody, "Register-PSRepository") || strings.Contains(functionBody, "Install-Module") {
				t.Fatal("Pester installation must not depend on PowerShellGet repository registration")
			}
		})
	}
}

func TestPublicTemplatesInstallPinnedAzureCLIPackage(t *testing.T) {
	root := repositoryRoot(t)
	for _, image := range []string{
		"ubuntu-slim",
		"ubuntu-22.04",
		"ubuntu-24.04",
		"ubuntu-26.04",
	} {
		t.Run(image, func(t *testing.T) {
			templateRoot := filepath.Join(root, "templates", "github-runner-"+image)
			dockerfileBytes, err := os.ReadFile(filepath.Join(templateRoot, "Dockerfile"))
			if err != nil {
				t.Fatal(err)
			}
			dockerfile := string(dockerfileBytes)
			for _, required := range []string{
				"ARG AZURE_CLI_VERSION=2.88.0",
				"ARG AZURE_CLI_JAMMY_DEB_SHA256=4decc8359ba3542becf2686474e3d068c2fc0b9bb9ec64cbcc8f5aa0cb7c2b61",
				"ARG AZURE_CLI_NOBLE_DEB_SHA256=dedb0d666ad557edce8548e025c36fa3a28ac00df4f3d1e889e1246d2a261c36",
			} {
				if !strings.Contains(dockerfile, required) {
					t.Fatalf("Dockerfile must pin the Azure CLI package with %q", required)
				}
			}

			scriptBytes, err := os.ReadFile(filepath.Join(templateRoot, "scripts", "setup-template.sh"))
			if err != nil {
				t.Fatal(err)
			}
			script := string(scriptBytes)
			for _, required := range []string{
				`: "${AZURE_CLI_VERSION:?AZURE_CLI_VERSION is required}"`,
				`install_azure_cli_from_microsoft_package() {`,
				`azure-cli_${AZURE_CLI_VERSION}-1~${suite}_amd64.deb`,
				`expected_sha256="$AZURE_CLI_JAMMY_DEB_SHA256"`,
				`expected_sha256="$AZURE_CLI_NOBLE_DEB_SHA256"`,
				`install-azure-cli.sh)`,
				`install_azure_cli_from_microsoft_package`,
				`test "$(az version --query '"azure-cli"' --output tsv)" = "$AZURE_CLI_VERSION"`,
				`run_upstream_tests_if_available "CLI.Tools" "Azure CLI"`,
			} {
				if !strings.Contains(script, required) {
					t.Fatalf("setup must install the checked Azure CLI package with %q", required)
				}
			}
			if strings.Contains(script, `upstream Azure CLI installer unavailable`) {
				t.Fatal("setup must not install a floating Azure CLI before falling back to the pinned package")
			}
		})
	}
}

func TestPublicTemplatesPinFloatingCompatibilityTools(t *testing.T) {
	root := repositoryRoot(t)
	testCases := []struct {
		image        string
		awsCLI       string
		awsCLISHA    string
		session      string
		sessionSHA   string
		sam          string
		samSHA       string
		githubCLI    string
		githubCLISHA string
		yq           string
		yqSHA        string
	}{
		{
			image:        "ubuntu-slim",
			awsCLI:       "2.33.2",
			awsCLISHA:    "03a62592085c43974bbb795c74df0da0345041cbceb97a22b29b04e5b5176a10",
			session:      "1.2.764.0",
			sessionSHA:   "beed4c95c42afd29756d9ecea59c3fcbf937b2c35b9ef84d12b93ac6e74726ba",
			sam:          "1.151.0",
			samSHA:       "679c54a86512e0f73616856d460b81b438fd5b9b004de5dcb624892dddfbb584",
			githubCLI:    "2.85.0",
			githubCLISHA: "4ed5ff89ef53da00af9d93ac1beaa5665694f18cee8cc8d644a201541d43148c",
			yq:           "4.50.1",
			yqSHA:        "c7a1278e6bbc4924f41b56db838086c39d13ee25dcb22089e7fbf16ac901f0d4",
		},
		{
			image:        "ubuntu-22.04",
			awsCLI:       "2.35.22",
			awsCLISHA:    "edd9ba798acb3ef6131e5bf902d81999ebc8ad72fbec8771d690f3ed0c059110",
			session:      "1.2.835.0",
			sessionSHA:   "7c6dcad12518571cc7959a713e6a8ae1bdf6ed66fd9bee37dc189e39ca58ae03",
			sam:          "1.163.0",
			samSHA:       "3f12863f45da82bc2ad1fb837444cca57450bd07fef73449b917362ef2f5ab70",
			githubCLI:    "2.96.0",
			githubCLISHA: "11a731f4e0ca8c3db96ef6d2cc404dcab3d78247ce0e07c53e07117e7627d6a1",
			yq:           "4.53.3",
			yqSHA:        "fa52a4e758c63d38299163fbdd1edfb4c4963247918bf9c1c5d31d84789eded4",
		},
		{
			image:        "ubuntu-24.04",
			awsCLI:       "2.35.22",
			awsCLISHA:    "edd9ba798acb3ef6131e5bf902d81999ebc8ad72fbec8771d690f3ed0c059110",
			session:      "1.2.835.0",
			sessionSHA:   "7c6dcad12518571cc7959a713e6a8ae1bdf6ed66fd9bee37dc189e39ca58ae03",
			sam:          "1.163.0",
			samSHA:       "3f12863f45da82bc2ad1fb837444cca57450bd07fef73449b917362ef2f5ab70",
			githubCLI:    "2.96.0",
			githubCLISHA: "11a731f4e0ca8c3db96ef6d2cc404dcab3d78247ce0e07c53e07117e7627d6a1",
			yq:           "4.53.3",
			yqSHA:        "fa52a4e758c63d38299163fbdd1edfb4c4963247918bf9c1c5d31d84789eded4",
		},
		{
			image:        "ubuntu-26.04",
			awsCLI:       "2.36.3",
			awsCLISHA:    "512949c6175e7736e77761661d2e73809152c35914abd032ca00d4758e4041de",
			session:      "1.2.835.0",
			sessionSHA:   "7c6dcad12518571cc7959a713e6a8ae1bdf6ed66fd9bee37dc189e39ca58ae03",
			sam:          "1.163.0",
			samSHA:       "3f12863f45da82bc2ad1fb837444cca57450bd07fef73449b917362ef2f5ab70",
			githubCLI:    "2.96.0",
			githubCLISHA: "11a731f4e0ca8c3db96ef6d2cc404dcab3d78247ce0e07c53e07117e7627d6a1",
			yq:           "4.53.3",
			yqSHA:        "fa52a4e758c63d38299163fbdd1edfb4c4963247918bf9c1c5d31d84789eded4",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.image, func(t *testing.T) {
			templateRoot := filepath.Join(root, "templates", "github-runner-"+tc.image)
			dockerfileBytes, err := os.ReadFile(filepath.Join(templateRoot, "Dockerfile"))
			if err != nil {
				t.Fatal(err)
			}
			dockerfile := string(dockerfileBytes)
			for _, required := range []string{
				"ARG AWS_CLI_VERSION=" + tc.awsCLI,
				"ARG AWS_CLI_ARCHIVE_SHA256=" + tc.awsCLISHA,
				"ARG AWS_SESSION_MANAGER_PLUGIN_VERSION=" + tc.session,
				"ARG AWS_SESSION_MANAGER_PLUGIN_DEB_SHA256=" + tc.sessionSHA,
				"ARG AWS_SAM_CLI_VERSION=" + tc.sam,
				"ARG AWS_SAM_CLI_ARCHIVE_SHA256=" + tc.samSHA,
				"ARG GITHUB_CLI_VERSION=" + tc.githubCLI,
				"ARG GITHUB_CLI_DEB_SHA256=" + tc.githubCLISHA,
				"ARG YQ_VERSION=" + tc.yq,
				"ARG YQ_BINARY_SHA256=" + tc.yqSHA,
				"ARG ZSTD_VERSION=1.5.7",
				"ARG ZSTD_ARCHIVE_SHA256=eb33e51f49a15e023950cd7825ca74a4a2b43db8354825ac24fc1b7ee09e6fa3",
			} {
				if !strings.Contains(dockerfile, required) {
					t.Fatalf("Dockerfile must pin the compatibility-report tool with %q", required)
				}
			}
			if tc.image != "ubuntu-slim" {
				for _, required := range []string{
					"ARG NINJA_VERSION=1.13.2",
					"ARG NINJA_ARCHIVE_SHA256=5749cbc4e668273514150a80e387a957f933c6ed3f5f11e03fb30955e2bbead6",
				} {
					if !strings.Contains(dockerfile, required) {
						t.Fatalf("versioned Dockerfile must pin Ninja with %q", required)
					}
				}
			}

			scriptBytes, err := os.ReadFile(filepath.Join(templateRoot, "scripts", "setup-template.sh"))
			if err != nil {
				t.Fatal(err)
			}
			script := string(scriptBytes)
			for _, required := range []string{
				`/usr/bin/curl --http1.1`,
				`install_aws_tools_from_checked_archives() {`,
				`awscli-exe-linux-x86_64-${AWS_CLI_VERSION}.zip`,
				`plugin/${AWS_SESSION_MANAGER_PLUGIN_VERSION}/ubuntu_64bit/session-manager-plugin.deb`,
				`aws-sam-cli/releases/download/v${AWS_SAM_CLI_VERSION}/aws-sam-cli-linux-x86_64.zip`,
				`install_github_cli_from_checked_package() {`,
				`cli/cli/releases/download/v${GITHUB_CLI_VERSION}/gh_${GITHUB_CLI_VERSION}_linux_amd64.deb`,
				`install_yq_from_checked_binary() {`,
				`mikefarah/yq/releases/download/v${YQ_VERSION}/yq_linux_amd64`,
				`install_zstd_from_checked_archive() {`,
				`facebook/zstd/releases/download/v${ZSTD_VERSION}/zstd-${ZSTD_VERSION}.tar.gz`,
				`install_ninja_from_checked_archive() {`,
				`ninja-build/ninja/releases/download/v${NINJA_VERSION}/ninja-linux.zip`,
				`install-container-tools.sh)`,
				`RUNNER_TEMPLATE_DIRECT_GITHUB_ASSETS=1 bash "$installer_path"`,
			} {
				if !strings.Contains(script, required) {
					t.Fatalf("setup must bypass floating release resolution with %q", required)
				}
			}
		})
	}
}

func TestPublicTemplateCheckedDownloadsResumeAcrossTransientFailures(t *testing.T) {
	root := repositoryRoot(t)
	downloadCheckedByImage := make(map[string]string)
	for _, image := range []string{
		"ubuntu-slim",
		"ubuntu-22.04",
		"ubuntu-24.04",
		"ubuntu-26.04",
	} {
		t.Run(image, func(t *testing.T) {
			scriptBytes, err := os.ReadFile(filepath.Join(
				root,
				"templates",
				"github-runner-"+image,
				"scripts",
				"setup-template.sh",
			))
			if err != nil {
				t.Fatal(err)
			}
			script := string(scriptBytes)
			functionStart := strings.Index(script, "download_checked() {")
			functionEnd := strings.Index(script, "\nrun_upstream_tests_if_available() {")
			if functionStart < 0 || functionEnd < functionStart {
				t.Fatal("setup must define download_checked before the upstream test helper")
			}
			downloadChecked := script[functionStart:functionEnd]
			downloadCheckedByImage[image] = downloadChecked
			for _, required := range []string{
				`RUNNER_TEMPLATE_DOWNLOAD_ATTEMPTS:-20`,
				`RUNNER_TEMPLATE_DOWNLOAD_RETRY_DELAY:-2`,
				`--continue-at -`,
				`sha256sum --check -`,
			} {
				if !strings.Contains(downloadChecked, required) {
					t.Fatalf("download_checked must preserve verified partial downloads with %q", required)
				}
			}
			if strings.Contains(downloadChecked, `--retry 5`) {
				t.Fatal("download_checked must use an explicit resume loop instead of curl retries that discard partial output")
			}
		})
	}

	for image, downloadChecked := range downloadCheckedByImage {
		t.Run(image+"-bounded-backoff", func(t *testing.T) {
			failureServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "transient failure", http.StatusServiceUnavailable)
			}))
			defer failureServer.Close()

			fixture := t.TempDir()
			fakeBin := filepath.Join(fixture, "bin")
			if err := os.MkdirAll(fakeBin, 0o755); err != nil {
				t.Fatal(err)
			}
			delayLog := filepath.Join(fixture, "delays")
			writeExecutable(t, filepath.Join(fakeBin, "sleep"), "#!/usr/bin/env bash\nset -euo pipefail\nprintf '%s\\n' \"$1\" >> \"$DELAY_LOG\"\n")
			testScript := filepath.Join(fixture, "download-checked.sh")
			writeExecutable(t, testScript, "#!/usr/bin/env bash\nset -euo pipefail\n"+
				downloadChecked+"\ndownload_checked \"$1\" \"$2\" \"$3\"\n")
			output, err := runCommand(
				t,
				"bash",
				[]string{testScript, failureServer.URL, filepath.Join(fixture, "artifact"), strings.Repeat("0", 64)},
				"PATH="+fakeBin+":"+os.Getenv("PATH"),
				"DELAY_LOG="+delayLog,
				"RUNNER_TEMPLATE_DOWNLOAD_ATTEMPTS=5",
				"RUNNER_TEMPLATE_DOWNLOAD_RETRY_DELAY=1",
				"RUNNER_TEMPLATE_DOWNLOAD_RETRY_MAX_DELAY=3",
			)
			if err == nil {
				t.Fatalf("download unexpectedly succeeded:\n%s", output)
			}
			delays, readErr := os.ReadFile(delayLog)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if got, want := strings.Fields(string(delays)), []string{"1", "2", "3", "3"}; !slices.Equal(got, want) {
				t.Fatalf("retry delays = %v, want bounded exponential backoff %v", got, want)
			}
		})
	}

	payload := []byte("checksum-pinned resumable template download")
	partialLength := 9
	var requestCount atomic.Int32
	var resumedRange atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requestCount.Add(1) == 1 {
			connection, buffer, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			_, _ = fmt.Fprintf(
				buffer,
				"HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
				len(payload),
				payload[:partialLength],
			)
			_ = buffer.Flush()
			_ = connection.Close()
			return
		}
		resumedRange.Store(r.Header.Get("Range"))
		if r.Header.Get("Range") != fmt.Sprintf("bytes=%d-", partialLength) {
			http.Error(w, "unexpected range", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set(
			"Content-Range",
			fmt.Sprintf("bytes %d-%d/%d", partialLength, len(payload)-1, len(payload)),
		)
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[partialLength:])
	}))
	defer server.Close()

	testScript := filepath.Join(t.TempDir(), "download-checked.sh")
	writeExecutable(t, testScript, "#!/usr/bin/env bash\nset -euo pipefail\n"+
		downloadCheckedByImage["ubuntu-slim"]+"\ndownload_checked \"$1\" \"$2\" \"$3\"\n")
	destination := filepath.Join(t.TempDir(), "artifact")
	expectedSHA := fmt.Sprintf("%x", sha256.Sum256(payload))
	output, err := runCommand(
		t,
		"bash",
		[]string{testScript, server.URL, destination, expectedSHA},
		"RUNNER_TEMPLATE_DOWNLOAD_ATTEMPTS=3",
		"RUNNER_TEMPLATE_DOWNLOAD_RETRY_DELAY=0",
	)
	if err != nil {
		t.Fatalf("resumable download failed: %v\n%s", err, output)
	}
	artifact, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(artifact) != string(payload) {
		t.Fatalf("resumed artifact = %q, want %q", artifact, payload)
	}
	if requestCount.Load() != 2 {
		t.Fatalf("download request count = %d, want 2", requestCount.Load())
	}
	if got, _ := resumedRange.Load().(string); got != fmt.Sprintf("bytes=%d-", partialLength) {
		t.Fatalf("resumed Range = %q", got)
	}
}

func TestPublicTemplatesRetryPythonInstallersAfterTransientNetworkFailures(t *testing.T) {
	root := repositoryRoot(t)
	var retryInstaller string
	for _, image := range []string{
		"ubuntu-slim",
		"ubuntu-22.04",
		"ubuntu-24.04",
		"ubuntu-26.04",
	} {
		t.Run(image, func(t *testing.T) {
			scriptBytes, err := os.ReadFile(filepath.Join(
				root,
				"templates",
				"github-runner-"+image,
				"scripts",
				"setup-template.sh",
			))
			if err != nil {
				t.Fatal(err)
			}
			script := string(scriptBytes)
			functionStart := strings.Index(script, "run_retryable_upstream_installer() {")
			functionEnd := strings.Index(script, "\nrun_upstream_installer() {")
			if functionStart < 0 || functionEnd < functionStart {
				t.Fatal("setup must define a retryable upstream installer helper")
			}
			retryInstaller = script[functionStart:functionEnd]
			for _, required := range []string{
				`RUNNER_TEMPLATE_UPSTREAM_INSTALL_ATTEMPTS:-3`,
				`RUNNER_TEMPLATE_UPSTREAM_INSTALL_RETRY_DELAY:-2`,
				`PIP_DEFAULT_TIMEOUT="${PIP_DEFAULT_TIMEOUT:-120}"`,
				`PIP_RETRIES="${PIP_RETRIES:-10}"`,
				`install-python.sh | install-pipx-packages.sh)`,
			} {
				if !strings.Contains(script, required) {
					t.Fatalf("setup must retry Python installers with %q", required)
				}
			}
			if !strings.Contains(script, `run_upstream_installer "$upstream_build/install-pipx-packages.sh"`) {
				t.Fatal("standalone pipx package installation must use the retryable installer wrapper")
			}
		})
	}

	tempDir := t.TempDir()
	attemptFile := filepath.Join(tempDir, "attempts")
	installer := filepath.Join(tempDir, "install-python.sh")
	writeExecutable(t, installer, `#!/usr/bin/env bash
set -euo pipefail
attempt=0
if [ -f "$ATTEMPT_FILE" ]; then
  attempt="$(cat "$ATTEMPT_FILE")"
fi
attempt=$((attempt + 1))
printf '%s\n' "$attempt" >"$ATTEMPT_FILE"
test "$PIP_DEFAULT_TIMEOUT" = 120
test "$PIP_RETRIES" = 10
test "$attempt" -ge 2
`)
	testScript := filepath.Join(tempDir, "retry-installer.sh")
	writeExecutable(t, testScript, "#!/usr/bin/env bash\nset -euo pipefail\n"+
		retryInstaller+"\nrun_retryable_upstream_installer \"$1\"\n")
	output, err := runCommand(
		t,
		"bash",
		[]string{testScript, installer},
		"ATTEMPT_FILE="+attemptFile,
		"RUNNER_TEMPLATE_UPSTREAM_INSTALL_ATTEMPTS=3",
		"RUNNER_TEMPLATE_UPSTREAM_INSTALL_RETRY_DELAY=0",
	)
	if err != nil {
		t.Fatalf("retryable installer failed: %v\n%s", err, output)
	}
	attempts, err := os.ReadFile(attemptFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(attempts)); got != "2" {
		t.Fatalf("installer attempts = %s, want 2", got)
	}
}

func TestPublicTemplatesInstallPinnedAzureDevOpsExtensionAfterAzureCLI(t *testing.T) {
	root := repositoryRoot(t)
	for _, image := range []string{
		"ubuntu-slim",
		"ubuntu-22.04",
		"ubuntu-24.04",
		"ubuntu-26.04",
	} {
		t.Run(image, func(t *testing.T) {
			directory := filepath.Join(root, "templates", "github-runner-"+image)
			dockerfileBytes, err := os.ReadFile(filepath.Join(directory, "Dockerfile"))
			if err != nil {
				t.Fatal(err)
			}
			dockerfile := string(dockerfileBytes)
			for _, pinned := range []string{
				"ARG AZURE_DEVOPS_EXTENSION_VERSION=1.0.6",
				"ARG AZURE_DEVOPS_EXTENSION_SHA256=fa779e1fd6e6e1b726c3656b6a1968537c208041c6af54d2a7476772d896b34b",
			} {
				if !strings.Contains(dockerfile, pinned) {
					t.Fatalf("Dockerfile must pin the Azure DevOps extension with %q", pinned)
				}
			}

			scriptBytes, err := os.ReadFile(filepath.Join(directory, "scripts", "setup-template.sh"))
			if err != nil {
				t.Fatal(err)
			}
			script := string(scriptBytes)
			for _, required := range []string{
				"install_azure_devops_extension",
				"https://azcliprod.blob.core.windows.net/cli-extensions/azure_devops-",
				"export AZURE_EXTENSION_DIR=/opt/az/azcliextensions",
				`set_etc_environment_variable "AZURE_EXTENSION_DIR" "$AZURE_EXTENSION_DIR"`,
				`test "$(az extension show --name azure-devops --query version -o tsv)" = "$AZURE_DEVOPS_EXTENSION_VERSION"`,
			} {
				if !strings.Contains(script, required) {
					t.Fatalf("setup must install the pinned Azure DevOps extension with %q", required)
				}
			}
			if strings.Contains(script, "install-azure-devops-cli.sh") {
				t.Fatal("setup must not use the upstream Python-client Azure DevOps extension download")
			}
			azureCLIIndex := strings.LastIndex(script, "install-azure-cli.sh")
			extensionIndex := strings.LastIndex(script, "install_azure_devops_extension")
			if azureCLIIndex < 0 || extensionIndex < azureCLIIndex {
				t.Fatalf("Azure DevOps extension must install after Azure CLI: cli=%d extension=%d", azureCLIIndex, extensionIndex)
			}
			if image != "ubuntu-slim" && !strings.Contains(script, `invoke-tests.sh" CLI.Tools "Azure DevOps CLI"`) {
				t.Fatal("versioned templates must retain the pinned upstream Azure DevOps CLI test")
			}
		})
	}
}

func TestUbuntu2604TemplateInstallsICUBeforePowerShell(t *testing.T) {
	root := repositoryRoot(t)
	scriptPath := filepath.Join(
		root,
		"templates",
		"github-runner-ubuntu-26.04",
		"scripts",
		"setup-template.sh",
	)
	scriptBytes, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	script := string(scriptBytes)
	icuIndex := strings.Index(script, "libicu78")
	powershellIndex := strings.LastIndex(script, "install_pinned_powershell_package")
	if icuIndex < 0 || powershellIndex < 0 {
		t.Fatalf(
			"Ubuntu 26.04 setup must install ICU before PowerShell: icu=%d powershell=%d",
			icuIndex,
			powershellIndex,
		)
	}
	if icuIndex > powershellIndex {
		t.Fatalf(
			"Ubuntu 26.04 ICU runtime must exist before PowerShell starts: icu=%d powershell=%d",
			icuIndex,
			powershellIndex,
		)
	}
}

func TestUbuntu2604TemplatePinsMicrosoftPowerShellPackage(t *testing.T) {
	root := repositoryRoot(t)
	dockerfileBytes, err := os.ReadFile(filepath.Join(
		root,
		"templates",
		"github-runner-ubuntu-26.04",
		"Dockerfile",
	))
	if err != nil {
		t.Fatal(err)
	}
	dockerfile := string(dockerfileBytes)
	for _, expected := range []string{
		"ARG POWERSHELL_VERSION=7.6.4",
		"ARG POWERSHELL_DEB_SHA256=e5688e0569568d48051c49d3e93504cde47af709cdaaabd9a8892bc676b3bdf3",
	} {
		if !strings.Contains(dockerfile, expected) {
			t.Fatalf("Ubuntu 26.04 Dockerfile missing pinned PowerShell input %q", expected)
		}
	}

	scriptBytes, err := os.ReadFile(filepath.Join(
		root,
		"templates",
		"github-runner-ubuntu-26.04",
		"scripts",
		"setup-template.sh",
	))
	if err != nil {
		t.Fatal(err)
	}
	script := string(scriptBytes)
	for _, expected := range []string{
		`: "${POWERSHELL_VERSION:?POWERSHELL_VERSION is required}"`,
		`: "${POWERSHELL_DEB_SHA256:?POWERSHELL_DEB_SHA256 is required}"`,
		`"https://packages.microsoft.com/ubuntu/24.04/prod/pool/main/p/powershell/powershell_${POWERSHELL_VERSION}-1.deb_amd64.deb"`,
		`download_checked`,
		`install_pinned_powershell_package`,
		`test "$(pwsh --version)" = "PowerShell $POWERSHELL_VERSION"`,
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("Ubuntu 26.04 setup missing pinned PowerShell behavior %q", expected)
		}
	}
	if strings.Contains(script, `bash "$upstream_build/install-powershell.sh"`) {
		t.Fatal("Ubuntu 26.04 must not query the GitHub Releases API through the generic upstream installer")
	}
	if strings.Contains(script, "github.com/PowerShell/PowerShell/releases") {
		t.Fatal("Ubuntu 26.04 must use Microsoft's package pool instead of a GitHub release asset")
	}
}

func TestVersionedTemplatesInstallNetcatProviderBeforeAptCommon(t *testing.T) {
	root := repositoryRoot(t)
	for _, image := range []string{"ubuntu-24.04", "ubuntu-26.04"} {
		t.Run(image, func(t *testing.T) {
			scriptPath := filepath.Join(
				root,
				"templates",
				"github-runner-"+image,
				"scripts",
				"setup-template.sh",
			)
			scriptBytes, err := os.ReadFile(scriptPath)
			if err != nil {
				t.Fatal(err)
			}
			script := string(scriptBytes)
			netcatIndex := strings.Index(script, "netcat-openbsd")
			aptCommonIndex := strings.LastIndex(script, "install-apt-common.sh")
			if netcatIndex < 0 || aptCommonIndex < 0 {
				t.Fatalf(
					"setup must install a concrete netcat provider before apt-common: netcat=%d apt-common=%d",
					netcatIndex,
					aptCommonIndex,
				)
			}
			if netcatIndex > aptCommonIndex {
				t.Fatalf(
					"netcat provider must exist before apt-common validation: netcat=%d apt-common=%d",
					netcatIndex,
					aptCommonIndex,
				)
			}
		})
	}
}

func TestCompatibilityGeneratorVerifiesConcreteNetcatProvider(t *testing.T) {
	root := repositoryRoot(t)
	const verification = `dpkg-query -W -f='${Status}' 'netcat-openbsd' | grep -qx 'install ok installed' && command -v netcat >/dev/null`

	generatorBytes, err := os.ReadFile(filepath.Join(root, "scripts", "generate-runner-image-compatibility.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(generatorBytes), verification) {
		t.Fatal("compatibility generator must verify the concrete netcat provider and command")
	}

	manifestBytes, err := os.ReadFile(filepath.Join(root, "templates", "runner-images-compatibility.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Images map[string]struct {
			Entries []struct {
				UpstreamName string `json:"upstream_name"`
				Verification string `json:"verification"`
			} `json:"entries"`
		} `json:"images"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, image := range []string{"ubuntu-24.04", "ubuntu-26.04"} {
		found := false
		for _, entry := range manifest.Images[image].Entries {
			if entry.UpstreamName != "netcat" {
				continue
			}
			found = true
			if entry.Verification != verification {
				t.Fatalf("%s netcat verification = %q", image, entry.Verification)
			}
		}
		if !found {
			t.Fatalf("%s has no netcat compatibility entry", image)
		}
	}
}

func TestCompatibilityGeneratorUsesCanonicalSystemdExecutable(t *testing.T) {
	fixture := t.TempDir()
	reportDir := filepath.Join(fixture, "reports")
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		t.Fatal(err)
	}
	report := []byte(`# Fixture
- OS Version: 26.04
- Systemd version: 259.5-0ubuntu3
`)
	reports := map[string]string{
		"ubuntu-slim":  "ubuntu-slim-Readme.md",
		"ubuntu-22.04": "Ubuntu2204-Readme.md",
		"ubuntu-24.04": "Ubuntu2404-Readme.md",
		"ubuntu-26.04": "Ubuntu2604-Readme.md",
	}
	reportChecksums := make(map[string]string, len(reports))
	for image, name := range reports {
		if err := os.WriteFile(filepath.Join(reportDir, name), report, 0o644); err != nil {
			t.Fatal(err)
		}
		reportChecksums[image] = fmt.Sprintf("%x", sha256.Sum256(report))
	}
	lockBytes, err := json.Marshal(map[string]any{
		"repository":    "actions/runner-images",
		"commit":        "fixture",
		"reports":       reports,
		"report_sha256": reportChecksums,
	})
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(fixture, "lock.json")
	if err := os.WriteFile(lockPath, lockBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(fixture, "compatibility.json")
	output, err := runCommand(
		t,
		"node",
		[]string{"scripts/generate-runner-image-compatibility.mjs"},
		"RUNNER_IMAGES_REPORT_DIR="+reportDir,
		"RUNNER_IMAGES_LOCK="+lockPath,
		"RUNNER_IMAGES_MANIFEST="+manifestPath,
	)
	if err != nil {
		t.Fatalf("generate compatibility manifest: %v\n%s", err, output)
	}
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Images map[string]struct {
			Entries []struct {
				UpstreamName string `json:"upstream_name"`
				Verification string `json:"verification"`
			} `json:"entries"`
		} `json:"images"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	const verification = "test -x /usr/lib/systemd/systemd && /usr/lib/systemd/systemd --version >/dev/null"
	for _, image := range []string{"ubuntu-slim", "ubuntu-22.04", "ubuntu-24.04", "ubuntu-26.04"} {
		found := false
		for _, entry := range manifest.Images[image].Entries {
			if entry.UpstreamName != "Systemd version" {
				continue
			}
			found = true
			if entry.Verification != verification {
				t.Fatalf("%s systemd verification = %q, want %q", image, entry.Verification, verification)
			}
		}
		if !found {
			t.Fatalf("%s has no Systemd version compatibility entry", image)
		}
	}
}

func TestVersionedTemplateBuildDefersOnlyPodmanNetworkingToRuntimeConformance(t *testing.T) {
	root := repositoryRoot(t)
	for _, image := range []string{
		"ubuntu-slim",
		"ubuntu-22.04",
		"ubuntu-24.04",
		"ubuntu-26.04",
	} {
		t.Run(image, func(t *testing.T) {
			scriptPath := filepath.Join(
				root,
				"templates",
				"github-runner-"+image,
				"scripts",
				"setup-template.sh",
			)
			scriptBytes, err := os.ReadFile(scriptPath)
			if err != nil {
				t.Fatal(err)
			}
			script := string(scriptBytes)
			for _, required := range []string{
				`podman_networking_test=/imagegeneration/tests/Tools.Tests.ps1`,
				`grep -Fxc '    It "podman networking" -TestCases "podman CNI plugins" {' "$podman_networking_test"`,
				`sed -i 's/    It "podman networking" -TestCases "podman CNI plugins" {/    It "podman networking" -Skip -TestCases "podman CNI plugins" {/' "$podman_networking_test"`,
				`grep -Fxc '    It "podman networking" -Skip -TestCases "podman CNI plugins" {' "$podman_networking_test"`,
				`grep -Fxc '    $testCases = @("podman", "buildah", "skopeo") | ForEach-Object { @{ContainerCommand = $_} }' "$podman_networking_test"`,
				`grep -Fxc '    It "<ContainerCommand>" -TestCases $testCases {' "$podman_networking_test"`,
				`grep -Fxc '        "$ContainerCommand -v" | Should -ReturnZeroExitCode' "$podman_networking_test"`,
			} {
				if strings.Count(script, required) != 1 {
					t.Fatalf("setup must contain exactly one %q guard/patch, got %d", required, strings.Count(script, required))
				}
			}
			if strings.Contains(script, `/opt/qiniu-runner-images/images/ubuntu/scripts/tests/Tools.Tests.ps1`) {
				t.Fatal("setup must not modify the pinned upstream source tree")
			}
		})
	}
}

func TestCompatibilityGeneratorRequiresPodmanNetworkLifecycle(t *testing.T) {
	root := repositoryRoot(t)
	const verification = `command -v podman >/dev/null || exit 1; podman_network="qiniu-conformance-$$-${RANDOM}"; cleanup_podman_network() { podman network rm "$podman_network" >/dev/null 2>&1 || true; }; trap cleanup_podman_network EXIT; podman network create -d bridge "$podman_network" >/dev/null || exit 1; podman network ls --format "{{.Name}}" | grep -Fx "$podman_network" >/dev/null || exit 1; podman network rm "$podman_network" >/dev/null || exit 1; trap - EXIT`

	generatorBytes, err := os.ReadFile(filepath.Join(root, "scripts", "generate-runner-image-compatibility.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(generatorBytes), verification) {
		t.Fatal("compatibility generator must emit the Podman network lifecycle assertion")
	}

	manifestBytes, err := os.ReadFile(filepath.Join(root, "templates", "runner-images-compatibility.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Images map[string]struct {
			Entries []struct {
				UpstreamName string `json:"upstream_name"`
				Status       string `json:"status"`
				Verification string `json:"verification"`
			} `json:"entries"`
		} `json:"images"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, image := range []string{"ubuntu-22.04", "ubuntu-24.04", "ubuntu-26.04"} {
		found := false
		for _, entry := range manifest.Images[image].Entries {
			if entry.UpstreamName != "Podman" {
				continue
			}
			found = true
			if entry.Status != "provided" || entry.Verification != verification {
				t.Fatalf("%s Podman assertion = status %q, verification %q", image, entry.Status, entry.Verification)
			}
		}
		if !found {
			t.Fatalf("%s has no Podman compatibility entry", image)
		}
	}

	conformanceBytes, err := os.ReadFile(filepath.Join(root, "scripts", "run-runner-image-conformance.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(conformanceBytes), `docker run --rm --privileged "$target"`) {
		t.Fatal("Docker conformance must execute the Podman lifecycle assertion with runtime namespace privileges")
	}
}

func TestCompatibilityManifestDocumentsSandboxDiskBoundary(t *testing.T) {
	root := repositoryRoot(t)
	manifestBytes, err := os.ReadFile(filepath.Join(root, "templates", "runner-images-compatibility.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Images map[string]struct {
			Entries []struct {
				UpstreamName string `json:"upstream_name"`
				Status       string `json:"status"`
				Reason       string `json:"reason"`
			} `json:"entries"`
		} `json:"images"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, image := range []string{"ubuntu-22.04", "ubuntu-24.04", "ubuntu-26.04"} {
		entries := map[string]struct {
			status string
			reason string
		}{}
		for _, entry := range manifest.Images[image].Entries {
			entries[entry.UpstreamName] = struct {
				status string
				reason string
			}{entry.Status, entry.Reason}
		}
		for _, provided := range []string{"PowerShell", "Pester", "apache2", "Buildah", "Podman", "Skopeo", "Ninja"} {
			entry, ok := entries[provided]
			if !ok || entry.status != "provided" {
				t.Fatalf("%s must provide %s, got %#v", image, provided, entry)
			}
		}
		dotnet, ok := entries[".NET Core SDK"]
		if !ok || dotnet.status != "excluded" || !strings.Contains(dotnet.reason, "22,222 MiB") {
			t.Fatalf("%s must explain the disk-bounded .NET exclusion, got %#v", image, dotnet)
		}
	}
}

func TestVersionedTemplateBuildMakesSystemctlShimVisibleToSudoAndCleansIt(t *testing.T) {
	root := repositoryRoot(t)
	for _, image := range []string{
		"ubuntu-slim",
		"ubuntu-22.04",
		"ubuntu-24.04",
		"ubuntu-26.04",
	} {
		t.Run(image, func(t *testing.T) {
			scriptPath := filepath.Join(
				root,
				"templates",
				"github-runner-"+image,
				"scripts",
				"setup-template.sh",
			)
			scriptBytes, err := os.ReadFile(scriptPath)
			if err != nil {
				t.Fatal(err)
			}
			script := string(scriptBytes)
			expose := `ln -s /tmp/qiniu-runner-build-tools/systemctl /usr/local/bin/systemctl`
			apache := `install-apache.sh`
			cleanup := `rm -f /usr/local/bin/systemctl`
			exposeIndex := strings.Index(script, expose)
			apacheIndex := strings.LastIndex(script, apache)
			cleanupIndex := strings.Index(script, cleanup)
			if exposeIndex < 0 || apacheIndex < 0 || cleanupIndex < 0 {
				t.Fatalf(
					"setup must expose and clean the build-only systemctl shim: expose=%d apache=%d cleanup=%d",
					exposeIndex,
					apacheIndex,
					cleanupIndex,
				)
			}
			if exposeIndex > apacheIndex || cleanupIndex < apacheIndex {
				t.Fatalf(
					"sudo-visible systemctl shim must exist during upstream validation and be removed afterward: expose=%d apache=%d cleanup=%d",
					exposeIndex,
					apacheIndex,
					cleanupIndex,
				)
			}
			if strings.Contains(script, `rm -f /usr/bin/systemctl`) ||
				strings.Contains(script, `/usr/bin/systemctl /usr/local/bin/systemctl`) {
				t.Fatal("setup must preserve the distribution-owned /usr/bin/systemctl")
			}
		})
	}
}

func TestVersionedTemplateSystemctlShimClosesDescriptorsBoundsAndVerifiesServices(t *testing.T) {
	root := repositoryRoot(t)
	for _, image := range []string{
		"ubuntu-slim",
		"ubuntu-22.04",
		"ubuntu-24.04",
		"ubuntu-26.04",
	} {
		t.Run(image, func(t *testing.T) {
			scriptPath := filepath.Join(
				root,
				"templates",
				"github-runner-"+image,
				"scripts",
				"setup-template.sh",
			)
			scriptBytes, err := os.ReadFile(scriptPath)
			if err != nil {
				t.Fatal(err)
			}
			script := string(scriptBytes)
			requiredFragments := []string{
				`action=${1:-}`,
				`unit=${unit%.service}`,
				`run_isolated() {`,
				`subprocess.run(sys.argv[1:], stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, close_fds=True, timeout=30)`,
				`case "$unit:$action" in`,
				`apache2:is-active)`,
				`test -s /run/apache2/apache2.pid && kill -0 "$(cat /run/apache2/apache2.pid)" 2>/dev/null`,
				`nginx:start)`,
				`run_isolated /usr/sbin/nginx`,
				`nginx:stop)`,
				`run_isolated /usr/sbin/nginx -s quit`,
				`nginx:is-active)`,
				`test -s /run/nginx.pid && kill -0 "$(cat /run/nginx.pid)" 2>/dev/null`,
				`if [ -x "/etc/init.d/$unit" ]; then`,
				`service_status() {`,
				`run_isolated /usr/sbin/service "$unit" status`,
				`run_isolated /usr/sbin/service "$unit" "$action"`,
				`service_result=$?`,
				`if service_status; then`,
				`[ "$action" = stop ] && exit "$service_result"`,
				`[ "$action" = stop ] && exit 0`,
			}
			if image == "ubuntu-slim" {
				requiredFragments = append(
					requiredFragments,
					`apache2:start|apache2:stop|apache2:restart)`,
					`run_isolated /usr/sbin/apachectl "$action"`,
				)
			} else {
				requiredFragments = append(
					requiredFragments,
					`run_detached_until_tcp_state() {`,
					`subprocess.Popen(`,
					`start_new_session=True`,
					`controller_pid_file = "/tmp/qiniu-runner-build-tools/apache2-controller.pid"`,
					`controller_file.write(str(process.pid))`,
					`socket.create_connection((host, port), timeout=0.2)`,
					`start_apache() {`,
					`run_detached_until_tcp_state active 127.0.0.1 80 /usr/sbin/apachectl -DFOREGROUND`,
					`stop_apache() {`,
					`os.killpg(controller_pid, signal.SIGTERM)`,
					`apache2:start)`,
					`apache2:stop)`,
					`apache2:restart)`,
				)
			}
			for _, required := range requiredFragments {
				if !strings.Contains(script, required) {
					t.Fatalf("systemctl shim must isolate, bound, and verify available SysV services; missing %q", required)
				}
			}
		})
	}
}

func TestVersionedTemplateBuildStopsValidatedServicesBetweenInstallers(t *testing.T) {
	root := repositoryRoot(t)
	for _, image := range []string{
		"ubuntu-22.04",
		"ubuntu-24.04",
		"ubuntu-26.04",
	} {
		t.Run(image, func(t *testing.T) {
			scriptPath := filepath.Join(
				root,
				"templates",
				"github-runner-"+image,
				"scripts",
				"setup-template.sh",
			)
			scriptBytes, err := os.ReadFile(scriptPath)
			if err != nil {
				t.Fatal(err)
			}
			script := string(scriptBytes)
			installerRun := `run_upstream_installer "$upstream_build/$installer"`
			serviceCleanup := `install-apache.sh) stop_validated_service apache2 ;;`
			installerRunIndex := strings.LastIndex(script, installerRun)
			if installerRunIndex < 0 {
				t.Fatal("versioned build must run the pinned installer loop")
			}
			installerLoop := script[installerRunIndex:]
			serviceCleanupIndex := strings.Index(installerLoop, serviceCleanup)
			loopEndIndex := strings.Index(installerLoop, "\n  done")
			if serviceCleanupIndex < 0 || loopEndIndex < 0 {
				t.Fatalf(
					"versioned build must stop validated services between installers: installer=%d cleanup=%d loop_end=%d",
					installerRunIndex,
					serviceCleanupIndex,
					loopEndIndex,
				)
			}
			if serviceCleanupIndex > loopEndIndex {
				t.Fatalf(
					"service cleanup must run inside the installer loop: cleanup=%d loop_end=%d",
					serviceCleanupIndex,
					loopEndIndex,
				)
			}
			for _, required := range []string{
				`stop_validated_service() {`,
				`if [ "$unit" = apache2 ]; then`,
				`apache2ctl stop`,
				`if ! ss -ltn 'sport = :80' | grep -q LISTEN; then`,
				`echo "validated service kept port 80 busy after cleanup: apache2" >&2`,
				`systemctl stop "$unit" || true`,
				`if systemctl is-active --quiet "$unit"; then`,
				`return 1`,
			} {
				if !strings.Contains(script, required) {
					t.Fatalf("service cleanup must verify the service stopped; missing %q", required)
				}
			}
		})
	}
}

func TestVersionedTemplateBuildReloadsPersistedEnvironmentBeforePipxPackages(t *testing.T) {
	root := repositoryRoot(t)
	for _, image := range []string{
		"ubuntu-22.04",
		"ubuntu-24.04",
		"ubuntu-26.04",
	} {
		t.Run(image, func(t *testing.T) {
			scriptPath := filepath.Join(
				root,
				"templates",
				"github-runner-"+image,
				"scripts",
				"setup-template.sh",
			)
			scriptBytes, err := os.ReadFile(scriptPath)
			if err != nil {
				t.Fatal(err)
			}
			script := string(scriptBytes)
			configureEnvironment := `bash "$upstream_build/configure-environment.sh"`
			reloadEnvironment := `. "$HELPER_SCRIPTS/etc-environment.sh"
  reload_etc_environment`
			installPipx := `run_upstream_installer "$upstream_build/install-pipx-packages.sh"`
			configureIndex := strings.Index(script, configureEnvironment)
			reloadIndex := strings.Index(script, reloadEnvironment)
			pipxIndex := strings.Index(script, installPipx)
			if configureIndex < 0 || reloadIndex < 0 || pipxIndex < 0 {
				t.Fatalf(
					"versioned build must reload persisted environment before pipx packages: configure=%d reload=%d pipx=%d",
					configureIndex,
					reloadIndex,
					pipxIndex,
				)
			}
			if configureIndex > reloadIndex || reloadIndex > pipxIndex {
				t.Fatalf(
					"persisted environment reload must follow environment configuration and precede pipx packages: configure=%d reload=%d pipx=%d",
					configureIndex,
					reloadIndex,
					pipxIndex,
				)
			}
		})
	}
}

func TestVersionedTemplateBuildUsesDiskBoundedToolset(t *testing.T) {
	root := repositoryRoot(t)
	for _, image := range []string{
		"ubuntu-22.04",
		"ubuntu-24.04",
		"ubuntu-26.04",
	} {
		t.Run(image, func(t *testing.T) {
			scriptPath := filepath.Join(
				root,
				"templates",
				"github-runner-"+image,
				"scripts",
				"setup-template.sh",
			)
			scriptBytes, err := os.ReadFile(scriptPath)
			if err != nil {
				t.Fatal(err)
			}
			script := string(scriptBytes)
			start := strings.Index(script, "else\n  . /etc/os-release")
			if start < 0 {
				t.Fatal("cannot find versioned installer branch")
			}
			end := strings.Index(script[start:], "\nfi\n\nif phase_selected runtime; then")
			if end < 0 {
				t.Fatalf("cannot isolate versioned installer branch: start=%d end=%d", start, end)
			}
			versioned := script[start : start+end]
			for _, required := range []string{
				"install-apt-common.sh",
				"install_azcopy_from_microsoft_package",
				"install-azure-cli.sh",
				"install-apache.sh",
				"install-aws-tools.sh",
				"install-container-tools.sh",
				"install-git.sh",
				"install-github-cli.sh",
				"install-google-cloud-cli.sh",
				"install-nodejs.sh",
				"install-python.sh",
				"install-yq.sh",
				"install-ninja.sh",
				"install_pester_for_upstream_tests",
			} {
				if !strings.Contains(versioned, required) {
					t.Fatalf("disk-bounded versioned toolset must retain %q", required)
				}
			}
			for _, excluded := range []string{
				"install-actions-cache.sh",
				"install-azcopy.sh",
				"install-dotnetcore-sdk.sh",
				"install-android-sdk.sh",
				"install-codeql-bundle.sh",
				"install-homebrew.sh",
				"Install-PowerShellModules.ps1",
				"Install-PowerShellAzModules.ps1",
				"Install-Toolset.ps1",
				"Configure-Toolset.ps1",
			} {
				if strings.Contains(versioned, excluded) {
					t.Fatalf("disk-bounded versioned toolset must not install %q", excluded)
				}
			}
		})
	}
}

func TestRunnerTemplatePinsGoogleCloudCLIArchive(t *testing.T) {
	root := repositoryRoot(t)
	for _, image := range []string{
		"ubuntu-slim",
		"ubuntu-22.04",
		"ubuntu-24.04",
		"ubuntu-26.04",
	} {
		t.Run(image, func(t *testing.T) {
			dockerfilePath := filepath.Join(
				root,
				"templates",
				"github-runner-"+image,
				"Dockerfile",
			)
			dockerfileBytes, err := os.ReadFile(dockerfilePath)
			if err != nil {
				t.Fatal(err)
			}
			dockerfile := string(dockerfileBytes)
			for _, required := range []string{
				"ARG GOOGLE_CLOUD_CLI_VERSION=",
				"ARG GOOGLE_CLOUD_CLI_ARCHIVE_SHA256=",
			} {
				if !strings.Contains(dockerfile, required) {
					t.Fatalf("template must pin the Google Cloud CLI archive; missing %q", required)
				}
			}

			scriptPath := filepath.Join(
				root,
				"templates",
				"github-runner-"+image,
				"scripts",
				"setup-template.sh",
			)
			scriptBytes, err := os.ReadFile(scriptPath)
			if err != nil {
				t.Fatal(err)
			}
			script := string(scriptBytes)
			for _, required := range []string{
				`: "${GOOGLE_CLOUD_CLI_VERSION:?GOOGLE_CLOUD_CLI_VERSION is required}"`,
				`: "${GOOGLE_CLOUD_CLI_ARCHIVE_SHA256:?GOOGLE_CLOUD_CLI_ARCHIVE_SHA256 is required}"`,
				"install_google_cloud_cli_from_archive() {",
				`google-cloud-cli-${GOOGLE_CLOUD_CLI_VERSION}-linux-x86_64.tar.gz`,
				`"$GOOGLE_CLOUD_CLI_ARCHIVE_SHA256"`,
				"run_upstream_installer() {",
				"install-google-cloud-cli.sh)",
				"install_google_cloud_cli_from_archive",
				"return",
				`run_upstream_installer "$upstream_build/$installer"`,
			} {
				if !strings.Contains(script, required) {
					t.Fatalf("template must install the pinned Google Cloud CLI archive; missing %q", required)
				}
			}
			for _, forbidden := range []string{
				`install-google-cloud-cli.sh) max_attempts=3 ;;`,
				`upstream installer returned success without gcloud`,
				`using checked Google Cloud CLI archive`,
			} {
				if strings.Contains(script, forbidden) {
					t.Fatalf("template must not resolve Google Cloud CLI through a floating APT install; found %q", forbidden)
				}
			}
			if strings.Contains(script, `bash "$upstream_build/$installer"`) {
				t.Fatal("installer loop bypasses bounded upstream installer wrapper")
			}
		})
	}
}

func TestRunnerTemplatePinsNVMArchive(t *testing.T) {
	root := repositoryRoot(t)
	for _, image := range []string{
		"ubuntu-slim",
		"ubuntu-22.04",
		"ubuntu-24.04",
		"ubuntu-26.04",
	} {
		t.Run(image, func(t *testing.T) {
			templateRoot := filepath.Join(root, "templates", "github-runner-"+image)
			dockerfileBytes, err := os.ReadFile(filepath.Join(templateRoot, "Dockerfile"))
			if err != nil {
				t.Fatal(err)
			}
			dockerfile := string(dockerfileBytes)
			for _, required := range []string{
				"ARG NVM_VERSION=0.40.6",
				"ARG NVM_ARCHIVE_SHA256=17302cad7feedb1ad33ba738f93d2176a90970724f22de119603624fcbdec1a2",
			} {
				if !strings.Contains(dockerfile, required) {
					t.Fatalf("template must pin the NVM archive; missing %q", required)
				}
			}

			scriptBytes, err := os.ReadFile(filepath.Join(templateRoot, "scripts", "setup-template.sh"))
			if err != nil {
				t.Fatal(err)
			}
			script := string(scriptBytes)
			for _, required := range []string{
				`: "${NVM_VERSION:?NVM_VERSION is required}"`,
				`: "${NVM_ARCHIVE_SHA256:?NVM_ARCHIVE_SHA256 is required}"`,
				"install_nvm_from_archive() {",
				`https://codeload.github.com/nvm-sh/nvm/tar.gz/refs/tags/v${NVM_VERSION}`,
				`"$NVM_ARCHIVE_SHA256"`,
				`install-nvm.sh)`,
				`install_nvm_from_archive`,
			} {
				if !strings.Contains(script, required) {
					t.Fatalf("template must install checked NVM from its pinned archive; missing %q", required)
				}
			}
		})
	}
}

func TestRunnerTemplatesMaterializeWritableNVMHome(t *testing.T) {
	root := repositoryRoot(t)
	for _, image := range []string{
		"ubuntu-slim",
		"ubuntu-22.04",
		"ubuntu-24.04",
		"ubuntu-26.04",
	} {
		t.Run(image, func(t *testing.T) {
			dockerfileBytes, err := os.ReadFile(filepath.Join(
				root,
				"templates",
				"github-runner-"+image,
				"Dockerfile",
			))
			if err != nil {
				t.Fatal(err)
			}
			dockerfile := string(dockerfileBytes)
			for _, required := range []string{
				"rm -rf /home/runner/.nvm",
				"cp -a /etc/skel/.nvm /home/runner/.nvm",
				"chown -R runner:runner /home/runner/.nvm",
			} {
				if !strings.Contains(dockerfile, required) {
					t.Fatalf("template must materialize a runner-owned NVM home; missing %q", required)
				}
			}
			toolchainIndex := strings.Index(dockerfile, "RUNNER_TEMPLATE_PHASE=toolchain")
			nvmHomeIndex := strings.Index(dockerfile, "rm -rf /home/runner/.nvm")
			runtimeIndex := strings.Index(dockerfile, "RUNNER_TEMPLATE_PHASE=runtime")
			if toolchainIndex < 0 || nvmHomeIndex < toolchainIndex || runtimeIndex < nvmHomeIndex {
				t.Fatal("template must materialize the NVM home in a cacheable layer between toolchain and runtime")
			}
		})
	}
}

func TestRunnerTemplateRangeDownloaderFetchesExactBytes(t *testing.T) {
	payload := []byte(strings.Repeat("0123456789abcdef", 8192))
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		match := regexp.MustCompile(`^bytes=(\d+)-(\d+)$`).FindStringSubmatch(request.Header.Get("Range"))
		if len(match) != 3 {
			http.Error(response, "range required", http.StatusBadRequest)
			return
		}
		start, startErr := strconv.Atoi(match[1])
		end, endErr := strconv.Atoi(match[2])
		if startErr != nil || endErr != nil || start < 0 || end < start || end >= len(payload) {
			http.Error(response, "invalid range", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		response.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
		response.WriteHeader(http.StatusPartialContent)
		_, _ = response.Write(payload[start : end+1])
	}))
	defer server.Close()

	root := repositoryRoot(t)
	for _, imageKey := range []string{"ubuntu-slim", "ubuntu-22.04", "ubuntu-24.04", "ubuntu-26.04"} {
		t.Run(imageKey, func(t *testing.T) {
			helper := filepath.Join(root, "templates", "github-runner-"+imageKey, "scripts", "download-checked-range")
			destination := filepath.Join(t.TempDir(), "chunk")
			const start = 173
			const end = 65572
			output, err := runCommand(t, "bash", []string{helper, server.URL, destination, strconv.Itoa(start), strconv.Itoa(end)})
			if err != nil {
				t.Fatalf("range downloader failed: %v\n%s", err, output)
			}
			chunk, err := os.ReadFile(destination)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(chunk, payload[start:end+1]) {
				t.Fatalf("downloaded range differs: got %d bytes, want %d", len(chunk), end-start+1)
			}
		})
	}
}

func TestRunnerTemplatesCacheLargeAWSSAMArchiveInCheckedRanges(t *testing.T) {
	tests := []struct {
		imageKey    string
		archiveSize int64
	}{
		{imageKey: "ubuntu-slim", archiveSize: 207357695},
		{imageKey: "ubuntu-22.04", archiveSize: 93672108},
		{imageKey: "ubuntu-24.04", archiveSize: 93672108},
		{imageKey: "ubuntu-26.04", archiveSize: 93672108},
	}
	root := repositoryRoot(t)
	rangePattern := regexp.MustCompile(`(?m)^RUN bash /usr/local/share/qiniu-sandbox-runner-template/download-checked-range \\\n+    "https://github.com/aws/aws-sam-cli/releases/download/v\$\{AWS_SAM_CLI_VERSION\}/aws-sam-cli-linux-x86_64\.zip" \\\n+    /opt/qiniu-runner-build-cache/aws-sam-cli\.part-(\d{2}) (\d+) (\d+)$`)

	for _, tc := range tests {
		t.Run(tc.imageKey, func(t *testing.T) {
			templateRoot := filepath.Join(root, "templates", "github-runner-"+tc.imageKey)
			dockerfileBytes, err := os.ReadFile(filepath.Join(templateRoot, "Dockerfile"))
			if err != nil {
				t.Fatal(err)
			}
			dockerfile := string(dockerfileBytes)
			matches := rangePattern.FindAllStringSubmatch(dockerfile, -1)
			if len(matches) < 2 {
				t.Fatalf("Dockerfile must split the AWS SAM archive across cacheable RUN layers:\n%s", dockerfile)
			}
			var nextStart int64
			for index, match := range matches {
				if match[1] != fmt.Sprintf("%02d", index) {
					t.Fatalf("range part %d uses suffix %q", index, match[1])
				}
				start, startErr := strconv.ParseInt(match[2], 10, 64)
				end, endErr := strconv.ParseInt(match[3], 10, 64)
				if startErr != nil || endErr != nil || start != nextStart || end < start {
					t.Fatalf("range part %d is not contiguous: %q-%q after %d", index, match[2], match[3], nextStart)
				}
				if end-start+1 > 16*1024*1024 {
					t.Fatalf("range part %d is too large for the observed slow network: %d bytes", index, end-start+1)
				}
				nextStart = end + 1
			}
			if nextStart != tc.archiveSize {
				t.Fatalf("cached ranges cover %d bytes, want %d", nextStart, tc.archiveSize)
			}
			platformCommand := "TEMPLATE_FLAVOR=versioned RUNNER_TEMPLATE_PHASE=platform bash /usr/local/share/qiniu-sandbox-runner-template/setup-template.sh"
			if tc.imageKey == "ubuntu-slim" {
				platformCommand = "TEMPLATE_FLAVOR=slim RUNNER_TEMPLATE_PHASE=platform bash /usr/local/share/qiniu-sandbox-runner-template/setup-template.sh"
			}
			bootstrap := strings.Index(dockerfile, "RUN TEMPLATE_FLAVOR=")
			cacheDirectory := strings.Index(dockerfile, "RUN install -d -m 0755 /opt/qiniu-runner-build-cache")
			firstRange := strings.Index(dockerfile, "RUN bash /usr/local/share/qiniu-sandbox-runner-template/download-checked-range")
			platform := strings.Index(dockerfile, platformCommand)
			if bootstrap < 0 || cacheDirectory < bootstrap || firstRange < cacheDirectory || platform < firstRange {
				t.Fatalf("AWS SAM ranges must be cached between bootstrap and platform provisioning")
			}
			for _, want := range []string{
				"COPY scripts/download-checked-range /usr/local/share/qiniu-sandbox-runner-template/download-checked-range",
				"cat /opt/qiniu-runner-build-cache/aws-sam-cli.part-* > /tmp/qiniu-aws-sam-cli.zip",
				`echo "$AWS_SAM_CLI_ARCHIVE_SHA256  /tmp/qiniu-aws-sam-cli.zip" | sha256sum --check -`,
				"rm -f /opt/qiniu-runner-build-cache/aws-sam-cli.part-*",
				"rmdir /opt/qiniu-runner-build-cache",
			} {
				if !strings.Contains(dockerfile, want) {
					t.Fatalf("Dockerfile missing checked range assembly %q", want)
				}
			}
			assemblyStart := strings.Index(dockerfile, "RUN set -eux; \\\n    cat /opt/qiniu-runner-build-cache/aws-sam-cli.part-*")
			if assemblyStart < 0 {
				t.Fatal("Dockerfile is missing the checked AWS SAM assembly layer")
			}
			assemblyEnd := strings.Index(dockerfile[assemblyStart:], "\nRUN ")
			if assemblyEnd < 0 {
				assemblyEnd = len(dockerfile) - assemblyStart
			}
			assemblyLayer := dockerfile[assemblyStart : assemblyStart+assemblyEnd]
			if !strings.Contains(assemblyLayer, platformCommand) {
				t.Fatal("Dockerfile must install AWS SAM in the same RUN that assembles the oversized archive")
			}
			if strings.Contains(dockerfile, "\nRUN "+platformCommand) {
				t.Fatal("platform provisioning must not start in a later layer after assembling the oversized AWS SAM archive")
			}
		})
	}
}

func TestRunnerTemplatesPreferOverseasUbuntuMirrors(t *testing.T) {
	root := repositoryRoot(t)
	for _, imageKey := range []string{"ubuntu-slim", "ubuntu-22.04", "ubuntu-24.04", "ubuntu-26.04"} {
		t.Run(imageKey, func(t *testing.T) {
			scriptBytes, err := os.ReadFile(filepath.Join(
				root,
				"templates",
				"github-runner-"+imageKey,
				"scripts",
				"setup-template.sh",
			))
			if err != nil {
				t.Fatal(err)
			}
			script := string(scriptBytes)
			functionStart := strings.Index(script, "configure_reliable_apt_sources() {")
			if functionStart < 0 {
				t.Fatal("setup script is missing configure_reliable_apt_sources")
			}
			functionEnd := strings.Index(script[functionStart:], "\n}")
			if functionEnd < 0 {
				t.Fatal("setup script is missing configure_reliable_apt_sources")
			}
			functionBody := script[functionStart : functionStart+functionEnd]
			archive := strings.Index(functionBody, "https://archive.ubuntu.com/ubuntu/\tpriority:1")
			kernel := strings.Index(functionBody, "https://mirrors.edge.kernel.org/ubuntu/")
			tsinghua := strings.Index(functionBody, "https://mirrors.tuna.tsinghua.edu.cn/ubuntu/")
			if archive < 0 || kernel < 0 || tsinghua < 0 || archive > kernel || kernel > tsinghua {
				t.Fatal("the official archive and overseas kernel mirror must precede Tsinghua")
			}
		})
	}
}

func TestRunnerTemplateVersionMetadataDoesNotInvalidateProvisioningLayers(t *testing.T) {
	root := repositoryRoot(t)
	for _, image := range []string{
		"ubuntu-slim",
		"ubuntu-22.04",
		"ubuntu-24.04",
		"ubuntu-26.04",
	} {
		t.Run(image, func(t *testing.T) {
			dockerfileBytes, err := os.ReadFile(filepath.Join(
				root,
				"templates",
				"github-runner-"+image,
				"Dockerfile",
			))
			if err != nil {
				t.Fatal(err)
			}
			dockerfile := string(dockerfileBytes)
			runtimeIndex := strings.Index(dockerfile, "RUNNER_TEMPLATE_PHASE=runtime")
			versionIndex := strings.Index(dockerfile, "ARG TEMPLATE_VERSION=")
			metadataIndex := strings.Index(dockerfile, "ENV ImageVersion=$TEMPLATE_VERSION")
			if runtimeIndex < 0 || versionIndex < runtimeIndex || metadataIndex < versionIndex {
				t.Fatal("template version metadata must be applied after provisioning so version bumps retain heavy layer caches")
			}
			if strings.Contains(dockerfile[:runtimeIndex], "TEMPLATE_VERSION") {
				t.Fatal("provisioning layers must not depend on template version metadata")
			}
		})
	}
}

func TestRunnerTemplateFallsBackToUbuntuDockerPackages(t *testing.T) {
	root := repositoryRoot(t)
	for _, image := range []string{
		"ubuntu-slim",
		"ubuntu-22.04",
		"ubuntu-24.04",
		"ubuntu-26.04",
	} {
		t.Run(image, func(t *testing.T) {
			scriptBytes, err := os.ReadFile(filepath.Join(
				root,
				"templates",
				"github-runner-"+image,
				"scripts",
				"setup-template.sh",
			))
			if err != nil {
				t.Fatal(err)
			}
			script := string(scriptBytes)
			for _, required := range []string{
				"configure_docker_apt_repository() {",
				"remove_official_docker_packages() {",
				`download_checked https://download.docker.com/linux/ubuntu/gpg /tmp/docker.gpg "$DOCKER_GPG_SHA256" || return 1`,
				`if [ "$installer_name" = install-docker-cli.sh ]; then`,
				"upstream Docker CLI installer unavailable; deferring to sandbox-aware installer",
				"official Docker packages unavailable; using Ubuntu archive packages",
				"apt-get purge -y \"${official_docker_packages[@]}\"",
				"dpkg --purge --force-depends \"${official_docker_packages[@]}\"",
				"apt-get -f install -y",
				"rm -f /etc/apt/sources.list.d/docker.list /etc/apt/keyrings/docker.gpg",
				"apt-get install -y --no-install-recommends docker.io docker-buildx docker-compose-v2",
				"docker --version",
				"docker buildx version",
				"docker compose version",
			} {
				if !strings.Contains(script, required) {
					t.Fatalf("template must retain a verified Ubuntu Docker fallback; missing %q", required)
				}
			}
		})
	}
}

func TestRunnerTemplatePinsBicepNuGetPackage(t *testing.T) {
	root := repositoryRoot(t)
	for _, image := range []string{
		"ubuntu-slim",
		"ubuntu-22.04",
		"ubuntu-24.04",
		"ubuntu-26.04",
	} {
		t.Run(image, func(t *testing.T) {
			templateRoot := filepath.Join(root, "templates", "github-runner-"+image)
			dockerfileBytes, err := os.ReadFile(filepath.Join(templateRoot, "Dockerfile"))
			if err != nil {
				t.Fatal(err)
			}
			dockerfile := string(dockerfileBytes)
			for _, required := range []string{
				"ARG BICEP_VERSION=",
				"ARG BICEP_NUGET_SHA256=",
			} {
				if !strings.Contains(dockerfile, required) {
					t.Fatalf("template must pin the Bicep NuGet package; missing %q", required)
				}
			}

			scriptBytes, err := os.ReadFile(filepath.Join(templateRoot, "scripts", "setup-template.sh"))
			if err != nil {
				t.Fatal(err)
			}
			script := string(scriptBytes)
			for _, required := range []string{
				`: "${BICEP_VERSION:?BICEP_VERSION is required}"`,
				`: "${BICEP_NUGET_SHA256:?BICEP_NUGET_SHA256 is required}"`,
				"install_bicep_from_nuget() {",
				`azure.bicep.commandline.linux-x64/${BICEP_VERSION}`,
				`"$BICEP_NUGET_SHA256"`,
				`install-bicep.sh)`,
				`install_bicep_from_nuget`,
				`run_upstream_tests_if_available "Tools" "Bicep"`,
			} {
				if !strings.Contains(script, required) {
					t.Fatalf("template must install checked Bicep from NuGet; missing %q", required)
				}
			}
			if strings.Contains(script, `invoke_tests "Tools" "Bicep"`) {
				t.Fatal("custom Bicep installer must not call an undefined shell function")
			}
		})
	}
}

func TestRunnerTemplateInstallsGitLFSFromUbuntuArchive(t *testing.T) {
	root := repositoryRoot(t)
	for _, image := range []string{
		"ubuntu-slim",
		"ubuntu-22.04",
		"ubuntu-24.04",
		"ubuntu-26.04",
	} {
		t.Run(image, func(t *testing.T) {
			scriptBytes, err := os.ReadFile(filepath.Join(
				root,
				"templates",
				"github-runner-"+image,
				"scripts",
				"setup-template.sh",
			))
			if err != nil {
				t.Fatal(err)
			}
			script := string(scriptBytes)
			for _, required := range []string{
				"install_git_lfs_from_ubuntu() {",
				"apt-get install -y --no-install-recommends git-lfs",
				`run_upstream_tests_if_available "Tools" "Git-lfs"`,
				`install-git-lfs.sh)`,
				`install_git_lfs_from_ubuntu`,
			} {
				if !strings.Contains(script, required) {
					t.Fatalf("template must install Git LFS from the configured Ubuntu archive; missing %q", required)
				}
			}
			if strings.Contains(script, `invoke_tests "Tools" "Git-lfs"`) {
				t.Fatal("custom Git LFS installer must not call an undefined shell function")
			}
		})
	}
}

func TestRunnerTemplateBuildUsesBoundedHTTPSAptSources(t *testing.T) {
	root := repositoryRoot(t)
	for _, image := range []string{
		"ubuntu-slim",
		"ubuntu-22.04",
		"ubuntu-24.04",
		"ubuntu-26.04",
	} {
		t.Run(image, func(t *testing.T) {
			scriptPath := filepath.Join(
				root,
				"templates",
				"github-runner-"+image,
				"scripts",
				"setup-template.sh",
			)
			scriptBytes, err := os.ReadFile(scriptPath)
			if err != nil {
				t.Fatal(err)
			}
			script := string(scriptBytes)
			bootstrapInstall := strings.Index(
				script,
				"\napt-get install -y --no-install-recommends ca-certificates\n",
			)
			firstReliableIndex := strings.Index(script, "\nconfigure_reliable_apt_sources\n")
			if bootstrapInstall < 0 || firstReliableIndex < 0 {
				t.Fatalf(
					"template must configure reliable apt sources after CA bootstrap: install=%d reliable=%d",
					bootstrapInstall,
					firstReliableIndex,
				)
			}
			if firstReliableIndex < bootstrapInstall {
				t.Fatalf(
					"HTTPS apt source hardening must follow CA bootstrap: install=%d reliable=%d",
					bootstrapInstall,
					firstReliableIndex,
				)
			}
			if strings.Count(script, "configure_reliable_apt_sources") < 3 {
				t.Fatal("apt source hardening must run initially and after upstream source configuration")
			}
			for _, required := range []string{
				`configure_reliable_apt_sources() {`,
				`/etc/apt/sources.list`,
				`/etc/apt/sources.list.d/ubuntu.sources`,
				`http://archive.ubuntu.com/ubuntu`,
				`https://archive.ubuntu.com/ubuntu`,
				`http://security.ubuntu.com/ubuntu`,
				`mirror+file:/etc/apt/apt-mirrors.txt`,
				`https://mirrors.tuna.tsinghua.edu.cn/ubuntu/`,
				`https://mirrors.edge.kernel.org/ubuntu/`,
				`https://archive.ubuntu.com/ubuntu/`,
				`Acquire::Retries "5";`,
				`Acquire::http::Timeout "30";`,
				`Acquire::https::Timeout "30";`,
			} {
				if !strings.Contains(script, required) {
					t.Fatalf("apt source hardening missing %q", required)
				}
			}
			if image == "ubuntu-26.04" {
				archiveMirror := strings.Index(script, "https://archive.ubuntu.com/ubuntu/\tpriority:1")
				kernelFallback := strings.Index(script, "https://mirrors.edge.kernel.org/ubuntu/\tpriority:2")
				tunaFallback := strings.Index(script, "https://mirrors.tuna.tsinghua.edu.cn/ubuntu/\tpriority:3")
				if archiveMirror < 0 || kernelFallback < 0 || tunaFallback < 0 || archiveMirror > kernelFallback || kernelFallback > tunaFallback {
					t.Fatalf(
						"Ubuntu 26.04 must prefer the official archive and kernel.org before TUNA: archive=%d kernel=%d tuna=%d",
						archiveMirror,
						kernelFallback,
						tunaFallback,
					)
				}
				for _, directMirrorRewrite := range []string{
					`'s|http://mirrors.tuna.tsinghua.edu.cn/ubuntu|mirror+file:/etc/apt/apt-mirrors.txt|g'`,
					`'s|https://mirrors.tuna.tsinghua.edu.cn/ubuntu|mirror+file:/etc/apt/apt-mirrors.txt|g'`,
					`'s|https://mirrors.edge.kernel.org/ubuntu|mirror+file:/etc/apt/apt-mirrors.txt|g'`,
				} {
					if !strings.Contains(script, directMirrorRewrite) {
						t.Fatalf("Ubuntu 26.04 must normalize direct mirror sources through the bounded mirror list; missing %q", directMirrorRewrite)
					}
				}
			}
		})
	}
}

func TestRunnerTemplateBuildBootstrapsTLSCertificatesBeforeStrictHTTPS(t *testing.T) {
	root := repositoryRoot(t)
	for _, image := range []string{
		"ubuntu-slim",
		"ubuntu-22.04",
		"ubuntu-24.04",
		"ubuntu-26.04",
	} {
		t.Run(image, func(t *testing.T) {
			scriptPath := filepath.Join(
				root,
				"templates",
				"github-runner-"+image,
				"scripts",
				"setup-template.sh",
			)
			scriptBytes, err := os.ReadFile(scriptPath)
			if err != nil {
				t.Fatal(err)
			}
			script := string(scriptBytes)
			bootstrap := strings.Index(
				script,
				"apt-get update\napt-get install -y --no-install-recommends ca-certificates",
			)
			strictSetup := strings.Index(
				script,
				"apt-get install -y --no-install-recommends ca-certificates\nconfigure_reliable_apt_sources\napt-get update",
			)
			if bootstrap < 0 || strictSetup < 0 {
				t.Fatalf(
					"template must bootstrap CA certificates before enabling HTTPS mirrors: bootstrap=%d strict=%d",
					bootstrap,
					strictSetup,
				)
			}
			if strings.Contains(script, "Acquire::https::Verify-Peer=false") {
				t.Fatal("template must never disable apt HTTPS peer verification")
			}
		})
	}
}

func TestRecoveredRunnerPIDRequiresRunnerTagAndExpectedPID(t *testing.T) {
	runnerTag := "github-runner"
	otherTag := "other"
	processes := []qnsandbox.ProcessInfo{
		{PID: 10, Tag: &otherTag},
		{PID: 20, Tag: &runnerTag},
	}
	if pid, ok := recoveredRunnerPID(processes, 20); !ok || pid != 20 {
		t.Fatalf("expected persisted runner PID, got pid=%d ok=%t", pid, ok)
	}
	if pid, ok := recoveredRunnerPID(processes, 0); !ok || pid != 20 {
		t.Fatalf("expected tagged runner PID, got pid=%d ok=%t", pid, ok)
	}
	if _, ok := recoveredRunnerPID(processes, 10); ok {
		t.Fatal("expected non-runner tag to be rejected")
	}
	processes = append(processes, qnsandbox.ProcessInfo{PID: 30, Tag: &runnerTag})
	if _, ok := recoveredRunnerPID(processes, 0); ok {
		t.Fatal("expected ambiguous tagged runner processes to be rejected")
	}
	if pid, ok := recoveredRunnerPID(processes, 20); !ok || pid != 20 {
		t.Fatalf("expected persisted PID to disambiguate tagged processes, got pid=%d ok=%t", pid, ok)
	}
}

func TestSandboxTimeoutSecondsRoundsUpAndClamps(t *testing.T) {
	if got := sandboxTimeoutSeconds(1500 * time.Millisecond); got != 2 {
		t.Fatalf("expected timeout to round up, got %d", got)
	}
	if got := sandboxTimeoutSeconds(0); got != 1 {
		t.Fatalf("expected minimum timeout of one second, got %d", got)
	}
}
