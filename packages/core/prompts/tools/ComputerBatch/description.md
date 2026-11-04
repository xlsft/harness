Runs several actions on one app in order, in one call, when you can predict them: click a field, type, press Enter, take a screenshot. Each step passes the same checks as its own tool, and the first refusal ends the batch; the result says what ran. Coordinates refer to the last screenshot of the app taken before each step.

Steps: `click` (x, y, button, count, modifiers), `drag` (from_x, from_y, to_x, to_y), `scroll` (x, y, dx, dy), `type` (text), `key` (keys, repeat), `wait` (ms, at most 5000), `screenshot`.
