#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { basename, dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const scriptDirectory = dirname(fileURLToPath(import.meta.url));
const repositoryRoot = resolve(scriptDirectory, "..");
const lockPath =
  process.env.RUNNER_IMAGES_LOCK ??
  join(repositoryRoot, "templates", "runner-images-upstream.lock.json");
const outputPath =
  process.env.RUNNER_IMAGES_MANIFEST ??
  join(repositoryRoot, "templates", "runner-images-compatibility.json");
const parserPath = join(
  repositoryRoot,
  "scripts",
  "lib",
  "parse-runner-image-report.awk",
);
const runnerPin = readFileSync(
  join(repositoryRoot, "templates", "common", "actions-runner.env"),
  "utf8",
);
const runnerVersion = runnerPin.match(/^RUNNER_VERSION=([0-9]+\.[0-9]+\.[0-9]+)$/m)?.[1];
if (!runnerVersion) {
  throw new Error("templates/common/actions-runner.env must pin RUNNER_VERSION");
}
const lock = JSON.parse(readFileSync(lockPath, "utf8"));
const reportDirectory = process.env.RUNNER_IMAGES_REPORT_DIR;
const workspace = mkdtempSync(join(tmpdir(), "runner-images-compatibility-"));

const commandByName = {
  ".NET Core SDK": "command -v dotnet >/dev/null",
  "Alibaba Cloud CLI": "command -v aliyun >/dev/null",
  Ansible: "command -v ansible >/dev/null",
  Ant: "command -v ant >/dev/null",
  "apt-fast": "command -v apt-fast >/dev/null",
  "AWS CLI": "command -v aws >/dev/null",
  "AWS CLI Session Manager Plugin":
    "command -v session-manager-plugin >/dev/null",
  "AWS SAM CLI": "command -v sam >/dev/null",
  "Azure CLI": "command -v az >/dev/null",
  "Azure CLI (azure-devops)":
    "az extension show --name azure-devops >/dev/null",
  AzCopy: "command -v azcopy >/dev/null && command -v azcopy10 >/dev/null",
  Bash: "command -v bash >/dev/null",
  Bazel: "command -v bazel >/dev/null",
  Bazelisk: "command -v bazelisk >/dev/null",
  Bicep: "command -v bicep >/dev/null",
  Bindgen: "command -v bindgen >/dev/null",
  Buildah: "command -v buildah >/dev/null",
  Cabal: "command -v cabal >/dev/null",
  Cargo: "command -v cargo >/dev/null",
  "Cargo audit": "cargo audit --version >/dev/null",
  "Cargo clippy": "cargo clippy --version >/dev/null",
  "Cargo outdated": "cargo outdated --version >/dev/null",
  Cbindgen: "command -v cbindgen >/dev/null",
  "ChromeDriver": "command -v chromedriver >/dev/null",
  Chromium: "command -v chromium >/dev/null || command -v chromium-browser >/dev/null",
  Clang: "command -v clang >/dev/null",
  "Clang-format": "command -v clang-format >/dev/null",
  "Clang-tidy": "command -v clang-tidy >/dev/null",
  CMake: "command -v cmake >/dev/null",
  "CodeQL Action Bundle": "command -v codeql >/dev/null",
  Composer: "command -v composer >/dev/null",
  cpan: "command -v cpan >/dev/null",
  Dash: "command -v dash >/dev/null",
  "Docker Amazon ECR Credential Helper":
    "command -v docker-credential-ecr-login >/dev/null",
  "Docker Client": "docker --version >/dev/null",
  "Docker Compose": "docker compose version >/dev/null",
  "Docker Compose v2": "docker compose version >/dev/null",
  "Docker Server": "ensure-docker && docker info >/dev/null",
  "Docker-Buildx": "docker buildx version >/dev/null",
  Fastlane: "command -v fastlane >/dev/null",
  GHC: "command -v ghc >/dev/null",
  GHCup: "command -v ghcup >/dev/null",
  Geckodriver: "command -v geckodriver >/dev/null",
  Git: "command -v git >/dev/null",
  "Git LFS": "git lfs version >/dev/null",
  "Git-ftp": "command -v git-ftp >/dev/null",
  "GitHub CLI": "command -v gh >/dev/null",
  "GNU C++": "command -v g++ >/dev/null",
  "GNU Fortran": "command -v gfortran >/dev/null",
  "Google Chrome": "command -v google-chrome >/dev/null",
  "Google Cloud CLI": "command -v gcloud >/dev/null",
  Gradle: "command -v gradle >/dev/null",
  Haveged: "command -v haveged >/dev/null",
  Helm: "command -v helm >/dev/null",
  Heroku: "command -v heroku >/dev/null",
  Homebrew: "test -x /home/linuxbrew/.linuxbrew/bin/brew",
  jq: "command -v jq >/dev/null",
  Julia: "command -v julia >/dev/null",
  Kind: "command -v kind >/dev/null",
  Kotlin: "command -v kotlin >/dev/null",
  Kubectl: "command -v kubectl >/dev/null",
  Kustomize: "command -v kustomize >/dev/null",
  Leiningen: "command -v lein >/dev/null",
  Lerna: "command -v lerna >/dev/null",
  Maven: "command -v mvn >/dev/null",
  MediaInfo: "command -v mediainfo >/dev/null",
  Mercurial: "command -v hg >/dev/null",
  "Microsoft Edge": "command -v microsoft-edge >/dev/null",
  "Microsoft Edge WebDriver": "command -v msedgedriver >/dev/null",
  Miniconda: "test -x /usr/share/miniconda/bin/conda",
  Minikube: "command -v minikube >/dev/null",
  MSBuild: "command -v msbuild >/dev/null",
  Mono: "command -v mono >/dev/null",
  "Mozilla Firefox": "command -v firefox >/dev/null",
  MySQL: "command -v mysql >/dev/null && command -v mysqld >/dev/null",
  n: "command -v n >/dev/null",
  nbgv: "command -v nbgv >/dev/null",
  Netlify: "command -v netlify >/dev/null",
  "Netlify CLI": "command -v netlify >/dev/null",
  Newman: "command -v newman >/dev/null",
  Ninja: "command -v ninja >/dev/null",
  "Node.js": "command -v node >/dev/null",
  Npm: "command -v npm >/dev/null",
  NuGet: "command -v nuget >/dev/null",
  nvm: "test -s /home/runner/.nvm/nvm.sh",
  "OpenShift CLI": "command -v oc >/dev/null",
  OpenSSL: "command -v openssl >/dev/null",
  "ORAS CLI": "command -v oras >/dev/null",
  Packer: "command -v packer >/dev/null",
  Parcel: "command -v parcel >/dev/null",
  Perl: "command -v perl >/dev/null",
  PHP: "command -v php >/dev/null",
  PHPUnit: "command -v phpunit >/dev/null",
  Pip: "command -v pip >/dev/null",
  Pip3: "command -v pip3 >/dev/null",
  Pipx: "command -v pipx >/dev/null",
  Podman:
    'command -v podman >/dev/null || exit 1; podman_network="qiniu-conformance-$$-${RANDOM}"; cleanup_podman_network() { podman network rm "$podman_network" >/dev/null 2>&1 || true; }; trap cleanup_podman_network EXIT; podman network create -d bridge "$podman_network" >/dev/null || exit 1; podman network ls --format "{{.Name}}" | grep -Fx "$podman_network" >/dev/null || exit 1; podman network rm "$podman_network" >/dev/null || exit 1; trap - EXIT',
  PostgreSQL:
    "command -v psql >/dev/null && command -v postgres >/dev/null",
  PowerShell: "command -v pwsh >/dev/null",
  Pulumi: "command -v pulumi >/dev/null",
  Python: "command -v python3 >/dev/null",
  R: "command -v R >/dev/null",
  Ruby: "command -v ruby >/dev/null",
  RubyGems: "command -v gem >/dev/null",
  Rust: "command -v rustc >/dev/null",
  Rustdoc: "command -v rustdoc >/dev/null",
  Rustfmt: "command -v rustfmt >/dev/null",
  Rustup: "command -v rustup >/dev/null",
  Sbt: "command -v sbt >/dev/null",
  "Selenium server": "test -f /usr/share/java/selenium-server.jar",
  Skopeo: "command -v skopeo >/dev/null",
  Sphinx: "command -v searchd >/dev/null",
  "Sphinx Open Source Search Server": "command -v searchd >/dev/null",
  SqlPackage: "command -v sqlpackage >/dev/null",
  sqlcmd: "command -v sqlcmd >/dev/null",
  sqlite3: "command -v sqlite3 >/dev/null",
  Stack: "command -v stack >/dev/null",
  SVN: "command -v svn >/dev/null",
  Swift: "command -v swift >/dev/null",
  Terraform: "command -v terraform >/dev/null",
  Vcpkg: "test -x /usr/local/share/vcpkg/vcpkg",
  "Vercel CLI": "command -v vercel >/dev/null",
  yamllint: "command -v yamllint >/dev/null",
  Yarn: "command -v yarn >/dev/null",
  yq: "command -v yq >/dev/null",
  zstd: "command -v zstd >/dev/null",
};

