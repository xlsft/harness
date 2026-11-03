//! harness desktop shell: the shared web UI (packages/ui) in a native window, talking to a
//! `harness-client` sidecar on this machine.
//!
//! The shell owns the sidecar. It asks it whether the user is signed in (`whoami --json`), runs the
//! device-flow sign-in (`login --json`, events forwarded to the UI as `login-event`), and once signed in
//! starts `serve --listen` on a free loopback port with a random bearer token. The UI gets the endpoint
//! from `agent_endpoint` and talks Connect to it, like the web UI does with a manually started client.

use std::io::{BufRead, BufReader};
use std::net::{SocketAddr, TcpListener, TcpStream};
use std::path::PathBuf;
use std::process::{Child, Command, Stdio};
use std::sync::Mutex;
use std::time::{Duration, Instant};

use serde::Serialize;
use serde_json::Value;
use tauri::{AppHandle, Emitter, Manager, RunEvent, State};
use tauri_plugin_opener::OpenerExt;

/// Origins the webview loads the UI from (macOS/Linux, Windows, dev server); the sidecar allows them.
const ORIGINS: &str = "tauri://localhost,http://tauri.localhost,http://localhost:3210";

#[derive(Default)]
struct Shell {
    serve: Mutex<Option<Serve>>,
    login: Mutex<Option<Child>>,
}

struct Serve {
    child: Child,
    endpoint: Endpoint,
}

#[derive(Clone, Serialize)]
struct Endpoint {
    url: String,
    token: String,
}

/// The sidecar binary: Tauri places external binaries next to the app executable.
fn sidecar() -> Result<PathBuf, String> {
    let exe = std::env::current_exe().map_err(|e| e.to_string())?;
    let name = if cfg!(windows) { "harness-client.exe" } else { "harness-client" };
    let path = exe.parent().ok_or("no executable directory")?.join(name);
    if path.exists() {
        Ok(path)
    } else {
        Err(format!("harness-client not found at {}", path.display()))
    }
}

fn random_token() -> Result<String, String> {
    let mut buf = [0u8; 32];
    getrandom::getrandom(&mut buf).map_err(|e| e.to_string())?;
    Ok(buf.iter().map(|b| format!("{b:02x}")).collect())
}

fn free_port() -> Result<u16, String> {
    let l = TcpListener::bind("127.0.0.1:0").map_err(|e| e.to_string())?;
    Ok(l.local_addr().map_err(|e| e.to_string())?.port())
}

/// Signed-in state from `harness-client whoami --json`: {signed_in, email, server, ...}.
#[tauri::command]
fn auth_status() -> Result<Value, String> {
    let out = Command::new(sidecar()?).args(["whoami", "--json"]).output().map_err(|e| e.to_string())?;
    let line = String::from_utf8_lossy(&out.stdout);
    serde_json::from_str(line.trim()).map_err(|e| format!("whoami: {e}: {line}"))
}

/// Starts the device-flow sign-in. Each JSON line of `login --json` is emitted as `login-event`
/// ({event: code | signed_in | error, ...}); the verification page opens in the default browser.
#[tauri::command]
fn login(app: AppHandle, shell: State<'_, Shell>, server: String) -> Result<(), String> {
    if let Some(mut old) = shell.login.lock().unwrap().take() {
        let _ = old.kill();
    }
    let mut child = Command::new(sidecar()?)
        .args(["login", "--json", server.trim()])
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .map_err(|e| e.to_string())?;
    let stdout = child.stdout.take().ok_or("no stdout")?;
    *shell.login.lock().unwrap() = Some(child);
    std::thread::spawn(move || {
        for line in BufReader::new(stdout).lines().map_while(Result::ok) {
            let Ok(ev) = serde_json::from_str::<Value>(&line) else { continue };
            if ev["event"] == "code" {
                if let Some(uri) = ev["verification_uri"].as_str() {
                    let _ = app.opener().open_url(uri, None::<&str>);
                }
            }
            let _ = app.emit("login-event", ev);
        }
    });
    Ok(())
}

#[tauri::command]
fn cancel_login(shell: State<'_, Shell>) {
    if let Some(mut child) = shell.login.lock().unwrap().take() {
        let _ = child.kill();
    }
}

