# escape=`
# GitHub runner Windows 11 Qiniu sandbox template.
#
# qshell.sandbox.toml sets from_template = "windows11", so qshell builds on
# the provider's Windows 11 template and ignores this FROM line. qshell always
# prepends USER root and WORKDIR /, and appends USER user and WORKDIR
# /home/user unless the Dockerfile sets its own. The Windows builder ignores
# USER and runs RUN steps through cmd.exe as the console user, so the explicit
# USER user and WORKDIR C:\runner only replace those Linux defaults.
FROM windows11

USER user
COPY github-runner-windows-11/scripts/setup-template.ps1 C:\qiniu-build\setup-template.ps1
COPY github-runner-windows-11/template.env C:\qiniu-build\build.env
RUN powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File C:\qiniu-build\setup-template.ps1 -BuildEnvironmentFile C:\qiniu-build\build.env
RUN rmdir /s /q C:\qiniu-build
WORKDIR C:\runner
