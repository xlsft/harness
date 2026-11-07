//! The app's own updates: checks the newest stable release's `latest.json` (tauri.conf.json, plugins.updater), and on the
//! user's word downloads and installs it. The plugin verifies the download against the configured public key before it
//! touches the installed app, and the sidecar comes along, being inside the bundle. Restarting is a separate step, so the
//! user decides when running work stops (lib/desktop.ts in the ui package warns first).

use serde::Serialize;
use tauri::{AppHandle, Manager, State};
use tauri_plugin_updater::UpdaterExt;

use crate::Shell;

#[derive(Serialize)]
#[serde(tag = "state", rename_all = "snake_case")]
pub enum Check {
    /// This build can't update: a development build, or a release built without the updater key.
    Unavailable { reason: String, current: String },
    UpToDate { current: String },
    /// A newer release exists, above the maximum version the organization's managed policy allows: not offered.
    Held { current: String, version: String, max: String },
    Available { current: String, version: String, notes: String },
}

/// Whether the policy's maximum holds a release back. A maximum that isn't a version holds nothing, like the client's.
fn held_back(version: &str, max: &str) -> bool {
    let parse = |v: &str| semver::Version::parse(v.trim().trim_start_matches('v')).ok();
    match (parse(version), parse(max)) {
        (Some(v), Some(m)) => v > m,
        _ => false,
    }
}

/// Why this build can't update, if so. An empty pubkey is the state of the repository until the release key is generated
/// (docs/release.md): the app builds and runs, and says so in Settings → About, while a release refuses to build.
fn unavailable(pubkey: &str, debug: bool) -> Option<String> {
    if debug {
        Some("development build".into())
    } else if pubkey.trim().is_empty() {
        Some("built without an update key".into())
    } else {
        None
    }
}

fn configured_pubkey(app: &AppHandle) -> String {
    app.config().plugins.0.get("updater").and_then(|c| c.get("pubkey")).and_then(|k| k.as_str()).unwrap_or("").to_string()
}

/// Whether a newer release exists. Finding none is an answer; failing to look is an error the UI keeps quiet about.
/// max_version is the managed policy's maximum client version as the client merged it (UserPreferences), "" for none: the
/// app is never offered a release above it. Only the newest release is published to the updater, so one above the maximum
/// means no update here; `harness-client update` can still install the maximum itself.
#[tauri::command]
pub async fn update_check(app: AppHandle, shell: State<'_, Shell>, max_version: Option<String>) -> Result<Check, String> {
    let current = app.package_info().version.to_string();
    if let Some(reason) = unavailable(&configured_pubkey(&app), cfg!(debug_assertions)) {
        return Ok(Check::Unavailable { reason, current });
    }
    let update = app.updater().map_err(|e| e.to_string())?.check().await.map_err(|e| e.to_string())?;
    let max = max_version.unwrap_or_default();
    if let Some(u) = update.as_ref().filter(|u| held_back(&u.version, &max)) {
        let check = Check::Held { current, version: u.version.clone(), max: max.trim().trim_start_matches('v').to_string() };
        *shell.update.lock().unwrap() = None;
        return Ok(check);
    }
    let check = match &update {
        Some(u) => Check::Available {
            current,
            version: u.version.clone(),
            notes: u.body.clone().unwrap_or_default(),
        },
        None => Check::UpToDate { current },
    };
    *shell.update.lock().unwrap() = update;
    Ok(check)
}

/// Downloads, verifies and installs the update the last check found. The running app keeps running from memory until
/// update_restart.
#[tauri::command]
pub async fn update_install(shell: State<'_, Shell>) -> Result<(), String> {
    let update = shell.update.lock().unwrap().clone().ok_or("no update to install")?;
    update.download_and_install(|_, _| {}, || {}).await.map_err(|e| e.to_string())?;
    *shell.update.lock().unwrap() = None;
    Ok(())
}

/// Restarts into the installed update. The user has been asked by now, so the quit guard stands aside.
#[tauri::command]
pub fn update_restart(app: AppHandle) {
    let shell = app.state::<Shell>();
    shell.quit_confirmed.store(true, std::sync::atomic::Ordering::SeqCst);
    crate::stop_serve(&shell);
    app.restart();
}

#[cfg(test)]
#[path = "update_tests.rs"]
mod tests;