function sha256(contents) {
  return createHash("sha256").update(contents).digest("hex");
}

function shellQuote(value) {
  return `'${String(value).replaceAll("'", `'\"'\"'`)}'`;
}

function verificationFor(imageKey, item) {
  const { category, upstream_name: name, upstream_value: value } = item;
  if (category === "Image metadata") {
    if (name === "OS Version") {
      const release =
        imageKey === "ubuntu-22.04"
          ? "22.04"
          : imageKey === "ubuntu-26.04"
            ? "26.04"
            : "24.04";
      return `. /etc/os-release; test "$VERSION_ID" = ${shellQuote(release)}`;
    }
    if (name === "Kernel Version") {
      return `test "$(uname -r)" = ${shellQuote(value)}`;
    }
    if (name === "Image Version") {
      return 'test -n "${ImageVersion:-}" && test -n "${IMAGE_VERSION:-}"';
    }
    if (name === "Systemd version") {
      return "test -x /usr/lib/systemd/systemd && /usr/lib/systemd/systemd --version >/dev/null";
    }
  }
  if (category === "Installed apt packages") {
    if (name === "upx") {
      return "command -v upx >/dev/null";
    }
    if (
      name === "netcat" &&
      (imageKey === "ubuntu-24.04" || imageKey === "ubuntu-26.04")
    ) {
      return "dpkg-query -W -f='${Status}' 'netcat-openbsd' | grep -qx 'install ok installed' && command -v netcat >/dev/null";
    }
    return (
      "dpkg-query -W -f='${Status}' " +
      `${shellQuote(name)} | grep -qx 'install ok installed'`
    );
  }
  if (category.includes("Environment variables")) {
    if (value === "") {
      return `test -z "\${${name}:-}"`;
    }
    return `test "\${${name}:-}" = ${shellQuote(value)}`;
  }
  if (category.startsWith("Cached Tools/")) {
    const tool = category.slice("Cached Tools/".length);
    return `find ${shellQuote(`/opt/hostedtoolcache/${tool}`)} -mindepth 1 -maxdepth 2 -type d -name ${shellQuote(`${name}*`)} | grep -q .`;
  }
  if (category === "PowerShell Tools/PowerShell Modules") {
    return `pwsh -NoLogo -NoProfile -Command ${shellQuote(`if (-not (Get-Module -ListAvailable -Name '${name}')) { exit 1 }`)}`;
  }
  if (category === "Web Servers") {
    return (
      "dpkg-query -W -f='${Status}' " +
      `${shellQuote(name)} | grep -qx 'install ok installed'`
    );
  }
  if (category === "Android") {
    const checks = {
      "Android Command Line Tools":
        "test -x /usr/local/lib/android/sdk/cmdline-tools/latest/bin/sdkmanager",
      "Android SDK Build-tools":
        "test -d /usr/local/lib/android/sdk/build-tools",
      "Android SDK Platform-Tools":
        "test -x /usr/local/lib/android/sdk/platform-tools/adb",
      "Android SDK Platforms":
        "test -d /usr/local/lib/android/sdk/platforms",
      "Android Support Repository":
        "test -d /usr/local/lib/android/sdk/extras/android/m2repository",
      CMake: "test -d /usr/local/lib/android/sdk/cmake",
      "Google Play services":
        "test -d /usr/local/lib/android/sdk/extras/google/google_play_services",
      "Google Repository":
        "test -d /usr/local/lib/android/sdk/extras/google/m2repository",
      NDK: "test -d /usr/local/lib/android/sdk/ndk",
    };
    return checks[name];
  }
  if (category === "Rust Tools/Packages" && commandByName[name]) {
    return commandByName[name];
  }
  const command = commandByName[name];
  if (!command) {
    throw new Error(`No concrete verification command for ${category} / ${name}`);
  }
  return command;
}

