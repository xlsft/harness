Suggests a git branch for the work in the current folder. The user gets a card to accept it, type another name or decline; once accepted, the branch is created from where you are, or switched to if it exists, and uncommitted changes come along.

Use it when work is about to start, or has grown, on a branch it should not stay on, such as the default branch. Do not use it when the user already named a branch or told you to stay, and do not switch branches behind their back with Bash when a suggestion would do.

Follow the repository's naming if you can see one (`git branch` and recent history); otherwise a short lowercase name that says what the work is. If the user typed another name, the result says which branch you are on. If they declined, stay on your branch and do not suggest again in this session unless asked.

- `name`: the branch name.
- `reason`: optional, one sentence shown on the card.
