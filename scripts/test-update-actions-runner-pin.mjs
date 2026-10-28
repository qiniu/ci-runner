#!/usr/bin/env node

import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";

const repositoryRoot = resolve(import.meta.dirname, "..");
const scriptPath = join(repositoryRoot, "scripts", "update-actions-runner-pin.mjs");

function spawnCli(args, { environment = {}, nodeArguments = [] } = {}) {
  return spawnSync(process.execPath, [...nodeArguments, scriptPath, ...args], {
    cwd: repositoryRoot,
    encoding: "utf8",
    env: { ...process.env, ...environment },
  });
}

function sha256(contents) {
  return createHash("sha256").update(contents).digest("hex");
}

function canonicalAsset(version, contents, overrides = {}) {
  const name = `actions-runner-linux-x64-${version}.tar.gz`;
  return {
    name,
    size: contents.length,
    digest: `sha256:${sha256(contents)}`,
    browser_download_url:
      `https://github.com/actions/runner/releases/download/v${version}/${name}`,
    ...overrides,
  };
}

function windowsArchive(contents) {
  return Buffer.from(`windows-${contents}`);
}

function windowsAsset(version, contents, overrides = {}) {
  const asset = canonicalAsset(version, contents, overrides);
  const name = `actions-runner-win-x64-${version}.zip`;
  return { ...asset, name,
    browser_download_url: `https://github.com/actions/runner/releases/download/v${version}/${name}`,
    ...overrides };
}

function windowsPin(version, contents) {
  return "# Preserve unrelated Windows tooling pins.\nTEMPLATE_NAME=github-runner-windows-11\n" +
    pin(version, contents).replaceAll("RUNNER_", "RUNNER_WIN_X64_") + "GIT_VERSION=2.56.0\n";
}

function stableRelease(version, contents, overrides = {}) {
  return {
    tag_name: `v${version}`,
    draft: false,
    prerelease: false,
    published_at: "2026-09-22T00:00:00Z",
    html_url: `https://github.com/actions/runner/releases/tag/v${version}`,
    assets: [canonicalAsset(version, contents), windowsAsset(version, windowsArchive(contents))],
    ...overrides,
  };
}

function pin(version, contents) {
  return [
    `RUNNER_VERSION=${version}`,
    `RUNNER_ARCHIVE_SHA256=${sha256(contents)}`,
    `RUNNER_ARCHIVE_SIZE=${contents.length}`,
    "",
  ].join("\n");
}