/// Signs out: stops the agent and forgets the credentials.
#[tauri::command]
fn logout(shell: State<'_, Shell>) -> Result<(), String> {
    stop_serve(&shell);
    let status = Command::new(sidecar()?).arg("logout").status().map_err(|e| e.to_string())?;
    if status.success() {
        Ok(())
    } else {
        Err("harness-client logout failed".into())
    }
}

/// The local AgentService endpoint, starting `harness-client serve` on first use (or after it exited).
#[tauri::command]
fn agent_endpoint(shell: State<'_, Shell>) -> Result<Endpoint, String> {
    let mut serve = shell.serve.lock().unwrap();
    if let Some(s) = serve.as_mut() {
        if matches!(s.child.try_wait(), Ok(None)) {
            return Ok(s.endpoint.clone());
        }
    }
    let port = free_port()?;
    let token = random_token()?;
    let addr = format!("127.0.0.1:{port}");
    let child = Command::new(sidecar()?)
        .args(["serve", "--listen", &addr])
        .env("HARNESS_AGENT_TOKEN", &token)
        .env("HARNESS_AGENT_CORS_ORIGINS", ORIGINS)
        // Exit when this app goes away without stopping it (crash, dev rebuild).
        .env("HARNESS_EXIT_WITH_PARENT", "1")
        .stdin(Stdio::null())
        .spawn()
        .map_err(|e| e.to_string())?;
    let endpoint = Endpoint { url: format!("http://{addr}"), token };
    let mut s = Serve { child, endpoint: endpoint.clone() };
    wait_listening(&mut s.child, &addr)?;
    *serve = Some(s);
    Ok(endpoint)
}

/// Waits until the sidecar accepts connections (or exits, e.g. when not signed in).
fn wait_listening(child: &mut Child, addr: &str) -> Result<(), String> {
    let sa: SocketAddr = addr.parse().map_err(|e: std::net::AddrParseError| e.to_string())?;
    let deadline = Instant::now() + Duration::from_secs(15);
    while Instant::now() < deadline {
        if let Ok(Some(status)) = child.try_wait() {
            return Err(format!("harness-client serve exited ({status})"));
        }
        if TcpStream::connect_timeout(&sa, Duration::from_millis(200)).is_ok() {
            return Ok(());
        }
        std::thread::sleep(Duration::from_millis(100));
    }
    let _ = child.kill();
    Err("harness-client serve did not start in time".into())
}

fn stop_serve(shell: &Shell) {
    if let Some(mut s) = shell.serve.lock().unwrap().take() {
        let _ = s.child.kill();
        let _ = s.child.wait();
    }
}

/// The window background behind the web page (seen before the UI paints): the ground colour of the
/// default theme of the system's kind (app/themes.ts in the ui package).
fn ground(theme: tauri::Theme) -> tauri::window::Color {
    match theme {
        tauri::Theme::Light => tauri::window::Color(0xff, 0xff, 0xff, 0xff),
        _ => tauri::window::Color(0, 0, 0, 0xff),
    }
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .plugin(tauri_plugin_dialog::init())
        .plugin(tauri_plugin_opener::init())
        .plugin(tauri_plugin_notification::init())
        .manage(Shell::default())
        .setup(|app| {
            for w in app.webview_windows().values() {
                if let Ok(theme) = w.theme() {
                    let _ = w.set_background_color(Some(ground(theme)));
                }
            }
            Ok(())
        })
        .on_window_event(|window, event| {
            if let tauri::WindowEvent::ThemeChanged(theme) = event {
                let _ = window.set_background_color(Some(ground(*theme)));
            }
        })
        .invoke_handler(tauri::generate_handler![auth_status, login, cancel_login, logout, agent_endpoint])
        .build(tauri::generate_context!())
        .expect("error while building the harness desktop app")
        .run(|app, event| {
            if let RunEvent::Exit = event {
                let shell = app.state::<Shell>();
                stop_serve(&shell);
                let login = shell.login.lock().unwrap().take();
                if let Some(mut child) = login {
                    let _ = child.kill();
                }
            }
        });
}
