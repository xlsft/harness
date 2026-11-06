"""The catalog through `harness-client plugin`, from a local path, with a temporary HARNESS_HOME and no network.

The real entries point at GitHub, so installing them here uses a copy of the catalog whose sources point at throwaway local
repositories holding the same folders. Everything else about the entries stays as it is listed.
"""
import json
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
CATALOG = ROOT / "catalog"
MARKET = "harness-catalog"


def sh(*args, cwd, env=None):
    return subprocess.run(args, cwd=cwd, env=env, capture_output=True, text=True, timeout=300)


@unittest.skipUnless(shutil.which("go") and shutil.which("git"), "needs go and git")
class Install(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory(prefix="harness-catalog-install-")
        cls.addClassCleanup(cls.tmp.cleanup)
        cls.work = Path(cls.tmp.name).resolve()
        cls.client = cls.work / "harness-client"
        built = sh("go", "build", "-o", str(cls.client), "./cmd/harness-client", cwd=ROOT / "packages" / "client")
        if built.returncode != 0:
            raise RuntimeError(built.stderr)
        cls.manifest = json.loads((CATALOG / ".claude-plugin" / "marketplace.json").read_text(encoding="utf-8"))

    def setUp(self):
        self.home = Path(tempfile.mkdtemp(prefix="home-", dir=self.work))
        self.env = {
            "PATH": os.environ["PATH"],
            "HOME": str(self.home),
            "HARNESS_HOME": str(self.home / "harness"),
            "HARNESS_KEYRING": "0",
            "GIT_CONFIG_GLOBAL": os.devnull,
            "GIT_CONFIG_NOSYSTEM": "1",
            "GIT_TERMINAL_PROMPT": "0",
        }

    def plugin(self, *args):
        return sh(str(self.client), "plugin", *args, cwd=self.work, env=self.env)

    def ok(self, *args):
        done = self.plugin(*args)
        self.assertEqual(done.returncode, 0, done.stderr + done.stdout)
        return done.stdout

    def git(self, repo, *args):
        done = sh("git", "-c", "user.name=t", "-c", "user.email=t@example.invalid", *args, cwd=repo, env=self.env)
        self.assertEqual(done.returncode, 0, done.stderr)
        return done.stdout.strip()

    def local_repo(self, name, entries):
        """A repository serving parts of itself, holding each entry's folder and license, and a folder no install may bring."""
        repo = self.home / "upstream" / name
        repo.mkdir(parents=True)
        self.git(repo, "init", "--quiet", "-b", "main")
        self.git(repo, "config", "uploadpack.allowFilter", "true")
        (repo / "skills" / "docx").mkdir(parents=True)
        (repo / "skills" / "docx" / "SKILL.md").write_text("---\nname: docx\ndescription: Must stay on the server\n---\n", encoding="utf-8")
        for path in [e["source"]["path"] for e in entries]:
            leaf = path.rsplit("/", 1)[-1]
            folder = repo / path
            if path.startswith("skills/"):
                folder.mkdir(parents=True)
                (folder / "SKILL.md").write_text(f"---\nname: {leaf}\ndescription: Stand-in for {leaf}\n---\n\nStand-in.\n", encoding="utf-8")
            else:
                (folder / ".claude-plugin").mkdir(parents=True)
                (folder / ".claude-plugin" / "plugin.json").write_text(json.dumps({"name": leaf, "version": "1.0.0"}), encoding="utf-8")
                (folder / "skills" / "one").mkdir(parents=True)
                (folder / "skills" / "one" / "SKILL.md").write_text("---\nname: one\ndescription: Stand-in\n---\n\nStand-in.\n", encoding="utf-8")
        for e in entries:
            (repo / e["licenseFile"]).write_text(f"License at {e['licenseFile']}.\n", encoding="utf-8")
        self.git(repo, "add", ".")
        self.git(repo, "commit", "--quiet", "-m", "fixture")
        return repo, self.git(repo, "rev-parse", "HEAD")

    def local_catalog(self):
        """A copy of the catalog whose sources point at local repositories, pinned to their commits."""
        manifest = json.loads(json.dumps(self.manifest))
        by_repo = {}
        for e in manifest["plugins"]:
            if isinstance(e["source"], dict):
                by_repo.setdefault(e["catalog"]["upstream"], []).append(e)
        for upstream, entries in by_repo.items():
            repo, sha = self.local_repo(upstream.replace("/", "-"), entries)
            for e in entries:
                e["source"]["url"], e["source"]["sha"] = f"file://{repo}", sha
        copy = self.home / "catalog-copy"
        shutil.copytree(CATALOG, copy)
        (copy / ".claude-plugin" / "marketplace.json").write_text(json.dumps(manifest), encoding="utf-8")
        self.assertNotIn("github.com/anthropics/skills.git", (copy / ".claude-plugin" / "marketplace.json").read_text(encoding="utf-8"))
        return copy, manifest

    def test_the_catalog_validates(self):
        out = self.ok("validate", str(CATALOG))
        self.assertIn(MARKET, out)
        self.assertIn(f"Plugins: {len(self.manifest['plugins'])}", out)

    def test_it_registers_from_a_local_path_and_lists_every_entry(self):
        self.assertIn("Nothing was installed", self.ok("marketplace", "add", str(CATALOG)))
        out = self.ok("marketplace", "list")
        for e in self.manifest["plugins"]:
            self.assertIn(f"{e['name']}@{MARKET}", out)
        self.assertIn("No plugins are installed", self.ok("list"))
        self.assertTrue((self.home / "harness" / "plugins" / "state.json").is_file(), "the state belongs under HARNESS_HOME")

    def test_every_entry_installs_pinned_and_runs_nothing(self):
        copy, manifest = self.local_catalog()
        self.ok("marketplace", "add", str(copy))
        for e in manifest["plugins"]:
            pid = f"{e['name']}@{MARKET}"
            out = self.ok("install", pid)
            self.assertIn("Nothing of it runs yet", out, pid)
            if isinstance(e["source"], dict):
                self.assertIn(f"Commit: {e['source']['sha']}", out, pid)
            self.assertIn("needs approval", out, pid)
            self.assertIn(f"{e['name']}:", out, pid)
        state = json.loads((self.home / "harness" / "plugins" / "state.json").read_text(encoding="utf-8"))
        for e in manifest["plugins"]:
            record = state["plugins"][f"{e['name']}@{MARKET}"]
            folder = self.home / "harness" / "plugins" / record["dir"]
            self.assertTrue(folder.is_dir())
            base = e["licenseFile"].rsplit("/", 1)[-1]
            self.assertTrue((folder / base).is_file(), f"{e['name']} was installed without {base}")
            self.assertFalse((folder / "skills" / "docx").exists(), e["name"])
            if isinstance(e["source"], dict):
                self.assertEqual(record["commit"], e["source"]["sha"])
                self.assertIn(f"License at {e['licenseFile']}.", (folder / base).read_text(encoding="utf-8"))
            else:
                self.assertIn("Apache License", (folder / base).read_text(encoding="utf-8"))
        self.assertFalse((self.home / ".claude").exists(), "nothing goes under .claude")

    def test_an_entrys_license_travels_with_its_record(self):
        copy, manifest = self.local_catalog()
        self.ok("marketplace", "add", str(copy))
        name = next(e["name"] for e in manifest["plugins"] if isinstance(e["source"], dict) and e["source"]["path"].startswith("skills/"))
        self.ok("install", f"{name}@{MARKET}")
        state = json.loads((self.home / "harness" / "plugins" / "state.json").read_text(encoding="utf-8"))
        self.assertEqual(state["plugins"][f"{name}@{MARKET}"]["overlay"]["license"], "Apache-2.0")

    def external(self, manifest):
        return next(e for e in manifest["plugins"] if isinstance(e["source"], dict))

    def test_a_pin_that_does_not_exist_is_refused(self):
        copy, manifest = self.local_catalog()
        entry = self.external(manifest)
        entry["source"]["sha"] = "0" * 40
        (copy / ".claude-plugin" / "marketplace.json").write_text(json.dumps(manifest), encoding="utf-8")
        self.ok("marketplace", "add", str(copy))
        done = self.plugin("install", f"{entry['name']}@{MARKET}")
        self.assertNotEqual(done.returncode, 0)
        self.assertNotIn(entry["name"], self.ok("list"))

    def test_a_server_that_cannot_send_a_part_is_refused(self):
        copy, manifest = self.local_catalog()
        entry = self.external(manifest)
        repo = entry["source"]["url"].removeprefix("file://")
        self.git(repo, "config", "uploadpack.allowFilter", "false")
        self.ok("marketplace", "add", str(copy))
        done = self.plugin("install", f"{entry['name']}@{MARKET}")
        self.assertNotEqual(done.returncode, 0)
        self.assertIn("can't send part of a repository", done.stderr + done.stdout)

    def test_without_a_terminal_nothing_is_approved(self):
        copy, manifest = self.local_catalog()
        self.ok("marketplace", "add", str(copy))
        pid = f"{self.external(manifest)['name']}@{MARKET}"
        self.ok("install", pid)
        done = subprocess.run([str(self.client), "plugin", "approve", pid], cwd=self.work, env=self.env, stdin=subprocess.DEVNULL, capture_output=True, text=True)
        self.assertNotEqual(done.returncode, 0)
        self.assertIn("needs approval", self.ok("info", pid))


if __name__ == "__main__":
    unittest.main()
