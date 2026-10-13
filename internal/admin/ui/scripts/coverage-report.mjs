/** Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */
import { readFileSync, readdirSync, writeFileSync } from "node:fs";
import { resolve, relative } from "node:path";
import { fileURLToPath } from "node:url";

// Vitest owns enforcement; this companion rejects empty/missing measurements
// (an empty glob is otherwise 100%) and retains exact area and per-file counts.
export function coverageReport(root) {
  const policy = JSON.parse(readFileSync(resolve(root, "coverage-policy.json"), "utf8"));
  const report = JSON.parse(readFileSync(resolve(root, "coverage/coverage-summary.json"), "utf8"));
  const raw = JSON.parse(readFileSync(resolve(root, "coverage/coverage-final.json"), "utf8"));
  const metrics = ["statements", "branches", "functions", "lines"];
  const files = Object.fromEntries(
    Object.entries(report)
      .filter(([key]) => key !== "total")
      .map(([key, value]) => [relative(root, key).replaceAll("\\", "/"), value]),
  );
  const sourceFiles = readdirSync(resolve(root, "src"), { recursive: true })
    .filter(
      (name) =>
        /\.(ts|tsx)$/.test(name) && !/\.d\.ts$|\.(test|spec)\.tsx?$|(^|\/)test\//.test(name),
    )
    .map((name) => `src/${name.replaceAll("\\", "/")}`);
  const matches = (pattern, name) =>
    pattern.endsWith("/**") ? name.startsWith(pattern.slice(0, -2)) : name === pattern;
  const failures = [];
  const check = (name, summary, floors) => {
    for (const [metric, floor] of Object.entries(floors)) {
      if (!Number.isFinite(summary?.[metric]?.pct) || summary[metric].pct < floor) {
        failures.push(`${name}: ${metric} ${summary?.[metric]?.pct ?? "missing"} < ${floor}`);
      }
    }
  };
  check("global", report.total, policy.global);
  const targets = {};
  for (const [pattern, floors] of Object.entries(policy.paths)) {
    const required = sourceFiles.filter((name) => matches(pattern, name));
    if (!required.length) failures.push(`${pattern}: empty production match`);
    for (const name of required) {
      if (!files[name] || !raw[resolve(root, name)]) failures.push(`${pattern}: missing ${name}`);
    }
    const summary = Object.fromEntries(
      metrics.map((metric) => {
        const total = required.reduce((n, name) => n + (files[name]?.[metric]?.total ?? 0), 0);
        const covered = required.reduce((n, name) => n + (files[name]?.[metric]?.covered ?? 0), 0);
        return [
          metric,
          { total, covered, pct: total ? Math.floor((10000 * covered) / total) / 100 : 100 },
        ];
      }),
    );
    check(pattern, summary, floors);
    targets[pattern] = { files: required, floors, summary };
  }
  const result = { global: report.total, globalFloors: policy.global, targets, files, failures };
  writeFileSync(
    resolve(root, "coverage/contract-summary.json"),
    `${JSON.stringify(result, null, 2)}\n`,
  );
  if (failures.length) throw new Error(failures.join("\n"));
  return result;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const result = coverageReport(process.cwd());
    console.log(
      `Coverage contract passed: ${Object.keys(result.files).length} production files; ${Object.keys(result.targets).length} required targets.`,
    );
  } catch (error) {
    console.error(`Coverage contract failed: ${error.message}`);
    process.exitCode = 1;
  }
}
