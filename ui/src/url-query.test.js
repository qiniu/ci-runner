import { expect, test } from "bun:test"
import { readableQuery } from "./url-query"

test("queries show ref slashes without changing reserved characters or double-decoding", () => {
  for (const ref of ["example/vite", "feature/中文", "release/jobs/deployments", "literal%2Fname", "feature/a&tag=b#c+d=e?f", "two words"]) {
    const query = new URLSearchParams({ branch: ref, job: "job/1" })
    const result = readableQuery(query)
    expect(new URLSearchParams(result).get("branch")).toBe(ref)
    expect(new URLSearchParams(result).get("job")).toBe("job/1")
    expect([...new URLSearchParams(result).keys()]).toEqual(["branch", "job"])
    if (ref.includes("/")) expect(result).toContain("/")
  }
  expect(readableQuery(new URLSearchParams({ branch: "literal%2Fname" }))).toBe("branch=literal%252Fname")
})
