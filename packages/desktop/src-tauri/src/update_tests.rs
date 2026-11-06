//! The updater against a fake release server over real HTTP: only the executable it replaces is pointed at a fake app,
//! and HTTP instead of HTTPS needs the plugin's dangerous switch, which the shipped app doesn't set.

use base64::Engine;
use std::io::{Cursor, Read, Write};
use std::net::TcpListener;
use std::path::{Path, PathBuf};
use tauri::test::{mock_builder, mock_context, noop_assets};
use tauri_plugin_updater::UpdaterExt;

use super::unavailable;

const B64: base64::engine::GeneralPurpose = base64::engine::general_purpose::STANDARD;

/// A key made for this test run, as `tauri signer generate` would: base64 of the minisign public key file.
struct Key {
    public: String,
    secret: minisign::SecretKey,
    public_key: minisign::PublicKey,
}

fn key() -> Key {
    let pair = minisign::KeyPair::generate_unencrypted_keypair().unwrap();
    let public = B64.encode(pair.pk.to_box().unwrap().to_string());
    Key { public, secret: pair.sk, public_key: pair.pk }
}

impl Key {
    /// A signature as `tauri build` makes it: base64 of the minisign signature file, version in the trusted comment.
    fn sign(&self, data: &[u8], version: &str) -> String {
        let comment = format!("timestamp:1\tfile:app.tar.gz\tversion:{version}");
        let sig = minisign::sign(Some(&self.public_key), &self.secret, Cursor::new(data), Some(&comment), Some("signature")).unwrap();
        B64.encode(sig.to_string())
    }
}

fn app_bundle(root: &Path, content: &str) -> PathBuf {
    let macos = root.join("Fake.app/Contents/MacOS");
    std::fs::create_dir_all(&macos).unwrap();
    let exe = macos.join("fake");
    std::fs::write(&exe, content).unwrap();
    exe
}

