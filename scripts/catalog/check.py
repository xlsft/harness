#!/usr/bin/env python3
"""Checks the skills catalog of xlsft`s harness: what may be listed, and that each license is what the entry says.

  scripts/catalog/check.py                         the rules, offline
  scripts/catalog/check.py --verify-licenses       and read each license at its pinned commit, through `gh api`
  scripts/catalog/check.py --fixtures DIR          the same, from DIR/<owner>/<repo>/<sha>/<path>, offline

A license is identified from its text, never assumed from the entry. Refused folders are refused before anything is fetched:
the checker must never download what the catalog is forbidden to read.
"""
import argparse
import hashlib
import json
import posixpath
import re
import subprocess
import sys
from pathlib import Path
from urllib.parse import quote

ROOT = Path(__file__).resolve().parents[2]
DEFAULT_CATALOG = ROOT / "catalog"
MARKETPLACE = Path(".claude-plugin") / "marketplace.json"

# What an entry may be licensed under, and what bundled files of it may additionally be.
ALLOWED_LICENSES = {"MIT", "Apache-2.0"}
ALLOWED_ADDITIONAL = {"OFL-1.1"}

SKILLS_REPO = "anthropics/skills"
# Their license forbids copying and derivative works.
FORBIDDEN_SKILLS = {"docx", "pdf", "pptx", "xlsx"}
# Not listed, each for its reason. See catalog/README.md.
EXCLUDED_SKILLS = {
    "doc-coauthoring": "has no license file",
    "claude-api": "only makes sense on its vendor's products",
    "web-artifacts-builder": "only makes sense on its vendor's products",
    "academy-guide": "only makes sense on its vendor's products",
    "brand-guidelines": "it is its vendor's brand",
}

SHA_RE = re.compile(r"^[0-9a-f]{40}$")
HEX64_RE = re.compile(r"^[0-9a-f]{64}$")
GITHUB_URL_RE = re.compile(r"^(?:https://github\.com/|ssh://git@github\.com/|git@github\.com:)([^/]+/[^/]+?)(?:\.git)?/?$", re.I)
OWNER_REPO_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$")
# A description states origin as a fact; it never claims the upstream made or stands behind this catalog.
CLAIM_RE = re.compile(r"\bofficial\b|\bendorsed\b|\bapproved by\b|\bpartner(ed)? with\b|\b(by|from) anthropic\b|\banthropic['’]s\b|\bmade by\b", re.I)
BRANDING_FIELDS = ("icon", "logo", "image")
# The name a license file may have: the client copies it to a plugin's root, where another name could be a component instead.
LICENSE_NAME_RE = re.compile(r"^(licen[cs]e|copying|notice)([._-][a-z0-9._-]*)?$", re.I)
# A fork keeps its upstream's license as it is, and states its origin as a repository name. Nothing else names the vendor.
FORK_BRANDING_RE = re.compile(r"anthropic(?!s/)|claude", re.I)
FORK_NOTICE = "Modified from "

# SHA-256 of the fixed text, whitespace collapsed: the terms of Apache-2.0, and the MIT text from its grant onwards.
APACHE_TERMS_SHA256 = "b316b715c7f48af32f6265b3de80b9656fdbddc57f5854215c8a4c6e0ad42c05"
MIT_BODY_SHA256 = "fe2a9817987f862eaced948f0468c7f51d2fedfc48c5c505b246a49a3870e9a5"
APACHE_HEADER = "Apache License Version 2.0, January 2004"
APACHE_TERMS_START = "TERMS AND CONDITIONS FOR USE, REPRODUCTION, AND DISTRIBUTION"
APACHE_TERMS_END = "END OF TERMS AND CONDITIONS"
APACHE_APPENDIX_END = "limitations under the License."
MIT_START = "Permission is hereby granted, free of charge"


def collapse(text):
    return " ".join(text.split())


def sha256(text):
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


def identify_license(text):
    """Returns (SPDX id or None, extra): extra is text around the license that is not part of it, whitespace collapsed."""
    flat = collapse(text)
    if APACHE_HEADER in flat and APACHE_TERMS_START in flat and APACHE_TERMS_END in flat:
        start, end = flat.index(APACHE_TERMS_START), flat.index(APACHE_TERMS_END) + len(APACHE_TERMS_END)
        if sha256(flat[start:end]) != APACHE_TERMS_SHA256:
            return None, ""
        before = flat[: flat.index(APACHE_HEADER)].strip()
        after = flat[end:].strip()
        if after.startswith("APPENDIX:") and APACHE_APPENDIX_END in after:
            after = after[after.index(APACHE_APPENDIX_END) + len(APACHE_APPENDIX_END) :].strip()
        return "Apache-2.0", " ".join(p for p in (before, after) if p)
    # Before MIT: the OFL opens its grant with the same words.
    upper = flat.upper()
    if "SIL OPEN FONT LICENSE" in upper and "VERSION 1.1" in upper and "PERMISSION & CONDITIONS" in upper:
        return "OFL-1.1", ""
    if MIT_START in flat:
        if sha256(flat[flat.index(MIT_START) :]) != MIT_BODY_SHA256:
            return None, ""
        return "MIT", ""
    return None, ""


