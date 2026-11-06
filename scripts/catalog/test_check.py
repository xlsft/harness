"""Tests of the catalog check, all offline: licenses come from a fixtures folder."""
import contextlib
import io
import json
import re
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import check  # noqa: E402

TESTDATA = Path(__file__).resolve().parent / "testdata"
APACHE = (TESTDATA / "apache-2.0.txt").read_text(encoding="utf-8")
MIT = (TESTDATA / "mit.txt").read_text(encoding="utf-8")
SHA = "a" * 40
SKILLS_URL = "https://github.com/anthropics/skills.git"


def entry(name="kit", path="skills/kit", license_id="Apache-2.0", repo="example/kit", url=None, **catalog_fields):
    meta = {"upstream": repo}
    license_file = catalog_fields.pop("licenseFile", f"{path}/LICENSE.txt")
    meta.update(catalog_fields)
    return {
        "name": name,
        "license": license_id,
        "licenseFile": license_file,
        "description": f"A kit. Origin: {repo}.",
        "source": {"source": "git-subdir", "url": url or f"https://github.com/{repo}.git", "path": path, "sha": SHA},
        "catalog": meta,
    }


class CatalogCase(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory(prefix="harness-catalog-check-")
        self.addCleanup(tmp.cleanup)
        self.dir = Path(tmp.name)
        self.catalog = self.dir / "catalog"
        self.fixtures = check.FixtureFetcher(self.dir / "fixtures")

    def upstream(self, text, repo="example/kit", path="skills/kit/LICENSE.txt"):
        file = self.dir / "fixtures" / repo / SHA / path
        file.parent.mkdir(parents=True, exist_ok=True)
        file.write_text(text, encoding="utf-8")

    def run_check(self, *entries, fetcher=None):
        (self.catalog / ".claude-plugin").mkdir(parents=True, exist_ok=True)
        (self.catalog / ".claude-plugin" / "marketplace.json").write_text(json.dumps({"name": "t", "plugins": list(entries)}), encoding="utf-8")
        errors, _ = check.check_catalog(self.catalog, fetcher)
        return errors

    def assertRefused(self, errors, text):
        self.assertTrue(any(text in e for e in errors), f"{text!r} not in {errors}")


class Rules(CatalogCase):
    def test_a_pinned_entry_with_its_license_passes(self):
        self.upstream(APACHE)
        self.assertEqual(self.run_check(entry(), fetcher=self.fixtures), [])

    def test_no_license_field(self):
        e = entry()
        del e["license"]
        self.assertRefused(self.run_check(e), "no license field")

    def test_unlisted_license(self):
        self.assertRefused(self.run_check(entry(license_id="GPL-3.0-only")), "not on the list")
        self.assertRefused(self.run_check(entry(license_id="Proprietary")), "not on the list")

    def test_unpinned_sources(self):
        for sha in (None, "main", "abc1234", "A" * 40, "a" * 39):
            e = entry()
            if sha is None:
                del e["source"]["sha"]
                e["source"]["ref"] = "main"
            else:
                e["source"]["sha"] = sha
            self.assertRefused(self.run_check(e), "unpinned")

    def test_an_unpinned_entry_is_not_fetched(self):
        e = entry()
        del e["source"]["sha"]
        self.run_check(e, fetcher=self.fixtures)
        self.assertEqual(self.fixtures.asked, [])

    def test_sources_that_cannot_be_pinned(self):
        for kind in ("npm", "archive", "command"):
            self.assertRefused(self.run_check({**entry(), "source": {"source": kind, "package": "x"}}), "can't be pinned")

    def test_no_source_and_duplicates(self):
        e = entry()
        del e["source"]
        self.assertRefused(self.run_check(e), "no source")
        self.assertRefused(self.run_check(entry(), entry()), "listed twice")

    def test_an_empty_catalog(self):
        self.assertRefused(self.run_check(), "no plugins")

    def test_a_host_we_cannot_verify(self):
        self.assertRefused(self.run_check(entry(url="https://example.com/kit.git")), "only GitHub")

    def test_a_path_that_leaves_the_repository(self):
        self.assertRefused(self.run_check(entry(path="skills/../../other")), "leaves the repository")


class Origin(CatalogCase):
    def test_claims_of_affiliation_are_refused(self):
        for text in ("The official kit. Origin: example/kit.", "Endorsed by them. Origin: example/kit.", "Made by Anthropic. Origin: example/kit.", "Anthropic's kit. Origin: example/kit."):
            self.assertRefused(self.run_check({**entry(), "description": text}), "as a fact")

    def test_the_description_names_its_origin(self):
        self.assertRefused(self.run_check({**entry(), "description": "A kit."}), "must name its origin")

    def test_an_entry_carries_no_logo(self):
        self.assertRefused(self.run_check({**entry(), "logo": "./logo.png"}), "carries no logo")

    def test_upstream_must_match_the_source(self):
        e = entry()
        e["catalog"]["upstream"] = "someone/else"
        self.assertRefused(self.run_check(e), "catalog.upstream")


class Forbidden(CatalogCase):
    SPELLINGS = (SKILLS_URL, "https://github.com/anthropics/skills", "https://github.com/Anthropics/Skills.git", "git@github.com:anthropics/skills.git", "ssh://git@github.com/anthropics/skills.git", "anthropics/skills")

    def test_the_four_are_refused_however_they_are_spelled_and_never_fetched(self):
        for name in ("docx", "pdf", "pptx", "xlsx"):
            for url in self.SPELLINGS:
                for path in (f"skills/{name}", f"skills/{name}/", f"./skills/{name}", f"skills/{name.upper()}", f"skills/{name}/scripts", f"skills/x/../{name}"):
                    if "../" in path:
                        continue  # any .. is refused as leaving; see below
                    errors = self.run_check(entry(name="renamed", path=path, repo="anthropics/skills", url=url), fetcher=self.fixtures)
                    self.assertRefused(errors, "forbidden")
                    self.assertEqual(self.fixtures.asked, [], (name, url, path))

    def test_their_names_are_refused_even_with_another_path(self):
        for name in ("docx", "pdf", "pptx", "xlsx"):
            self.assertRefused(self.run_check(entry(name=name, path="skills/kit", repo="anthropics/skills", url=SKILLS_URL)), "forbidden")

    def test_a_folder_holding_them_is_refused(self):
        for path in ("skills", "."):
            self.assertRefused(self.run_check(entry(path=path, repo="anthropics/skills", url=SKILLS_URL)), "forbidden")
        whole = {"name": "all", "license": "Apache-2.0", "description": "All. Origin: anthropics/skills.", "licenseFile": "skills/kit/LICENSE.txt", "source": {"source": "github", "repo": "anthropics/skills", "sha": SHA}, "catalog": {"upstream": "anthropics/skills"}}
        self.assertRefused(self.run_check(whole, fetcher=self.fixtures), "forbidden")
        self.assertEqual(self.fixtures.asked, [])

    def test_a_climb_back_into_them_is_refused(self):
        errors = self.run_check(entry(path="skills/kit/../docx", repo="anthropics/skills", url=SKILLS_URL), fetcher=self.fixtures)
        self.assertRefused(errors, "leaves the repository")
        self.assertEqual(self.fixtures.asked, [])

    def test_a_license_file_inside_them_is_not_fetched(self):
        errors = self.run_check(entry(path="skills/kit", repo="anthropics/skills", url=SKILLS_URL, licenseFile="skills/docx/LICENSE.txt"), fetcher=self.fixtures)
        self.assertRefused(errors, "forbidden")
        self.assertEqual(self.fixtures.asked, [])

    def test_the_fetchers_refuse_on_their_own(self):
        with self.assertRaises(LookupError):
            self.fixtures.fetch("anthropics/skills", SHA, "skills/pptx/LICENSE.txt")
        with self.assertRaises(LookupError):
            check.GhFetcher().fetch("anthropics/skills", SHA, "skills/xlsx/SKILL.md")

    def test_a_same_named_skill_elsewhere_is_fine(self):
        self.upstream(APACHE, repo="example/kit", path="skills/pdf/LICENSE.txt")
        self.assertEqual(self.run_check(entry(name="pdf-tool", path="skills/pdf"), fetcher=self.fixtures), [])

    def test_the_excluded_are_refused(self):
        for name in check.EXCLUDED_SKILLS:
            self.assertRefused(self.run_check(entry(name=name, path=f"skills/{name}", repo="anthropics/skills", url=SKILLS_URL)), "excluded")

    def test_a_local_entry_claiming_their_skill_is_refused(self):
        folder = self.catalog / "plugins" / "docx"
        folder.mkdir(parents=True)
        (folder / "LICENSE").write_text(MIT, encoding="utf-8")
        e = {"name": "docx", "license": "MIT", "description": "Words.", "source": "./plugins/docx", "catalog": {"upstream": "anthropics/skills"}}
        self.assertRefused(self.run_check(e), "forbidden")


class Licenses(CatalogCase):
    def test_a_license_that_is_not_what_the_entry_says(self):
        self.upstream(MIT)
        self.assertRefused(self.run_check(entry(), fetcher=self.fixtures), "reads as MIT")

    def test_no_license_at_the_pin(self):
        self.assertRefused(self.run_check(entry(), fetcher=self.fixtures), "no license to read")

    def test_a_proprietary_text_is_nothing_we_recognize(self):
        self.upstream("All rights reserved. Copying and derivative works are forbidden.")
        self.assertRefused(self.run_check(entry(), fetcher=self.fixtures), "nothing we recognize")

    def test_altered_terms_are_not_apache(self):
        altered = APACHE.replace("irrevocable", "revocable")
        self.assertNotEqual(altered, APACHE)
        self.upstream(altered)
        self.assertRefused(self.run_check(entry(), fetcher=self.fixtures), "nothing we recognize")

    def test_altered_mit_is_not_mit(self):
        altered = MIT.replace("free of charge", "for a fee")
        self.assertNotEqual(altered, MIT)
        self.upstream(altered)
        self.assertRefused(self.run_check(entry(license_id="MIT"), fetcher=self.fixtures), "nothing we recognize")

    def test_the_font_license_is_not_taken_for_mit(self):
        text = "SIL OPEN FONT LICENSE Version 1.1 - 26 February 2007\nPERMISSION & CONDITIONS\nPermission is hereby granted, free of charge, to any person\n"
        self.assertEqual(check.identify_license(text), ("OFL-1.1", ""))

    def test_whitespace_and_the_appendix_do_not_matter(self):
        self.assertEqual(check.identify_license(" ".join(APACHE.split())), ("Apache-2.0", ""))
        without = APACHE[: APACHE.index("END OF TERMS AND CONDITIONS") + len("END OF TERMS AND CONDITIONS")]
        self.assertEqual(check.identify_license(without), ("Apache-2.0", ""))

    def test_stray_text_must_be_acknowledged_by_hash(self):
        self.upstream(APACHE + "\nSome stray line.\n")
        self.assertRefused(self.run_check(entry(), fetcher=self.fixtures), "no part of Apache-2.0")
        known = check.sha256("Some stray line.")
        self.assertEqual(self.run_check(entry(licenseExtraTextSha256=known), fetcher=self.fixtures), [])
        self.upstream(APACHE + "\nAnother line, which adds a condition.\n")
        self.assertRefused(self.run_check(entry(licenseExtraTextSha256=known), fetcher=self.fixtures), "no part of Apache-2.0")

    def test_a_stale_acknowledgement_is_refused(self):
        self.upstream(APACHE)
        self.assertRefused(self.run_check(entry(licenseExtraTextSha256="0" * 64), fetcher=self.fixtures), "has no extra text")

    def test_a_license_at_the_repository_root(self):
        self.upstream(APACHE, path="LICENSE")
        self.assertEqual(self.run_check(entry(licenseFile="LICENSE"), fetcher=self.fixtures), [])

    def test_a_license_file_the_client_would_not_install(self):
        for name in ("skills/kit/SKILL.md", ".mcp.json", "README.md"):
            self.assertRefused(self.run_check(entry(licenseFile=name)), "isn't named like a license")

    def test_a_license_read_at_a_familys_own_source(self):
        self.upstream(APACHE)
        ofl = "SIL OPEN FONT LICENSE Version 1.1\nPERMISSION & CONDITIONS\n"
        self.upstream(ofl, repo="foundry/family", path="OFL.txt")
        origin = {"repo": "foundry/family", "sha": SHA, "file": "OFL.txt", "url": "https://example.invalid/OFL.txt"}
        good = [{"license": "OFL-1.1", "covers": ["skills/kit/fonts/a.ttf"], "sources": [origin]}]
        self.assertEqual(self.run_check(entry(additionalLicenses=good), fetcher=self.fixtures), [])
        self.upstream(MIT, repo="foundry/family", path="OFL.txt")
        self.assertRefused(self.run_check(entry(additionalLicenses=good), fetcher=self.fixtures), "should be OFL-1.1")
        unpinned = [{"license": "OFL-1.1", "covers": ["a.ttf"], "sources": [{**origin, "sha": "main"}]}]
        self.assertRefused(self.run_check(entry(additionalLicenses=unpinned)), "pinned sha")
        uncovered = [{"license": "OFL-1.1", "sources": [origin]}]
        self.assertRefused(self.run_check(entry(additionalLicenses=uncovered)), "which files it covers")

    def test_additional_licenses(self):
        self.upstream(APACHE)
        ofl = "SIL OPEN FONT LICENSE Version 1.1 - 26 February 2007\nPERMISSION & CONDITIONS\n"
        self.upstream(ofl, path="skills/kit/fonts/a-OFL.txt")
        good = [{"license": "OFL-1.1", "files": ["skills/kit/fonts/a-OFL.txt"]}]
        self.assertEqual(self.run_check(entry(additionalLicenses=good), fetcher=self.fixtures), [])
        self.upstream(MIT, path="skills/kit/fonts/a-OFL.txt")
        self.assertRefused(self.run_check(entry(additionalLicenses=good), fetcher=self.fixtures), "should be OFL-1.1")
        self.assertRefused(self.run_check(entry(additionalLicenses=[{"license": "GPL-3.0-only", "files": []}])), "not on the list")


class Local(CatalogCase):
    def plugin(self, text=None):
        folder = self.catalog / "plugins" / "own"
        folder.mkdir(parents=True, exist_ok=True)
        if text:
            (folder / "LICENSE").write_text(text, encoding="utf-8")
        return {"name": "own", "license": "MIT", "description": "Our own.", "source": "./plugins/own"}

    def test_a_bundled_plugin_with_its_license_passes(self):
        self.assertEqual(self.run_check(self.plugin(MIT)), [])

    def test_a_bundled_plugin_needs_the_license_it_claims(self):
        self.assertRefused(self.run_check(self.plugin()), "no LICENSE file")
        self.assertRefused(self.run_check(self.plugin(APACHE)), "reads as Apache-2.0")

    def test_a_missing_folder(self):
        self.assertRefused(self.run_check({"name": "gone", "license": "MIT", "description": "x", "source": "./plugins/gone"}), "not a folder")


class Forks(CatalogCase):
    NOTICE = f"Modified from example/kit, skills/kit, commit {SHA}, under the Apache License 2.0.\n"

    def fork(self, **fields):
        folder = self.catalog / "plugins" / "kit"
        folder.mkdir(parents=True, exist_ok=True)
        (folder / "LICENSE.txt").write_text(APACHE, encoding="utf-8")
        (folder / "SKILL.md").write_text(self.NOTICE + "Changed.\n", encoding="utf-8")
        (folder / "same.md").write_text("Same.\n", encoding="utf-8")
        self.upstream(APACHE)
        self.upstream("Original.\n", path="skills/kit/SKILL.md")
        self.upstream("Same.\n", path="skills/kit/same.md")
        record = {"upstream": "example/kit", "sha": SHA, "path": "skills/kit", "modifiedFiles": ["SKILL.md"]}
        record.update(fields)
        return {"name": "kit", "license": "Apache-2.0", "licenseFile": "plugins/kit/LICENSE.txt", "description": "A kit. Forked from example/kit.", "source": "./plugins/kit", "catalog": {"upstream": "example/kit", "fork": record}}

    def test_a_proper_fork_passes(self):
        self.assertEqual(self.run_check(self.fork(), fetcher=self.fixtures), [])

    def test_a_fork_needs_its_license(self):
        e = self.fork()
        (self.catalog / "plugins" / "kit" / "LICENSE.txt").unlink()
        self.assertRefused(self.run_check(e), "no LICENSE file")

    def test_a_fork_must_keep_the_license_as_it_was(self):
        e = self.fork()
        (self.catalog / "plugins" / "kit" / "LICENSE.txt").write_text(APACHE + "\n", encoding="utf-8")
        self.assertRefused(self.run_check(e, fetcher=self.fixtures), "differs from")

    def test_a_fork_needs_its_origin(self):
        e = self.fork()
        del e["catalog"]["fork"]
        self.assertRefused(self.run_check(e), "needs catalog.fork")
        self.assertRefused(self.run_check(self.fork(sha="main")), "unpinned")
        self.assertRefused(self.run_check(self.fork(path=None)), "must name the folder")
        self.assertRefused(self.run_check(self.fork(upstream="someone/else")), "must be the entry's upstream")

    def test_a_changed_file_needs_a_notice(self):
        e = self.fork()
        (self.catalog / "plugins" / "kit" / "SKILL.md").write_text("Changed, silently.\n", encoding="utf-8")
        self.assertRefused(self.run_check(e), "must say so")

    def test_a_notice_must_name_the_pinned_commit(self):
        e = self.fork()
        (self.catalog / "plugins" / "kit" / "SKILL.md").write_text("Modified from example/kit.\n", encoding="utf-8")
        self.assertRefused(self.run_check(e), "must say so")

    def test_the_list_of_changes_must_be_true(self):
        self.assertRefused(self.run_check(self.fork(modifiedFiles=[])), "must list what was changed")
        self.assertRefused(self.run_check(self.fork(modifiedFiles=["SKILL.md", "gone.md"])), "isn't in the fork")
        e = self.fork()
        (self.catalog / "plugins" / "kit" / "same.md").write_text("Quietly changed.\n", encoding="utf-8")
        self.assertRefused(self.run_check(e, fetcher=self.fixtures), "isn't in modifiedFiles")
        e = self.fork(modifiedFiles=["SKILL.md", "same.md"])
        (self.catalog / "plugins" / "kit" / "same.md").write_text(self.NOTICE, encoding="utf-8")
        self.upstream(self.NOTICE, path="skills/kit/same.md")
        self.assertRefused(self.run_check(e, fetcher=self.fixtures), "same as upstream")

    def test_an_added_file_is_a_change(self):
        e = self.fork()
        (self.catalog / "plugins" / "kit" / "new.md").write_text("New.\n", encoding="utf-8")
        self.assertRefused(self.run_check(e, fetcher=self.fixtures), "needs a notice")

    def test_a_fork_carries_no_upstream_identity(self):
        for text in ("Keep the Anthropic colors.", "Works in claude.ai."):
            e = self.fork()
            (self.catalog / "plugins" / "kit" / "same.md").write_text(text, encoding="utf-8")
            self.assertRefused(self.run_check(e), "identity")

    def test_a_fork_of_the_forbidden_is_refused_and_not_fetched(self):
        for name in ("docx", "pdf", "pptx", "xlsx"):
            e = self.fork(upstream="anthropics/skills", path=f"skills/{name}")
            e["catalog"]["upstream"] = "anthropics/skills"
            self.assertRefused(self.run_check(e, fetcher=self.fixtures), "forbidden")
            self.assertEqual(self.fixtures.asked, [])

    def test_a_fork_states_its_origin_as_a_fact(self):
        self.assertRefused(self.run_check({**self.fork(), "description": "A kit."}), "must name its origin")
        self.assertRefused(self.run_check({**self.fork(), "description": "The official kit, from example/kit."}), "as a fact")


class TheCatalog(unittest.TestCase):
    """The real catalog, offline: what is listed obeys the rules. Reading its licenses is the job of --verify-licenses in CI."""

    def test_it_obeys_the_rules(self):
        errors, _ = check.check_catalog(check.DEFAULT_CATALOG)
        self.assertEqual(errors, [])

    def test_every_external_entry_is_pinned_to_a_full_commit(self):
        manifest = json.loads((check.DEFAULT_CATALOG / check.MARKETPLACE).read_text(encoding="utf-8"))
        external = [e for e in manifest["plugins"] if isinstance(e["source"], dict)]
        self.assertTrue(external)
        for e in external:
            self.assertRegex(e["source"]["sha"], r"^[0-9a-f]{40}$", e["name"])
            self.assertIn(e["license"], check.ALLOWED_LICENSES)
            self.assertTrue(e["licenseFile"])
            self.assertNotIn("licenseNotInstalled", e["catalog"])

    def test_it_lists_none_of_the_forbidden(self):
        manifest = json.loads((check.DEFAULT_CATALOG / check.MARKETPLACE).read_text(encoding="utf-8"))
        for e in manifest["plugins"]:
            if isinstance(e["source"], dict):
                self.assertNotIn(e["source"].get("path", "").rsplit("/", 1)[-1], check.FORBIDDEN_SKILLS | set(check.EXCLUDED_SKILLS))


    def test_the_font_families_record_what_was_read(self):
        manifest = json.loads((check.DEFAULT_CATALOG / check.MARKETPLACE).read_text(encoding="utf-8"))
        canvas = next(e for e in manifest["plugins"] if e["name"] == "canvas-design")
        groups = [g for g in canvas["catalog"]["additionalLicenses"] if g.get("sources")]
        covered = sorted(c.rsplit("/", 1)[-1] for g in groups for c in g["covers"])
        self.assertEqual(covered, sorted(f"IBMPlexSerif-{s}.ttf" for s in ("Bold", "BoldItalic", "Italic", "Regular")) + ["InstrumentSerif-Italic.ttf", "InstrumentSerif-Regular.ttf"])

    def test_the_art_fork_has_lost_the_brand(self):
        folder = check.DEFAULT_CATALOG / "plugins" / "algorithmic-art"
        for name in ("SKILL.md", "templates/viewer.html", "templates/generator_template.js"):
            text = (folder / name).read_text(encoding="utf-8").lower()
            marks = r"poppins|\blora\b|fonts\.googleapis|#d97757|#6a9bcc|#788c5d|#c86641|#b0aea5|#e8e6dc|#faf9f5|#141413|#f5f3ee|250, 249, 245"
            self.assertEqual(re.findall(marks, text), [], name)


class CommandLine(CatalogCase):
    def main(self, *args):
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = check.main(list(args))
        return code, out.getvalue(), err.getvalue()

    def test_exit_codes(self):
        self.upstream(APACHE)
        self.run_check(entry())
        code, out, _ = self.main("--catalog", str(self.catalog), "--fixtures", str(self.dir / "fixtures"))
        self.assertEqual(code, 0)
        self.assertIn(f"verified  kit: Apache-2.0, example/kit/skills/kit/LICENSE.txt at {SHA}", out)
        self.upstream(MIT)
        code, _, err = self.main("--catalog", str(self.catalog), "--fixtures", str(self.dir / "fixtures"))
        self.assertEqual(code, 1)
        self.assertIn("reads as MIT", err)

    def test_without_a_fetcher_it_says_licenses_were_not_read(self):
        self.run_check(entry())
        code, out, _ = self.main("--catalog", str(self.catalog))
        self.assertEqual(code, 0)
        self.assertIn("Licenses were not read", out)

    def test_a_missing_catalog_fails(self):
        code, _, _ = self.main("--catalog", str(self.dir / "nowhere"))
        self.assertEqual(code, 1)


if __name__ == "__main__":
    unittest.main()
