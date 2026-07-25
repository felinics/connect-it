#!/usr/bin/env node

import fs from "node:fs";

const maxEvidenceBytes = 16 * 1024 * 1024;

function reject() {
  process.stderr.write("provider real-smoke JSON evidence is invalid\n");
  process.exit(1);
}

const [evidencePath, expectedPackage, expectedTest] = process.argv.slice(2);
if (
  process.argv.length !== 5 ||
  !evidencePath ||
  !expectedPackage ||
  !/^Test[A-Za-z0-9]+RealSmoke$/.test(expectedTest)
) {
  reject();
}

let evidenceStat;
let evidenceText;
try {
  evidenceStat = fs.lstatSync(evidencePath);
  if (
    !evidenceStat.isFile() ||
    evidenceStat.isSymbolicLink() ||
    evidenceStat.size <= 0 ||
    evidenceStat.size > maxEvidenceBytes
  ) {
    reject();
  }
  evidenceText = fs.readFileSync(evidencePath, "utf8");
} catch {
  reject();
}

let exactRuns = 0;
let exactPasses = 0;
let packagePasses = 0;
let eventCount = 0;

const lines = evidenceText.split(/\r?\n/);
if (lines.at(-1) === "") {
  lines.pop();
}
for (const line of lines) {
  if (line === "") {
    reject();
  }

  let event;
  try {
    event = JSON.parse(line);
  } catch {
    reject();
  }
  if (
    event === null ||
    typeof event !== "object" ||
    Array.isArray(event) ||
    typeof event.Action !== "string" ||
    event.Package !== expectedPackage
  ) {
    reject();
  }
  eventCount += 1;

  const hasTest = Object.hasOwn(event, "Test");
  if (
    hasTest &&
    (typeof event.Test !== "string" ||
      (event.Test !== expectedTest &&
        !event.Test.startsWith(`${expectedTest}/`)))
  ) {
    reject();
  }

  // A skipped top-level test exits go test successfully. Reject every skip or
  // failure in the selected test tree and every package-level skip/failure.
  if (event.Action === "skip" || event.Action === "fail") {
    reject();
  }

  if (hasTest && event.Test === expectedTest) {
    if (event.Action === "run") {
      exactRuns += 1;
    } else if (event.Action === "pass") {
      exactPasses += 1;
    }
  }
  if (!hasTest && event.Action === "pass") {
    packagePasses += 1;
  }
}

if (
  eventCount === 0 ||
  exactRuns !== 1 ||
  exactPasses !== 1 ||
  packagePasses !== 1
) {
  reject();
}