const versionedExtras = new Set([
  "Ansible",
  "Buildah",
  "Docker Compose",
  "Docker Server",
  "Ninja",
  "Pester",
  "Podman",
  "Skopeo",
  "apache2",
  "yamllint",
]);

function entryKey(entry) {
  return `${entry.category}\0${entry.upstream_name}`;
}

function compatibilityEntry(imageKey, parsed, slimEntryKeys) {
  const kernelExcluded = parsed.upstream_name === "Kernel Version";
  const fullInventoryExcluded =
    imageKey !== "ubuntu-slim" &&
    !slimEntryKeys.has(entryKey(parsed)) &&
    !versionedExtras.has(parsed.upstream_name);
  const excluded = kernelExcluded || fullInventoryExcluded;
  return {
    category: parsed.category,
    kind: parsed.kind,
    upstream_name: parsed.upstream_name,
    upstream_value: parsed.upstream_value,
    status: excluded ? "excluded" : "provided",
    verification: verificationFor(imageKey, parsed),
    ...(excluded
      ? {
          reason: kernelExcluded
            ? "Qiniu Sandbox containers share the Sandbox host kernel; an image template cannot install or select the pinned Azure host kernel build."
            : "The current Qiniu Sandbox public-template build allocation exposes a 22,222 MiB root disk, and the full GitHub-hosted runner inventory exceeds that limit. This public template guarantees the pinned Ubuntu Slim-compatible core on the requested OS release plus documented extensions; this full-image-only item is not guaranteed and should be installed at job time or included in a custom template.",
        }
      : {}),
  };
}

