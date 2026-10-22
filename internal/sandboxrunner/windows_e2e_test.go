package sandboxrunner

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestWindowsRunnerE2E starts a real Windows runner through E2BService and
// waits for one job to finish. It is skipped unless RUNNERD_WINDOWS_E2E=1.
//
// Required environment:
//   - QINIU_SANDBOX_API_URL and QINIU_API_KEY: Sandbox service credentials.
//   - RUNNERD_E2E_TEMPLATE_ID: a github-runner-windows-11 template ID.
//   - RUNNERD_E2E_REPOSITORY_URL: for example https://github.com/OWNER/REPO.
//   - RUNNERD_E2E_REGISTRATION_TOKEN: a fresh runner registration token.
//
// Optional environment:
//   - RUNNERD_E2E_TRIGGER_CMD: a bash command run once the runner listens,
//     which must queue a job matching RUNNERD_E2E_LABELS. Without it, trigger
//     the job manually within the timeout.
//   - RUNNERD_E2E_LABELS: defaults to self-hosted,windows,x64,qiniu,windows-11.
//   - RUNNERD_E2E_TEMPLATE_VERSION: expected IMAGE_VERSION, when set.
func TestWindowsRunnerE2E(t *testing.T) {
	if os.Getenv("RUNNERD_WINDOWS_E2E") != "1" {
		t.Skip("set RUNNERD_WINDOWS_E2E=1 to run the Windows runner end-to-end test")
	}
	required := func(name string) string {
		t.Helper()
		value := strings.TrimSpace(os.Getenv(name))
		if value == "" {
			t.Fatalf("%s is required", name)
		}
		return value
	}
	apiURL := required("QINIU_SANDBOX_API_URL")
	apiKey := required("QINIU_API_KEY")
	templateID := required("RUNNERD_E2E_TEMPLATE_ID")
	repositoryURL := required("RUNNERD_E2E_REPOSITORY_URL")
	registrationToken := required("RUNNERD_E2E_REGISTRATION_TOKEN")
	labels := "self-hosted,windows,x64,qiniu,windows-11"
	// The template's preinstalled baseline is the tracked Windows pin. GitHub
	// may officially update the Runner before the job, so the effective
	// version only has to reach that baseline.
	const windowsPins = "templates/github-runner-windows-11/template.env"
	baselineVersion := readTemplatePin(t, windowsPins, "RUNNER_WIN_X64_VERSION")
	baselineSHA256 := readTemplatePin(t, windowsPins, "RUNNER_WIN_X64_ARCHIVE_SHA256")
	if value := strings.TrimSpace(os.Getenv("RUNNERD_E2E_LABELS")); value != "" {
		labels = value
	}

	service, err := NewE2BService(apiKey, apiURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	var outputMu sync.Mutex
	var stdout, stderr strings.Builder
	listening := make(chan struct{})
	var listeningOnce sync.Once
	exited := make(chan ExitResult, 1)
	requestID := "windows-e2e-" + time.Now().UTC().Format("20060102150405")
	input := StartInput{
		Platform:          PlatformWindows,
		RequestID:         requestID,
		RunnerName:        "runnerd-" + requestID,
		RepositoryURL:     repositoryURL,
		RegistrationToken: registrationToken,
		Labels:            strings.Split(labels, ","),
		RunnerApplications: []RunnerApplication{{
			OS:             "win",
			Architecture:   "x64",
			Version:        baselineVersion,
			DownloadURL:    "https://github.com/actions/runner/releases/download/v" + baselineVersion + "/actions-runner-win-x64-" + baselineVersion + ".zip",
			SHA256Checksum: baselineSHA256,
		}},
		TemplateID:     templateID,
		Timeout:        30 * time.Minute,
		CommandContext: ctx,
		OnStdout: func(data []byte) {
			outputMu.Lock()
			defer outputMu.Unlock()
			stdout.Write(data)
			t.Logf("stdout: %s", strings.TrimRight(string(data), "\r\n"))
			if strings.Contains(stdout.String(), "Listening for Jobs") {
				listeningOnce.Do(func() { close(listening) })
			}
		},
		OnStderr: func(data []byte) {
			outputMu.Lock()
			defer outputMu.Unlock()
			stderr.Write(data)
			t.Logf("stderr: %s", strings.TrimRight(string(data), "\r\n"))
		},
		OnExit: func(result ExitResult, err error) {
			if err != nil {
				t.Logf("runner wait error: %v", err)
			}
			exited <- result
		},
	}

	result, err := service.StartRunner(ctx, input)
	if err != nil {
		t.Fatalf("StartRunner: %v", err)
	}
	t.Logf("sandbox=%s pid=%d template_version=%q runner_version=%q", result.SandboxID, result.PID, result.TemplateVersion, result.RunnerVersion)
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Minute)
		defer stopCancel()
		if err := service.StopRunner(stopCtx, result.SandboxID, result.PID); err != nil {
			t.Errorf("StopRunner: %v", err)
		}
	})
	if result.PID == 0 {
		t.Fatal("StartRunner returned no PID")
	}
	if result.RunnerVersion != baselineVersion {
		t.Fatalf("runtime runner version = %q, want template baseline %s", result.RunnerVersion, baselineVersion)
	}
	if want := strings.TrimSpace(os.Getenv("RUNNERD_E2E_TEMPLATE_VERSION")); want != "" && result.TemplateVersion != want {
		t.Fatalf("runtime template version = %q, want %q", result.TemplateVersion, want)
	}

	select {
	case <-listening:
	case runnerResult := <-exited:
		t.Fatalf("runner exited before listening: %#v", runnerResult)
	case <-ctx.Done():
		t.Fatal("runner did not start listening for jobs")
	}

	// Recovery must find the same tagged process and read the same versions.
	recovered, err := service.RecoverRunner(ctx, RecoverInput{
		Platform:  PlatformWindows,
		RequestID: requestID,
		SandboxID: result.SandboxID,
		PID:       result.PID,
		Timeout:   30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("RecoverRunner: %v", err)
	}
	if recovered.PID != result.PID || recovered.RunnerVersion != result.RunnerVersion {
		t.Fatalf("RecoverRunner = %#v, want PID %d and runner version %q", recovered, result.PID, result.RunnerVersion)
	}

	if command := strings.TrimSpace(os.Getenv("RUNNERD_E2E_TRIGGER_CMD")); command != "" {
		if output, err := exec.CommandContext(ctx, "bash", "-c", command).CombinedOutput(); err != nil {
			t.Fatalf("trigger command: %v: %s", err, output)
		}
	} else {
		t.Logf("runner %s is listening; trigger a job with labels %s", input.RunnerName, labels)
	}

	var runnerResult ExitResult
	select {
	case runnerResult = <-exited:
	case <-ctx.Done():
		t.Fatal("runner did not finish a job before the timeout")
	}
	outputMu.Lock()
	output := stdout.String()
	outputMu.Unlock()
	if runnerResult.ExitCode != 0 {
		t.Fatalf("runner exit code = %d, error=%q", runnerResult.ExitCode, runnerResult.Error)
	}
	if !runnerVersionAtLeast(runnerResult.EffectiveRunnerVersion, baselineVersion) {
		t.Fatalf("effective runner version = %q, want at least %s", runnerResult.EffectiveRunnerVersion, baselineVersion)
	}
	for _, want := range []string{
		"GitHub Actions runner " + baselineVersion + " is already installed",
		runnerJobStartedMarker,
		"completed with result: Succeeded",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("runner stdout does not contain %q", want)
		}
	}
}

// runnerVersionAtLeast compares the numeric major.minor.patch prefix.
func runnerVersionAtLeast(version, baseline string) bool {
	parse := func(value string) ([3]int, bool) {
		var parts [3]int
		fields := strings.SplitN(value, ".", 3)
		if len(fields) != 3 {
			return parts, false
		}
		fields[2], _, _ = strings.Cut(fields[2], "-")
		for i, field := range fields {
			number, err := strconv.Atoi(field)
			if err != nil {
				return parts, false
			}
			parts[i] = number
		}
		return parts, true
	}
	got, ok := parse(version)
	want, wantOK := parse(baseline)
	if !ok || !wantOK {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return got[i] > want[i]
		}
	}
	return true
}
