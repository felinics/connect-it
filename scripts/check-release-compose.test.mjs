import assert from "node:assert/strict";
import test from "node:test";
import { isImmutableImage, validateReleaseCompose } from "./check-release-compose.mjs";
const required = [
  "DATABASE_URL", "CONNECT_IT_SECRET_KEY", "COOKIE_SECRET",
  "CONNECT_IT_ADMIN_PASSWORD", "CONNECT_IT_BASE_URL",
  "CONNECT_IT_EXPECTED_DATABASE_NAME", "CONNECT_IT_EXPECTED_DATABASE_SCHEMA",
  "CONNECT_IT_EXPECTED_DATABASE_SYSTEM_IDENTIFIER", "CONNECT_IT_EXPECTED_DATABASE_APPLICATION_ROLE",
  "CONNECT_IT_EXPECTED_DATABASE_OWNER_ROLE",
];
const image = (name) => `\${${name}:?required}`;
const healthy = (name) => ({ [name]: { condition: "service_healthy", required: true } });
const baseModel = () => ({
  services: {
    postgres: {
      image: image("CONNECT_IT_POSTGRES_IMAGE"), pull_policy: "always",
      networks: { default: null }, environment: { POSTGRES_DB: "connect_it" },
      healthcheck: { test: ["CMD-SHELL", "pg_isready"], retries: 12 },
      volumes: [{ type: "volume", source: "postgres-data",
        target: "/var/lib/postgresql/data", volume: {} }],
    },
    "connect-it": {
      image: image("CONNECT_IT_SERVER_IMAGE"), pull_policy: "always",
      networks: { default: null }, depends_on: healthy("postgres"),
      environment: {
        ...Object.fromEntries(required.map((key) => [key, image(key)])),
        CONNECT_IT_ALLOW_INSECURE_PROVIDER_HTTP: "${CONNECT_IT_ALLOW_INSECURE_PROVIDER_HTTP:-false}",
        CONNECT_IT_ALLOW_PRIVATE_PROVIDER_NETWORK: "${CONNECT_IT_ALLOW_PRIVATE_PROVIDER_NETWORK:-false}",
        LISTEN_ADDR: ":8080",
      },
    },
    web: {
      image: image("CONNECT_IT_WEB_IMAGE"), pull_policy: "always",
      networks: { default: null }, depends_on: healthy("connect-it"),
      ports: [{ target: 8080, published: "8080", protocol: "tcp", mode: "ingress" }],
    },
  },
  networks: { default: { name: "connect-it_default" } },
  volumes: { "postgres-data": { name: "connect-it_postgres-data" } },
});
test("immutable image syntax", () => {
  const digest = "0".repeat(64);
  for (const candidate of [
    `registry.example/team/server@sha256:${digest}`,
    `localhost:5000/team/server:v1@sha256:${digest}`,
  ]) assert.equal(isImmutableImage(candidate), true, candidate);
  for (const candidate of [
    "server:latest", `https://registry/server@sha256:${digest}`,
    `registry//server@sha256:${digest}`, `registry/team/../server@sha256:${digest}`,
    `Registry/server@sha256:${digest}`,
  ]) assert.equal(isImmutableImage(candidate), false, candidate);
});
test("release model rejects unsafe mutations", () => {
  validateReleaseCompose(baseModel());
  const cases = [
    ["build", (m) => { m.services.web.build = "."; }],
    ["command", (m) => { m.services.web.command = ["sh"]; }],
    ["privileged", (m) => { m.services.postgres.privileged = true; }],
    ["host namespace", (m) => { m.services.web.network_mode = "host"; }],
    ["application mount", (m) => { m.services["connect-it"].volumes = ["/:/host"]; }],
    ["database port", (m) => { m.services.postgres.ports = ["5432:5432"]; }],
    ["wrong ingress", (m) => { m.services.web.ports[0].published = "80"; }],
    ["external network", (m) => { m.networks.default = { name: "x", external: true }; }],
    ["wrong dependency", (m) => { m.services.web.depends_on["connect-it"].condition = "service_started"; }],
    ["missing healthcheck", (m) => { delete m.services.postgres.healthcheck; }],
    ["composed image slot", (m) => { m.services.web.image += "${UNREVIEWED}"; }],
    ["unsafe egress default", (m) => { m.services["connect-it"].environment.CONNECT_IT_ALLOW_PRIVATE_PROVIDER_NETWORK = "true"; }],
  ];
  for (const [name, mutate] of cases) {
    const model = baseModel();
    mutate(model);
    assert.throws(() => validateReleaseCompose(model), Error, name);
  }
});
