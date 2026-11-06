// Scripted OpenAI-compatible upstream for the end-to-end test.
// Turn 1: the model replies with text and a Bash tool call that writes a marker file.
// Turn 2 (once a tool result is present): the model replies with a final text answer.
// When the prompt says "use mcp to" (a skill list from the home folder may mention MCP tools), turn 1 calls the MCP tool mcp__e2e__echo instead and turn 2
// repeats its result.
// When the prompt contains "websearch-e2e", turn 1 calls WebSearch, turn 2 calls WebFetch on this
// server's /page and turn 3 repeats both results; the WebFetch summarization request (it has no
// tools) gets an answer built from the page. The same server is also the fake SearXNG
// (GET /search) and the fetched page (GET /page).
// Usage: node upstream.mjs <port>
import { createServer } from "node:http";

const port = Number(process.argv[2] ?? 4001);
const MARKER = "harness-e2e";

function sse(res, chunks) {
  res.writeHead(200, { "content-type": "text/event-stream" });
  for (const c of chunks) res.write(`data: ${JSON.stringify(c)}\n\n`);
  res.end("data: [DONE]\n\n");
}

createServer(async (req, res) => {
  if (req.method === "GET" && req.url.startsWith("/search")) {
    console.log(`searxng ${req.url}`);
    res.writeHead(200, { "content-type": "application/json" });
    res.end(JSON.stringify({ results: [{ title: "Harness e2e result", url: "https://example.com/e2e", content: "A snippet." }] }));
    return;
  }
  if (req.method === "GET" && req.url.startsWith("/page")) {
    console.log(`page ${req.url}`);
    res.writeHead(200, { "content-type": "text/html" });
    res.end(`<html><body><h1>Marker</h1><p>The marker is ${MARKER}-web.</p><script>ignored()</script></body></html>`);
    return;
  }
  let body = "";
  for await (const c of req) body += c;
  const j = JSON.parse(body || "{}");
  const roles = (j.messages ?? []).map((m) => m.role).join(",");
  const tools = (j.tools ?? []).map((t) => t.function?.name).join(",");
  console.log(`upstream ${req.method} ${req.url} messages=${roles} tools=${tools}`);

  const base = { id: "c1", object: "chat.completion.chunk", model: "mock" };
  const toolMsg = (j.messages ?? []).find((m) => m.role === "tool");
  const hasToolResult = !!toolMsg;
  const text = (c) => (typeof c === "string" ? c : (c ?? []).map((p) => p.text ?? "").join(""));
  const wantsMcp = (j.messages ?? []).some((m) => m.role === "user" && text(m.content).includes("use mcp to"));
  const wantsWeb = (j.messages ?? []).some((m) => m.role === "user" && text(m.content).includes("websearch-e2e"));
  const toolMsgs = (j.messages ?? []).filter((m) => m.role === "tool");
  const reply = (content) =>
    sse(res, [
      { ...base, choices: [{ index: 0, delta: { role: "assistant", content } }] },
      { ...base, choices: [{ index: 0, delta: {}, finish_reason: "stop" }], usage: { prompt_tokens: 120, completion_tokens: 10 } },
    ]);
  const call = (id, name, args) =>
    sse(res, [
      { ...base, choices: [{ index: 0, delta: { tool_calls: [{ index: 0, id, type: "function", function: { name, arguments: JSON.stringify(args) } }] } }] },
      { ...base, choices: [{ index: 0, delta: {}, finish_reason: "tool_calls" }], usage: { prompt_tokens: 100, completion_tokens: 20 } },
    ]);
  if (!(j.tools ?? []).length && body.includes(`${MARKER}-web`)) {
    // The WebFetch summarization request: it carries the page converted to Markdown.
    const page = (j.messages ?? []).map((m) => text(m.content)).join("\n");
    reply(page.includes("# Marker") && !page.includes("ignored()") ? `marker=${MARKER}-web` : "the page was not converted");
    return;
  }
  if (wantsWeb) {
    if (toolMsgs.length === 0) call("call_s", "WebSearch", { query: "harness e2e" });
    else if (toolMsgs.length === 1) call("call_f", "WebFetch", { url: `http://127.0.0.1:${port}/page`, prompt: "What is the marker?" });
    else {
      const search = text(toolMsgs[0].content).includes("1. Harness e2e result") ? "search ok" : "search missing";
      reply(`Web said: ${search}; ${text(toolMsgs[1].content)}`);
    }
    return;
  }
  const prompt = (j.messages ?? []).filter((m) => m.role === "user").map((m) => text(m.content)).join("\n");
  if (prompt.includes("library-recall-e2e")) {
    reply(prompt.includes("e2e_note.md") ? "recalled the note" : "the note was not loaded");
    return;
  }
  if (prompt.includes("library-e2e")) {
    const steps = [
      ["ArtifactPublish", { title: "E2E report", name: "report.html", content: "<h1>e2e page</h1>", description: "e2e report" }],
      ["Memory", { command: "write", name: "e2e_note", type: "project", description: "The e2e note", content: "Remember this." }],
      ["TaskTracker", { action: "create", title: "E2E task", project: "e2e" }],
      ["KnowledgeSearch", { query: "canary rollout", description: "e2e search" }],
    ];
    if (toolMsgs.length < steps.length) call(`call_l${toolMsgs.length}`, ...steps[toolMsgs.length]);
    else reply(`Library said: ${toolMsgs.map((m) => text(m.content)).join(" | ")}`);
    return;
  }
  if (wantsMcp) {
    if (!hasToolResult) {
      const args = JSON.stringify({ text: MARKER, reveal: true });
      sse(res, [
        { ...base, choices: [{ index: 0, delta: { tool_calls: [{ index: 0, id: "call_m", type: "function", function: { name: "mcp__e2e__echo", arguments: args } }] } }] },
        { ...base, choices: [{ index: 0, delta: {}, finish_reason: "tool_calls" }], usage: { prompt_tokens: 100, completion_tokens: 20 } },
      ]);
    } else {
      sse(res, [
        { ...base, choices: [{ index: 0, delta: { role: "assistant", content: `MCP said: ${text(toolMsg.content)}` } }] },
        { ...base, choices: [{ index: 0, delta: {}, finish_reason: "stop" }], usage: { prompt_tokens: 150, completion_tokens: 10 } },
      ]);
    }
    return;
  }
  if (!hasToolResult) {
    const args = JSON.stringify({ command: `echo ${MARKER} > out.txt && cat out.txt`, description: "Write marker file" });
    sse(res, [
      { ...base, choices: [{ index: 0, delta: { role: "assistant", content: "Creating the file." } }] },
      { ...base, choices: [{ index: 0, delta: { tool_calls: [{ index: 0, id: "call_1", type: "function", function: { name: "Bash", arguments: "" } }] } }] },
      { ...base, choices: [{ index: 0, delta: { tool_calls: [{ index: 0, function: { arguments: args } }] } }] },
      { ...base, choices: [{ index: 0, delta: {}, finish_reason: "tool_calls" }], usage: { prompt_tokens: 100, completion_tokens: 20 } },
    ]);
  } else {
    sse(res, [
      { ...base, choices: [{ index: 0, delta: { role: "assistant", content: `Done: out.txt contains ${MARKER}.` } }] },
      { ...base, choices: [{ index: 0, delta: {}, finish_reason: "stop" }], usage: { prompt_tokens: 150, completion_tokens: 10 } },
    ]);
  }
}).listen(port, "127.0.0.1", () => console.log(`upstream listening on ${port}`));
