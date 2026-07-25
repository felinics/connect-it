import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import http from "node:http";
import net from "node:net";

const MiB = 1024 * 1024;
const goURL = requiredEnv("CONNECT_IT_INGRESS_GO_URL");
const nginxURL = requiredEnv("CONNECT_IT_INGRESS_NGINX_URL");
const sessionToken = requiredEnv("CONNECT_IT_INGRESS_SESSION_TOKEN");
const composeProject = requiredEnv("CONNECT_IT_INGRESS_PROJECT");
const baseCompose = requiredEnv("CONNECT_IT_INGRESS_BASE_COMPOSE");
const testCompose = requiredEnv("CONNECT_IT_INGRESS_TEST_COMPOSE");
const expectedTooLarge = {
  error: "input_too_large",
  message: "MCP request body exceeds the 36 MiB limit",
};
const expectedRequestTimeout = {
  error: "request_timeout",
  message: "MCP request body was not received in time",
};

function requiredEnv(name) {
  const value = process.env[name];
  if (!value) {
    throw new Error(`${name} is required`);
  }
  return value;
}

function bodyLayout(totalBytes) {
  const marker = "__PADDING__";
  const encoded = JSON.stringify({
    jsonrpc: "2.0",
    id: 1,
    method: "initialize",
    params: {
      protocolVersion: "2025-06-18",
      capabilities: {},
      clientInfo: { name: "connect-it-ingress-e2e", version: "1" },
    },
    padding: marker,
  });
  const markerOffset = encoded.indexOf(marker);
  assert.notEqual(markerOffset, -1);
  const prefix = Buffer.from(encoded.slice(0, markerOffset));
  const suffix = Buffer.from(encoded.slice(markerOffset + marker.length));
  const paddingBytes = totalBytes - prefix.length - suffix.length;
  assert.ok(paddingBytes >= 0, `body size ${totalBytes} is too small`);
  return { prefix, suffix, paddingBytes };
}

function requestOptions(baseURL, contentLength) {
  const endpoint = new URL("/mcp", baseURL);
  const headers = {
    Authorization: `Bearer ${sessionToken}`,
    "Content-Type": "application/json",
    Accept: "application/json, text/event-stream",
  };
  if (contentLength !== null) {
    headers["Content-Length"] = String(contentLength);
  }
  return {
    protocol: endpoint.protocol,
    hostname: endpoint.hostname,
    port: endpoint.port,
    path: endpoint.pathname,
    method: "POST",
    agent: false,
    headers,
  };
}

// Shared by every sender below. At most 1 MiB of the response is retained: the
// bodies under assertion are a small JSON envelope or a single SSE message, so
// a larger response can only fail the assertions, never pass them silently.
function collectResponse(state, resolve, onResponse) {
  return (response) => {
    state.responseSeen = true;
    onResponse?.();
    const chunks = [];
    let responseBytes = 0;
    response.on("data", (chunk) => {
      responseBytes += chunk.length;
      if (responseBytes <= MiB) {
        chunks.push(chunk);
      }
    });
    response.on("end", () => {
      resolve({
        status: response.statusCode,
        headers: response.headers,
        body: Buffer.concat(chunks).toString("utf8"),
      });
    });
  };
}

async function sendProbe(
  baseURL,
  { method = "GET", path = "/", headers = {}, body = null },
) {
  const endpoint = new URL(path, baseURL);
  const payload = body === null ? null : Buffer.from(body);
  const requestHeaders = { ...headers };
  if (payload !== null) {
    requestHeaders["Content-Length"] = String(payload.length);
  }
  return await new Promise((resolve, reject) => {
    const state = { responseSeen: false };
    const request = http.request(
      {
        protocol: endpoint.protocol,
        hostname: endpoint.hostname,
        port: endpoint.port,
        path: `${endpoint.pathname}${endpoint.search}`,
        method,
        agent: false,
        headers: requestHeaders,
      },
      collectResponse(state, resolve),
    );
    request.setTimeout(10_000, () => {
      request.destroy(new Error("probe timed out"));
    });
    request.on("error", (error) => {
      if (!state.responseSeen) {
        reject(error);
      }
    });
    request.end(payload ?? undefined);
  });
}

