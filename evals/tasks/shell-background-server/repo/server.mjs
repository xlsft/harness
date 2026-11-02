// Development server. It warms up for a few seconds, then listens on a random free port,
// prints "listening on http://127.0.0.1:<port>" and writes its pid to server.pid.
// GET /version returns the build id computed at startup.
import { createServer } from "node:http";
import { createHash } from "node:crypto";
import { readFileSync, writeFileSync } from "node:fs";

const build = createHash("sha256").update(readFileSync(new URL("./build.seed", import.meta.url))).update(String(process.pid % 1)).digest("hex").slice(0, 12);
console.log("warming up caches...");
setTimeout(() => {
  const server = createServer((req, res) => {
    if (req.url === "/version") { res.end(JSON.stringify({ build })); return; }
    res.statusCode = 404; res.end("not found");
  });
  server.listen(0, "127.0.0.1", () => {
    writeFileSync(new URL("./server.pid", import.meta.url), String(process.pid));
    console.log(`listening on http://127.0.0.1:${server.address().port}`);
  });
}, 3000);
