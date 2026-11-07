//! `harness://` links. A link only ever opens something: a session, the new-session screen with a draft, or a section of
//! Settings. Nothing runs from a link: a draft's prompt waits in the composer until the user reads it and sends it, which
//! the UI says above it. Every part of a link is checked here, before the UI hears of it; a link that doesn't pass is
//! dropped whole.
//!
//!   harness://session/<id>                          open a session
//!   harness://new?prompt=<text>&folder=<abs path>   the new-session screen with a draft (both optional)
//!   harness://settings/<section>                    a section of Settings
//!
//! Links arrive while the app runs (the deep-link plugin's open-url event) or as the reason it started. They wait in a
//! queue until the UI takes them (take_deep_links), so a link that came before the page loaded isn't lost.

use std::path::{Component, Path};
use std::sync::Mutex;

use serde::Serialize;
use tauri::{AppHandle, Emitter, Manager, State};

/// The longest prompt a link may carry, in characters; longer links are refused rather than cut.
pub const MAX_PROMPT: usize = 8000;
const MAX_LINK: usize = 32 * 1024;
const MAX_QUEUE: usize = 16;

/// The sections of Settings a link may open (SettingsPage.vue in the ui package).
pub const SECTIONS: &[&str] = &[
    "account",
    "usage",
    "appearance",
    "sessions",
    "sandbox",
    "hooks",
    "memory",
    "codehosts",
    "import",
    "models",
    "classifiers",
    "search",
    "speech",
    "computer",
    "ssh",
    "users",
    "ldap",
    "sso",
    "provisioning",
    "audit",
    "policy",
    "about",
];

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum DeepLink {
    Session {
        id: String,
    },
    /// folder is an absolute path to a directory that exists; "" when the link named none, or one that isn't.
    New {
        prompt: String,
        folder: String,
        folder_dropped: bool,
    },
    Settings {
        section: String,
    },
}

#[derive(Default)]
pub struct Links(pub Mutex<Vec<DeepLink>>);

/// Reads a link; None for anything that isn't one of the forms above, exactly.
pub fn parse(raw: &str, is_dir: impl Fn(&Path) -> bool) -> Option<DeepLink> {
    if raw.len() > MAX_LINK {
        return None;
    }
    let rest = raw.strip_prefix("harness://")?;
    let (path, query) = match rest.split_once('?') {
        Some((p, q)) => (p, q),
        None => (rest, ""),
    };
    if path.contains('#') || query.contains('#') {
        return None;
    }
    let path = path.trim_end_matches('/');
    let params = parse_query(query)?;
    let (head, tail) = match path.split_once('/') {
        Some((h, t)) => (h, Some(t)),
        None => (path, None),
    };
    match (head, tail) {
        ("session", Some(id)) if params.is_empty() => {
            let id = percent_decode(id)?;
            valid_id(&id).then_some(DeepLink::Session { id })
        }
        ("settings", Some(section)) if params.is_empty() => {
            SECTIONS.contains(&section).then(|| DeepLink::Settings {
                section: section.to_string(),
            })
        }
        ("new", None) => {
            let mut prompt = String::new();
            let mut folder = String::new();
            for (k, v) in params {
                match k.as_str() {
                    "prompt" if prompt.is_empty() => prompt = v,
                    "folder" | "cwd" if folder.is_empty() => folder = v,
                    // An unknown or repeated parameter: refuse rather than guess what the link meant.
                    _ => return None,
                }
            }
            if prompt.chars().count() > MAX_PROMPT
                || prompt
                    .chars()
                    .any(|c| c.is_control() && c != '\n' && c != '\t')
            {
                return None;
            }
            let mut folder_dropped = false;
            if !folder.is_empty() && !valid_folder(&folder, &is_dir) {
                folder.clear();
                folder_dropped = true;
            }
            Some(DeepLink::New {
                prompt,
                folder,
                folder_dropped,
            })
        }
        _ => None,
    }
}

fn valid_id(id: &str) -> bool {
    !id.is_empty()
        && id.len() <= 128
        && id
            .chars()
            .all(|c| c.is_ascii_alphanumeric() || c == '-' || c == '_')
}

/// An absolute, plain path (no `.` or `..`, no control characters) to a directory that exists.
fn valid_folder(folder: &str, is_dir: &impl Fn(&Path) -> bool) -> bool {
    let p = Path::new(folder);
    if !p.is_absolute() || folder.chars().any(char::is_control) || folder.len() > 4096 {
        return false;
    }
    // Path::components drops "." in the middle of a path, so the segments are read as written too.
    if p.components()
        .any(|c| matches!(c, Component::ParentDir | Component::CurDir))
        || folder.split('/').any(|s| s == "." || s == "..")
    {
        return false;
    }
    is_dir(p)
}

fn parse_query(q: &str) -> Option<Vec<(String, String)>> {
    let mut out = Vec::new();
    for pair in q.split('&').filter(|s| !s.is_empty()) {
        let (k, v) = pair.split_once('=').unwrap_or((pair, ""));
        out.push((
            percent_decode(&k.replace('+', " "))?,
            percent_decode(&v.replace('+', " "))?,
        ));
    }
    Some(out)
}

