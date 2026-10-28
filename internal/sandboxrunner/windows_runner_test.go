package sandboxrunner

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	qnsandbox "github.com/qiniu/go-sdk/v7/sandbox"
)

func TestPlatformForLabels(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		labels []string
		want   Platform
	}{
		{nil, PlatformLinux},
		{[]string{"self-hosted", "linux", "x64"}, PlatformLinux},
		{[]string{"self-hosted", "windows-11"}, PlatformLinux},
		{[]string{"self-hosted", " Windows ", "x64"}, PlatformWindows},
		{[]string{"qiniu", "windows"}, PlatformWindows},
	} {
		if got := PlatformForLabels(tt.labels); got != tt.want {
			t.Errorf("PlatformForLabels(%q) = %q, want %q", tt.labels, got, tt.want)
		}
	}
}

func TestWindowsRunnerBootstrapUsesEnvdDefaultUser(t *testing.T) {
	t.Parallel()
	if got := PlatformWindows.bootstrapUser(); got != qnsandbox.DefaultUser {
		t.Fatalf("Windows bootstrap user = %q, want %q", got, qnsandbox.DefaultUser)
	}
	if got := PlatformLinux.bootstrapUser(); got != runnerBootstrapUser {
		t.Fatalf("Linux bootstrap user = %q, want %q", got, runnerBootstrapUser)
	}
	if got := Platform("").bootstrapUser(); got != runnerBootstrapUser {
		t.Fatalf("unspecified platform bootstrap user = %q, want %q", got, runnerBootstrapUser)
	}
}

func TestWindowsStartScriptEncodesEveryArgument(t *testing.T) {
	t.Parallel()
	input := StartInput{
		Platform:            PlatformWindows,
		RequestID:           `req'$(calc)`,
		RepositoryURL:       `https://github.com/o/r'; calc; '`,
		RegistrationToken:   `tok"&calc`,
		RunnerName:          "runner`calc",
		Labels:              []string{"self-hosted", "windows", `x64'$(calc)`},
		RunnerGroup:         `group';calc;'`,
		CacheS3Region:       "cn-east-1",
		CacheS3Bucket:       "bucket'",
		CacheS3Endpoint:     "https://s3.example",
		CacheS3ReadPrefixes: "a,b",
		CacheS3WritePrefix:  "w",
		CacheS3AccessKeyID:  "ak'",
		CacheS3SecretKey:    "sk'",
		CacheS3SessionToken: "st'",
	}
	sandboxID := `sandbox'$(calc)`
	script := windowsStartScript(input, sandboxID)

	if strings.Contains(script, "%!") {
		t.Fatalf("script has formatting artifacts:\n%s", script)
	}
	if strings.Contains(script, "%[") {
		t.Fatalf("script has unsubstituted placeholders:\n%s", script)
	}
	for _, char := range script {
		if char > 0x7f {
			t.Fatalf("script contains non-ASCII character %q", char)
		}
	}
	for _, raw := range []string{
		input.RequestID,
		input.RepositoryURL,
		input.RegistrationToken,
		input.RunnerName,
		strings.Join(input.Labels, ","),
		input.RunnerGroup,
		input.CacheS3Bucket,
		input.CacheS3AccessKeyID,
		input.CacheS3SecretKey,
		input.CacheS3SessionToken,
		sandboxID,
	} {
		if strings.Contains(script, raw) {
			t.Fatalf("script contains raw argument %q", raw)
		}
		if !strings.Contains(script, "Decode-Argument '"+base64.StdEncoding.EncodeToString([]byte(raw))+"'") {
			t.Fatalf("script does not decode encoded argument %q", raw)
		}
	}
	if !strings.Contains(script, "if ('0' -eq '1')") {
		t.Fatal("script does not render the Docker requirement flag")
	}
}

