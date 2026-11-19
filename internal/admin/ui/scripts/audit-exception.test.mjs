/** Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */
import assert from "node:assert/strict";
import { execSync } from "node:child_process";
import { test } from "node:test";

test("the picomatch audit exception does not cover production dependencies", () => {
  const output = execSync("pnpm list picomatch --prod --depth Infinity --json", {
    encoding: "utf8",
  });
  const packages = JSON.parse(output);
  assert.equal(packages.length, 1);
  assert.equal(packages[0].name, "jul-console-v2");
  assert.deepEqual(Object.keys(packages[0].dependencies ?? {}), []);
  assert.deepEqual(Object.keys(packages[0].optionalDependencies ?? {}), []);
});