/// Decodes %XX escapes into UTF-8; None for a broken escape or bytes that aren't UTF-8.
fn percent_decode(s: &str) -> Option<String> {
    let b = s.as_bytes();
    let mut out = Vec::with_capacity(b.len());
    let mut i = 0;
    while i < b.len() {
        if b[i] == b'%' {
            let hex = s.get(i + 1..i + 3)?;
            out.push(u8::from_str_radix(hex, 16).ok()?);
            i += 3;
        } else {
            out.push(b[i]);
            i += 1;
        }
    }
    String::from_utf8(out).ok()
}

/// Queues the links that pass and tells the UI there is something to take.
pub fn receive(app: &AppHandle, urls: impl IntoIterator<Item = String>) {
    let links = app.state::<Links>();
    let mut queue = links.0.lock().unwrap();
    let before = queue.len();
    for url in urls {
        if let Some(link) = parse(&url, |p| p.is_dir()) {
            if queue.len() < MAX_QUEUE {
                queue.push(link);
            }
        }
    }
    let added = queue.len() > before;
    drop(queue);
    if added {
        let _ = app.emit("deep-link", ());
        if let Some(w) = app.get_webview_window("main") {
            let _ = w.unminimize();
            let _ = w.set_focus();
        }
    }
}

/// The links that came since the last call, oldest first.
#[tauri::command]
pub fn take_deep_links(links: State<'_, Links>) -> Vec<DeepLink> {
    std::mem::take(&mut *links.0.lock().unwrap())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn dirs(p: &Path) -> bool {
        p == Path::new("/Users/ada/code") || p == Path::new("/Users/ada/my project")
    }

    fn new(prompt: &str, folder: &str, dropped: bool) -> Option<DeepLink> {
        Some(DeepLink::New {
            prompt: prompt.into(),
            folder: folder.into(),
            folder_dropped: dropped,
        })
    }

    #[test]
    fn sessions() {
        assert_eq!(
            parse("harness://session/0b9f-AB_12", dirs),
            Some(DeepLink::Session {
                id: "0b9f-AB_12".into()
            })
        );
        assert_eq!(
            parse("harness://session/abc/", dirs),
            Some(DeepLink::Session { id: "abc".into() })
        );
        for bad in [
            "harness://session/",
            "harness://session/a/b",
            "harness://session/..%2Fx",
            "harness://session/a%20b",
            "harness://session/abc?run=1",
            "harness://session/abc#x",
            "https://session/abc",
            "harness:session/abc",
        ] {
            assert_eq!(parse(bad, dirs), None, "{bad}");
        }
        assert_eq!(
            parse(&format!("harness://session/{}", "a".repeat(129)), dirs),
            None
        );
    }

    #[test]
    fn drafts() {
        assert_eq!(parse("harness://new", dirs), new("", "", false));
        assert_eq!(
            parse(
                "harness://new?prompt=fix%20the%20tests%0Aplease&folder=%2FUsers%2Fada%2Fcode",
                dirs
            ),
            new("fix the tests\nplease", "/Users/ada/code", false)
        );
        assert_eq!(
            parse("harness://new?folder=/Users/ada/my+project&prompt=hi", dirs),
            new("hi", "/Users/ada/my project", false)
        );
        // A folder that isn't a plain absolute path to a directory is dropped, and the UI says so.
        for folder in [
            "relative/dir",
            "/Users/ada/code/../..",
            "/Users/ada/./code",
            "/nope",
            "/Users/ada/code%00",
        ] {
            assert_eq!(
                parse(&format!("harness://new?prompt=x&folder={folder}"), dirs),
                new("x", "", true),
                "{folder}"
            );
        }
        for bad in [
            "harness://new?prompt=a&prompt=b",
            "harness://new?prompt=a&run=1",
            "harness://new?prompt=a&autosend=true",
            "harness://new?prompt=%ZZ",
            "harness://new?prompt=%FF",
            "harness://new?prompt=ring%07",
            "harness://new/extra",
        ] {
            assert_eq!(parse(bad, dirs), None, "{bad}");
        }
        assert!(parse(
            &format!("harness://new?prompt={}", "a".repeat(MAX_PROMPT)),
            dirs
        )
        .is_some());
        assert_eq!(
            parse(
                &format!("harness://new?prompt={}", "a".repeat(MAX_PROMPT + 1)),
                dirs
            ),
            None
        );
    }

    #[test]
    fn settings() {
        assert_eq!(
            parse("harness://settings/about", dirs),
            Some(DeepLink::Settings {
                section: "about".into()
            })
        );
        assert_eq!(
            parse("harness://settings/models/", dirs),
            Some(DeepLink::Settings {
                section: "models".into()
            })
        );
        for bad in [
            "harness://settings",
            "harness://settings/",
            "harness://settings/nope",
            "harness://settings/about/x",
            "harness://settings/about?x=1",
        ] {
            assert_eq!(parse(bad, dirs), None, "{bad}");
        }
    }

    #[test]
    fn json_for_the_ui() {
        let v = serde_json::to_value(DeepLink::New {
            prompt: "p".into(),
            folder: "/f".into(),
            folder_dropped: false,
        })
        .unwrap();
        assert_eq!(
            v,
            serde_json::json!({"kind": "new", "prompt": "p", "folder": "/f", "folder_dropped": false})
        );
    }
}
