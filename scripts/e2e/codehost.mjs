// A fake code host for scripts/e2e/hosts.sh: enough of GitLab's REST API for merge requests, and real git over HTTP through
// `git http-backend`, which checks credentials the way a code host does. No network.
// Usage: node codehost.mjs <port> <repos-dir> <token>
import { spawn } from "node:child_process";
import { createServer } from "node:http";

const port = Number(process.argv[2]);
const reposDir = process.argv[3];
const token = process.argv[4];
const mrs = [];

function json(res, status, body) {
  res.writeHead(status, { "content-type": "application/json" });
  res.end(JSON.stringify(body));
}

function mrView(m) {
  return { id: m.iid, iid: m.iid, title: m.title, description: m.description, state: "opened", source_branch: m.source_branch, target_branch: m.target_branch, web_url: `http://127.0.0.1:${port}/acme/widgets/-/merge_requests/${m.iid}`, author: { username: "host" }, updated_at: new Date().toISOString(), draft: /^draft:/i.test(m.title), sha: "0".repeat(40) };
}

async function readBody(req) {
  const chunks = [];
  for await (const c of req) chunks.push(c);
  return Buffer.concat(chunks);
}

/** Git's smart HTTP through the CGI that ships with git. Pushing needs the token as HTTP basic credentials. */
async function git(req, res, url) {
  const auth = req.headers.authorization ?? "";
  const given = auth.startsWith("Basic ") ? Buffer.from(auth.slice(6), "base64").toString() : "";
  if (given !== `oauth2:${token}`) {
    console.log(`git ${req.method} ${url.pathname} refused`);
    res.writeHead(401, { "www-authenticate": 'Basic realm="fake"' });
    res.end();
    return;
  }
  console.log(`git ${req.method} ${url.pathname}${url.search} ok`);
  const child = spawn("git", ["http-backend"], {
    env: {
      PATH: process.env.PATH,
      GIT_PROJECT_ROOT: reposDir,
      GIT_HTTP_EXPORT_ALL: "1",
      REQUEST_METHOD: req.method,
      PATH_INFO: url.pathname,
      QUERY_STRING: url.search.slice(1),
      CONTENT_TYPE: req.headers["content-type"] ?? "",
      CONTENT_LENGTH: req.headers["content-length"] ?? "",
      HTTP_CONTENT_ENCODING: req.headers["content-encoding"] ?? "",
      GIT_CONFIG_NOSYSTEM: "1",
      GIT_CONFIG_GLOBAL: "/dev/null",
      REMOTE_USER: "oauth2",
    },
  });
  req.pipe(child.stdin);
  let head = Buffer.alloc(0);
  let started = false;
  child.stdout.on("data", (chunk) => {
    if (started) return void res.write(chunk);
    head = Buffer.concat([head, chunk]);
    const i = head.indexOf("\r\n\r\n");
    if (i < 0) return;
    const lines = head.subarray(0, i).toString().split("\r\n");
    let status = 200;
    const headers = {};
    for (const l of lines) {
      const [k, v] = [l.slice(0, l.indexOf(":")), l.slice(l.indexOf(":") + 1).trim()];
      if (k.toLowerCase() === "status") status = Number(v.split(" ")[0]);
      else headers[k] = v;
    }
    res.writeHead(status, headers);
    res.write(head.subarray(i + 4));
    started = true;
  });
  child.stderr.on("data", (d) => process.stderr.write(d));
  child.on("close", () => res.end());
}

createServer(async (req, res) => {
  const url = new URL(req.url, `http://127.0.0.1:${port}`);
  if (url.pathname.endsWith("/info/refs") || url.pathname.endsWith("/git-upload-pack") || url.pathname.endsWith("/git-receive-pack")) return git(req, res, url);
  if (req.headers.authorization !== `Bearer ${token}`) return json(res, 401, { message: "401 Unauthorized" });
  if (url.pathname === "/api/v4/user") return json(res, 200, { username: "e2e-bot" });
  const m = /^\/api\/v4\/projects\/([^/]+)\/merge_requests(?:\/(\d+)(\/.*)?)?$/.exec(url.pathname);
  if (m) {
    if (req.method === "POST" && !m[2]) {
      const body = JSON.parse((await readBody(req)).toString());
      const mr = { iid: mrs.length + 1, ...body };
      mrs.push(mr);
      console.log(`merge request created ${mr.source_branch} -> ${mr.target_branch}: ${mr.title}`);
      return json(res, 201, mrView(mr));
    }
    if (req.method === "GET" && !m[2]) {
      const branch = url.searchParams.get("source_branch");
      return json(res, 200, mrs.filter((x) => !branch || x.source_branch === branch).map(mrView));
    }
    if (m[3] === "/approvals") return json(res, 200, { approved: false, approved_by: [] });
    if (req.method === "GET" && m[2] && !m[3]) {
      const mr = mrs.find((x) => x.iid === Number(m[2]));
      return mr ? json(res, 200, mrView(mr)) : json(res, 404, { message: "404" });
    }
  }
  console.log(`unhandled ${req.method} ${req.url}`);
  json(res, 404, { message: "404 Not Found" });
}).listen(port, "127.0.0.1", () => console.log(`code host on ${port}`));
