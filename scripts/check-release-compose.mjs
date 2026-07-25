#!/usr/bin/env node
import { execFileSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";
const imageVariables = {
  postgres: "CONNECT_IT_POSTGRES_IMAGE",
  "connect-it": "CONNECT_IT_SERVER_IMAGE",
  web: "CONNECT_IT_WEB_IMAGE",
};
const requiredServerEnvironment = [
  "DATABASE_URL",
  "CONNECT_IT_SECRET_KEY",
  "COOKIE_SECRET",
  "CONNECT_IT_ADMIN_PASSWORD",
  "CONNECT_IT_BASE_URL",
  "CONNECT_IT_EXPECTED_DATABASE_NAME",
  "CONNECT_IT_EXPECTED_DATABASE_SCHEMA",
  "CONNECT_IT_EXPECTED_DATABASE_SYSTEM_IDENTIFIER",
  "CONNECT_IT_EXPECTED_DATABASE_APPLICATION_ROLE",
  "CONNECT_IT_EXPECTED_DATABASE_OWNER_ROLE",
];
const forbiddenRuntimeKeys = `
  build command entrypoint privileged runtime network_mode pid ipc uts
  userns_mode devices device_cgroup_rules volumes_from cap_add security_opt
  extra_hosts env_file secrets configs extends profiles develop deploy gpus
  use_api_socket
`.trim().split(/\s+/);
const component = String.raw`[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*`;
const digestPattern = new RegExp(
  String.raw`^(?:${component}(?::[0-9]+)?/)?(?:${component}/)*${component}` +
    String.raw`(?::[a-z0-9_][a-z0-9_.-]{0,127})?@sha256:[0-9a-f]{64}$`,
);
function check(condition, message) {
  if (!condition) throw new Error(message);
}
function object(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}
function nonempty(value) {
  return value !== null && value !== undefined &&
    (!Array.isArray(value) || value.length > 0);
}
function exactKeys(value, keys) {
  return object(value) &&
    Object.keys(value).sort().join("\0") === [...keys].sort().join("\0");
}
function environmentMap(value) {
  if (object(value)) return new Map(Object.entries(value));
  if (!Array.isArray(value)) return null;
  const entries = value.map((item) => {
    const separator = typeof item === "string" ? item.indexOf("=") : -1;
    return separator > 0 ? [item.slice(0, separator), item.slice(separator + 1)] : [];
  });
  return entries.every((entry) => entry.length === 2) &&
      new Set(entries.map(([key]) => key)).size === entries.length
    ? new Map(entries)
    : null;
}
function onlyDefaultNetwork(value) {
  return exactKeys(value, ["default"]) &&
    (value.default === null || exactKeys(value.default, []));
}
function exactHealthyDependency(service, dependency) {
  return exactKeys(service.depends_on, [dependency]) &&
    service.depends_on[dependency].condition === "service_healthy" &&
    service.depends_on[dependency].required === true;
}
function requiredExpression(value, key) {
  return typeof value === "string" &&
    new RegExp(`^\\$\\{${key}:\\?[^}\\r\\n]+\\}$`).test(value);
}

export function isImmutableImage(image) {
  return typeof image === "string" && digestPattern.test(image);
}

export function validateReleaseCompose(model) {
  check(object(model) && exactKeys(model.services, ["connect-it", "postgres", "web"]),
    "release Compose must contain exactly postgres, connect-it, and web");
  const { postgres, "connect-it": server, web } = model.services;

  for (const [name, service] of Object.entries(model.services)) {
    check(object(service), `release service ${name} is invalid`);
    for (const key of forbiddenRuntimeKeys) {
      check(!Object.hasOwn(service, key) || !nonempty(service[key]),
        `release runtime override is forbidden: ${name}.${key}`);
    }
    check(onlyDefaultNetwork(service.networks),
      `release service ${name} must use only the private default network`);
    check(requiredExpression(service.image, imageVariables[name]),
      `release service ${name} must use ${imageVariables[name]}`);
    check(service.pull_policy === "always",
      `release service ${name} must always pull its digest-pinned image`);
  }

  check(!nonempty(server.volumes) && !nonempty(web.volumes),
    "release application services must not mount filesystems");
  check(!nonempty(server.ports) && !nonempty(server.expose) &&
      !nonempty(postgres.ports) && !nonempty(postgres.expose) &&
      !nonempty(web.expose),
    "only web may publish a port");
  check(Array.isArray(web.ports) && web.ports.length === 1 &&
      web.ports[0].target === 8080 && String(web.ports[0].published) === "8080" &&
      web.ports[0].protocol === "tcp" && web.ports[0].mode === "ingress" &&
      !Object.hasOwn(web.ports[0], "host_ip"),
    "release web ingress must be exactly TCP 8080:8080");
  check(Array.isArray(postgres.volumes) && postgres.volumes.length === 1 &&
      postgres.volumes[0].type === "volume" &&
      postgres.volumes[0].source === "postgres-data" &&
      postgres.volumes[0].target === "/var/lib/postgresql/data" &&
      postgres.volumes[0].read_only !== true,
    "release postgres must use only the reviewed named volume");
  check(exactKeys(model.networks, ["default"]) &&
      exactKeys(model.networks.default, ["name"]) &&
      model.networks.default.external !== true,
    "release network must be private and engine-managed");
  check(exactKeys(model.volumes, ["postgres-data"]) &&
      exactKeys(model.volumes["postgres-data"], ["name"]) &&
      model.volumes["postgres-data"].external !== true,
    "release postgres volume must be engine-managed");

  check(exactHealthyDependency(server, "postgres") &&
      exactHealthyDependency(web, "connect-it"),
    "release dependencies must wait for healthy postgres and connect-it");
  check(object(postgres.healthcheck) &&
      Array.isArray(postgres.healthcheck.test) &&
      postgres.healthcheck.test[0] === "CMD-SHELL" &&
      postgres.healthcheck.test[1]?.includes("pg_isready") &&
      postgres.healthcheck.retries > 0,
    "release postgres must retain its readiness healthcheck");

  const environment = environmentMap(server.environment);
  check(environment !== null, "release server environment is invalid");
  for (const key of requiredServerEnvironment) {
    check(requiredExpression(environment.get(key), key),
      `release server ${key} must be required operator input`);
  }
  for (const key of [
    "CONNECT_IT_ALLOW_INSECURE_PROVIDER_HTTP",
    "CONNECT_IT_ALLOW_PRIVATE_PROVIDER_NETWORK",
  ]) {
    check(environment.get(key) === `\${${key}:-false}`,
      `release server ${key} must default to false`);
  }
  check(environment.get("LISTEN_ADDR") === ":8080",
    "release server LISTEN_ADDR must remain :8080");
}

function main() {
  for (const variable of Object.values(imageVariables)) {
    check(isImmutableImage(process.env[variable]),
      `${variable} must be a lowercase digest-pinned image`);
  }
  check(new Set(Object.values(imageVariables).map((key) => process.env[key].slice(-64))).size === 3,
    "release service images must be distinct");

  const repositoryRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
  const output = execFileSync("docker", [
    "compose", "--profile", "*",
    "--file", path.join(repositoryRoot, "docker/docker-compose.yml"),
    "--file", path.join(repositoryRoot, "docker/docker-compose.release.yml"),
    "config", "--no-interpolate", "--no-env-resolution", "--format", "json",
  ], { encoding: "utf8", maxBuffer: 4 * 1024 * 1024 });
  validateReleaseCompose(JSON.parse(output));
  process.stdout.write("release Compose static validation passed\n");
}

if (process.argv[1] &&
    path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    main();
  } catch (error) {
    process.stderr.write(`check-release-compose: ${error.message}\n`);
    process.exitCode = 1;
  }
}
