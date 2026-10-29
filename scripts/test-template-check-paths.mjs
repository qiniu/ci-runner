import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { matchesGlob } from "node:path";
import test from "node:test";

const filters = JSON.parse(await readFile(new URL("../.github/template-check-paths.json", import.meta.url)));
const workflow = await readFile(new URL("../.github/workflows/template-check.yaml", import.meta.url), "utf8");
const jobs = {
  source: "template-source",
  compatibility: "template-compatibility",
  service: "service-dockerfile",
  dockerfiles: "template-dockerfiles",
};
const all = Object.keys(jobs);
// paths-filter enables picomatch's dot option. Node hides leading dots, so
// replace literal dots in both operands before matching this simple glob subset.
const matches = (file, pattern) => matchesGlob(file.replaceAll(".", "\u0001"), pattern.replaceAll(".", "\u0001"));
const selected = (files) => all.filter((job) => files.some((file) =>
  filters[job].some((pattern) => matches(file, pattern))));

// These positive, segment-only globs have the same semantics in Node and the
// Action's picomatch matcher. Keep richer glob syntax out of this contract.
test("filters use only the supported glob subset", () => {
  for (const patterns of Object.values(filters)) {
    for (const pattern of patterns) {
      assert.match(pattern, /^[A-Za-z0-9_./*-]+$/);
      assert.ok(pattern.split("/").every((part) => !part.includes("**") || part === "**"));
    }
  }
});

const cases = [
  ["PR 151 migration and development tasks", ["Taskfile.yaml", ".github/workflows/check.yaml", "internal/state/offline_migration.go", "docs/testing.md", "runnerd.yaml.example"], []],
  ["application and UI", ["cmd/runnerd/main.go", "ui/src/app.tsx", "go.mod"], []],
  ["ordinary documentation", ["README.md", "docs/zh/testing.md", "ui/src/site-docs.test.js"], []],
  ["unrelated script", ["scripts/smee.sh"], []],
  ["service Dockerfile", ["Dockerfile"], ["service"]],
  ["release Dockerfile", ["Dockerfile.release"], ["service"]],
  ["service context ignore", [".dockerignore"], ["service"]],
  ["release inputs", [".goreleaser.yml", ".github/workflows/release.yaml"], ["service"]],
  ["Linux template Dockerfile", ["templates/github-runner-ubuntu-24.04/Dockerfile"], ["source", "dockerfiles"]],
  ["template Dockerfile ignore", ["templates/github-runner-ubuntu-slim/Dockerfile.dockerignore"], ["source", "dockerfiles"]],
  ["template context ignore", ["templates/.dockerignore"], ["source", "dockerfiles"]],
  ["template setup", ["templates/github-runner-ubuntu-26.04/scripts/setup-template.sh"], ["source", "dockerfiles"]],
  ["common template helper", ["templates/common/scripts/curl"], ["source", "dockerfiles"]],
  ["Runner upgrade", ["templates/common/actions-runner.env", "templates/runner-images-compatibility.json", "templates/github-runner-windows-11/template.env"], ["source", "compatibility", "dockerfiles"]],
  ["template documentation and configuration", ["templates/README.md", "templates/github-runner-ubuntu-22.04/qshell.sandbox.toml"], ["source"]],
  ["Windows template", ["templates/github-runner-windows-11/Dockerfile"], ["source"]],
  ["archive test dependency", ["scripts/prepare-runner-archive.sh"], ["source"]],
  ["disk test dependency", ["scripts/run-runner-image-conformance.sh"], ["source"]],
  ["pin updater", ["scripts/update-actions-runner-pin.mjs"], ["source"]],
  ["manual updater workflow", [".github/workflows/actions-runner-update.yaml"], ["source"]],
  ["compatibility parser", ["scripts/lib/parse-runner-image-report.awk"], ["compatibility"]],
  ["compatibility generator", ["scripts/generate-runner-image-compatibility.mjs"], ["compatibility"]],
  ["compatibility lock", ["templates/runner-images-upstream.lock.json"], ["source", "compatibility"]],
  ["workflow selection", [".github/workflows/template-check.yaml"], all],
  ["filter configuration", [".github/template-check-paths.json"], all],
  ["mixed service and template PR", ["Dockerfile", "templates/common/scripts/curl"], ["source", "service", "dockerfiles"]],
];

for (const [name, files, expected] of cases) {
  test(name, () => assert.deepEqual(selected(files), expected));
}

test("push and PR event paths cover exactly the job inputs and regression test", () => {
  const expected = new Set([...Object.values(filters).flat(), "scripts/test-template-check-paths.mjs"]);
  const blocks = [...workflow.matchAll(/    paths:\n((?:      - "[^"\n]+"\n)+)/g)];
  assert.equal(blocks.length, 2);
  for (const [, block] of blocks) {
    const paths = [...block.matchAll(/      - "([^"\n]+)"/g)].map((match) => match[1]);
    assert.equal(paths.length, new Set(paths).size);
    assert.deepEqual(new Set(paths), expected);
    for (const [name, files, expectedJobs] of cases) {
      assert.equal(files.some((file) => paths.some((pattern) => matches(file, pattern))),
        expectedJobs.length > 0, name);
    }
  }
});

test("each job uses its filter and manual dispatch runs every job", () => {
  assert.match(workflow, /  workflow_dispatch:/);
  assert.match(workflow, /filters: .github\/template-check-paths.json/);
  for (const [output, job] of Object.entries(jobs)) {
    assert.ok(workflow.includes(`  ${job}:\n    needs: changes\n    if: needs.changes.outputs.${output} == 'true'\n`));
    assert.ok(workflow.includes(`${output}: \${{ github.event_name == 'workflow_dispatch' || steps.filter.outputs.${output} == 'true' }}`));
  }
});