func TestWindowsStartScriptUsesEphemeralRunnerAndHooks(t *testing.T) {
	t.Parallel()
	script := windowsStartScript(StartInput{Platform: PlatformWindows}, "sandbox-1")
	for _, want := range []string{
		"'--ephemeral', '--unattended', '--replace'",
		"'--runnergroup', $runnerGroup",
		"$env:ACTIONS_RUNNER_HOOK_JOB_STARTED = Join-Path $hookRoot 'job-started.ps1'",
		"$env:ACTIONS_RUNNER_HOOK_JOB_COMPLETED = Join-Path $hookRoot 'job-completed.ps1'",
		`RUNNERD_EFFECTIVE_RUNNER_VERSION=`,
		`RUNNERD_JOB_STARTED`,
		"& $runnerListener configure @configArguments",
		"& $runnerListener remove --token $registrationToken",
		"Get-FileHash -LiteralPath $runnerUpdateArchive -Algorithm SHA256",
		"--max-filesize 536870912",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("script does not contain %q", want)
		}
	}
	// cmd.exe would reparse values such as a Runner Group named R&D.
	if strings.Contains(script, "'config.cmd'") || strings.Contains(script, "$runnerConfig") {
		t.Fatal("script passes registration arguments through a batch file")
	}
}

func TestWindowsStartScriptBoundsRunnerDownloadAcrossRetries(t *testing.T) {
	t.Parallel()
	script := windowsStartScript(StartInput{Platform: PlatformWindows}, "sandbox-1")
	for _, want := range []string{
		"$runnerDownloadDeadline = [DateTime]::UtcNow.AddSeconds(300)",
		"--max-time $remainingSeconds",
		"if ($remainingSeconds -le 0) {",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("script does not contain %q", want)
		}
	}
	if strings.Contains(script, "--retry") || strings.Contains(script, "--max-time 300") {
		t.Fatal("curl retries or per-attempt limits can extend the total download deadline")
	}
}

func TestWindowsStartScriptRequiresCurlThatBoundsUnknownSizes(t *testing.T) {
	t.Parallel()
	script := windowsStartScript(StartInput{Platform: PlatformWindows}, "sandbox-1")
	guard := strings.Index(script, "if (-not (Test-RunnerVersionAtLeast $curlVersion '8.4.0')) {")
	download := strings.Index(script, "& curl.exe --fail")
	if guard < 0 || download < guard {
		t.Fatal("preflight download does not require curl 8.4.0 or newer first")
	}
}

func TestWindowsStartScriptChecksDockerBeforePreflight(t *testing.T) {
	t.Parallel()
	script := windowsStartScript(StartInput{Platform: PlatformWindows, RequireDocker: true}, "sandbox-1")
	docker := strings.Index(script, "if ('1' -eq '1') {")
	manifest := strings.Index(script, "$runnerApplicationsManifest = Decode-Argument")
	if docker < 0 || manifest < docker {
		t.Fatal("Docker requirement is checked after the preflight update")
	}
}

func TestWindowsStartScriptToleratesRunnerRemovalFailure(t *testing.T) {
	t.Parallel()
	script := windowsStartScript(StartInput{Platform: PlatformWindows}, "sandbox-1")
	cleanup := script[strings.LastIndex(script, "} finally {"):]
	try := strings.Index(cleanup, "try {")
	remove := strings.Index(cleanup, "& $runnerListener remove --token $registrationToken")
	if try < 0 || remove < try || !strings.Contains(cleanup[remove:], "} catch {") || !strings.HasSuffix(strings.TrimSpace(script), "exit $runnerExitCode") {
		t.Fatalf("runner removal can mask the runner exit code:\n%s", cleanup)
	}
}

func TestWindowsStartScriptStopsPollingAfterMarkerRelay(t *testing.T) {
	t.Parallel()
	script := windowsStartScript(StartInput{Platform: PlatformWindows}, "sandbox-1")
	if !strings.Contains(script, "while (-not $hookSignalRelayed -and -not $runner.WaitForExit(100)) {") || strings.Contains(script, "while (-not $runner.HasExited)") {
		t.Fatal("start script polls for the whole job instead of blocking after the marker relay")
	}
}

