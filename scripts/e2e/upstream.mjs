// Scripted OpenAI-compatible upstream for the end-to-end test.
// Turn 1: the model replies with text and a Bash tool call that writes a marker file.
// Turn 2 (once a tool result is present): the model replies with a final text answer.
// When the prompt mentions "mcp", turn 1 calls the MCP tool mcp__e2e__echo instead and turn 2
// repeats its result.
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
  const wantsMcp = (j.messages ?? []).some((m) => m.role === "user" && text(m.content).includes("mcp"));
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