def repo_of(source):
    """The owner/repo a source points at, lower case, whichever way it is spelled. None when it is not on GitHub."""
    if source.get("source") == "github":
        repo = source.get("repo") or ""
    else:
        url = source.get("url") or ""
        if OWNER_REPO_RE.match(url):
            repo = url
        else:
            match = GITHUB_URL_RE.match(url)
            if not match:
                return None
            repo = match.group(1)
    repo = repo.removesuffix(".git")
    return repo.lower() if OWNER_REPO_RE.match(repo) else None


def clean_path(path):
    """A repository path in its plain form, or None when it leaves the repository. The root is ""."""
    if path is None:
        return ""
    if not isinstance(path, str) or path.startswith("/") or "\\" in path or ".." in path.split("/"):
        return None
    norm = posixpath.normpath(path)
    return "" if norm == "." else norm


def refused_folder(repo, path):
    """Why this folder of a repository can't be read at all, or None."""
    if repo != SKILLS_REPO:
        return None
    lowered = (path or "").lower()
    for name in sorted(FORBIDDEN_SKILLS):
        folder = "skills/" + name
        if lowered == folder or lowered.startswith(folder + "/"):
            return f"{SKILLS_REPO} {folder} is forbidden: its license forbids copying and derivative works"
        if lowered == "" or lowered == "skills":
            return f"{SKILLS_REPO}/{path or '.'} holds {folder}, which is forbidden: point at one skill's folder"
    parts = lowered.split("/")
    if len(parts) >= 2 and parts[0] == "skills" and parts[1] in EXCLUDED_SKILLS:
        return f"{SKILLS_REPO} skills/{parts[1]} is excluded: it {EXCLUDED_SKILLS[parts[1]]}"
    return None


class GhFetcher:
    """Reads a file at a commit through `gh api`."""

    def fetch(self, repo, sha, path):
        if refused_folder(repo, path):
            raise LookupError(f"refusing to fetch {repo}/{path}")
        api = f"repos/{repo}/contents/{quote(path)}?ref={sha}"
        done = subprocess.run(["gh", "api", api, "-H", "Accept: application/vnd.github.raw"], capture_output=True, text=True)
        if done.returncode != 0:
            raise LookupError(f"{repo}/{path} at {sha}: {done.stderr.strip() or 'gh api failed'}")
        return done.stdout


class FixtureFetcher:
    """Reads the same files from a folder, for tests."""

    def __init__(self, root):
        self.root = Path(root)
        self.asked = []

    def fetch(self, repo, sha, path):
        self.asked.append((repo, sha, path))
        if refused_folder(repo, path):
            raise LookupError(f"refusing to fetch {repo}/{path}")
        file = self.root / repo / sha / path
        if not file.is_file():
            raise LookupError(f"{repo}/{path} at {sha}: not found")
        return file.read_text(encoding="utf-8")


def local_folder(entry, manifest, catalog):
    """The folder of an entry with a path as its source."""
    source = entry["source"]
    if source != "." and not source.startswith("./"):
        plugin_root = (manifest.get("metadata") or {}).get("pluginRoot")
        if "/" in source or not plugin_root:
            return None
        source = plugin_root.rstrip("/") + "/" + source
    if ".." in source.split("/"):
        return None
    return catalog / source