func TestWindowsStartScriptRestoresPreviousRunnerFromFinally(t *testing.T) {
	t.Parallel()
	script := windowsStartScript(StartInput{Platform: PlatformWindows}, "sandbox-1")
	finally := script[strings.Index(script, "} finally {\n      # This also runs"):]
	finally = finally[:strings.Index(finally, "Set-Location -LiteralPath $workdir")]
	for _, want := range []string{
		"if ($runnerCutoverState -eq 'previous-moved') {",
		"Move-Item -LiteralPath $runnerPreviousWorkdir -Destination $workdir -ErrorAction Stop",
		"$keepRunnerUpdateRoot = $true",
		"if (-not $keepRunnerUpdateRoot) {",
	} {
		if !strings.Contains(finally, want) {
			t.Fatalf("cutover finally block does not contain %q:\n%s", want, finally)
		}
	}
	if strings.Index(script, "$runnerCutoverState = 'previous-moved'") > strings.Index(script, "Move-Item -LiteralPath $runnerUpdateCandidate -Destination $workdir") {
		t.Fatal("cutover state is recorded after the candidate move")
	}
}

func TestWindowsJobStartedHookWaitsForMarkerConsumption(t *testing.T) {
	t.Parallel()
	script := windowsStartScript(StartInput{Platform: PlatformWindows}, "sandbox-1")
	hook := script[strings.Index(script, "job-started.ps1') -Encoding ASCII -Value @'"):]
	hook = hook[:strings.Index(hook, "\n'@")]
	publish := strings.Index(hook, "Move-Item -LiteralPath $temporary")
	wait := strings.Index(hook, "while ((Test-Path -LiteralPath $env:RUNNERD_HOOK_SIGNAL_FALLBACK_PATH)")
	drop := strings.Index(hook, "Remove-Item -LiteralPath $env:RUNNERD_HOOK_SIGNAL_FALLBACK_PATH")
	if publish < 0 || wait < publish || drop < wait {
		t.Fatalf("hook does not wait for consumption and drop an unconsumed marker:\n%s", hook)
	}
	relay := script[strings.Index(script, "while (-not $hookSignalRelayed"):]
	if strings.Index(relay, "$hookSignalRelayed = $true") > strings.Index(relay, "ReadAllText($hookSignalPath)") {
		t.Fatal("relay can forward more than one marker")
	}
}

func TestHasRunnerApplicationRequiresSelectedPlatform(t *testing.T) {
	t.Parallel()
	linux := RunnerApplication{
		Architecture:   "x64",
		Version:        "2.337.0",
		DownloadURL:    "https://github.com/actions/runner/releases/download/v2.337.0/actions-runner-linux-x64-2.337.0.tar.gz",
		SHA256Checksum: "70920811a4f8ad4328818682bca5c6469c1c942fab52448868071d0063816613",
	}
	if !HasRunnerApplication(PlatformLinux, []RunnerApplication{linux}) {
		t.Fatal("Linux application is not accepted for Linux")
	}
	if HasRunnerApplication(PlatformWindows, []RunnerApplication{linux}) {
		t.Fatal("Linux-only applications are accepted for Windows")
	}
	if _, err := (&E2BService{}).StartRunner(context.Background(), StartInput{Platform: PlatformWindows, RunnerApplications: []RunnerApplication{linux}}); !errors.Is(err, ErrRunnerApplicationMissing) {
		t.Fatalf("StartRunner error = %v, want ErrRunnerApplicationMissing", err)
	}
}

func TestWindowsRuntimeEnvironmentProbesPreinstalledRunnerOnly(t *testing.T) {
	t.Parallel()
	if strings.Contains(windowsRuntimeEnvironmentCommand, `C:\runner\actions-runner`) ||
		!strings.Contains(windowsRuntimeEnvironmentCommand, `if exist C:\actions-runner\bin\Runner.Listener.exe C:\actions-runner\bin\Runner.Listener.exe --version`) {
		t.Fatalf("Windows probe must read only the preinstalled Runner: %s", windowsRuntimeEnvironmentCommand)
	}
}