async function writeWithBackpressure(request, buffer, responseStarted) {
  if (buffer.length === 0 || responseStarted.value) {
    return;
  }
  if (request.write(buffer)) {
    return;
  }
  await new Promise((resolve) => {
    let resolved = false;
    const finish = () => {
      if (resolved) {
        return;
      }
      resolved = true;
      request.off("drain", finish);
      request.off("error", finish);
      request.off("close", finish);
      resolve();
    };
    request.once("drain", finish);
    request.once("error", finish);
    request.once("close", finish);
    void responseStarted.promise.then(finish);
  });
}

async function sendSized(baseURL, totalBytes, { chunked = false } = {}) {
  const layout = bodyLayout(totalBytes);
  const responseStarted = {};
  responseStarted.value = false;
  responseStarted.promise = new Promise((resolve) => {
    responseStarted.resolve = resolve;
  });

  return await new Promise((resolve, reject) => {
    const state = { responseSeen: false };
    const request = http.request(
      requestOptions(baseURL, chunked ? null : totalBytes),
      collectResponse(state, resolve, () => {
        responseStarted.value = true;
        responseStarted.resolve();
      }),
    );
    request.setTimeout(180_000, () => {
      request.destroy(new Error("MCP request timed out"));
    });
    request.on("error", (error) => {
      if (!state.responseSeen) {
        reject(error);
      }
    });

    void (async () => {
      try {
        await writeWithBackpressure(
          request,
          layout.prefix,
          responseStarted,
        );
        let remaining = layout.paddingBytes;
        const chunk = Buffer.alloc(64 * 1024, "x");
        while (remaining > 0 && !responseStarted.value) {
          const size = Math.min(remaining, chunk.length);
          await writeWithBackpressure(
            request,
            chunk.subarray(0, size),
            responseStarted,
          );
          remaining -= size;
        }
        await writeWithBackpressure(
          request,
          layout.suffix,
          responseStarted,
        );
        if (!responseStarted.value) {
          request.end();
        }
      } catch (error) {
        if (!state.responseSeen) {
          reject(error);
        }
      }
    })();
  });
}

async function sendDeclaredOversize(baseURL, declaredBytes) {
  return await new Promise((resolve, reject) => {
    const state = { responseSeen: false };
    const options = requestOptions(baseURL, declaredBytes);
    options.headers.Expect = "100-continue";
    const request = http.request(options, collectResponse(state, resolve));
    request.setTimeout(30_000, () => {
      request.destroy(new Error("declared-oversize request timed out"));
    });
    request.on("continue", () => {
      // Nginx may acknowledge Expect before the upstream Go middleware
      // returns its finer 36 MiB rejection. Keep the request half-open and
      // wait for that final response; no body bytes are needed for the
      // Content-Length fast path.
    });
    request.on("error", (error) => {
      if (!state.responseSeen) {
        reject(error);
      }
    });
    // Only headers are sent. A conforming Go/Nginx boundary rejects the
    // declared size before issuing 100 Continue or reading any body bytes.
    request.flushHeaders();
  });
}

function parseServerSentJSON(label, body) {
  const messages = [];
  for (const block of body.split(/\r?\n\r?\n/)) {
    if (block.trim() === "") {
      continue;
    }
    let eventName = "";
    const data = [];
    for (const line of block.split(/\r?\n/)) {
      if (line.startsWith("event:")) {
        eventName = line.slice("event:".length).trim();
      } else if (line.startsWith("data:")) {
        data.push(line.slice("data:".length).replace(/^ /, ""));
      }
    }
    if (data.length === 0) {
      continue;
    }
    assert.ok(
      eventName === "" || eventName === "message",
      `${label}: initialize returned an unexpected SSE event`,
    );
    try {
      messages.push(JSON.parse(data.join("\n")));
    } catch {
      throw new Error(`${label}: initialize SSE data was not valid JSON`);
    }
  }
  return messages;
}

