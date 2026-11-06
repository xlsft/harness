#!/usr/bin/env python3
"""Checks a built DMG against tauri.conf.json's bundle.macOS.dmg, as a person opening it would see it.

The window's layout lives in the volume's .DS_Store, which Finder writes while the DMG is made (AppleScript in tauri's
bundle_dmg.sh). Where that was skipped, or Finder saved too early, there is still a DMG: without the background, with small
icons in the wrong place against it. It also refuses an app Finder would draw with a prohibited sign. A release can't ship
any of that unnoticed.

Usage: check-dmg.py <dmg> <tauri.conf.json>
"""
import json
import pathlib
import platform
import plistlib
import struct
import subprocess
import sys
import tempfile


ICON_SIZE = 128


def version(text):
    return tuple(int(part) for part in text.split("."))


def blobs(store, code):
    """Every value of a .DS_Store record type, as raw bytes: `<code>blob<u32 length><data>`."""
    needle = code + b"blob"
    at = store.find(needle)
    while at != -1:
        size = struct.unpack(">I", store[at + 8 : at + 12])[0]
        yield store[at + 12 : at + 12 + size]
        at = store.find(needle, at + 1)


def check(volume, conf):
    problems = []
    volume = pathlib.Path(volume)
    apps = list(volume.glob("*.app"))
    if not apps:
        problems.append("no .app in the DMG")
    else:
        app = apps[0]
        info = plistlib.loads((app / "Contents/Info.plist").read_bytes())
        exe = app / "Contents/MacOS" / info["CFBundleExecutable"]
        if not exe.is_file() or not exe.stat().st_mode & 0o111:
            problems.append(f"{exe.name} is missing or not executable")
        minimum = info.get("LSMinimumSystemVersion", "0")
        if version(minimum) > version(platform.mac_ver()[0]):
            problems.append(f"LSMinimumSystemVersion {minimum} is above this macOS: Finder would show a prohibited sign")
    if not (volume / "Applications").is_symlink():
        problems.append("no Applications link")
    if not (volume / ".background" / pathlib.Path(conf["background"]).name).is_file():
        problems.append("no background image on the volume")
    store_file = volume / ".DS_Store"
    if not store_file.is_file():
        problems.append("no .DS_Store: the Finder layout step didn't run")
        return problems
    store = store_file.read_bytes()

    views = list(blobs(store, b"icvp"))
    view = plistlib.loads(views[0]) if views else {}
    # The size bundle_dmg.sh asks for, which the background and the positions in tauri.conf.json are drawn for.
    if view.get("iconSize") != ICON_SIZE:
        problems.append(f"icons are {view.get('iconSize')} px, not {ICON_SIZE}: they sit wrong against the background")
    # 2 is Finder's value for "use a picture". Which picture: an alias in the view on older systems, a bookmark record now.
    if view.get("backgroundType") != 2 or not (view.get("backgroundImageAlias") or b"pBBk" in store):
        problems.append("the window doesn't use a background picture")

    windows = list(blobs(store, b"bwsp"))
    bounds = plistlib.loads(windows[0]).get("WindowBounds", "") if windows else ""
    size = conf["windowSize"]
    if not bounds.endswith(f"{{{size['width']}, {size['height']}}}}}"):
        problems.append(f"window bounds are {bounds or 'unset'}, not {size['width']}x{size['height']}")

    positions = {struct.unpack(">II", blob[:8]) for blob in blobs(store, b"Iloc")}
    for name in ("appPosition", "applicationFolderPosition"):
        want = (conf[name]["x"], conf[name]["y"])
        half = ICON_SIZE // 2
        if not (half <= want[0] <= size["width"] - half and half <= want[1] <= size["height"] - half):
            problems.append(f"{name} {want} puts the icon outside the {size['width']}x{size['height']} window")
        if want not in positions:
            problems.append(f"nothing at {name} {want}: icons are at {sorted(positions)}")
    return problems


def main():
    dmg, conf_path = sys.argv[1], sys.argv[2]
    conf = json.loads(pathlib.Path(conf_path).read_text())["bundle"]["macOS"]["dmg"]
    with tempfile.TemporaryDirectory() as mount:
        subprocess.run(
            ["hdiutil", "attach", "-nobrowse", "-noverify", "-noautoopen", "-readonly", "-mountpoint", mount, dmg],
            check=True,
            stdout=subprocess.DEVNULL,
        )
        try:
            problems = check(mount, conf)
        finally:
            subprocess.run(["hdiutil", "detach", mount, "-force"], check=False, stdout=subprocess.DEVNULL)
    if problems:
        print(f"{dmg}: the layout isn't what tauri.conf.json asks for:", file=sys.stderr)
        for p in problems:
            print(f"  - {p}", file=sys.stderr)
        return 1
    print(f"{dmg}: layout ok")
    return 0


if __name__ == "__main__":
    sys.exit(main())
