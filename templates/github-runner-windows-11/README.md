# GitHub runner Windows 11 template

This 8-vCPU, 8192-MiB Windows 11 x64 template is built with qshell on top of
the provider's `windows11` template. Windows Sandbox templates cannot start from
a Docker image, so `qshell.sandbox.toml` sets `from_template = "windows11"` and
qshell ignores the Dockerfile `FROM` line. Windows requires 50 GiB free on C: at release.
New templates request 53,760 MiB, including 5% extra for filesystem overhead. Same-name rebuilds retain the existing disk setting.
The Windows preview has a separate acceptance step. Check the system drive in a fresh Windows Sandbox before promotion:

```powershell
if ((Get-PSDrive C).Free -lt 50GB) { throw 'C: has less than 50 GiB available' }
```

The Dockerfile uses the `` ` `` escape directive so Windows paths keep their
backslashes. qshell always prepends `USER root` and `WORKDIR /`, and appends
`USER user` and `WORKDIR /home/user` unless the Dockerfile sets its own. The
Windows builder ignores `USER` and runs `RUN` steps through `cmd.exe` as the
console user, so the explicit `USER user` and `WORKDIR C:\runner` only replace
those Linux defaults. It copies `scripts/setup-template.ps1` into
`C:\qiniu-build` and `template.env` to `C:\qiniu-build\build.env`, runs the
script with Windows PowerShell 5.1, then removes the directory. Direct installer
and Runner downloads are pinned by URL, size, and SHA-256. MSYS2 packages use
the stable rolling repository with pacman's signature verification. The script installs:

- the GitHub Actions Runner win-x64 archive in `C:\actions-runner`;
- Git for Windows, with `Git\cmd` and `Git\bin` on the machine `PATH`, so
  `git`, `bash`, and `sh` resolve like they do on GitHub-hosted Windows images;
- PowerShell 7 (`pwsh`), the default `run` shell on Windows runners;
- MSYS2 in `C:\msys64`, fully upgraded before installing
  `mingw-w64-ucrt-x86_64-gcc`, the x64 UCRT C/C++ toolchain. Only `C:\msys64\ucrt64\bin`
  is added to machine `PATH`, not MSYS2's `usr\bin`, preserving Git Bash and
  Windows tool resolution. This provides `gcc` and `g++`, without explicitly
  installing Fortran or LLVM. Setup records compiler versions, checks the x64
  target, and compiles/runs C and C++ smoke programs.

Installed package versions are saved in
`C:\ProgramData\qiniu-runner\msys2-packages.txt`. Rebuilds may select newer packages;
retain the tested template version rather than expecting byte-identical rebuilds.
`CC` and `CXX` are not set machine-wide; workflows choose their own compiler settings.

It also creates `C:\runner\work` and `C:\hostedtoolcache`, sets
`RUNNER_TOOL_CACHE`, `AGENT_TOOLSDIRECTORY`, `ImageOS=win11`, and
`ImageVersion` machine-wide, and writes template identity to
`C:\ProgramData\qiniu-runner\image.env`.

Templates are memory snapshots, so envd keeps the environment it had before
setup. runnerd's Windows start script reloads the persisted machine and user
environment before it starts the runner.

The template guarantees only this minimal core. Other GitHub-hosted
windows-2025 tools, Docker, and Visual Studio Build Tools are not provided;
install them in the workflow or build a custom template.

## Build

```bash
QINIU_SANDBOX_API_URL=<sandbox-api-url> QINIU_API_KEY=<api-key> \
  task template-build-windows-11
```

`scripts/build-windows-template.sh` checks the Windows pins, then runs the
shared qshell build helper with `qshell.sandbox.toml`. Neither `template.env`
nor `qshell.sandbox.toml` may contain an environment-specific template ID, and
`TEMPLATE_NAME` must equal the toml `name`. `RUNNER_WIN_X64_VERSION` must equal
`RUNNER_VERSION` in `templates/common/actions-runner.env`; the build helper
fails closed when an Actions Runner update leaves the Windows archive pin
behind. Bump `TEMPLATE_VERSION` when the template contents change.

## Use

Match Windows jobs with a spec whose labels include `windows`, for example
`self-hosted,windows,x64,qiniu,windows-11`. runnerd selects its Windows launch
path from the `windows` label. Windows runners never require Docker.