function assertInitializeSucceeded(label, response) {
  assert.equal(
    response.status,
    200,
    `${label}: initialize did not return HTTP 200`,
  );
  assert.match(
    String(response.headers["content-type"]),
    /^text\/event-stream\b/i,
    `${label}: content-type`,
  );
  const messages = parseServerSentJSON(label, response.body);
  assert.equal(
    messages.length,
    1,
    `${label}: initialize did not return exactly one JSON-RPC message`,
  );
  const message = messages[0];
  assert.equal(message?.jsonrpc, "2.0", `${label}: JSON-RPC version`);
  assert.equal(message?.id, 1, `${label}: JSON-RPC id`);
  assert.ok(
    message?.result !== null &&
      typeof message?.result === "object" &&
      !Array.isArray(message?.result),
    `${label}: JSON-RPC result is missing`,
  );
  assert.equal(
    Object.hasOwn(message, "error"),
    false,
    `${label}: initialize returned a JSON-RPC error`,
  );
  assert.equal(
    message.result.protocolVersion,
    "2025-06-18",
    `${label}: negotiated protocol version`,
  );
  assert.equal(
    message.result.serverInfo?.name,
    "connect-it",
    `${label}: server identity`,
  );
}

function assertExactJSONError(label, response, status, expectedBody) {
  assert.equal(response.status, status, `${label}: unexpected HTTP status`);
  assert.match(
    String(response.headers["content-type"]),
    /^application\/json\b/i,
    `${label}: content-type`,
  );
  let body;
  try {
    body = JSON.parse(response.body);
  } catch {
    throw new Error(`${label}: response was not valid JSON`);
  }
  if (
    body === null ||
    typeof body !== "object" ||
    Array.isArray(body) ||
    Object.keys(body).sort().join("\n") !==
      Object.keys(expectedBody).sort().join("\n")
  ) {
    throw new Error(`${label}: safe error envelope fields did not match`);
  }
  for (const [key, value] of Object.entries(expectedBody)) {
    if (body[key] !== value) {
      // Do not include the actual value: an erroneous implementation could
      // have reflected request content into this otherwise-safe response.
      throw new Error(`${label}: safe error envelope value did not match`);
    }
  }
}

function assertNoSensitiveValues(label, text, sensitiveValues) {
  for (const sensitive of sensitiveValues) {
    assert.equal(
      text.includes(sensitive.value),
      false,
      `${label}: leaked ${sensitive.label}`,
    );
  }
}

function assertTooLarge(label, response) {
  assertExactJSONError(label, response, 413, expectedTooLarge);
}

function composeArgs(...args) {
  return [
    "compose",
    "--project-name",
    composeProject,
    "--file",
    baseCompose,
    "--file",
    testCompose,
    ...args,
  ];
}

function composeExec(service, ...args) {
  const result = spawnSync(
    "docker",
    composeArgs("exec", "-T", service, ...args),
    { encoding: "utf8", maxBuffer: 8 * MiB },
  );
  if (result.status !== 0) {
    throw new Error(
      `docker compose exec ${service} failed:\n${result.stdout}\n${result.stderr}`,
    );
  }
  return `${result.stdout}${result.stderr}`;
}

function composeLogs(...services) {
  const result = spawnSync(
    "docker",
    composeArgs("logs", "--no-color", ...services),
    { encoding: "utf8", maxBuffer: 16 * MiB },
  );
  if (result.status !== 0) {
    // Do not copy log output into this error: it is the data under secret
    // inspection and may contain the very canary that caused the failure.
    throw new Error("docker compose logs failed during secret inspection");
  }
  return `${result.stdout}${result.stderr}`;
}

async function sendLogCanaryProbe(label, options) {
  let response;
  try {
    response = await sendProbe(nginxURL, options);
  } catch {
    // The path/body/header may contain a secret canary. Keep failure output
    // deliberately content-free.
    throw new Error(`${label}: ingress log canary request failed`);
  }
  assert.ok(
    response.status >= 200 && response.status < 500,
    `${label}: ingress log canary did not reach a stable HTTP response`,
  );
  return response;
}