function contractEntries(imageKey) {
  const templateName = {
    "ubuntu-slim": "github-runner-ubuntu-slim",
    "ubuntu-22.04": "github-runner-ubuntu-22-04",
    "ubuntu-24.04": "github-runner-ubuntu-24-04",
    "ubuntu-26.04": "github-runner-ubuntu-26-04",
  }[imageKey];
  return [
    {
      category: "Qiniu runner contract",
      kind: "path",
      upstream_name: "runner user and home",
      upstream_value: "/home/runner",
      status: "provided",
      verification:
        "test \"$(getent passwd runner | cut -d: -f6)\" = /home/runner",
    },
    {
      category: "Qiniu runner contract",
      kind: "path",
      upstream_name: "runner work",
      upstream_value: "/home/runner/work",
      status: "provided",
      verification:
        "test -d /home/runner/work && test \"$(stat -c %U /home/runner/work)\" = runner",
    },
    {
      category: "Qiniu runner contract",
      kind: "path",
      upstream_name: "hosted tool cache",
      upstream_value: "/opt/hostedtoolcache",
      status: "provided",
      verification:
        "test -d /opt/hostedtoolcache && test -w /opt/hostedtoolcache",
    },
    {
      category: "Qiniu runner contract",
      kind: "environment",
      upstream_name: "runner tool cache environment",
      upstream_value: "/opt/hostedtoolcache",
      status: "provided",
      verification:
        'test "$RUNNER_TOOL_CACHE" = /opt/hostedtoolcache && test "$AGENT_TOOLSDIRECTORY" = /opt/hostedtoolcache',
    },
    {
      category: "Qiniu runner contract",
      kind: "software",
      upstream_name: "preinstalled GitHub Actions runner",
      upstream_value: `/opt/actions-runner (${runnerVersion})`,
      status: "provided",
      verification:
        `test -x /opt/actions-runner/config.sh && test -x /opt/actions-runner/run.sh && test "$(/opt/actions-runner/bin/Runner.Listener --version)" = ${runnerVersion}`,
    },
    {
      category: "Qiniu runner contract",
      kind: "service",
      upstream_name: "non-root Docker socket",
      upstream_value: "docker group 0660",
      status: "provided",
      verification:
        "ensure-docker && test \"$(stat -c %a /var/run/docker.sock)\" = 660 && test \"$(stat -c %G /var/run/docker.sock)\" = docker",
    },
    {
      category: "Qiniu runner contract",
      kind: "environment",
      upstream_name: "template identification",
      upstream_value: templateName,
      status: "provided",
      verification: `test "$IMAGE_TEMPLATE" = ${shellQuote(templateName)} && test -n "$ImageVersion"`,
    },
    {
      category: "Qiniu runner contract",
      kind: "service",
      upstream_name: "systemd PID 1 service control",
      upstream_value: "systemd",
      status: "excluded",
      verification: 'test "$(ps -p 1 -o comm= | tr -d " ")" = systemd',
      reason:
        "The Qiniu Sandbox runtime starts the job container without systemd as PID 1; installed services use direct process startup and are disabled by default.",
    },
  ];
}

