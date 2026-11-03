Closes a turn with a small box the user sees under your last message: a recap, and when the next move is theirs, one question with buttons. A click on a button sends its label to you as the user's reply.

Call it once, when the work of this turn is done, in every turn in which you changed files, ran commands or finished work with several steps. Skip it for a short answer or plain conversation. After the call, write your closing message: your full answer to the user, which the box does not replace. Make no more tool calls after it in this turn.

- `recap`: two or three short sentences on what is now true and where it stands, including what is not done or not verified (nothing committed, not checked in the app). The user may read it away from the conversation, so do not point at anything else ("see above", "listed below").
- `ask`: only when the turn leaves the user a decision or a step to take, such as a choice of approach or a go-ahead to fix, commit or push. One decision in one short sentence, without the reason, your recommendation or the options. Leave it out when nothing waits on them. Do not repeat this question in your closing message.
- `actions`: the answers to `ask`, one or two, in the user's words ("Yes, fix it", "Later"). The first is your recommendation; add a second only if the user might really pick it.
