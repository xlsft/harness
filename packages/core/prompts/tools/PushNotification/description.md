Sends the user a system notification saying that this session needs them: on their desktop or in their browser, and on another device that is following the session.

Use it when the user has probably stepped away and something now waits on them or would change what they do next: a long job they were waiting for ended, a decision blocks the work, something failed in a way only they can fix. Do not use it for progress updates, for ordinary questions while they are in the conversation, or to repeat your closing message.

The client decides whether it goes out. It stays quiet while the user is looking at this session, and it limits how often a session can notify. The result says what happened; when nothing was sent, carry on without retrying.

- `title`: up to 80 characters, what this is about ("Migration finished").
- `body`: up to 300 characters, what happened and what you need from them. It is read away from the conversation, so make it stand on its own.