func TestWindowsRuntimeEnvironmentCommandAvoidsPowerShellAndFullEnvironment(t *testing.T) {
	t.Parallel()
	command := strings.ToLower(windowsRuntimeEnvironmentCommand)
	// PowerShell startup can exceed the probe budget, and the probe must never
	// print the whole image.env or process environment.
	for _, forbidden := range []string{"powershell", "pwsh", "type ", "set ", "%"} {
		if strings.Contains(command, forbidden) {
			t.Fatalf("Windows probe contains %q: %s", forbidden, windowsRuntimeEnvironmentCommand)
		}
	}
	if !strings.Contains(windowsRuntimeEnvironmentCommand, `findstr /b /c:"IMAGE_VERSION=" /c:"ImageVersion="`) ||
		!strings.HasSuffix(windowsRuntimeEnvironmentCommand, " & exit /b 0") {
		t.Fatalf("Windows probe is not allowlisted and nonfatal: %s", windowsRuntimeEnvironmentCommand)
	}
}

func TestParseWindowsRuntimeEnvironment(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("9", runtimeEnvironmentValueLimit+1)
	for _, tt := range []struct {
		name   string
		output string
		want   runtimeEnvironment
	}{
		{
			name:   "template and runner",
			output: "IMAGE_VERSION=20261021.1\r\nImageVersion=other\r\nRUNNERD_PREINSTALLED_RUNNER_VERSION \r\n2.337.0\r\n",
			want:   runtimeEnvironment{TemplateVersion: "20261021.1", RunnerVersion: "2.337.0"},
		},
		{
			name:   "quoted ImageVersion only",
			output: "ImageVersion=\"20261021.1\"\r\nRUNNERD_PREINSTALLED_RUNNER_VERSION\r\n",
			want:   runtimeEnvironment{TemplateVersion: "20261021.1"},
		},
		{
			name:   "missing image.env",
			output: "RUNNERD_PREINSTALLED_RUNNER_VERSION \r\n\r\n2.337.0\r\n",
			want:   runtimeEnvironment{RunnerVersion: "2.337.0"},
		},
		{
			name:   "lines without separator are not a runner version",
			output: "IMAGE_VERSION=1\r\n2.337.0\r\n",
			want:   runtimeEnvironment{TemplateVersion: "1"},
		},
		{
			name:   "oversized values",
			output: "IMAGE_VERSION=" + long + "\r\nRUNNERD_PREINSTALLED_RUNNER_VERSION\r\n" + long + "\r\n",
			want:   runtimeEnvironment{},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseWindowsRuntimeEnvironment(tt.output); got != tt.want {
				t.Fatalf("parseWindowsRuntimeEnvironment = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestRunnerApplicationsManifestFiltersByPlatform(t *testing.T) {
	t.Parallel()
	applications := []RunnerApplication{
		{
			Architecture:   "x64",
			Version:        "2.337.0",
			DownloadURL:    "https://github.com/actions/runner/releases/download/v2.337.0/actions-runner-linux-x64-2.337.0.tar.gz",
			SHA256Checksum: "70920811a4f8ad4328818682bca5c6469c1c942fab52448868071d0063816613",
		},
		{
			OS:             "win",
			Architecture:   "x64",
			Version:        "2.337.0",
			DownloadURL:    "https://github.com/actions/runner/releases/download/v2.337.0/actions-runner-win-x64-2.337.0.zip",
			SHA256Checksum: "1150692afa94e71f872017e254ea55b6eece1eece3fe7e3a6d4c93d0a1b85cfc",
		},
	}
	linux := runnerApplicationsManifest(PlatformLinux, applications)
	if linux != "x64\t2.337.0\t"+applications[0].DownloadURL+"\t"+applications[0].SHA256Checksum {
		t.Fatalf("Linux manifest = %q", linux)
	}
	windows := runnerApplicationsManifest(PlatformWindows, applications)
	if windows != "x64\t2.337.0\t"+applications[1].DownloadURL+"\t"+applications[1].SHA256Checksum {
		t.Fatalf("Windows manifest = %q", windows)
	}
	if got := runnerApplicationsManifest("", applications); got != linux {
		t.Fatalf("unspecified platform manifest = %q, want Linux manifest", got)
	}
}

func TestWindowsTemplateBuildUsesQshellOnWindows11Template(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	argsFile := filepath.Join(root, "args")
	configFile := filepath.Join(root, "config")
	fakeQshell := filepath.Join(root, "qshell")
	writeExecutable(t, fakeQshell, `#!/usr/bin/env bash
if [ "$1" = version ]; then
  echo v2.19.13
  exit 0
fi
{ pwd; printf '%s\n' "$@"; } >"`+argsFile+`"
cat "${@: -1}" >"`+configFile+`"
echo 'Status: ready'
`)
	output, err := runCommand(t, "bash", []string{"scripts/build-windows-template.sh", "templates/github-runner-windows-11"},
		"QSHELL="+fakeQshell, "QINIU_SANDBOX_API_URL=https://sandbox.invalid", "QINIU_API_KEY=test")
	if err != nil {
		t.Fatalf("build helper: %v: %s", err, output)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(repositoryRoot(t), "templates", "github-runner-windows-11") + "\nsandbox\ntemplate\nbuild\n--wait\n--config\n"; !strings.HasPrefix(string(args), want) {
		t.Fatalf("qshell invocation = %s, want prefix %q", args, want)
	}
	config, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`name = "github-runner-windows-11"`,
		`from_template = "windows11"`,
		`dockerfile = "./Dockerfile"`,
		`path = ".."`,
	} {
		if !strings.Contains(string(config), want+"\n") {
			t.Fatalf("qshell config does not contain %q:\n%s", want, config)
		}
	}
	if regexp.MustCompile(`(?m)^\s*template_id\s*=`).Match(config) {
		t.Fatalf("qshell config contains a template ID:\n%s", config)
	}

	dockerfile, err := os.ReadFile(filepath.Join(repositoryRoot(t), "templates", "github-runner-windows-11", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# escape=`\n",
		"\nUSER user\n",
		"\nCOPY github-runner-windows-11/scripts/setup-template.ps1 C:\\qiniu-build\\setup-template.ps1\n",
		"\nCOPY github-runner-windows-11/template.env C:\\qiniu-build\\build.env\n",
		"-File C:\\qiniu-build\\setup-template.ps1 -BuildEnvironmentFile C:\\qiniu-build\\build.env\n",
		"\nRUN rmdir /s /q C:\\qiniu-build\n",
	} {
		if !strings.Contains(string(dockerfile), want) {
			t.Fatalf("Windows Dockerfile does not contain %q:\n%s", want, dockerfile)
		}
	}
	// The Windows builder ignores USER, and the Linux Runner archive chunks
	// are unused. Comments may still describe qshell's Linux defaults.
	for line := range strings.SplitSeq(string(dockerfile), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		if strings.Contains(line, "USER root") || strings.Contains(line, "common/.build") {
			t.Fatalf("Windows Dockerfile uses a Linux-only build step: %s", line)
		}
	}
}

// readTemplatePin returns a KEY=VALUE pin from a tracked template file.
func readTemplatePin(t *testing.T, path, key string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(repositoryRoot(t), path))
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(string(content), "\n") {
		if value, ok := strings.CutPrefix(line, key+"="); ok {
			return value
		}
	}
	t.Fatalf("%s has no %s", path, key)
	return ""
}

func TestWindowsTemplateRunnerPinMatchesCommonPin(t *testing.T) {
	t.Parallel()
	common := readTemplatePin(t, "templates/common/actions-runner.env", "RUNNER_VERSION")
	windows := readTemplatePin(t, "templates/github-runner-windows-11/template.env", "RUNNER_WIN_X64_VERSION")
	if windows != common {
		t.Fatalf("Windows runner pin %s does not match common pin %s; update template.env with the win-x64 archive digest and size", windows, common)
	}
}

func TestWindowsTemplateDownloadUsesBoundedHTTP11Transfer(t *testing.T) {
	t.Parallel()
	content, err := os.ReadFile(filepath.Join(repositoryRoot(t), "templates/github-runner-windows-11/scripts/setup-template.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(content)
	for _, want := range []string{
		"--http1.1",
		"--speed-limit 1024",
		"--speed-time 60",
		"--max-time 900",
		"--continue-at -",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("Windows template download does not contain %q", want)
		}
	}
}
