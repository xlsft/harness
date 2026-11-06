Gives an agent of this session a message. It keeps everything it knows.

- A running agent reads the message after its current tool calls. A finished, stopped or interrupted agent starts working again, and its next final message comes to you as a report.
- `to` is the agent's id, or its name when only one agent has it. ListAgents shows both.
- The agent sees the message as coming from you, not from the user. Say what it needs: it does not see this conversation.
- Prefer this to starting a new agent when the work builds on what one already did.
- Members of a team use this to reach each other. A subagent reaches the session's main agent with `to: "lead"`; that wakes it if it is idle.