def check_local(entry, manifest, catalog, errors):
    name, license_id = entry.get("name"), entry.get("license")
    folder = local_folder(entry, manifest, catalog)
    if folder is None or not folder.is_dir():
        errors.append(f"{name}: the source {entry['source']!r} is not a folder of the catalog")
        return
    meta = entry.get("catalog") or {}
    upstream = meta.get("upstream", "")
    if str(upstream).lower() == SKILLS_REPO and str(name).lower() in FORBIDDEN_SKILLS:
        errors.append(f"{name}: {SKILLS_REPO} skills/{name} is forbidden: its license forbids copying and derivative works")
        return
    if upstream and not meta.get("fork"):
        errors.append(f"{name}: a bundled entry with an upstream is a fork, and needs catalog.fork")
    files = [p for p in sorted(folder.iterdir()) if p.is_file() and p.name.upper().startswith(("LICENSE", "LICENCE"))]
    if not files:
        errors.append(f"{name}: the plugin folder has no LICENSE file")
        return
    found, _ = identify_license(files[0].read_text(encoding="utf-8"))
    if license_id in ALLOWED_LICENSES and found != license_id:
        errors.append(f"{name}: the entry says {license_id}, but {files[0].name} reads as {found or 'nothing we recognize'}")


def check_description(entry, upstream, errors):
    """A description exists, makes no claim, and names the origin when there is one."""
    name, text = entry.get("name"), entry.get("description")
    if not isinstance(text, str) or not text.strip():
        errors.append(f"{name}: no description")
        return
    if CLAIM_RE.search(text):
        errors.append(f"{name}: the description must state origin as a fact, without claiming affiliation: {text!r}")
    if upstream and upstream.lower() not in text.lower():
        errors.append(f"{name}: the description must name its origin, {upstream}")


def external_paths(entry, source, repo, errors):
    """The files to read for an entry from a repository, after refusing what must not be read."""
    name = entry.get("name")
    meta = entry.get("catalog")
    if not isinstance(meta, dict):
        errors.append(f"{name}: an external entry needs a catalog object with upstream and licenseFile")
        return None
    if str(meta.get("upstream", "")).lower() != repo:
        errors.append(f"{name}: catalog.upstream must be {repo}, the repository of its source")
    license_file = clean_path(entry.get("licenseFile")) if entry.get("licenseFile") else None
    if not license_file:
        errors.append(f"{name}: licenseFile must name the license at the pinned commit")
        return None
    if not LICENSE_NAME_RE.match(posixpath.basename(license_file)):
        errors.append(f"{name}: licenseFile {license_file} isn't named like a license, so the client won't install it")
        return None
    wanted = [license_file]
    additional = meta.get("additionalLicenses", [])
    if not isinstance(additional, list):
        errors.append(f"{name}: catalog.additionalLicenses must be a list")
        additional = []
    for extra in additional:
        license_id = extra.get("license") if isinstance(extra, dict) else None
        if license_id not in ALLOWED_ADDITIONAL:
            errors.append(f"{name}: the additional license {license_id!r} is not on the list")
            continue
        for source_of_family in extra.get("sources") or []:
            if not (isinstance(source_of_family, dict) and SHA_RE.match(str(source_of_family.get("sha", ""))) and source_of_family.get("repo") and source_of_family.get("file") and source_of_family.get("url")):
                errors.append(f"{name}: a license source needs repo, a pinned sha, file and url: {source_of_family!r}")
        if extra.get("sources") and not extra.get("covers"):
            errors.append(f"{name}: a license read at another source must say which files it covers")
        for rel in extra.get("files") or []:
            clean = clean_path(rel)
            if not clean:
                errors.append(f"{name}: {rel!r} is not a path of the repository")
                continue
            wanted.append(clean)
    for rel in wanted:
        why = refused_folder(repo, rel)
        if why:
            errors.append(f"{name}: {why}")
            return None
    return wanted


def check_external(entry, errors):
    """The offline rules for a source in a repository. Returns what to verify, or None when it must not be touched."""
    name, source = entry.get("name"), entry["source"]
    kind = source.get("source")
    if kind not in ("github", "url", "git-subdir"):
        errors.append(f"{name}: a source of type {kind!r} can't be pinned")
        return None
    repo = repo_of(source)
    if repo is None:
        errors.append(f"{name}: only GitHub sources can have their license verified")
        return None
    sha = source.get("sha")
    pinned = isinstance(sha, str) and bool(SHA_RE.match(sha))
    if not pinned:
        errors.append(f"{name}: unpinned: the source needs a sha, a full commit id (a ref alone moves)")
    path = clean_path(source.get("path"))
    if path is None:
        errors.append(f"{name}: the path {source.get('path')!r} leaves the repository")
        return None
    if kind == "git-subdir" and not path:
        errors.append(f"{name}: a git-subdir source needs a path")
    refused = refused_folder(repo, path)
    if refused:
        errors.append(f"{name}: {refused}")
        return None
    if repo == SKILLS_REPO and str(name).lower() in FORBIDDEN_SKILLS:
        errors.append(f"{name}: {SKILLS_REPO} skills/{name} is forbidden: its license forbids copying and derivative works")
        return None
    check_description(entry, repo, errors)
    wanted = external_paths(entry, source, repo, errors)
    if wanted is None or not pinned:
        return None
    return repo, sha, wanted


