#!/usr/bin/env node

import { createHash, randomUUID } from "node:crypto";
import {
  appendFile,
  readFile,
  rename,
  stat,
  unlink,
  writeFile,
} from "node:fs/promises";
import { createReadStream } from "node:fs";
import { basename, dirname, join, resolve } from "node:path";
import { Readable, Writable } from "node:stream";
import { pipeline } from "node:stream/promises";

const repositoryRoot = resolve(import.meta.dirname, "..");
const latestReleaseApi = "https://api.github.com/repos/actions/runner/releases/latest";
const requiredPinKeys = [
  "RUNNER_VERSION",
  "RUNNER_ARCHIVE_SHA256",
  "RUNNER_ARCHIVE_SIZE",
];
const maxRunnerArchiveSize = 256 * 1024 * 1024;

function fail(message) {
  throw new Error(message);
}

function parseArguments(argv) {
  const options = {
    pinFile: join(repositoryRoot, "templates", "common", "actions-runner.env"),
    githubOutput: process.env.GITHUB_OUTPUT,
  };
  const names = new Map([
    ["--release-json", "releaseJson"],
    ["--asset-file", "assetFile"],
    ["--pin-file", "pinFile"],
    ["--github-output", "githubOutput"],
  ]);
  const seen = new Set();
  for (let index = 0; index < argv.length; index += 2) {
    const name = argv[index];
    const key = names.get(name);
    const value = argv[index + 1];
    if (!key || value === undefined || value.startsWith("--")) {
      fail(`usage: ${basename(process.argv[1])} [--release-json PATH] [--asset-file PATH] [--pin-file PATH] [--github-output PATH]`);
    }
    if (seen.has(name)) {
      fail(`duplicate option ${name}`);
    }
    seen.add(name);
    options[key] = resolve(value);
  }
  return options;
}

function parseVersion(value, description) {
  const match = /^(\d+)\.(\d+)\.(\d+)$/.exec(value);
  if (!match) {
    fail(`${description} must be MAJOR.MINOR.PATCH, got ${JSON.stringify(value)}`);
  }
  const parts = match.slice(1).map(Number);
  if (parts.some((part) => !Number.isSafeInteger(part))) {
    fail(`${description} contains an unsafe integer`);
  }
  return parts;
}

function compareVersions(left, right) {
  for (let index = 0; index < left.length; index += 1) {
    if (left[index] !== right[index]) {
      return left[index] < right[index] ? -1 : 1;
    }
  }
  return 0;
}

function parsePin(contents) {
  const values = new Map();
  for (const line of contents.split(/\r?\n/)) {
    if (line === "") continue;
    const match = /^([A-Z0-9_]+)=(.*)$/.exec(line);
    if (!match || !requiredPinKeys.includes(match[1])) {
      fail(`pin contains unsupported line ${JSON.stringify(line)}`);
    }
    if (values.has(match[1])) {
      fail(`pin contains duplicate ${match[1]}`);
    }
    values.set(match[1], match[2]);
  }
  for (const key of requiredPinKeys) {
    if (!values.has(key)) fail(`pin is missing ${key}`);
  }
  const version = values.get("RUNNER_VERSION");
  parseVersion(version, "pinned Runner version");
  const digest = values.get("RUNNER_ARCHIVE_SHA256");
  if (!/^[0-9a-f]{64}$/.test(digest)) {
    fail("pinned Runner archive SHA-256 must be 64 lowercase hexadecimal characters");
  }
  const sizeText = values.get("RUNNER_ARCHIVE_SIZE");
  const size = Number(sizeText);
  if (!/^[1-9]\d*$/.test(sizeText) || !Number.isSafeInteger(size)) {
    fail("pinned Runner archive size must be a positive safe integer");
  }
  return { version, digest, size };
}

async function loadLatestRelease(path) {
  if (path) {
    return JSON.parse(await readFile(path, "utf8"));
  }
  const headers = {
    Accept: "application/vnd.github+json",
    "User-Agent": "qiniu-ci-runner-actions-runner-updater",
    "X-GitHub-Api-Version": "2022-11-28",
  };
  if (process.env.GITHUB_TOKEN) {
    headers.Authorization = `Bearer ${process.env.GITHUB_TOKEN}`;
  }
  const response = await fetch(latestReleaseApi, {
    headers,
    signal: AbortSignal.timeout(30_000),
  });
  if (!response.ok) {
    fail(`GitHub latest release request failed with HTTP ${response.status}`);
  }
  return response.json();
}