async function waitForComposeLogMarker(marker) {
  const deadline = Date.now() + 10_000;
  while (Date.now() < deadline) {
    const logs = composeLogs("web", "connect-it");
    if (logs.includes(marker)) {
      return logs;
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error("container log capture did not reach its safe marker");
}

async function proveIngressLogsDoNotPersistSecrets() {
  const cookieCanary = `cookie-${randomBytes(24).toString("hex")}`;
  const bodyCanary = `body-${randomBytes(24).toString("hex")}`;
  const oauthStateCanary = `oauth-state-${randomBytes(24).toString("hex")}`;
  const oauthCodeCanary = `oauth-code-${randomBytes(24).toString("hex")}`;
  const installStateCanary =
    `install-state-${randomBytes(24).toString("hex")}`;
  const installProofCanary =
    `install-proof-${randomBytes(24).toString("hex")}`;

  await sendLogCanaryProbe("Cookie", {
    path: "/admin/connections",
    headers: { Cookie: `connect_it_admin=${cookieCanary}` },
  });
  await sendLogCanaryProbe("request body", {
    method: "POST",
    path: "/admin/login",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ username: "admin", password: bodyCanary }),
  });
  await sendLogCanaryProbe("OAuth callback query", {
    path:
      `/v1/oauth/callback?state=${encodeURIComponent(oauthStateCanary)}` +
      `&code=${encodeURIComponent(oauthCodeCanary)}`,
  });
  await sendLogCanaryProbe("install callback query", {
    path:
      `/v1/install/callback?state=${encodeURIComponent(installStateCanary)}` +
      `&signature=${encodeURIComponent(installProofCanary)}`,
  });

  // Positive control: the ordinary access log must contain this non-secret
  // marker after every canary request. Otherwise an empty/stale log snapshot
  // could make the absence checks pass vacuously.
  const logMarker = `safe-log-marker-${randomBytes(16).toString("hex")}`;
  const markerResponse = await sendLogCanaryProbe("log positive control", {
    path: `/healthz?ingress_log_probe=${encodeURIComponent(logMarker)}`,
  });
  assert.equal(
    markerResponse.status,
    200,
    "log positive control did not reach healthz",
  );

  const logs = await waitForComposeLogMarker(logMarker);
  assertNoSensitiveValues("Nginx/server logs", logs, [
    { label: "Bearer token", value: sessionToken },
    { label: "Cookie", value: cookieCanary },
    { label: "request body", value: bodyCanary },
    { label: "OAuth state query", value: oauthStateCanary },
    { label: "OAuth code query", value: oauthCodeCanary },
    { label: "install state query", value: installStateCanary },
    { label: "install proof query", value: installProofCanary },
  ]);
}

function setSessionStatus(status) {
  assert.ok(status === "active" || status === "revoked");
  composeExec(
    "postgres",
    "psql",
    "-X",
    "-v",
    "ON_ERROR_STOP=1",
    "-U",
    "connect_it",
    "-d",
    "connect_it",
    "-c",
    `update mcp_sessions set status = '${status}' where id = '00000000-0000-4000-8000-0000000000a1'`,
  );
}

function decodeChunkedBody(encoded) {
  const chunks = [];
  const lineBreak = Buffer.from("\r\n");
  let offset = 0;
  while (offset < encoded.length) {
    const lineEnd = encoded.indexOf(lineBreak, offset);
    if (lineEnd < 0) {
      throw new Error("raw HTTP response had an incomplete chunk size");
    }
    const sizeToken = encoded
      .subarray(offset, lineEnd)
      .toString("ascii")
      .split(";", 1)[0];
    if (!/^[0-9a-f]+$/i.test(sizeToken)) {
      throw new Error("raw HTTP response had an invalid chunk size");
    }
    const size = Number.parseInt(sizeToken, 16);
    offset = lineEnd + lineBreak.length;
    if (size === 0) {
      return Buffer.concat(chunks);
    }
    const chunkEnd = offset + size;
    if (
      chunkEnd + lineBreak.length > encoded.length ||
      !encoded.subarray(chunkEnd, chunkEnd + lineBreak.length).equals(lineBreak)
    ) {
      throw new Error("raw HTTP response had an incomplete chunk");
    }
    chunks.push(encoded.subarray(offset, chunkEnd));
    offset = chunkEnd + lineBreak.length;
  }
  throw new Error("raw HTTP response omitted its terminating chunk");
}