def verify(entry, target, fetcher, errors):
    """Reads the licenses at the pinned commit and compares them with what the entry says."""
    name, license_id, meta = entry.get("name"), entry.get("license"), entry["catalog"]
    repo, sha, wanted = target
    try:
        text = fetcher.fetch(repo, sha, wanted[0])
    except LookupError as e:
        errors.append(f"{name}: no license to read: {e}")
        return
    found, extra = identify_license(text)
    if found != license_id:
        errors.append(f"{name}: the entry says {license_id}, but {repo}/{wanted[0]} at {sha} reads as {found or 'nothing we recognize'}")
        return
    known = meta.get("licenseExtraTextSha256")
    if extra and known != sha256(extra):
        errors.append(f"{name}: {wanted[0]} has text that is no part of {found}; read it, and if it changes no terms set catalog.licenseExtraTextSha256 to {sha256(extra)}: {extra[:160]!r}")
    if not extra and known:
        errors.append(f"{name}: catalog.licenseExtraTextSha256 is set, but {wanted[0]} has no extra text")
    for group in meta.get("additionalLicenses", []):
        for rel in group.get("files") or []:
            try:
                found, _ = identify_license(fetcher.fetch(repo, sha, clean_path(rel)))
            except LookupError as e:
                errors.append(f"{name}: {e}")
                continue
            if found != group["license"]:
                errors.append(f"{name}: {rel} should be {group['license']}, but reads as {found or 'nothing we recognize'}")
        for origin in group.get("sources") or []:
            try:
                found, _ = identify_license(fetcher.fetch(origin["repo"], origin["sha"], origin["file"]))
            except (LookupError, KeyError) as e:
                errors.append(f"{name}: {e}")
                continue
            if found != group["license"]:
                errors.append(f"{name}: {origin['repo']}/{origin['file']} should be {group['license']}, but reads as {found or 'nothing we recognize'}")


def fork_files(folder):
    """The files of a fork, as paths from its folder."""
    return sorted(p.relative_to(folder).as_posix() for p in folder.rglob("*") if p.is_file())


def check_fork(entry, folder, errors):
    """The offline rules for a fork. Returns the upstream folder to compare it with, or None when it must not be touched."""
    name, fork = entry.get("name"), entry["catalog"].get("fork")
    if not isinstance(fork, dict):
        errors.append(f"{name}: catalog.fork must record where the fork comes from")
        return None
    repo, sha, path = str(fork.get("upstream", "")).lower(), fork.get("sha"), clean_path(fork.get("path"))
    if not OWNER_REPO_RE.match(repo) or repo != str(entry["catalog"].get("upstream", "")).lower():
        errors.append(f"{name}: catalog.fork.upstream must be the entry's upstream, owner/repo")
        return None
    if not (isinstance(sha, str) and SHA_RE.match(sha)):
        errors.append(f"{name}: the fork's origin is unpinned: catalog.fork.sha needs a full commit id")
        return None
    if not path:
        errors.append(f"{name}: catalog.fork.path must name the folder it was forked from")
        return None
    refused = refused_folder(repo, path)
    if refused:
        errors.append(f"{name}: {refused}")
        return None
    check_description(entry, repo, errors)
    modified = fork.get("modifiedFiles")
    if not isinstance(modified, list) or not modified:
        errors.append(f"{name}: catalog.fork.modifiedFiles must list what was changed")
        return None
    files = fork_files(folder)
    for rel in modified:
        if rel not in files:
            errors.append(f"{name}: catalog.fork.modifiedFiles names {rel}, which isn't in the fork")
            continue
        text = (folder / rel).read_text(encoding="utf-8")
        if FORK_NOTICE not in text or repo not in text.lower() or sha not in text:
            errors.append(f"{name}: {rel} was changed, so it must say so: \"{FORK_NOTICE}{repo}\" with the commit {sha}")
    for rel in files:
        if LICENSE_NAME_RE.match(posixpath.basename(rel)):
            continue
        hit = FORK_BRANDING_RE.search((folder / rel).read_text(encoding="utf-8", errors="replace"))
        if hit:
            errors.append(f"{name}: {rel} still carries the upstream's identity: {hit.group(0)!r}")
    license_files = [rel for rel in files if LICENSE_NAME_RE.match(rel)]
    if not license_files:
        errors.append(f"{name}: the fork lost its license")
        return None
    return repo, sha, path, files, modified, license_files


