#!/usr/bin/env python3
"""VCS hooks of xlsft`s harness for Jujutsu (docs/design/vcs-hooks.md).

One script for the four events; it reads which from hook_event_name:

- WorktreeCreate: `jj workspace add` next to the repository, in <repo>-workspaces/<name> (or $HARNESS_JJ_WORKSPACES/<name>).
- WorktreeRemove: snapshots the workspace, `jj workspace forget`, then deletes its folder. The workspace's last commit
  stays in the repository, so nothing is lost and `force` changes nothing here.
- VcsStatus: the files `jj diff --git -r @` changes, with line counts, against @-.
- VcsDiff: `jj diff --git -r @` for the paths asked for.

A folder outside a Jujutsu repository gets no output, which declines: a git repository then gets a git worktree, and the
Changes pane keeps its own rules. Needs jj and Python 3; uses nothing else.
"""

import json
import os
import shutil
import subprocess
import sys


def jj(args, cwd):
    r = subprocess.run(["jj", "--no-pager", "--color=never", *args], cwd=cwd, capture_output=True, text=True)
    if r.returncode != 0:
        sys.stderr.write(r.stderr or "jj %s failed\n" % " ".join(args))
        sys.exit(1)
    return r.stdout


def repo_root(cwd):
    try:
        r = subprocess.run(["jj", "--no-pager", "root"], cwd=cwd, capture_output=True, text=True)
    except OSError:
        return None
    return r.stdout.strip() if r.returncode == 0 and r.stdout.strip() else None


def fileset(path):
    """A path as a jj fileset that means exactly that file, relative to the workspace root."""
    return 'root-file:"%s"' % path.replace("\\", "\\\\").replace('"', '\\"')


def parse_git_diff(text):
    files, cur, in_hunk = [], None, False
    for line in text.split("\n"):
        if line.startswith("diff --git "):
            a, _, b = line[len("diff --git "):].partition(" b/")
            cur = {"path": b, "status": "modified", "additions": 0, "deletions": 0}
            old = a[2:] if a.startswith("a/") else a
            if old != b:
                cur["status"], cur["oldPath"] = "renamed", old
            files.append(cur)
            in_hunk = False
        elif cur is None:
            continue
        elif line.startswith("@@"):
            in_hunk = True
        elif not in_hunk:
            if line.startswith("new file mode"):
                cur["status"] = "added"
            elif line.startswith("deleted file mode"):
                cur["status"] = "deleted"
            elif line.startswith("rename from "):
                cur["status"], cur["oldPath"] = "renamed", line[len("rename from "):]
            elif line.startswith("rename to "):
                cur["path"] = line[len("rename to "):]
            elif line.startswith("Binary files") or line.startswith("GIT binary patch"):
                cur["binary"] = True
        elif line.startswith("+"):
            cur["additions"] += 1
        elif line.startswith("-"):
            cur["deletions"] += 1
    for f in files:
        if f.get("binary"):
            del f["additions"], f["deletions"]
    return files


def specific(event, **fields):
    print(json.dumps({"hookSpecificOutput": dict(hookEventName=event, **fields)}))


def worktree_create(data):
    source = data["source_dir"]
    root = repo_root(source)
    if root is None:
        return  # not ours: decline
    name = data["name"]
    parent = os.environ.get("HARNESS_JJ_WORKSPACES") or os.path.join(os.path.dirname(root), os.path.basename(root) + "-workspaces")
    os.makedirs(parent, exist_ok=True)
    dest = os.path.join(parent, name)
    args = ["workspace", "add", "--name", name]
    if data.get("base_revision"):
        args += ["--revision", data["base_revision"]]
    jj(args + [dest], root)
    rel = os.path.relpath(os.path.realpath(source), os.path.realpath(root))
    cwd = dest if rel == "." else os.path.join(dest, rel)
    if not os.path.isdir(cwd):
        cwd = dest
    base = jj(["log", "--no-graph", "-r", "@-", "-T", 'change_id.short() ++ "\\n"'], dest).split("\n")[0].strip()
    specific("WorktreeCreate", worktreePath=dest, cwd=cwd, baseRevision=base)


def worktree_remove(data):
    path = data["worktree_path"]
    if not os.path.isdir(path):
        return
    name = data.get("name") or os.path.basename(path)
    jj(["status"], path)  # snapshot the last edits into the workspace's commit
    jj(["workspace", "forget", name], path)
    shutil.rmtree(path)


def vcs_status(data):
    root = repo_root(data["cwd"])
    if root is None:
        return
    files = parse_git_diff(jj(["diff", "--git", "-r", "@"], root))
    specific("VcsStatus", root=root, base="@-", files=files)


def vcs_diff(data):
    root = repo_root(data["cwd"])
    if root is None:
        return
    paths = [fileset(p) for p in data.get("paths") or []]
    sys.stdout.write(jj(["diff", "--git", "-r", "@", *paths], root))


def main():
    data = json.load(sys.stdin)
    handlers = {"WorktreeCreate": worktree_create, "WorktreeRemove": worktree_remove, "VcsStatus": vcs_status, "VcsDiff": vcs_diff}
    handler = handlers.get(data.get("hook_event_name"))
    if handler is None:
        sys.stderr.write("jj-hooks.py does not handle %s\n" % data.get("hook_event_name"))
        sys.exit(1)
    handler(data)


if __name__ == "__main__":
    main()
