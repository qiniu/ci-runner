param(
  [Parameter(Mandatory = $true)][string]$BuildEnvironmentFile
)

$ErrorActionPreference = 'Stop'
# Progress bars need a console buffer, which envd build steps do not have.
$ProgressPreference = 'SilentlyContinue'
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

# Write-Output inside a function becomes part of its return value; log
# directly to stdout instead.
function Log([string]$message) {
  [Console]::Out.WriteLine($message)
}

$pins = @{}
foreach ($line in Get-Content -LiteralPath $BuildEnvironmentFile) {
  if ($line -match '^([A-Z0-9_]+)=(.*)$') {
    $pins[$Matches[1]] = $Matches[2].Trim()
  }
}
function Pin([string]$name) {
  if (-not $pins.ContainsKey($name) -or -not $pins[$name]) {
    throw "missing build pin $name"
  }
  return $pins[$name]
}

$actionsRunnerRoot = 'C:\actions-runner'
$runnerRoot = 'C:\runner'
$toolCache = 'C:\hostedtoolcache'
$metadataRoot = 'C:\ProgramData\qiniu-runner'
$downloadRoot = Join-Path $env:TEMP 'qiniu-runner-template'
$gitBin = 'C:\Program Files\Git\bin'
$gitCmd = 'C:\Program Files\Git\cmd'
$pwshRoot = 'C:\Program Files\PowerShell\7'

function Get-CheckedDownload([string]$name, [string]$url, [string]$sha256, [long]$size) {
  New-Item -ItemType Directory -Force -Path $downloadRoot | Out-Null
  $path = Join-Path $downloadRoot $name
  Log "downloading $url"
  for ($attempt = 1; ; $attempt++) {
    # curl.exe ships with Windows 11; resume partial transfers, keep the
    # GitHub release connection on HTTP/1.1 for the Windows Schannel path, and
    # abort a stalled transfer before the per-attempt deadline.
    & curl.exe --http1.1 --fail --location --silent --show-error --connect-timeout 10 --speed-limit 1024 --speed-time 60 --max-time 900 --max-filesize ($size + 1) --continue-at - --output $path $url
    if ($LASTEXITCODE -eq 0) {
      break
    }
    if ($attempt -ge 10) {
      throw "download failed after $attempt attempts: $url"
    }
    Log "download attempt $attempt failed with curl exit $LASTEXITCODE; retrying"
    Start-Sleep -Seconds ([Math]::Min(30, 2 * $attempt))
  }
  $actualSize = (Get-Item -LiteralPath $path).Length
  if ($actualSize -ne $size) {
    throw "$name size is $actualSize, want $size"
  }
  $actualChecksum = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
  if ($actualChecksum -ne $sha256) {
    throw "$name SHA-256 is $actualChecksum, want $sha256"
  }
  return $path
}

function Invoke-Checked([string]$description, [string]$file, [string[]]$arguments, [int[]]$successCodes = @(0)) {
  Log $description
  $process = Start-Process -FilePath $file -ArgumentList $arguments -Wait -PassThru -NoNewWindow
  if ($successCodes -notcontains $process.ExitCode) {
    throw "$description failed with exit code $($process.ExitCode)"
  }
}

function Add-MachinePath([string[]]$directories) {
  $entries = [Environment]::GetEnvironmentVariable('Path', 'Machine').Split(';') | Where-Object { $_ }
  foreach ($directory in $directories) {
    if ($entries -notcontains $directory) {
      $entries += $directory
    }
  }
  [Environment]::SetEnvironmentVariable('Path', ($entries -join ';'), 'Machine')
}

function Assert-Output([string]$description, [string]$file, [string[]]$arguments, [string]$pattern) {
  $output = ((& $file @arguments 2>&1) | Out-String).Trim()
  if ($LASTEXITCODE -ne 0 -or $output -notmatch $pattern) {
    throw "$description verification failed: $output"
  }
  Log "$description`: $output"
}

# Runner directories and hosted-runner compatible environment.
foreach ($directory in @($actionsRunnerRoot, $runnerRoot, (Join-Path $runnerRoot 'work'), $toolCache, $metadataRoot)) {
  New-Item -ItemType Directory -Force -Path $directory | Out-Null
}
New-ItemProperty -Path 'HKLM:\SYSTEM\CurrentControlSet\Control\FileSystem' -Name LongPathsEnabled -Value 1 -PropertyType DWord -Force | Out-Null
[Environment]::SetEnvironmentVariable('RUNNER_TOOL_CACHE', $toolCache, 'Machine')
[Environment]::SetEnvironmentVariable('AGENT_TOOLSDIRECTORY', $toolCache, 'Machine')
[Environment]::SetEnvironmentVariable('ImageOS', 'win11', 'Machine')
[Environment]::SetEnvironmentVariable('ImageVersion', (Pin 'TEMPLATE_VERSION'), 'Machine')