try {
  const parsedByImage = {};
  for (const imageKey of [
    "ubuntu-slim",
    "ubuntu-22.04",
    "ubuntu-24.04",
    "ubuntu-26.04",
  ]) {
    const reportPath = lock.reports[imageKey];
    const reportName = basename(reportPath);
    const localReport = join(workspace, reportName);
    const reportContents = reportDirectory
      ? readFileSync(join(reportDirectory, reportName))
      : execFileSync("curl", [
          "-fsSL",
          "--retry",
          "3",
          `https://raw.githubusercontent.com/${lock.repository}/${lock.commit}/${reportPath}`,
        ]);
    if (sha256(reportContents) !== lock.report_sha256[imageKey]) {
      throw new Error(`${imageKey} report checksum does not match the upstream lock`);
    }
    writeFileSync(localReport, reportContents);
    const parsed = execFileSync("awk", ["-f", parserPath, localReport], {
      encoding: "utf8",
    })
      .trim()
      .split("\n")
      .filter(Boolean)
      .map((line) => {
        const [category, upstream_name, upstream_value, kind] = line.split("\t");
        return { category, upstream_name, upstream_value, kind };
      });
    parsedByImage[imageKey] = {
      reportPath,
      entries: parsed,
    };
  }

  const slimEntryKeys = new Set(
    parsedByImage["ubuntu-slim"].entries.map(entryKey),
  );
  const images = {};
  for (const imageKey of [
    "ubuntu-slim",
    "ubuntu-22.04",
    "ubuntu-24.04",
    "ubuntu-26.04",
  ]) {
    const { reportPath, entries } = parsedByImage[imageKey];
    images[imageKey] = {
      report_path: reportPath,
      template_directory: `templates/github-runner-${imageKey}`,
      entries: [
        ...entries.map((entry) =>
          compatibilityEntry(imageKey, entry, slimEntryKeys),
        ),
        ...contractEntries(imageKey),
      ],
    };
  }
  const manifest = {
    schema_version: 1,
    upstream_repository: lock.repository,
    upstream_commit: lock.commit,
    generated_from: "pinned upstream reports; regenerate with this script",
    images,
  };
  writeFileSync(outputPath, `${JSON.stringify(manifest, null, 2)}\n`);
  process.stdout.write(`wrote ${outputPath}\n`);
} finally {
  rmSync(workspace, { recursive: true, force: true });
}