fn tempdir(name: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!("harness-update-test-{}-{name}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).unwrap();
    dir
}

/// The update archive: a .app bundle in a gzipped tar, as `tauri build` makes it.
fn archive_of(name: &str, content: &str) -> Vec<u8> {
    let dir = tempdir(&format!("archive-{name}"));
    app_bundle(&dir, content);
    let mut out = Vec::new();
    {
        let gz = flate2::write::GzEncoder::new(&mut out, flate2::Compression::fast());
        let mut tar = tar::Builder::new(gz);
        tar.append_dir_all("Fake.app", dir.join("Fake.app")).unwrap();
        tar.into_inner().unwrap().finish().unwrap();
    }
    out
}

struct Release {
    version: &'static str,
    signature: String,
    archive: Vec<u8>,
    /// Claim the whole archive, send half of it, hang up.
    cut: bool,
}

/// Serves `/latest.json` and `/app.tar.gz` on loopback; returns the base URL.
fn serve(release: Release) -> String {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let base = format!("http://{}", listener.local_addr().unwrap());
    let latest = serde_json::json!({
        "version": release.version,
        "notes": "- What changed",
        "pub_date": "2026-11-06T00:00:00Z",
        "platforms": {
            "darwin-aarch64": { "signature": release.signature, "url": format!("{base}/app.tar.gz") },
            "darwin-x86_64": { "signature": release.signature, "url": format!("{base}/app.tar.gz") }
        }
    })
    .to_string();
    std::thread::spawn(move || {
        for stream in listener.incoming() {
            let Ok(mut s) = stream else { continue };
            let mut buf = [0u8; 4096];
            let n = s.read(&mut buf).unwrap_or(0);
            let is_latest = String::from_utf8_lossy(&buf[..n]).starts_with("GET /latest.json");
            let body = if is_latest { latest.as_bytes() } else { &release.archive[..] };
            let sent = if release.cut && !is_latest { body.len() / 2 } else { body.len() };
            let _ = write!(s, "HTTP/1.1 200 OK\r\nContent-Length: {}\r\nConnection: close\r\n\r\n", body.len());
            let _ = s.write_all(&body[..sent]);
        }
    });
    base
}

/// Checks and installs the way update_check and update_install do. The plugin is configured from the app's real
/// tauri.conf.json, so requireSignedVersion is what the shipped app has.
fn update(key: &Key, base: &str, exe: &Path) -> Result<Option<String>, String> {
    let mut shipped: serde_json::Value = serde_json::from_str(include_str!("../tauri.conf.json")).unwrap();
    shipped["plugins"]["updater"]["dangerousInsecureTransportProtocol"] = true.into();
    let mut ctx = mock_context(noop_assets());
    ctx.config_mut().plugins.0.insert("updater".into(), shipped["plugins"]["updater"].clone());
    let app = mock_builder().plugin(tauri_plugin_updater::Builder::new().build()).build(ctx).unwrap();
    let updater = app
        .updater_builder()
        .pubkey(key.public.clone())
        .executable_path(exe)
        .endpoints(vec![format!("{base}/latest.json").parse().unwrap()])
        .unwrap()
        .build()
        .map_err(|e| e.to_string())?;
    tauri::async_runtime::block_on(async {
        let Some(update) = updater.check().await.map_err(|e| e.to_string())? else {
            return Ok(None);
        };
        update.download_and_install(|_, _| {}, || {}).await.map_err(|e| e.to_string())?;
        Ok(Some(update.version))
    })
}

#[test]
fn says_why_a_build_cannot_update() {
    assert_eq!(unavailable("", false).as_deref(), Some("built without an update key"));
    assert_eq!(unavailable("  ", false).as_deref(), Some("built without an update key"));
    assert_eq!(unavailable("key", true).as_deref(), Some("development build"));
    assert_eq!(unavailable("key", false), None);
}

#[test]
fn shipped_config_follows_stable_releases_only() {
    let conf: serde_json::Value = serde_json::from_str(include_str!("../tauri.conf.json")).unwrap();
    let updater = &conf["plugins"]["updater"];
    // releases/latest never resolves to a draft or a pre-release.
    assert_eq!(updater["endpoints"][0], "https://github.com/xlsft/harness/releases/latest/download/latest.json");
    assert_eq!(updater["requireSignedVersion"], true);
    assert!(updater.get("dangerousInsecureTransportProtocol").is_none());
}

#[cfg(target_os = "macos")]
mod install {
    use super::*;
    use std::process::Command;

    #[test]
    fn a_good_update_replaces_the_app() {
        let k = key();
        let exe = app_bundle(&tempdir("good"), "old");
        let archive = archive_of("good", "new");
        let signature = k.sign(&archive, "99.0.0");
        let base = serve(Release { version: "99.0.0", signature, archive, cut: false });
        // The installed app was opened past Gatekeeper, so it carries quarantine; the update does not.
        quarantine(exe.parent().unwrap().parent().unwrap().parent().unwrap());
        assert_eq!(update(&k, &base, &exe).unwrap().as_deref(), Some("99.0.0"));
        assert_eq!(std::fs::read_to_string(&exe).unwrap(), "new");
        assert!(!has_quarantine(exe.parent().unwrap().parent().unwrap().parent().unwrap()));
    }

    fn quarantine(path: &Path) {
        let ok = Command::new("xattr").args(["-w", "com.apple.quarantine", "0081;00000000;Safari;"]).arg(path).status().unwrap();
        assert!(ok.success());
    }

    fn has_quarantine(path: &Path) -> bool {
        let out = Command::new("xattr").arg("-lr").arg(path).output().unwrap();
        String::from_utf8_lossy(&out.stdout).contains("com.apple.quarantine")
    }

    /// A real update between two real bundles, for a person checking a build: set the HARNESS_REAL_* variables and run with
    /// --ignored. OLD_APP is a throwaway copy of the older .app, as it gets replaced.
    #[test]
    #[ignore]
    fn real_bundles() {
        let env = |name: &str| std::env::var(format!("HARNESS_REAL_{name}")).unwrap();
        let app = PathBuf::from(env("OLD_APP"));
        let archive = std::fs::read(env("ARCHIVE")).unwrap();
        let signature = std::fs::read_to_string(env("SIGNATURE")).unwrap().trim().to_string();
        let version: &'static str = env("VERSION").leak();
        let key = Key { public: env("PUBKEY"), ..self::key() };
        let exe = app.join("Contents/MacOS/harness-desktop");
        let requirement = |path: &Path| {
            let out = Command::new("codesign").args(["-d", "-r-"]).arg(path).output().unwrap();
            let text = String::from_utf8_lossy(&out.stdout).to_string() + &String::from_utf8_lossy(&out.stderr);
            text.lines().find(|l| l.contains("designated")).unwrap_or("").to_string()
        };
        quarantine(&app);
        let before = requirement(&app);
        let base = serve(Release { version, signature, archive, cut: false });
        assert_eq!(update(&key, &base, &exe).unwrap().as_deref(), Some(version));
        let after = requirement(&app);
        println!("designated requirement before: {before}\ndesignated requirement after:  {after}");
        println!("same identity for TCC: {}", before == after);
        assert!(!has_quarantine(&app), "the updated bundle is quarantined");
        assert!(Command::new("codesign").args(["--verify", "--deep", "--strict"]).arg(&app).status().unwrap().success());
        let sidecar = Command::new(app.join("Contents/MacOS/harness-client")).arg("version").output().unwrap();
        assert!(String::from_utf8_lossy(&sidecar.stdout).contains(version), "the sidecar came along");
    }

    #[test]
    fn a_signature_of_other_bytes_refuses_and_keeps_the_app() {
        let k = key();
        let exe = app_bundle(&tempdir("badsig"), "old");
        let signature = k.sign(&archive_of("other", "something else"), "99.0.0");
        let base = serve(Release { version: "99.0.0", signature, archive: archive_of("evil", "evil"), cut: false });
        assert!(update(&k, &base, &exe).is_err());
        assert_eq!(std::fs::read_to_string(&exe).unwrap(), "old");
    }

    #[test]
    fn another_key_refuses_and_keeps_the_app() {
        let (k, other) = (key(), key());
        let exe = app_bundle(&tempdir("otherkey"), "old");
        let archive = archive_of("otherkey", "evil");
        let signature = other.sign(&archive, "99.0.0");
        let base = serve(Release { version: "99.0.0", signature, archive, cut: false });
        assert!(update(&k, &base, &exe).is_err());
        assert_eq!(std::fs::read_to_string(&exe).unwrap(), "old");
    }

    #[test]
    fn an_old_release_passed_off_as_new_refuses() {
        let k = key();
        let exe = app_bundle(&tempdir("replay"), "old");
        let archive = archive_of("replay", "an older build");
        let signature = k.sign(&archive, "1.0.0");
        let base = serve(Release { version: "99.0.0", signature, archive, cut: false });
        assert!(update(&k, &base, &exe).is_err());
        assert_eq!(std::fs::read_to_string(&exe).unwrap(), "old");
    }

    #[test]
    fn an_interrupted_download_keeps_the_app() {
        let k = key();
        let exe = app_bundle(&tempdir("cut"), "old");
        let archive = archive_of("cut", "new");
        let signature = k.sign(&archive, "99.0.0");
        let base = serve(Release { version: "99.0.0", signature, archive, cut: true });
        assert!(update(&k, &base, &exe).is_err());
        assert_eq!(std::fs::read_to_string(&exe).unwrap(), "old");
    }

    #[test]
    fn nothing_newer_is_no_update() {
        let k = key();
        let exe = app_bundle(&tempdir("none"), "old");
        let base = serve(Release { version: "0.0.1", signature: String::new(), archive: Vec::new(), cut: false });
        assert_eq!(update(&k, &base, &exe).unwrap(), None);
        assert_eq!(std::fs::read_to_string(&exe).unwrap(), "old");
    }
}