async function fixture(t, { currentVersion = "2.336.0", currentContents = Buffer.from("old-runner") } = {}) {
  const directory = await mkdtemp(join(tmpdir(), "runner-pin-update-test-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const pinPath = join(directory, "actions-runner.env");
  const releasePath = join(directory, "release.json");
  const archivePath = join(directory, "runner.tar.gz");
  const outputPath = join(directory, "github-output");
  const windowsPinPath = join(directory, "template.env");
  const windowsArchivePath = join(directory, "runner.zip");
  await writeFile(windowsPinPath, windowsPin(currentVersion, windowsArchive(currentContents)));
  await writeFile(pinPath, pin(currentVersion, currentContents));
  return { directory, pinPath, releasePath, archivePath, outputPath, windowsPinPath, windowsArchivePath };
}

async function runCli(
  paths,
  release,
  archive,
  {
    additionalArgs = [],
    includeArchive = true,
    environment = {},
    nodeArguments = [],
    windowsContents = windowsArchive(archive),
  } = {},
) {
  await writeFile(paths.releasePath, `${JSON.stringify(release)}\n`);
  if (includeArchive) {
    await writeFile(paths.archivePath, archive);
    await writeFile(paths.windowsArchivePath, windowsContents);
  }
  const args = [
    "--release-json",
    paths.releasePath,
    "--pin-file",
    paths.pinPath,
    "--windows-pin-file",
    paths.windowsPinPath,
    "--github-output",
    paths.outputPath,
  ];
  if (includeArchive) {
    args.push("--asset-file", paths.archivePath, "--windows-asset-file", paths.windowsArchivePath);
  }
  args.push(...additionalArgs);
  return spawnCli(args, { environment, nodeArguments });
}

async function assertRejectedWithoutMutation(paths, release, archive, message, options) {
  const before = await readFile(paths.pinPath);
  const windowsBefore = await readFile(paths.windowsPinPath);
  const result = await runCli(paths, release, archive, options);
  assert.notEqual(result.status, 0, result.stdout);
  assert.deepEqual(await readFile(paths.pinPath), before);
  assert.deepEqual(await readFile(paths.windowsPinPath), windowsBefore);
  assert.match(result.stderr, message);
}

for (const [option, value] of [
  ["--release-json", "releasePath"],
  ["--asset-file", "archivePath"],
  ["--pin-file", "pinPath"],
  ["--windows-pin-file", "windowsPinPath"],
  ["--windows-asset-file", "windowsArchivePath"],
  ["--github-output", "outputPath"],
]) {
  test(`duplicate ${option} is rejected`, async (t) => {
    const archive = Buffer.from("current-runner");
    const paths = await fixture(t, {
      currentVersion: "2.337.0",
      currentContents: archive,
    });

    const result = await runCli(paths, stableRelease("2.337.0", archive), archive, {
      additionalArgs: [option, paths[value]],
    });

    assert.notEqual(result.status, 0, result.stdout);
    assert.match(result.stderr, new RegExp(`duplicate option ${option}`));
  });
}

test("atomic pin write preserves its primary error when cleanup also fails", async (t) => {
  const paths = await fixture(t);
  const archive = Buffer.from("verified-runner-2.337.0");
  const preload = `data:text/javascript,${encodeURIComponent(`
    import fs from "node:fs";
    import { syncBuiltinESMExports } from "node:module";
    fs.promises.rename = async () => { throw new Error("primary rename failure"); };
    fs.promises.unlink = async () => { throw new Error("cleanup unlink failure"); };
    syncBuiltinESMExports();
  `)}`;

  const result = await runCli(paths, stableRelease("2.337.0", archive), archive, {
    nodeArguments: ["--import", preload],
  });

  assert.notEqual(result.status, 0, result.stdout);
  assert.match(result.stderr, /cleanup failed after write error: cleanup unlink failure/);
  assert.match(result.stderr, /actions runner update: primary rename failure/);
});

test("verified upgrade rewrites both pins and emits Actions outputs", async (t) => {
  const paths = await fixture(t);
  const archive = Buffer.from("verified-runner-2.337.0");
  const release = stableRelease("2.337.0", archive);

  const result = await runCli(paths, release, archive);

  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(JSON.parse(result.stdout), {
    changed: true,
    version: "2.337.0",
    release_url: "https://github.com/actions/runner/releases/tag/v2.337.0",
    published_at: "2026-09-22T00:00:00Z",
  });
  assert.equal(await readFile(paths.pinPath, "utf8"), pin("2.337.0", archive));
  assert.equal(await readFile(paths.windowsPinPath, "utf8"), windowsPin("2.337.0", windowsArchive(archive)));
  assert.equal(
    await readFile(paths.outputPath, "utf8"),
    [
      "changed=true",
      "version=2.337.0",
      "release_url=https://github.com/actions/runner/releases/tag/v2.337.0",
      "published_at=2026-09-22T00:00:00Z",
      "",
    ].join("\n"),
  );
});

test("verified upgrade does not require temporary archive storage", async (t) => {
  const paths = await fixture(t);
  const archive = Buffer.from("verified-runner-without-temp-storage");
  const missingTempDirectory = join(paths.directory, "missing-temp-directory");

  const result = await runCli(paths, stableRelease("2.337.0", archive), archive, {
    environment: {
      TMPDIR: missingTempDirectory,
      TMP: missingTempDirectory,
      TEMP: missingTempDirectory,
    },
  });

  assert.equal(result.status, 0, result.stderr);
  assert.equal(await readFile(paths.pinPath, "utf8"), pin("2.337.0", archive));
  assert.equal(await readFile(paths.windowsPinPath, "utf8"), windowsPin("2.337.0", windowsArchive(archive)));
});

test("already pinned release is a no-op and does not download an archive", async (t) => {
  const archive = Buffer.from("current-runner");
  const paths = await fixture(t, {
    currentVersion: "2.337.0",
    currentContents: archive,
  });
  const before = await readFile(paths.pinPath);

  const result = await runCli(paths, stableRelease("2.337.0", archive), archive, {
    includeArchive: false,
  });

  assert.equal(result.status, 0, result.stderr);
  assert.equal(JSON.parse(result.stdout).changed, false);
  assert.deepEqual(await readFile(paths.pinPath), before);
});

test("downgrade is rejected without changing the pin", async (t) => {
  const paths = await fixture(t, { currentVersion: "2.337.0" });
  const archive = Buffer.from("older-runner");
  await assertRejectedWithoutMutation(
    paths,
    stableRelease("2.336.0", archive),
    archive,
    /refusing to downgrade from 2\.337\.0 to 2\.336\.0/,
  );
});

for (const field of ["draft", "prerelease"]) {
  test(`${field} release is rejected without changing the pin`, async (t) => {
    const paths = await fixture(t);
    const archive = Buffer.from(`${field}-runner`);
    await assertRejectedWithoutMutation(
      paths,
      stableRelease("2.337.0", archive, { [field]: true }),
      archive,
      /latest release must be stable/,
    );
  });
}

test("malformed published timestamp is rejected before changing the pin", async (t) => {
  const paths = await fixture(t);
  const archive = Buffer.from("runner");
  await assertRejectedWithoutMutation(
    paths,
    stableRelease("2.337.0", archive, { published_at: "\n2026-09-22\n" }),
    archive,
    /release published_at must be an RFC 3339 UTC timestamp/,
  );
});

test("noncanonical release URL is rejected without changing the pin", async (t) => {
  const paths = await fixture(t);
  const archive = Buffer.from("runner");
  await assertRejectedWithoutMutation(
    paths,
    stableRelease("2.337.0", archive, {
      html_url: "https://example.com/actions-runner-v2.337.0",
    }),
    archive,
    /release URL is not canonical for actions\/runner/,
  );
});

test("missing Linux x64 asset is rejected without changing the pin", async (t) => {
  const paths = await fixture(t);
  const archive = Buffer.from("runner");
  await assertRejectedWithoutMutation(
    paths,
    stableRelease("2.337.0", archive, { assets: [] }),
    archive,
    /expected exactly one actions-runner-linux-x64-2\.337\.0\.tar\.gz asset, found 0/,
  );
});

test("duplicate Linux x64 assets are rejected without changing the pin", async (t) => {
  const paths = await fixture(t);
  const archive = Buffer.from("runner");
  const asset = canonicalAsset("2.337.0", archive);
  await assertRejectedWithoutMutation(
    paths,
    stableRelease("2.337.0", archive, { assets: [asset, { ...asset }] }),
    archive,
    /expected exactly one actions-runner-linux-x64-2\.337\.0\.tar\.gz asset, found 2/,
  );
});

const invalidAssetCases = [
  {
    name: "noncanonical asset URL",
    overrides: { browser_download_url: "https://example.com/runner.tar.gz" },
    message: /asset download URL is not canonical/,
  },
  {
    name: "malformed asset digest",
    overrides: { digest: "sha256:not-a-digest" },
    message: /asset digest must be sha256/,
  },
  {
    name: "nonpositive asset size",
    overrides: { size: 0 },
    message: /asset size must be a positive integer/,
  },
  {
    name: "asset larger than the managed template archive capacity",
    overrides: { size: 268435457 },
    message: /asset size 268435457 exceeds managed template maximum 268435456/,
  },
];

for (const { name, overrides, message } of invalidAssetCases) {
  test(`${name} is rejected without changing the pin`, async (t) => {
    const paths = await fixture(t);
    const archive = Buffer.from("runner");
    const release = stableRelease("2.337.0", archive, {
      assets: [canonicalAsset("2.337.0", archive, overrides)],
    });
    await assertRejectedWithoutMutation(paths, release, archive, message);
  });
}

test("download digest mismatch is rejected without changing the pin", async (t) => {
  const paths = await fixture(t);
  const metadataContents = Buffer.from("runner-A");
  const downloadedContents = Buffer.from("runner-B");
  await assertRejectedWithoutMutation(
    paths,
    stableRelease("2.337.0", metadataContents),
    downloadedContents,
    /downloaded archive SHA-256 .* does not match release metadata/,
  );
});

test("download size mismatch is rejected without changing the pin", async (t) => {
  const paths = await fixture(t);
  const archive = Buffer.from("runner");
  const release = stableRelease("2.337.0", archive, {
    assets: [canonicalAsset("2.337.0", archive, { size: archive.length + 1 }),
      windowsAsset("2.337.0", windowsArchive(archive))],
  });
  await assertRejectedWithoutMutation(
    paths,
    release,
    archive,
    /downloaded archive size .* does not match release metadata/,
  );
});

test("download stops when received bytes exceed release metadata", async (t) => {
  const paths = await fixture(t);
  const metadataContents = Buffer.from("runner");
  const downloadedContents = Buffer.from("runner-with-unexpected-trailing-bytes");
  await assertRejectedWithoutMutation(
    paths,
    stableRelease("2.337.0", metadataContents),
    downloadedContents,
    /downloaded archive exceeded release metadata size 6/,
  );
});

for (const { name, overrides, message } of invalidAssetCases) {
  test(`Windows ${name} rejects both pins before writing`, async (t) => {
    const paths = await fixture(t);
    const archive = Buffer.from("runner");
    const release = stableRelease("2.337.0", archive, {
      assets: [canonicalAsset("2.337.0", archive),
        windowsAsset("2.337.0", windowsArchive(archive), overrides)],
    });
    await assertRejectedWithoutMutation(paths, release, archive, message);
  });
}

for (const count of [0, 2]) {
  test(`${count} Windows assets reject both pins`, async (t) => {
    const paths = await fixture(t);
    const archive = Buffer.from("runner");
    const release = stableRelease("2.337.0", archive, {
      assets: [canonicalAsset("2.337.0", archive),
        ...Array.from({ length: count }, () => windowsAsset("2.337.0", windowsArchive(archive)))],
    });
    await assertRejectedWithoutMutation(paths, release, archive,
      /expected exactly one actions-runner-win-x64-2\.337\.0\.zip asset/);
  });
}

for (const contents of [Buffer.from("windows-runnex"), Buffer.from("short"), Buffer.from("windows-runner-too-long")]) {
  test(`Windows corrupt download (${contents.length} bytes) leaves both pins intact`, async (t) => {
    const paths = await fixture(t);
    const archive = Buffer.from("runner");
    await assertRejectedWithoutMutation(paths, stableRelease("2.337.0", archive), archive,
      /downloaded archive (SHA-256|size|exceeded)/, { windowsContents: contents });
  });
}

test("Windows metadata drift blocks a Linux upgrade before any write", async (t) => {
  const paths = await fixture(t);
  const archive = Buffer.from("runner");
  await writeFile(paths.windowsPinPath, windowsPin("2.337.0", Buffer.from("different")));
  await assertRejectedWithoutMutation(paths, stableRelease("2.337.0", archive), archive,
    /release metadata for current Runner .* differs/);
});

test("a newer Windows pin is never downgraded", async (t) => {
  const paths = await fixture(t);
  const archive = Buffer.from("runner");
  await writeFile(paths.windowsPinPath, windowsPin("2.338.0", windowsArchive(archive)));
  await assertRejectedWithoutMutation(paths, stableRelease("2.337.0", archive), archive,
    /refusing to downgrade from 2\.338\.0/);
});

test("an older Windows pin is repaired when Linux is already current", async (t) => {
  const archive = Buffer.from("runner");
  const paths = await fixture(t, { currentVersion: "2.337.0", currentContents: archive });
  await writeFile(paths.windowsPinPath, windowsPin("2.336.0", Buffer.from("old-windows")));
  const result = await runCli(paths, stableRelease("2.337.0", archive), archive);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(JSON.parse(result.stdout).changed, true);
  assert.equal(await readFile(paths.pinPath, "utf8"), pin("2.337.0", archive));
  assert.equal(await readFile(paths.windowsPinPath, "utf8"), windowsPin("2.337.0", windowsArchive(archive)));
});

test("failure writing Windows restores the previous Linux pin", async (t) => {
  const paths = await fixture(t);
  const archive = Buffer.from("runner");
  const preload = `data:text/javascript,${encodeURIComponent(`
    import fs from "node:fs";
    import { syncBuiltinESMExports } from "node:module";
    const rename = fs.promises.rename;
    fs.promises.rename = async (source, target) => {
      if (target.endsWith("/template.env")) throw new Error("Windows write failure");
      return rename(source, target);
    };
    syncBuiltinESMExports();
  `)}`;
  await assertRejectedWithoutMutation(paths, stableRelease("2.337.0", archive), archive,
    /Windows write failure/, { nodeArguments: ["--import", preload] });
});

test("a second run is a no-op and needs neither downloaded archive", async (t) => {
  const paths = await fixture(t);
  const archive = Buffer.from("runner");
  const release = stableRelease("2.337.0", archive);
  const first = await runCli(paths, release, archive);
  assert.equal(first.status, 0, first.stderr);
  await rm(paths.archivePath);
  await rm(paths.windowsArchivePath);
  const second = await runCli(paths, release, archive, { includeArchive: false });
  assert.equal(second.status, 0, second.stderr);
  assert.equal(JSON.parse(second.stdout).changed, false);
});

test("duplicate Windows pin fields are rejected without mutation", async (t) => {
  const paths = await fixture(t);
  const archive = Buffer.from("runner");
  await writeFile(paths.windowsPinPath,
    (await readFile(paths.windowsPinPath, "utf8")) + "RUNNER_WIN_X64_VERSION=2.336.0\n");
  await assertRejectedWithoutMutation(paths, stableRelease("2.337.0", archive), archive,
    /Windows pin must contain exactly one RUNNER_WIN_X64_VERSION/);
});