function parseRawHTTPResponse(rawBuffer) {
  const headerBoundary = Buffer.from("\r\n\r\n");
  const headerEnd = rawBuffer.indexOf(headerBoundary);
  if (headerEnd < 0) {
    throw new Error("raw HTTP response omitted its header boundary");
  }
  const headerLines = rawBuffer
    .subarray(0, headerEnd)
    .toString("latin1")
    .split("\r\n");
  const statusMatch = headerLines.shift()?.match(/^HTTP\/1\.[01] (\d{3})\b/);
  if (!statusMatch) {
    throw new Error("raw HTTP response had an invalid status line");
  }
  const headers = {};
  for (const line of headerLines) {
    const separator = line.indexOf(":");
    if (separator <= 0) {
      throw new Error("raw HTTP response had an invalid header");
    }
    const name = line.slice(0, separator).trim().toLowerCase();
    const value = line.slice(separator + 1).trim();
    headers[name] = headers[name] ? `${headers[name]}, ${value}` : value;
  }

  const encodedBody = rawBuffer.subarray(
    headerEnd + headerBoundary.length,
  );
  let body = encodedBody;
  if (String(headers["transfer-encoding"]).toLowerCase().includes("chunked")) {
    body = decodeChunkedBody(encodedBody);
  } else if (headers["content-length"] !== undefined) {
    const declaredLength = Number.parseInt(headers["content-length"], 10);
    if (
      !Number.isSafeInteger(declaredLength) ||
      declaredLength < 0 ||
      encodedBody.length !== declaredLength
    ) {
      throw new Error("raw HTTP response body length did not match its header");
    }
  }
  return {
    status: Number(statusMatch[1]),
    headers,
    body: body.toString("utf8"),
    raw: rawBuffer.toString("utf8"),
  };
}

function rawRequest(baseURL, initialBytes, finishRequest, timeoutMs) {
  const endpoint = new URL(baseURL);
  return new Promise((resolve, reject) => {
    const socket = net.createConnection({
      host: endpoint.hostname,
      port: Number(endpoint.port),
    });
    const response = [];
    let settled = false;
    const timeout = setTimeout(() => {
      socket.destroy();
      reject(new Error(`raw request timed out after ${timeoutMs}ms`));
    }, timeoutMs);
    socket.on("error", (error) => {
      if (!settled) {
        clearTimeout(timeout);
        reject(error);
      }
    });
    socket.on("data", (chunk) => response.push(chunk));
    socket.on("end", () => {
      settled = true;
      clearTimeout(timeout);
      try {
        resolve(parseRawHTTPResponse(Buffer.concat(response)));
      } catch {
        reject(new Error("invalid raw HTTP response"));
      }
    });
    socket.on("connect", async () => {
      try {
        socket.write(
          "POST /mcp HTTP/1.1\r\n" +
            `Host: ${endpoint.host}\r\n` +
            `Authorization: Bearer ${sessionToken}\r\n` +
            "Content-Type: application/json\r\n" +
            "Accept: application/json, text/event-stream\r\n" +
            "Transfer-Encoding: chunked\r\n" +
            "Connection: close\r\n\r\n",
        );
        socket.write(`${initialBytes.length.toString(16)}\r\n`);
        socket.write(initialBytes);
        socket.write("\r\n");
        await finishRequest(socket);
      } catch (error) {
        socket.destroy();
        reject(error);
      }
    });
  });
}

async function proveProxyDoesNotBufferRequest() {
  const layout = bodyLayout(4096);
  const first = layout.prefix;
  const rest = Buffer.concat([
    Buffer.alloc(layout.paddingBytes, "x"),
    layout.suffix,
  ]);
  setSessionStatus("active");
  try {
    const response = await rawRequest(
      nginxURL,
      first,
      async (socket) => {
        await new Promise((resolve) => setTimeout(resolve, 2000));
        // With proxy_request_buffering off, Go has already authenticated and
        // is waiting in its bounded body reader. A buffering proxy would only
        // forward after this revocation and therefore return 401.
        setSessionStatus("revoked");
        socket.write(`${rest.length.toString(16)}\r\n`);
        socket.write(rest);
        socket.write("\r\n0\r\n\r\n");
      },
      20_000,
    );
    assert.notEqual(
      response.status,
      401,
      "Nginx buffered the request before proxying",
    );
    assertInitializeSucceeded("Nginx streaming request", response);
  } finally {
    setSessionStatus("active");
  }
}