function validateRelease(release) {
  // Fail closed unless GitHub explicitly identifies the release as stable.
  if (release?.draft !== false || release?.prerelease !== false) {
    fail("latest release must be stable, not a draft or prerelease");
  }
  const tagMatch = /^v(\d+\.\d+\.\d+)$/.exec(release?.tag_name ?? "");
  if (!tagMatch) {
    fail(`latest release tag must be vMAJOR.MINOR.PATCH, got ${JSON.stringify(release?.tag_name)}`);
  }
  const version = tagMatch[1];
  const versionParts = parseVersion(version, "latest Runner version");
  const releaseUrl = `https://github.com/actions/runner/releases/tag/v${version}`;
  if (release.html_url !== releaseUrl) {
    fail("release URL is not canonical for actions/runner");
  }
  if (
    typeof release.published_at !== "string" ||
    !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z$/.test(release.published_at) ||
    !Number.isFinite(Date.parse(release.published_at))
  ) {
    fail("release published_at must be an RFC 3339 UTC timestamp");
  }
  const assetName = `actions-runner-linux-x64-${version}.tar.gz`;
  const matchingAssets = Array.isArray(release.assets)
    ? release.assets.filter((asset) => asset?.name === assetName)
    : [];
  if (matchingAssets.length !== 1) {
    fail(`expected exactly one ${assetName} asset, found ${matchingAssets.length}`);
  }
  const asset = matchingAssets[0];
  const assetUrl =
    `https://github.com/actions/runner/releases/download/v${version}/${assetName}`;
  if (asset.browser_download_url !== assetUrl) {
    fail("asset download URL is not canonical for actions/runner");
  }
  const digestMatch = /^sha256:([0-9a-f]{64})$/.exec(asset.digest ?? "");
  if (!digestMatch) {
    fail("asset digest must be sha256 followed by 64 lowercase hexadecimal characters");
  }
  if (!Number.isSafeInteger(asset.size) || asset.size <= 0) {
    fail("asset size must be a positive integer");
  }
  if (asset.size > maxRunnerArchiveSize) {
    fail(
      `asset size ${asset.size} exceeds managed template maximum ${maxRunnerArchiveSize}`,
    );
  }
  return {
    version,
    versionParts,
    releaseUrl,
    publishedAt: release.published_at,
    assetUrl,
    digest: digestMatch[1],
    size: asset.size,
  };
}

async function archiveSource(assetFile, assetUrl) {
  if (assetFile) {
    return createReadStream(assetFile);
  }
  const response = await fetch(assetUrl, {
    headers: { "User-Agent": "qiniu-ci-runner-actions-runner-updater" },
    redirect: "follow",
    signal: AbortSignal.timeout(300_000),
  });
  if (!response.ok || !response.body) {
    fail(`Actions Runner archive download failed with HTTP ${response.status}`);
  }
  return Readable.fromWeb(response.body);
}

async function downloadAndVerify(assetFile, release) {
  const hash = createHash("sha256");
  let size = 0;
  const verifier = new Writable({
    write(chunk, _encoding, callback) {
      size += chunk.length;
      if (size > release.size) {
        callback(
          new Error(`downloaded archive exceeded release metadata size ${release.size}`),
        );
        return;
      }
      hash.update(chunk);
      callback();
    },
  });
  const source = await archiveSource(assetFile, release.assetUrl);
  await pipeline(source, verifier);
  const digest = hash.digest("hex");
  if (size !== release.size) {
    fail(`downloaded archive size ${size} does not match release metadata ${release.size}`);
  }
  if (digest !== release.digest) {
    fail(`downloaded archive SHA-256 ${digest} does not match release metadata ${release.digest}`);
  }
  return { digest, size };
}

function pinContents(version, digest, size) {
  return [
    `RUNNER_VERSION=${version}`,
    `RUNNER_ARCHIVE_SHA256=${digest}`,
    `RUNNER_ARCHIVE_SIZE=${size}`,
    "",
  ].join("\n");
}

async function writePinAtomically(pinFile, contents) {
  const temporaryPath = join(
    dirname(pinFile),
    `.${basename(pinFile)}.${process.pid}.${randomUUID()}.tmp`,
  );
  let writeError;
  try {
    const current = await stat(pinFile);
    await writeFile(temporaryPath, contents, {
      encoding: "utf8",
      flag: "wx",
      mode: current.mode & 0o777,
    });
    await rename(temporaryPath, pinFile);
  } catch (error) {
    writeError = error;
    throw error;
  } finally {
    await unlink(temporaryPath).catch((error) => {
      if (error.code === "ENOENT") return;
      if (!writeError) throw error;
      process.stderr.write(
        `actions runner update: cleanup failed after write error: ${error.message}\n`,
      );
    });
  }
}

async function emitResult(result, githubOutput) {
  process.stdout.write(`${JSON.stringify(result)}\n`);
  if (!githubOutput) return;
  for (const [key, value] of Object.entries(result)) {
    const text = String(value);
    if (text.includes("\n") || text.includes("\r")) {
      fail(`cannot emit multiline GitHub Actions output ${key}`);
    }
    await appendFile(githubOutput, `${key}=${text}\n`, "utf8");
  }
}

async function main() {
  const options = parseArguments(process.argv.slice(2));
  const current = parsePin(await readFile(options.pinFile, "utf8"));
  const release = validateRelease(await loadLatestRelease(options.releaseJson));
  const comparison = compareVersions(
    release.versionParts,
    parseVersion(current.version, "pinned Runner version"),
  );
  if (comparison < 0) {
    fail(`refusing to downgrade from ${current.version} to ${release.version}`);
  }
  const result = {
    changed: comparison > 0,
    version: release.version,
    release_url: release.releaseUrl,
    published_at: release.publishedAt,
  };
  if (comparison === 0) {
    if (current.digest !== release.digest || current.size !== release.size) {
      fail(
        `release metadata for current Runner ${current.version} differs from the pinned archive`,
      );
    }
    await emitResult(result, options.githubOutput);
    return;
  }
  const verified = await downloadAndVerify(options.assetFile, release);
  await writePinAtomically(
    options.pinFile,
    pinContents(release.version, verified.digest, verified.size),
  );
  await emitResult(result, options.githubOutput);
}

main().catch((error) => {
  process.stderr.write(`actions runner update: ${error.message}\n`);
  process.exitCode = 1;
});
