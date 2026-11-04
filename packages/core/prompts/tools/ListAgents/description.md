Lists the agents of this session with id, name, type, state and model, and the agent types you can start.

States: `queued` (waiting for a free slot), `running`, `finished`, `failed`, `stopped`, `interrupted` (it was running when the client stopped). You are told when an agent ends, so there is no need to call this to wait for one.