def verify_fork(entry, folder, target, fetcher, errors):
    """Compares a fork with its origin at the pinned commit: the license kept, every change declared, nothing undeclared."""
    name = entry.get("name")
    repo, sha, path, files, modified, license_files = target
    for rel in files:
        ours = (folder / rel).read_bytes()
        try:
            theirs = fetcher.fetch(repo, sha, f"{path}/{rel}").encode("utf-8")
        except LookupError:
            if rel not in modified:
                errors.append(f"{name}: {rel} isn't in {repo}/{path} at {sha}, so it needs a notice and a place in modifiedFiles")
            continue
        if rel in license_files and ours != theirs:
            errors.append(f"{name}: the license {rel} differs from {repo}/{path}/{rel} at {sha}")
        elif ours != theirs and rel not in modified:
            errors.append(f"{name}: {rel} differs from {repo}/{path}/{rel} at {sha}, but isn't in modifiedFiles")
        elif ours == theirs and rel in modified:
            errors.append(f"{name}: {rel} is the same as upstream, but is listed in modifiedFiles")


def check_catalog(catalog, fetcher=None):
    """Every problem found, and the lines of what was verified."""
    catalog = Path(catalog)
    errors, verified = [], []
    try:
        manifest = json.loads((catalog / MARKETPLACE).read_text(encoding="utf-8"))
    except (OSError, ValueError) as e:
        return [f"{catalog / MARKETPLACE}: {e}"], verified
    entries = manifest.get("plugins")
    if not isinstance(entries, list) or not entries:
        return [f"{catalog / MARKETPLACE}: no plugins are listed"], verified
    seen = set()
    for entry in entries:
        name = entry.get("name")
        if not name:
            errors.append(f"an entry has no name: {json.dumps(entry)[:120]}")
            continue
        if name in seen:
            errors.append(f"{name}: listed twice")
        seen.add(name)
        license_id = entry.get("license")
        if not license_id:
            errors.append(f"{name}: no license field")
        elif license_id not in ALLOWED_LICENSES:
            errors.append(f"{name}: the license {license_id!r} is not on the list ({', '.join(sorted(ALLOWED_LICENSES))})")
        for field in BRANDING_FIELDS:
            if field in entry:
                errors.append(f"{name}: an entry carries no {field}")
        source = entry.get("source")
        if isinstance(source, str):
            folder = local_folder(entry, manifest, catalog)
            is_fork = isinstance(entry.get("catalog"), dict) and "fork" in entry["catalog"]
            if not is_fork:
                check_description(entry, "", errors)
            check_local(entry, manifest, catalog, errors)
            if is_fork and folder is not None and folder.is_dir():
                target = check_fork(entry, folder, errors)
                if target and fetcher is not None:
                    before = len(errors)
                    verify_fork(entry, folder, target, fetcher, errors)
                    if len(errors) == before:
                        verified.append(f"{name}: fork of {target[0]}/{target[2]} at {target[1]}, changed: {', '.join(target[4])}")
        elif isinstance(source, dict):
            target = check_external(entry, errors)
            if target and license_id in ALLOWED_LICENSES and fetcher is not None:
                before = len(errors)
                verify(entry, target, fetcher, errors)
                if len(errors) == before:
                    verified.append(f"{name}: {license_id}, {target[0]}/{target[2][0]} at {target[1]}")
        else:
            errors.append(f"{name}: no source")
    return errors, verified


def main(argv=None):
    parser = argparse.ArgumentParser(description="Check the skills catalog: licenses, pins and refused sources.")
    parser.add_argument("--catalog", default=str(DEFAULT_CATALOG), help="the catalog folder")
    parser.add_argument("--verify-licenses", action="store_true", help="read each license at its pinned commit through gh api")
    parser.add_argument("--fixtures", help="read licenses from this folder instead: <owner>/<repo>/<sha>/<path>")
    args = parser.parse_args(argv)
    fetcher = FixtureFetcher(args.fixtures) if args.fixtures else GhFetcher() if args.verify_licenses else None
    errors, verified = check_catalog(args.catalog, fetcher)
    for line in verified:
        print("verified  " + line)
    for line in errors:
        print("FAIL      " + line, file=sys.stderr)
    if errors:
        print(f"{len(errors)} problem(s) in the catalog.", file=sys.stderr)
        return 1
    print("The catalog is in order." + ("" if fetcher else " Licenses were not read: run with --verify-licenses."))
    return 0


if __name__ == "__main__":
    sys.exit(main())
