Asks for a turn of this session later, so you can stop now and pick up when there is something to look at. The call returns at once. When the time comes you get your note as a reminder, which starts a turn if the session is idle; if the session was closed on this machine it opens again first.

- Set exactly one of: `delay_seconds` or `at` for one time (at least 60 seconds from now), `every_seconds` (at least 60) or `cron` to repeat.
- `note` is what you will read then, and all you are told. Say what to check and what to do about it, as if to someone who hasn't seen this turn.
- A repeating wake-up ends after `max_runs` runs (48 by default, 200 at most) and after a week. Cancel it with ScheduleCancel as soon as it has done its job.
- Pick the longest delay that works: each wake-up is a turn the user pays for. To wait inside this turn for something quick, use Wait; for lines from a running task, Monitor.
- The wake-up belongs to this session and this machine. It runs only while the client is up here; a time missed while it's down comes as soon as it is back.
- Use it for what you will act on yourself. A task the user wants run on their own schedule is theirs to make: mention the Scheduled screen.