# Git for Windows. Git\bin provides git, bash, and sh like GitHub-hosted
# Windows images, without shadowing Windows tools with Git\usr\bin.
$gitVersion = Pin 'GIT_VERSION'
$gitInstaller = Get-CheckedDownload 'git-installer.exe' (Pin 'GIT_INSTALLER_URL') (Pin 'GIT_INSTALLER_SHA256') ([long](Pin 'GIT_INSTALLER_SIZE'))
Invoke-Checked "installing Git for Windows $gitVersion" $gitInstaller @('/VERYSILENT', '/NORESTART', '/NOCANCEL', '/SP-', '/SUPPRESSMSGBOXES', '/CLOSEAPPLICATIONS', '/o:PathOption=Cmd', '/o:CRLFOption=CRLFAlways')
Add-MachinePath @($gitCmd, $gitBin)
$gitMarketingVersion = ($gitVersion -replace '^(\d+\.\d+\.\d+)\.\d+$', '$1')
Assert-Output 'git' (Join-Path $gitCmd 'git.exe') @('--version') ([regex]::Escape("git version $gitMarketingVersion"))
Assert-Output 'bash' (Join-Path $gitBin 'bash.exe') @('-c', 'echo bash-ok') '^bash-ok$'

# PowerShell 7 (pwsh), the default `run` shell on GitHub-hosted Windows runners.
$powerShellVersion = Pin 'POWERSHELL_VERSION'
$powerShellMSI = Get-CheckedDownload 'powershell.msi' (Pin 'POWERSHELL_MSI_URL') (Pin 'POWERSHELL_MSI_SHA256') ([long](Pin 'POWERSHELL_MSI_SIZE'))
Invoke-Checked "installing PowerShell $powerShellVersion" 'msiexec.exe' @('/i', "`"$powerShellMSI`"", '/qn', '/norestart', 'ADD_PATH=1', 'USE_MU=0', 'ENABLE_MU=0', 'DISABLE_TELEMETRY=1') @(0, 3010)
Add-MachinePath @($pwshRoot)
Assert-Output 'pwsh' (Join-Path $pwshRoot 'pwsh.exe') @('-NoLogo', '-NoProfile', '-Command', '$PSVersionTable.PSVersion.ToString()') ("^" + [regex]::Escape($powerShellVersion) + "$")

# GitHub Actions Runner.
$runnerVersion = Pin 'RUNNER_WIN_X64_VERSION'
$runnerArchive = Get-CheckedDownload 'actions-runner.zip' "https://github.com/actions/runner/releases/download/v$runnerVersion/actions-runner-win-x64-$runnerVersion.zip" (Pin 'RUNNER_WIN_X64_ARCHIVE_SHA256') ([long](Pin 'RUNNER_WIN_X64_ARCHIVE_SIZE'))
Log "installing GitHub Actions runner $runnerVersion"
Get-ChildItem -LiteralPath $actionsRunnerRoot -Force | Remove-Item -Recurse -Force
Expand-Archive -LiteralPath $runnerArchive -DestinationPath $actionsRunnerRoot -Force
Assert-Output 'Runner.Listener' (Join-Path $actionsRunnerRoot 'bin\Runner.Listener.exe') @('--version') ("^" + [regex]::Escape($runnerVersion) + "$")

# Template identity read by runnerd and the start script.
$imageEnvironment = @(
  "IMAGE_VERSION=$(Pin 'TEMPLATE_VERSION')",
  "ImageVersion=$(Pin 'TEMPLATE_VERSION')",
  'ImageOS=win11',
  "TEMPLATE_NAME=$(Pin 'TEMPLATE_NAME')"
)
Set-Content -LiteralPath (Join-Path $metadataRoot 'image.env') -Encoding ASCII -Value $imageEnvironment

Remove-Item -LiteralPath $downloadRoot -Recurse -Force -ErrorAction SilentlyContinue
$freeGiB = [Math]::Round((Get-PSDrive C).Free / 1GB, 1)
Log "template setup complete; C: free ${freeGiB} GiB"