async function proveConcurrentAdmissionBudgetAndSlotRelease() {
  const slowBodyCanary = `slow-body-${randomBytes(24).toString("hex")}`;
  const slowBodyPrefix = Buffer.from(
    `{"slow_probe":"${slowBodyCanary}"`,
  );
  // Four authenticated partial bodies occupy the four production admission
  // slots. Each receives Go's 25-second timeout before Nginx's 35-second
  // client timeout.
  const blockers = Array.from({ length: 4 }, () =>
    rawRequest(
      nginxURL,
      slowBodyPrefix,
      async () => {
        // Deliberately leave the chunked request incomplete.
      },
      40_000,
    ),
  );
  await new Promise((resolve) => setTimeout(resolve, 2000));

  // A fifth, complete request must wait outside the raw-body budget. It must
  // not reach the SDK until one of the four timeout paths releases its slot.
  const waitingStartedAt = Date.now();
  const waitingRequest = sendSized(nginxURL, 4096);
  const completedEarly = await Promise.race([
    waitingRequest.then(() => true),
    new Promise((resolve) => setTimeout(() => resolve(false), 5000)),
  ]);
  assert.equal(
    completedEarly,
    false,
    "a fifth body bypassed the four-slot MCP admission budget",
  );

  const blockerResponses = await Promise.all(blockers);
  for (const [index, response] of blockerResponses.entries()) {
    assertNoSensitiveValues(`slow body ${index} response`, response.raw, [
      { label: "Bearer token", value: sessionToken },
      { label: "request body", value: slowBodyCanary },
    ]);
    assertExactJSONError(
      `slow body ${index}`,
      response,
      408,
      expectedRequestTimeout,
    );
  }
  const waitingResponse = await waitingRequest;
  assertInitializeSucceeded(
    "queued fifth request after slot release",
    waitingResponse,
  );
  const waitingMilliseconds = Date.now() - waitingStartedAt;
  assert.ok(
    waitingMilliseconds >= 15_000 && waitingMilliseconds < 35_000,
    `queued fifth request waited ${waitingMilliseconds}ms`,
  );

  const after = await sendSized(nginxURL, 4096);
  assertInitializeSucceeded(
    "request after all slow-body timeouts",
    after,
  );
}

function assertLoadedNginxMCPConfig(loadedConfig) {
  const exactLocation = loadedConfig.match(
    /location = \/mcp \{[\s\S]*?\n\s*\}/,
  );
  assert.ok(exactLocation, "loaded Nginx config lacks exact /mcp location");
  for (const directive of [
    /client_max_body_size 37m;/,
    /proxy_request_buffering off;/,
    /proxy_buffering off;/,
    /client_body_timeout 35s;/,
    /error_page 413 = @mcp_body_too_large;/,
  ]) {
    assert.match(
      exactLocation[0],
      directive,
      `loaded Nginx /mcp config lacks ${directive.source}`,
    );
  }
}

const loadedConfig = composeExec("web", "nginx", "-T");
assertLoadedNginxMCPConfig(loadedConfig);

for (const [label, baseURL] of [
  ["direct Go", goURL],
  ["real Nginx", nginxURL],
]) {
  assertInitializeSucceeded(
    `${label} legal body above 1 MiB`,
    await sendSized(baseURL, 2 * MiB),
  );
  assertInitializeSucceeded(
    `${label} exact 36 MiB body`,
    await sendSized(baseURL, 36 * MiB),
  );
}

const directOversize = await sendDeclaredOversize(goURL, 36 * MiB + 1);
const nginxOversize = await sendDeclaredOversize(nginxURL, 36 * MiB + 1);
assertTooLarge("direct Go 36 MiB + 1", directOversize);
assertTooLarge("Nginx-to-Go 36 MiB + 1", nginxOversize);

assertTooLarge(
  "chunked Nginx-to-Go 36 MiB + 1",
  await sendSized(nginxURL, 36 * MiB + 1, { chunked: true }),
);
assertTooLarge(
  "Nginx coarse cap 37 MiB + 1",
  await sendDeclaredOversize(nginxURL, 37 * MiB + 1),
);

await proveProxyDoesNotBufferRequest();
await proveConcurrentAdmissionBudgetAndSlotRelease();
await proveIngressLogsDoNotPersistSecrets();

process.stdout.write(
  "MCP Docker ingress E2E passed: direct Go + real Nginx boundaries, " +
    "chunked streaming, no proxy request buffering, and bounded concurrent " +
    "timeout slot release, with secret-free Nginx/server logs.\n",
);
