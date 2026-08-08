use std::{
    collections::{HashMap, HashSet},
    hash::{DefaultHasher, Hash, Hasher},
    sync::{
        atomic::{AtomicBool, Ordering},
        Arc, Mutex,
    },
    time::{Duration, Instant, SystemTime, UNIX_EPOCH},
};

use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use tauri::{
    menu::{Menu, MenuItem},
    tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent},
    webview::{Cookie, NewWindowResponse, PageLoadEvent, WebviewBuilder},
    Emitter, LogicalPosition, LogicalSize, Manager, RunEvent, Url, WebviewUrl,
    WebviewWindowBuilder, WindowEvent,
};
use tauri_plugin_shell::{
    process::{CommandChild, CommandEvent},
    ShellExt,
};
use tokio::sync::oneshot;
use uuid::Uuid;

struct EngineRuntime {
    child: Mutex<Option<CommandChild>>,
    online: AtomicBool,
    native_secret: String,
    pending: Mutex<HashMap<String, oneshot::Sender<String>>>,
    // Child WebViews are intentionally destroyed when the browser screen is
    // not visible so they release their runtime leases. Remember only their
    // last safe Douyin location here so remounting the same stable instance
    // restores its page instead of always jumping back to the home page.
    browser_locations: Mutex<HashMap<String, String>>,
    // Instance records are disposable: closing a card and creating it again for
    // the same account yields a new instance id. Mirror the last safe location
    // per account, and remember which account owns a mounted instance, so a
    // recreated card resumes that page instead of reloading the heavy home feed.
    browser_account_locations: Mutex<HashMap<String, String>>,
    browser_instance_accounts: Mutex<HashMap<String, String>>,
    // A card whose document rendered before the injected cookies landed keeps
    // showing Douyin's anonymous shell. Throttle the self-healing reload so a
    // recurring adaptive check cannot reload the page on every tick, and keep
    // the first observation so a session Douyin keeps rejecting can eventually
    // be reported instead of being retried forever behind a healthy badge.
    browser_anonymous_healed_at: Mutex<HashMap<String, Instant>>,
    browser_anonymous_since: Mutex<HashMap<String, Instant>>,
    // A reloading page alternates between “still empty” and “anonymous shell”,
    // so a single clean observation must not reset the first-seen timer or the
    // escalation below could never be reached. Require a quiet period instead.
    browser_anonymous_last_at: Mutex<HashMap<String, Instant>>,
    // Douyin's explicit login dialog is authoritative, but a card can paint one
    // transient frame while a fresh session restores. Remember when the wall
    // first appeared so a persistent one becomes CK expiry instead of being
    // deferred forever by a re-assert that keeps re-arming the inject grace.
    browser_login_wall_since: Mutex<HashMap<String, Instant>>,
    // Closing a child WKWebView is asynchronous at the WebKit/Tauri boundary.
    // Keep a native tombstone so overlapping layout, scroll, and modal cleanup
    // requests cannot reuse or close the same invalidating handle twice.
    // First undetermined render observation and last recovery for a card whose
    // Douyin document never finishes painting, tracked separately from the
    // anonymous-shell timers because neither signal implies the other.
    browser_stalled_render_since: Mutex<HashMap<String, Instant>>,
    browser_stalled_render_healed_at: Mutex<HashMap<String, Instant>>,
    browser_webviews_closing: Mutex<HashSet<String>>,
    browser_red_packet_contexts: Mutex<HashSet<String>>,
    browser_cookie_synced_at: Mutex<HashMap<String, Instant>>,
    // WebView2 applies Cookie-manager writes asynchronously. Remember when a
    // complete login snapshot was confirmed in a newly mounted account
    // profile so an initial login-dialog paint cannot immediately invalidate
    // the canonical CK while Douyin is still restoring the session.
    browser_cookie_injected_at: Mutex<HashMap<String, Instant>>,
}

impl Default for EngineRuntime {
    fn default() -> Self {
        Self {
            child: Mutex::new(None),
            online: AtomicBool::new(false),
            native_secret: Uuid::new_v4().to_string(),
            pending: Mutex::new(HashMap::new()),
            browser_locations: Mutex::new(HashMap::new()),
            browser_account_locations: Mutex::new(HashMap::new()),
            browser_instance_accounts: Mutex::new(HashMap::new()),
            browser_anonymous_healed_at: Mutex::new(HashMap::new()),
            browser_anonymous_since: Mutex::new(HashMap::new()),
            browser_anonymous_last_at: Mutex::new(HashMap::new()),
            browser_login_wall_since: Mutex::new(HashMap::new()),
            browser_stalled_render_since: Mutex::new(HashMap::new()),
            browser_stalled_render_healed_at: Mutex::new(HashMap::new()),
            browser_webviews_closing: Mutex::new(HashSet::new()),
            browser_red_packet_contexts: Mutex::new(HashSet::new()),
            browser_cookie_synced_at: Mutex::new(HashMap::new()),
            browser_cookie_injected_at: Mutex::new(HashMap::new()),
        }
    }
}

fn start_engine(app: tauri::AppHandle, runtime: Arc<EngineRuntime>) -> Result<(), String> {
    let command = app
        .shell()
        .sidecar("fubao-engine")
        .map_err(|error| format!("准备 Go 引擎失败：{error}"))?
        .env("FUBAO_NATIVE_RPC_SECRET", runtime.native_secret.clone());
    let (mut receiver, child) = command
        .spawn()
        .map_err(|error| format!("启动 Go 引擎失败：{error}"))?;

    runtime.online.store(true, Ordering::SeqCst);
    *runtime.child.lock().map_err(|_| "引擎状态锁不可用")? = Some(child);

    let event_runtime = runtime.clone();
    let participation_runtime = runtime.clone();
    let participation_app = app.clone();
    tauri::async_runtime::spawn(async move {
        let mut stdout_buffer = String::new();
        while let Some(event) = receiver.recv().await {
            match event {
                CommandEvent::Stdout(bytes) => {
                    if let Ok(message) = String::from_utf8(bytes) {
                        stdout_buffer.push_str(&message);
                        while let Some(newline) = stdout_buffer.find('\n') {
                            let line = stdout_buffer[..newline].trim().to_string();
                            stdout_buffer.drain(..=newline);
                            if !line.is_empty() {
                                let response_id = serde_json::from_str::<Value>(&line)
                                    .ok()
                                    .and_then(|value| value.get("id")?.as_str().map(str::to_owned));
                                if let Some(response_id) = response_id {
                                    if let Some(sender) = event_runtime
                                        .pending
                                        .lock()
                                        .ok()
                                        .and_then(|mut pending| pending.remove(&response_id))
                                    {
                                        let _ = sender.send(line);
                                        continue;
                                    }
                                }
                                let _ = app.emit("engine://message", line);
                            }
                        }
                    }
                }
                CommandEvent::Stderr(bytes) => {
                    if let Ok(message) = String::from_utf8(bytes) {
                        let _ = app.emit("engine://log", message.trim());
                    }
                }
                CommandEvent::Terminated(payload) => {
                    runtime.online.store(false, Ordering::SeqCst);
                    let _ = app.emit("engine://terminated", payload.code);
                }
                _ => {}
            }
        }
        runtime.online.store(false, Ordering::SeqCst);
    });

    tauri::async_runtime::spawn(async move {
        poll_page_participation_tasks(participation_app, participation_runtime).await;
    });

    Ok(())
}

fn stop_engine(app: &tauri::AppHandle) {
    let runtime = app.state::<Arc<EngineRuntime>>();
    if let Ok(mut guard) = runtime.child.lock() {
        if let Some(child) = guard.take() {
            let _ = child.kill();
        }
    }
    runtime.online.store(false, Ordering::SeqCst);
}

#[cfg(windows)]
fn open_engine_wait_handle(pid: u32) -> Result<usize, String> {
    use windows_sys::Win32::System::Threading::{OpenProcess, PROCESS_SYNCHRONIZE};

    let handle = unsafe { OpenProcess(PROCESS_SYNCHRONIZE, 0, pid) };
    if handle.is_null() {
        return Err(format!("无法等待 Go 引擎进程退出（PID {pid}）"));
    }
    // windows-sys models HANDLE as `*mut c_void`, which is not Send. Store
    // its pointer-sized value while it crosses into spawn_blocking, then
    // restore the HANDLE only inside that blocking thread.
    Ok(handle as usize)
}

#[cfg(windows)]
fn wait_for_engine_exit(handle_value: usize) -> Result<(), String> {
    use windows_sys::Win32::{
        Foundation::{CloseHandle, HANDLE, WAIT_OBJECT_0},
        System::Threading::WaitForSingleObject,
    };

    const ENGINE_EXIT_TIMEOUT_MS: u32 = 10_000;
    let handle = handle_value as HANDLE;
    let result = unsafe { WaitForSingleObject(handle, ENGINE_EXIT_TIMEOUT_MS) };
    unsafe { CloseHandle(handle) };
    if result != WAIT_OBJECT_0 {
        return Err("Go 引擎未能及时退出，请稍后重试更新".into());
    }
    Ok(())
}

#[cfg(windows)]
fn stop_stale_engine_processes() -> Result<(), String> {
    use std::{os::windows::process::CommandExt, process::Command, thread, time::Duration};

    const CREATE_NO_WINDOW: u32 = 0x0800_0000;
    const ENGINE_IMAGE: &str = "fubao-engine.exe";

    // A previous client can have left its sidecar alive after a tray restart
    // or an interrupted update. The updater only knows about the current
    // Tauri child handle, so explicitly terminate any stale copy of our
    // uniquely-named bundled sidecar before NSIS tries to replace it.
    let _ = Command::new("taskkill")
        .args(["/F", "/T", "/IM", ENGINE_IMAGE])
        .creation_flags(CREATE_NO_WINDOW)
        .output()
        .map_err(|error| format!("清理旧 Go 引擎失败：{error}"))?;

    for _ in 0..100 {
        let output = Command::new("tasklist")
            .args(["/FI", "IMAGENAME eq fubao-engine.exe", "/FO", "CSV", "/NH"])
            .creation_flags(CREATE_NO_WINDOW)
            .output()
            .map_err(|error| format!("检查 Go 引擎残留失败：{error}"))?;
        let listing = String::from_utf8_lossy(&output.stdout);
        if !listing
            .lines()
            .any(|line| line.to_ascii_lowercase().contains(ENGINE_IMAGE))
        {
            return Ok(());
        }
        thread::sleep(Duration::from_millis(100));
    }

    Err("仍有旧 Go 引擎进程占用更新文件，请关闭其他福宝客户端后重试".into())
}

#[tauri::command]
async fn prepare_app_update(runtime: tauri::State<'_, Arc<EngineRuntime>>) -> Result<(), String> {
    let child = runtime.child.lock().map_err(|_| "引擎状态锁不可用")?.take();

    if let Some(child) = child {
        #[cfg(windows)]
        let wait_handle = open_engine_wait_handle(child.pid())?;

        child
            .kill()
            .map_err(|error| format!("停止 Go 引擎失败：{error}"))?;

        #[cfg(windows)]
        tauri::async_runtime::spawn_blocking(move || wait_for_engine_exit(wait_handle))
            .await
            .map_err(|error| format!("等待 Go 引擎退出失败：{error}"))??;

        #[cfg(not(windows))]
        tokio::time::sleep(Duration::from_millis(250)).await;
    }

    #[cfg(windows)]
    stop_stale_engine_processes()?;

    runtime.online.store(false, Ordering::SeqCst);
    if let Ok(mut pending) = runtime.pending.lock() {
        pending.clear();
    }
    Ok(())
}

const MAIN_WINDOW_LABEL: &str = "main";

/// Rebuild the primary console window after it was truly destroyed. Matches
/// tauri.conf.json defaults so tray reopen never depends on a living handle.
fn recreate_main_window(app: &tauri::AppHandle) -> Result<tauri::WebviewWindow, String> {
    let window_labels: Vec<String> = app.windows().keys().cloned().collect();
    let webview_labels: Vec<String> = app.webviews().keys().cloned().collect();
    eprintln!(
        "[fubao-tray] recreating main window; windows={window_labels:?} webviews={webview_labels:?}"
    );

    let builder = WebviewWindowBuilder::new(
        app,
        MAIN_WINDOW_LABEL,
        WebviewUrl::App("index.html".into()),
    )
    .title("福宝控制台")
    .inner_size(1180.0, 760.0)
    .min_inner_size(760.0, 560.0)
    .resizable(true)
    .decorations(true)
    .focused(true)
    .visible(true)
    .center();

    #[cfg(target_os = "macos")]
    let builder = builder
        .title_bar_style(tauri::TitleBarStyle::Overlay)
        .hidden_title(true)
        .traffic_light_position(LogicalPosition::new(15.0, 20.0))
        .background_color(tauri::webview::Color(255, 255, 255, 255));

    let window = builder
        .build()
        .map_err(|error| format!("重建主窗口失败：{error}"))?;

    #[cfg(windows)]
    apply_windows_titlebar_palette(&window);

    Ok(window)
}

fn show_main_window(app: &tauri::AppHandle) {
    // On macOS, show the NSApplication first so a previously hidden app can
    // come forward from the tray menu.
    #[cfg(target_os = "macos")]
    if let Err(error) = app.show() {
        eprintln!("[fubao-tray] app.show failed: {error}");
    }

    // IMPORTANT: do NOT rely only on get_webview_window("main").
    // Tauri treats a window as a WebviewWindow only when every attached webview
    // shares the window label. Mounted browser-instance / rebind child WebViews
    // use labels like `browser-…--main`, which makes is_webview_window() false
    // and get_webview_window("main") return None even though the window is live.
    // Prefer the Window manager (unstable feature), which still finds "main".
    if let Some(window) = app.get_window(MAIN_WINDOW_LABEL) {
        if let Err(error) = reveal_window(&window) {
            eprintln!("[fubao-tray] reveal main window failed: {error}");
        } else {
            eprintln!("[fubao-tray] main window revealed via Window handle");
        }
        let _ = window.request_user_attention(Some(tauri::UserAttentionType::Informational));
        return;
    }

    if let Some(window) = app.get_webview_window(MAIN_WINDOW_LABEL) {
        if let Err(error) = window.unminimize() {
            eprintln!("[fubao-tray] unminimize failed: {error}");
        }
        if let Err(error) = window.show() {
            eprintln!("[fubao-tray] window.show failed: {error}");
        }
        #[cfg(windows)]
        force_windows_webview_front(&window);
        if let Err(error) = window.set_focus() {
            eprintln!("[fubao-tray] set_focus failed: {error}");
        }
        #[cfg(windows)]
        force_windows_webview_front(&window);
        let _ = window.request_user_attention(Some(tauri::UserAttentionType::Informational));
        eprintln!("[fubao-tray] main window revealed via WebviewWindow handle");
        return;
    }

    match recreate_main_window(app) {
        Ok(window) => {
            #[cfg(windows)]
            force_windows_webview_front(&window);
            let _ = window.set_focus();
            eprintln!("[fubao-tray] main window recreated");
        }
        Err(error) => {
            // Stale label race: webview map still has "main" but Window lookup
            // failed. Retry Window once more after the error path.
            eprintln!("[fubao-tray] cannot open 福宝控制台: {error}");
            if let Some(window) = app.get_window(MAIN_WINDOW_LABEL) {
                let _ = reveal_window(&window);
            } else if let Some(webview) = app.get_webview(MAIN_WINDOW_LABEL) {
                let window = webview.window();
                let _ = reveal_window(&window);
            }
        }
    }
}

fn reveal_window(window: &tauri::Window) -> Result<(), String> {
    window
        .show()
        .map_err(|error| format!("显示窗口失败：{error}"))?;
    let _ = window.unminimize();
    #[cfg(windows)]
    force_windows_window_front(window);
    window
        .set_focus()
        .map_err(|error| format!("聚焦窗口失败：{error}"))?;
    #[cfg(windows)]
    force_windows_window_front(window);
    Ok(())
}

#[cfg(windows)]
fn force_windows_window_front(window: &tauri::Window) {
    let Ok(hwnd) = window.hwnd() else {
        return;
    };
    force_windows_hwnd_front(hwnd.0);
}

#[cfg(windows)]
fn force_windows_webview_front(window: &tauri::WebviewWindow) {
    let Ok(hwnd) = window.hwnd() else {
        return;
    };
    force_windows_hwnd_front(hwnd.0);
}

#[cfg(windows)]
fn force_windows_hwnd_front(hwnd: windows_sys::Win32::Foundation::HWND) {
    use windows_sys::Win32::UI::WindowsAndMessaging::{
        BringWindowToTop, SetForegroundWindow, SetWindowPos, ShowWindow, HWND_NOTOPMOST,
        HWND_TOPMOST, SWP_NOMOVE, SWP_NOSIZE, SWP_SHOWWINDOW, SW_RESTORE,
    };

    let flags = SWP_NOMOVE | SWP_NOSIZE | SWP_SHOWWINDOW;
    unsafe {
        let _ = ShowWindow(hwnd, SW_RESTORE);
        let _ = SetWindowPos(hwnd, HWND_TOPMOST, 0, 0, 0, 0, flags);
        let _ = BringWindowToTop(hwnd);
        let _ = SetForegroundWindow(hwnd);
        // Use TOPMOST only as an activation assist. The instance window must
        // not remain above unrelated applications after it receives focus.
        let _ = SetWindowPos(hwnd, HWND_NOTOPMOST, 0, 0, 0, 0, flags);
    }
}

#[cfg(windows)]
fn apply_windows_titlebar_palette(window: &tauri::WebviewWindow) {
    use windows_sys::Win32::Graphics::Dwm::{
        DwmSetWindowAttribute, DWMWA_BORDER_COLOR, DWMWA_CAPTION_COLOR, DWMWA_TEXT_COLOR,
    };

    let Ok(hwnd) = window.hwnd() else {
        return;
    };
    // COLORREF is encoded as 0x00BBGGRR. Keep native window controls while
    // matching the warm sidebar/topbar palette used by the web surface.
    let attributes = [
        (DWMWA_CAPTION_COLOR as u32, 0x00F7F9FA_u32), // #faf9f7
        (DWMWA_TEXT_COLOR as u32, 0x00272B2D_u32),    // #2d2b27
        (DWMWA_BORDER_COLOR as u32, 0x00DFE5E8_u32),  // #e8e5df
    ];
    for (attribute, color) in attributes {
        unsafe {
            let _ = DwmSetWindowAttribute(
                hwnd.0,
                attribute,
                (&color as *const u32).cast(),
                std::mem::size_of_val(&color) as u32,
            );
        }
    }
}

fn setup_system_tray(app: &tauri::AppHandle) -> tauri::Result<()> {
    let show = MenuItem::with_id(app, "tray-show-main", "打开福宝控制台", true, None::<&str>)?;
    let quit = MenuItem::with_id(app, "tray-quit", "彻底退出", true, None::<&str>)?;
    let menu = Menu::with_items(app, &[&show, &quit])?;

    let mut builder = TrayIconBuilder::with_id("fubao-main-tray")
        .menu(&menu)
        .show_menu_on_left_click(false)
        .tooltip("福宝控制台 · 后台任务运行中")
        .on_menu_event(|app, event| {
            let id = event.id.as_ref();
            match id {
                "tray-show-main" => {
                    // Defer one tick so macOS finishes dismissing the tray
                    // menu before we steal focus; otherwise set_focus can no-op.
                    let handle = app.clone();
                    let _ = app.run_on_main_thread(move || {
                        show_main_window(&handle);
                    });
                }
                "tray-quit" => {
                    stop_engine(app);
                    app.exit(0);
                }
                _ => {}
            }
        })
        .on_tray_icon_event(|tray, event| {
            if matches!(
                event,
                TrayIconEvent::Click {
                    button: MouseButton::Left,
                    button_state: MouseButtonState::Up,
                    ..
                } | TrayIconEvent::DoubleClick {
                    button: MouseButton::Left,
                    ..
                }
            ) {
                let handle = tray.app_handle().clone();
                let _ = tray.app_handle().run_on_main_thread(move || {
                    show_main_window(&handle);
                });
            }
        });
    if let Some(icon) = app.default_window_icon().cloned() {
        builder = builder.icon(icon);
    }
    builder.build(app)?;
    Ok(())
}

async fn native_engine_request(
    runtime: Arc<EngineRuntime>,
    method: &str,
    params: Value,
) -> Result<Value, String> {
    let id = format!("native-{}", Uuid::new_v4());
    let payload = json!({
        "v": 1,
        "id": id,
        "method": method,
        "params": params,
    });
    let line = format!("{payload}\n");
    let (sender, receiver) = oneshot::channel();
    runtime
        .pending
        .lock()
        .map_err(|_| "原生请求状态锁不可用")?
        .insert(id.clone(), sender);
    let write_result = runtime
        .child
        .lock()
        .map_err(|_| "引擎状态锁不可用")?
        .as_mut()
        .ok_or("Go 引擎未运行")?
        .write(line.as_bytes())
        .map_err(|error| format!("发送原生请求失败：{error}"));
    if let Err(error) = write_result {
        if let Ok(mut pending) = runtime.pending.lock() {
            pending.remove(&id);
        }
        return Err(error);
    }
    let response_line = tokio::time::timeout(Duration::from_secs(8), receiver)
        .await
        .map_err(|_| "等待 Go 引擎响应超时".to_string())?
        .map_err(|_| "Go 引擎响应通道已关闭".to_string())?;
    let response: Value = serde_json::from_str(&response_line)
        .map_err(|error| format!("解析 Go 引擎响应失败：{error}"))?;
    if response.get("ok").and_then(Value::as_bool) != Some(true) {
        return Err(response
            .pointer("/error/message")
            .and_then(Value::as_str)
            .unwrap_or("Go 引擎请求失败")
            .to_string());
    }
    Ok(response.get("result").cloned().unwrap_or(Value::Null))
}

#[derive(Deserialize)]
struct BrowserBounds {
    x: f64,
    y: f64,
    width: f64,
    height: f64,
}

#[derive(Deserialize)]
struct NativeBrowserCredential {
    instance_id: String,
    account_id: String,
    account_name: String,
    cookie: String,
    cookie_status: String,
    #[serde(default)]
    surface: String,
    /// Native WebView data-store key. Scan-login sets `create-{session}` so the
    /// instance reuses the already-authenticated create WebView store.
    #[serde(default)]
    browser_profile_key: String,
}

#[derive(Deserialize)]
struct NativeAccountCredential {
    account_id: String,
    account_name: String,
    cookie: String,
}

#[derive(Deserialize)]
struct NativePageParticipationTask {
    task_id: String,
    action: String,
    instance_id: String,
    account_id: String,
    web_rid: String,
    actual_room_id: String,
    box_id: String,
    packet_id: Option<String>,
    user_id: Option<String>,
    sec_uid: Option<String>,
    anchor_id: Option<String>,
    box_type: Option<String>,
    send_time: Option<String>,
    delay_time: Option<String>,
}

struct NativePageParticipationResult {
    endpoint: String,
    http_status: i64,
    body: String,
    error: String,
    attempts: i64,
    context_missing: bool,
    login_expired: bool,
    challenge_blocked: bool,
    /// Safe join diagnostics (no token values): msToken/a_bogus flags, room/box.
    diag: String,
}

#[derive(Deserialize, Serialize)]
struct NativeFollowingLiveItem {
    room_id: String,
    web_rid: String,
    user_id: String,
    sec_uid: String,
    nickname: String,
    avatar_url: String,
    title: String,
    viewer_count: String,
}

#[derive(Deserialize)]
struct NativeFollowingLivePageResult {
    status_code: i64,
    status_msg: String,
    items: Vec<NativeFollowingLiveItem>,
}

impl NativePageParticipationResult {
    fn context_missing(message: impl Into<String>) -> Self {
        Self {
            endpoint: "page".into(),
            http_status: 0,
            body: String::new(),
            error: message.into(),
            attempts: 0,
            context_missing: true,
            login_expired: false,
            challenge_blocked: false,
            diag: String::new(),
        }
    }

    fn login_expired(message: impl Into<String>) -> Self {
        Self {
            endpoint: "page".into(),
            http_status: 0,
            body: String::new(),
            error: message.into(),
            attempts: 0,
            context_missing: false,
            login_expired: true,
            challenge_blocked: false,
            diag: String::new(),
        }
    }
}

const DOUYIN_CHROME_USER_AGENT: &str = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36";

// Capture real page-signed luckybox traffic (join/rush/receive). Receive is the
// personal draw-result path; join/rush capture is diagnostic only, since native
// participation issues its own pure-API join and never synthesizes a DOM click.
const RED_PACKET_RECEIVE_CAPTURE_SCRIPT: &str = r#"(() => {
  // Re-install after SPA navigations so fetch/XHR hooks stay outside bdms.
  // Queues are preserved on window so a re-hook does not drop recent joins.
  const receiveQueue = window.__fubaoReceiveQueue || (window.__fubaoReceiveQueue = []);
  const actionQueue = window.__fubaoActionQueue || (window.__fubaoActionQueue = []);
  const scalar = (value) => value === undefined || value === null ? '' : String(value);
  const idsFromURL = (url) => {
    try {
      const parsed = new URL(String(url || ''), location.href);
      return [parsed.searchParams.get('box_id'), parsed.searchParams.get('activity_id'),
        parsed.searchParams.get('luckybox_id'), parsed.searchParams.get('red_packet_id')]
        .map(scalar).map((value) => value.trim()).filter(Boolean);
    } catch (_) { return []; }
  };
  const idsFor = (item) => {
    if (!item || typeof item !== 'object') return [];
    return [item.box_id_str, item.boxIdStr, item.box_id, item.boxId,
      item.activity_id, item.activityId, item.red_packet_id,
      item.redPacketId, item.lottery_id, item.lotteryId]
      .map(scalar).map((value) => value.trim()).filter(Boolean);
  };
  const safeInfo = (item) => {
    if (!item || typeof item !== 'object') return null;
    const result = {};
    for (const key of ['succeed', 'success', 'box_id', 'box_id_str', 'boxId', 'boxIdStr',
      'activity_id', 'activityId', 'red_packet_id', 'redPacketId', 'lottery_id', 'lotteryId',
      'box_type', 'boxType', 'diamond_count', 'cash_count', 'diamond', 'amount',
      'gift_name', 'giftName', 'gift_count', 'giftCount', 'gift_num', 'giftNum', 'count',
      'prize_name', 'prizeName', 'reward_name', 'rewardName', 'hit_bonus', 'hitBonus',
      'can_rush_gem', 'canRushGem', 'rush_too_much', 'rush_spam']) {
      if (Object.prototype.hasOwnProperty.call(item, key)) result[key] = item[key];
    }
    return result;
  };
  const rememberReceive = (url, status, text) => {
    try {
      if (!/\/webcast\/luckybox\/receive(?:\/|$|\?)/.test(String(url || ''))) return;
      const parsed = JSON.parse(String(text || ''));
      const infos = parsed && parsed.data && parsed.data.receive_info;
      if (!Array.isArray(infos) || !infos.length) return;
      const reduced = infos.map(safeInfo).filter(Boolean);
      if (!reduced.length) return;
      receiveQueue.push({status: Number(status || 200), ids: reduced.flatMap(idsFor), count: reduced.length,
        body: JSON.stringify({status_code: parsed.status_code, status_msg: parsed.status_msg,
          data: {receive_info: reduced}})});
      while (receiveQueue.length > 24) receiveQueue.shift();
    } catch (_) {}
  };
  // Live identity sniffed from real page traffic (succeed:true captures always
  // carry room_id + sec_anchor_id + user_unique_id from the SPA session).
  const liveIdentity = window.__fubaoLiveIdentity || (window.__fubaoLiveIdentity = {});
  const rememberLiveIdentity = (url, bodyText) => {
    try {
      const path = String(url || '');
      if (!/live\.douyin\.com\/webcast\//.test(path) && !/\/webcast\//.test(path)) return;
      const parsed = new URL(path, location.href);
      const take = (key, value, minLen) => {
        const text = String(value == null ? '' : value).trim();
        if (!text) return;
        if (minLen && text.length < minLen) return;
        liveIdentity[key] = text;
        liveIdentity.saved_at = Date.now();
      };
      const takeParam = (key, minLen) => take(key, parsed.searchParams.get(key), minLen);
      takeParam('room_id', 10);
      takeParam('anchor_id', 5);
      takeParam('anchor_id_str', 5);
      takeParam('sec_anchor_id', 10);
      takeParam('user_id', 5);
      takeParam('sec_user_id', 10);
      takeParam('user_unique_id', 5);
      takeParam('msToken', 8);
      // room/web/enter body shape (matches Go ProbeLive):
      // data.enter_room_id + data.data[0].id_str (array), not data.room.
      if (bodyText && /\/webcast\/(?:room\/web\/enter|room\/enter|im\/fetch)/.test(path)) {
        try {
          const payload = JSON.parse(String(bodyText));
          const data = payload && payload.data && typeof payload.data === 'object' ? payload.data : payload;
          take('room_id', data && data.enter_room_id, 10);
          let room = null;
          if (data && Array.isArray(data.data) && data.data[0] && typeof data.data[0] === 'object') {
            room = data.data[0];
          } else if (data && data.room && typeof data.room === 'object') {
            room = data.room;
          }
          if (room) {
            take('room_id', room.id_str || room.id || room.room_id, 10);
            const owner = room.owner || room.anchor || {};
            take('anchor_id', owner.id_str || owner.id, 5);
            take('anchor_id_str', owner.id_str || owner.id, 5);
            take('sec_anchor_id', owner.sec_uid || owner.sec_user_id, 10);
          }
          if (data && data.user && typeof data.user === 'object') {
            take('user_id', data.user.id_str || data.user.id, 5);
            take('user_unique_id', data.user_unique_id || data.user.user_unique_id, 5);
          }
          take('user_unique_id', data && data.user_unique_id, 5);
        } catch (_) {}
      }
    } catch (_) {}
  };
  // Always read window.__fubaoLiveIdentity — never a closed-over snapshot that
  // can diverge after enter-probe writes a refreshed object.
  window.__fubaoGetLiveIdentity = () => Object.assign({}, window.__fubaoLiveIdentity || liveIdentity);
  const rememberAction = (url, status, text) => {
    try {
      rememberLiveIdentity(url);
      const path = String(url || '');
      let endpoint = '';
      if (/\/webcast\/luckybox\/join(?:\/|$|\?)/.test(path)) endpoint = 'join';
      else if (/\/webcast\/luckybox\/rush(?:\/|$|\?)/.test(path)) endpoint = 'rush';
      else return;
      const parsed = JSON.parse(String(text || ''));
      const data = parsed && parsed.data && typeof parsed.data === 'object' ? parsed.data : {};
      const reduced = safeInfo(data) || {};
      const ids = idsFromURL(path).concat(idsFor(data));
      actionQueue.push({
        endpoint, status: Number(status || 200), ids: Array.from(new Set(ids.filter(Boolean))),
        body: JSON.stringify({status_code: parsed.status_code, status_msg: parsed.status_msg, data: reduced}),
        saved_at: Date.now()
      });
      while (actionQueue.length > 24) actionQueue.shift();
    } catch (_) {}
  };
  // Always rebind take/clear helpers so already-mounted WebViews pick up fixes
  // without requiring a full page reload (hooks themselves stay one-shot).
  window.__fubaoTakeRedPacketReceiveResult = (boxId, packetId) => {
    const wanted = new Set([scalar(boxId).trim(), scalar(packetId).trim()].filter(Boolean));
    let index = -1;
    for (let i = receiveQueue.length - 1; i >= 0; i -= 1) {
      if (receiveQueue[i].ids.some((id) => wanted.has(id))) { index = i; break; }
    }
    if (index < 0 && receiveQueue.length === 1) index = 0;
    return index < 0 ? null : receiveQueue.splice(index, 1)[0] || null;
  };
  // afterTs: only accept captures saved at/after this time (ms). Used so the
  // page-click fallback never re-accepts our own earlier synthetic soft-denies.
  // preferSuccess: when several post-click items match, pick succeed:true first.
  window.__fubaoTakeRedPacketJoinResult = (boxId, packetId, maxAgeMs, afterTs, preferSuccess) => {
    const wanted = new Set([scalar(boxId).trim(), scalar(packetId).trim()].filter(Boolean));
    const cutoff = Date.now() - Math.max(500, Number(maxAgeMs || 3500));
    const minTs = Math.max(cutoff, Number(afterTs || 0) || 0);
    let bestIndex = -1;
    let bestScore = -1;
    for (let i = actionQueue.length - 1; i >= 0; i -= 1) {
      const item = actionQueue[i];
      if (!item || Number(item.saved_at || 0) < minTs) continue;
      if (wanted.size > 0 && !item.ids.some((id) => wanted.has(id))) continue;
      let score = 10 + Math.min(9, i); // newer slightly preferred among equals
      if (preferSuccess) {
        try {
          const parsed = JSON.parse(String(item.body || ''));
          const data = parsed && parsed.data && typeof parsed.data === 'object' ? parsed.data : {};
          if (data.succeed === true || data.succeeded === true || data.joined === true ||
            data.has_joined === true || data.rush_too_much) score = 1000 + i;
          else if (data.succeed === false) score = 20 + i;
        } catch (_) {}
      }
      if (score > bestScore) {
        bestScore = score;
        bestIndex = i;
      }
    }
    return bestIndex < 0 ? null : actionQueue.splice(bestIndex, 1)[0] || null;
  };
  window.__fubaoClearRedPacketJoinResults = (boxId, packetId) => {
    const wanted = new Set([scalar(boxId).trim(), scalar(packetId).trim()].filter(Boolean));
    if (!wanted.size) {
      actionQueue.length = 0;
      return;
    }
    for (let i = actionQueue.length - 1; i >= 0; i -= 1) {
      const item = actionQueue[i];
      if (!item || !Array.isArray(item.ids)) continue;
      if (item.ids.some((id) => wanted.has(id))) actionQueue.splice(i, 1);
    }
  };
  // Stash remember* on window so a later rebind can keep one hook implementation.
  window.__fubaoRememberReceive = rememberReceive;
  window.__fubaoRememberAction = rememberAction;
  // Install (or re-install) fetch capture so the base is the *current* window.fetch
  // (ideally already wrapped by bdms). Freezing originalFetch to the first native
  // fetch permanently caused join to skip a_bogus injection → succeed=false.
  window.__fubaoEnsureFetchHook = () => {
    try {
      if (typeof window.fetch !== 'function') return;
      // Already outermost and base is set — still refresh base if a newer wrapper
      // sits under us is impossible; instead re-wrap only when not our hook.
      if (window.fetch && window.fetch.__fubaoHook) return;
      const base = window.fetch.bind(window);
      const hook = function(input, init) {
        const request = input && typeof input === 'object' ? input : null;
        const url = scalar(request && request.url || input);
        try { rememberLiveIdentity(url); } catch (_) {}
        return base(input, init).then((response) => {
          try { rememberLiveIdentity(response && response.url || url); } catch (_) {}
          const finalURL = String(response && response.url || url);
          const needBody =
            /\/webcast\/luckybox\/(?:receive|join|rush)(?:\/|$|\?)/.test(url) ||
            /\/webcast\/luckybox\/(?:receive|join|rush)(?:\/|$|\?)/.test(finalURL) ||
            /\/webcast\/(?:room\/web\/enter|room\/enter|im\/fetch)/.test(url) ||
            /\/webcast\/(?:room\/web\/enter|room\/enter|im\/fetch)/.test(finalURL);
          if (needBody) {
            try {
              response.clone().text().then((text) => {
                try { rememberLiveIdentity(finalURL, text); } catch (_) {}
                try { window.__fubaoRememberReceive && window.__fubaoRememberReceive(finalURL, response.status, text); } catch (_) {}
                try { window.__fubaoRememberAction && window.__fubaoRememberAction(finalURL, response.status, text); } catch (_) {}
              });
            } catch (_) {}
          }
          return response;
        });
      };
      hook.__fubaoHook = true;
      window.__fubaoFetchBase = base;
      window.fetch = hook;
      window.__fubaoFetchHooked = true;
    } catch (_) {}
  };
  if (window.__fubaoRedPacketReceiveCaptureInstalled) {
    try { window.__fubaoEnsureFetchHook(); } catch (_) {}
    return;
  }
  window.__fubaoRedPacketReceiveCaptureInstalled = true;
  try {
    window.__fubaoEnsureFetchHook();
  } catch (_) {}
  try {
    const XHR = window.XMLHttpRequest;
    if (XHR && XHR.prototype && !window.__fubaoXhrHooked) {
      window.__fubaoXhrHooked = true;
      const open = XHR.prototype.open;
      const send = XHR.prototype.send;
      XHR.prototype.open = function(method, url) {
        this.__fubaoLuckyboxURL = scalar(url);
        return open.apply(this, arguments);
      };
      XHR.prototype.send = function() {
        try { rememberLiveIdentity(this.__fubaoLuckyboxURL); } catch (_) {}
        this.addEventListener('load', () => {
          const url = scalar(this.__fubaoLuckyboxURL || this.responseURL);
          let body = '';
          try { body = this.responseText || ''; } catch (_) {}
          try { rememberLiveIdentity(url, body); } catch (_) {}
          if (/\/webcast\/luckybox\/(?:receive|join|rush)(?:\/|$|\?)/.test(url)) {
            try { window.__fubaoRememberReceive && window.__fubaoRememberReceive(url, this.status, body); } catch (_) {}
            try { window.__fubaoRememberAction && window.__fubaoRememberAction(url, this.status, body); } catch (_) {}
          }
        });
        return send.apply(this, arguments);
      };
    }
  } catch (_) {}
})();"#;
const DOUYIN_LOGIN_COOKIE_NAMES: [&str; 9] = [
    "sessionid_ss",
    "sessionid",
    "sid_guard",
    "sid_tt",
    "sid_ucp_v1",
    "ssid_ucp_v1",
    "uid_tt",
    "uid_tt_ss",
    "passport_assist_user",
];

fn safe_window_label_part(value: &str) -> String {
    value.replace(|character: char| !character.is_ascii_alphanumeric(), "-")
}

fn browser_webview_prefix(instance_id: &str) -> String {
    format!("browser-{}--", safe_window_label_part(instance_id))
}

fn browser_webview_label(instance_id: &str, parent_label: &str) -> String {
    format!(
        "{}{}",
        browser_webview_prefix(instance_id),
        safe_window_label_part(parent_label)
    )
}

fn browser_webview_is_closing(runtime: &EngineRuntime, label: &str) -> bool {
    runtime
        .browser_webviews_closing
        .lock()
        .map(|labels| labels.contains(label))
        .unwrap_or(true)
}

fn begin_browser_webview_close(runtime: &EngineRuntime, label: &str) -> Result<bool, String> {
    runtime
        .browser_webviews_closing
        .lock()
        .map_err(|_| "浏览器实例销毁状态锁不可用".to_string())
        .map(|mut labels| labels.insert(label.to_string()))
}

fn browser_webviews_for_instance(
    app: &tauri::AppHandle,
    runtime: &EngineRuntime,
    instance_id: &str,
) -> Vec<tauri::Webview> {
    let prefix = browser_webview_prefix(instance_id);
    app.webviews()
        .into_values()
        .filter(|webview| {
            webview.label().starts_with(&prefix)
                && !browser_webview_is_closing(runtime, webview.label())
        })
        .collect()
}

fn browser_webview_for_instance(
    app: &tauri::AppHandle,
    runtime: &EngineRuntime,
    instance_id: &str,
) -> Option<tauri::Webview> {
    browser_webviews_for_instance(app, runtime, instance_id)
        .into_iter()
        .next()
}

fn browser_data_store_identifier(store_key: &str) -> [u8; 16] {
    let mut first = DefaultHasher::new();
    "fubao-browser-primary".hash(&mut first);
    store_key.hash(&mut first);
    let mut second = DefaultHasher::new();
    "fubao-browser-secondary".hash(&mut second);
    store_key.hash(&mut second);
    let mut result = [0_u8; 16];
    result[..8].copy_from_slice(&first.finish().to_be_bytes());
    result[8..].copy_from_slice(&second.finish().to_be_bytes());
    result
}

/// Resolve the native WebView data-store key for an account. Prefer the
/// scan-login profile key (`create-{session}`) when present so instances share
/// the live session established during 扫码登录并添加.
fn account_browser_store_key(account_id: &str, browser_profile_key: &str) -> String {
    let profile = browser_profile_key.trim();
    if !profile.is_empty() {
        profile.to_string()
    } else {
        account_id.trim().to_string()
    }
}

fn rebind_webview_label(account_id: &str, parent_label: &str) -> String {
    format!(
        "account-rebind-{}--{}",
        safe_window_label_part(account_id),
        safe_window_label_part(parent_label)
    )
}

fn create_account_webview_label(session_id: &str, parent_label: &str) -> String {
    format!(
        "account-create-{}--{}",
        safe_window_label_part(session_id),
        safe_window_label_part(parent_label)
    )
}

fn rebind_data_store_identifier(account_id: &str) -> [u8; 16] {
    // Rebinding and the account's browser instance deliberately share one
    // native WebKit data store. Different accounts remain fully isolated.
    browser_data_store_identifier(account_id)
}

fn inject_douyin_cookie(webview: &tauri::Webview, raw_cookie: &str) -> Result<(), String> {
    // WebView2 CookieManager treats a leading-dot domain as "include subdomains"
    // and is far more reliable after a first-party context exists. macOS WKWebView
    // accepts both forms; always write the parent domain with a leading dot.
    // On Windows also mirror host-scoped rows so SPA navigations to www/live
    // still see login tokens when the parent jar lags.
    #[cfg(target_os = "windows")]
    const DOMAINS: [&str; 3] = [".douyin.com", "www.douyin.com", "live.douyin.com"];
    // macOS: only the parent domain. Host-scoped www/live rows can shadow the
    // parent session and leave scan-login instance cards on the login wall.
    #[cfg(not(target_os = "windows"))]
    const DOMAINS: [&str; 1] = [".douyin.com"];
    let max_age = cookie::time::Duration::days(180);
    let mut wrote_any = false;
    let mut last_error = String::new();
    for item in raw_cookie.split(';') {
        let Some((name, value)) = item.trim().split_once('=') else {
            continue;
        };
        let name = name.trim();
        // Keep values as exported; only trim surrounding whitespace. Percent
        // decoding can corrupt signed Douyin login tokens on WebView2.
        let value = value.trim();
        if name.is_empty() {
            continue;
        }
        let is_login = DOUYIN_LOGIN_COOKIE_NAMES.contains(&name);
        for domain in DOMAINS {
            // Windows: CDP-like Secure + SameSite=None + HttpOnly first.
            // macOS: prefer Lax+HttpOnly for login (WKWebView scan-login), then
            // also attempt None so either transport shape can stick.
            let attempts: &[(cookie::SameSite, bool)] = if cfg!(target_os = "windows") {
                if is_login {
                    &[
                        (cookie::SameSite::None, true),
                        (cookie::SameSite::None, false),
                        (cookie::SameSite::Lax, true),
                        (cookie::SameSite::Lax, false),
                    ]
                } else {
                    &[
                        (cookie::SameSite::None, false),
                        (cookie::SameSite::Lax, false),
                        (cookie::SameSite::None, true),
                        (cookie::SameSite::Lax, true),
                    ]
                }
            } else if is_login {
                &[
                    (cookie::SameSite::Lax, true),
                    (cookie::SameSite::None, true),
                    (cookie::SameSite::Lax, false),
                    (cookie::SameSite::None, false),
                ]
            } else {
                &[
                    (cookie::SameSite::Lax, false),
                    (cookie::SameSite::None, false),
                ]
            };
            // macOS login cookies: write every successful SameSite/HttpOnly pair
            // so a single API-accepted but non-functional row cannot stop the
            // working combination from being applied.
            let write_all = !cfg!(target_os = "windows") && is_login;
            let mut domain_wrote = false;
            for (same_site, http_only) in attempts.iter().copied() {
                let mut builder = Cookie::build((name.to_string(), value.to_string()))
                    .domain(domain)
                    .path("/")
                    .secure(true)
                    .same_site(same_site)
                    .max_age(max_age);
                if http_only {
                    builder = builder.http_only(true);
                }
                match webview.set_cookie(builder.build()) {
                    Ok(()) => {
                        wrote_any = true;
                        domain_wrote = true;
                        if !write_all {
                            break;
                        }
                    }
                    Err(error) => last_error = error.to_string(),
                }
            }
            // Parent domain success is enough for non-Windows hosts.
            if domain_wrote && domain == ".douyin.com" && !cfg!(target_os = "windows") {
                break;
            }
        }
    }
    if !wrote_any {
        return Err(if last_error.is_empty() {
            "同步账号 Cookie 失败：没有可写入的 Cookie".into()
        } else {
            format!("同步账号 Cookie 失败：{last_error}")
        });
    }
    Ok(())
}

/// Drop existing Douyin jar rows so a previously anonymous SPA session cannot
/// keep winning over a freshly injected import Cookie (common on WebView2).
async fn clear_douyin_session_cookies(webview: &tauri::Webview) {
    let cookies = match webview.cookies() {
        Ok(cookies) => cookies,
        Err(error) => {
            if cfg!(debug_assertions) {
                eprintln!("[embedded-browser] clear cookies failed: {error}");
            }
            return;
        }
    };
    for cookie in cookies {
        let Some(domain) = cookie.domain() else {
            continue;
        };
        let domain = domain.trim_start_matches('.').to_ascii_lowercase();
        if domain != "douyin.com" && !domain.ends_with(".douyin.com") {
            continue;
        }
        let _ = webview.delete_cookie(cookie);
    }
    tokio::time::sleep(Duration::from_millis(220)).await;
}

/// Seed the account WebView so Douyin's SPA boots with the store Cookie.
///
/// macOS WKWebView accepts cookie writes before the first navigation.
/// Windows WebView2 usually needs a first-party document context first, then
/// cookie write + reload — otherwise the same imported Cookie logs in on Mac
/// but stays anonymous on Windows.
async fn bootstrap_browser_account_session(
    webview: &tauri::Webview,
    runtime: &EngineRuntime,
    instance_id: &str,
    raw_cookie: &str,
    target_url: Url,
) -> Result<(), String> {
    if login_cookie_values(raw_cookie).is_empty() {
        return Err("账号 Cookie 缺少登录凭据，请重新扫码登录".into());
    }

    #[cfg(target_os = "windows")]
    {
        // 1) Open first-party origin so CookieManager has a real site context.
        webview
            .navigate(target_url.clone())
            .map_err(|error| format!("加载抖音页面失败：{error}"))?;
        tokio::time::sleep(Duration::from_millis(1_600)).await;
        // 2) Drop the anonymous SPA session that the first navigation created.
        clear_douyin_session_cookies(webview).await;
        // 3) Write store cookies (CDP-like attributes) and confirm when possible.
        if let Err(error) = inject_douyin_cookie_and_confirm(webview, raw_cookie).await {
            if cfg!(debug_assertions) {
                eprintln!(
                    "[embedded-browser] windows post-context cookie confirm soft-failed instance={instance_id}: {error}"
                );
            }
            let _ = inject_douyin_cookie(webview, raw_cookie);
            tokio::time::sleep(Duration::from_millis(450)).await;
        }
        if let Ok(mut injected) = runtime.browser_cookie_injected_at.lock() {
            injected.insert(instance_id.to_string(), Instant::now());
        }
        // 4) Reload so the SPA boots with the injected session (critical on WebView2).
        webview
            .navigate(target_url.clone())
            .map_err(|error| format!("重新加载已注入登录态的抖音页面失败：{error}"))?;
        tokio::time::sleep(Duration::from_millis(1_000)).await;
        // 5) Re-assert store cookies after SPA init without wiping the jar first.
        let _ = inject_douyin_cookie(webview, raw_cookie);
        tokio::time::sleep(Duration::from_millis(280)).await;
        if inject_douyin_cookie_and_confirm(webview, raw_cookie)
            .await
            .is_ok()
        {
            if let Ok(mut injected) = runtime.browser_cookie_injected_at.lock() {
                injected.insert(instance_id.to_string(), Instant::now());
            }
        } else if cfg!(debug_assertions) {
            eprintln!(
                "[embedded-browser] windows final cookie confirm failed instance={instance_id}"
            );
        }
        // 6) Soft re-write after confirm so late SPA cookies cannot shadow login.
        let _ = inject_douyin_cookie(webview, raw_cookie);
        tokio::time::sleep(Duration::from_millis(200)).await;
    }

    #[cfg(not(target_os = "windows"))]
    {
        // macOS WKWebView: set_cookie is far more reliable once a first-party
        // Douyin document exists. Scan-login cards previously failed when we
        // only pre-seeded from about:blank and then let the anonymous SPA win.
        // Sequence: seed → open Douyin → re-assert → hard reload with cookies.
        //
        // A recreated instance reuses the account-keyed data store, so the jar
        // can still hold the previous anonymous session whose host-scoped rows
        // outrank the parent-domain rows written below. Clear only when the jar
        // cannot already prove this store session — clearing unconditionally
        // wiped valid scan-login sessions.
        let stale_jar = read_douyin_cookie(webview).ok().is_some_and(|current| {
            !current.trim().is_empty() && !login_cookie_snapshot_usable(raw_cookie, &current)
        });
        if stale_jar {
            if cfg!(debug_assertions) {
                eprintln!(
                    "[embedded-browser] macOS clearing residual jar instance={instance_id}"
                );
            }
            clear_douyin_session_cookies(webview).await;
        }
        let _ = inject_douyin_cookie(webview, raw_cookie);
        webview
            .navigate(target_url.clone())
            .map_err(|error| format!("加载抖音页面失败：{error}"))?;
        tokio::time::sleep(Duration::from_millis(1_000)).await;
        if let Err(error) = inject_douyin_cookie_and_confirm(webview, raw_cookie).await {
            if cfg!(debug_assertions) {
                eprintln!(
                    "[embedded-browser] macOS post-nav cookie confirm soft-failed instance={instance_id}: {error}"
                );
            }
            let _ = inject_douyin_cookie(webview, raw_cookie);
            tokio::time::sleep(Duration::from_millis(250)).await;
        }
        if let Ok(mut injected) = runtime.browser_cookie_injected_at.lock() {
            injected.insert(instance_id.to_string(), Instant::now());
        }
        // Hard reload so the SPA boots with the jar we just wrote (critical for
        // scan-login accounts whose create WebView used a different data store).
        // A second full www.douyin.com load dominates the mount cost, so skip it
        // when the first load already rendered an authenticated document.
        if !douyin_render_is_authenticated(webview).await {
            webview
                .navigate(target_url.clone())
                .map_err(|error| format!("重新加载已注入登录态的抖音页面失败：{error}"))?;
            tokio::time::sleep(Duration::from_millis(900)).await;
            let _ = inject_douyin_cookie(webview, raw_cookie);
            tokio::time::sleep(Duration::from_millis(220)).await;
            if inject_douyin_cookie_and_confirm(webview, raw_cookie)
                .await
                .is_ok()
            {
                if let Ok(mut injected) = runtime.browser_cookie_injected_at.lock() {
                    injected.insert(instance_id.to_string(), Instant::now());
                }
            } else if cfg!(debug_assertions) {
                eprintln!(
                    "[embedded-browser] macOS final cookie confirm failed instance={instance_id}"
                );
            }
            let _ = inject_douyin_cookie(webview, raw_cookie);
        } else if cfg!(debug_assertions) {
            eprintln!(
                "[embedded-browser] macOS first load authenticated, skipping reload instance={instance_id}"
            );
        }
        // If a login wall is still up, or Douyin rendered its anonymous shell
        // before the cookies landed, clear SPA storage (not the cookie jar) and
        // hard-reload with store cookies. Douyin often keeps an anonymous shell
        // in localStorage even after HttpOnly session rows are present.
        let stalled_render = inspect_douyin_login(webview).await.ok().is_some_and(|s| {
            matches!(s.state, BrowserLoginState::LoggedOut) || s.anonymous_render
        });
        if stalled_render {
            if cfg!(debug_assertions) {
                eprintln!(
                    "[embedded-browser] macOS login-wall recovery clear-storage+reload instance={instance_id}"
                );
            }
            let _ = webview.eval(
                r#"(() => {
                  try { localStorage.clear(); } catch (_) {}
                  try { sessionStorage.clear(); } catch (_) {}
                  try {
                    if (window.indexedDB && indexedDB.databases) {
                      indexedDB.databases().then((dbs) => {
                        (dbs || []).forEach((db) => {
                          if (db && db.name) try { indexedDB.deleteDatabase(db.name); } catch (_) {}
                        });
                      });
                    }
                  } catch (_) {}
                })();"#,
            );
            tokio::time::sleep(Duration::from_millis(200)).await;
            let _ = inject_douyin_cookie_and_confirm(webview, raw_cookie).await;
            let _ = inject_douyin_cookie(webview, raw_cookie);
            let _ = webview.navigate(target_url);
            tokio::time::sleep(Duration::from_millis(1_000)).await;
            let _ = inject_douyin_cookie(webview, raw_cookie);
            if let Ok(mut injected) = runtime.browser_cookie_injected_at.lock() {
                injected.insert(instance_id.to_string(), Instant::now());
            }
        }
    }

    if cfg!(debug_assertions) {
        let login_state = inspect_douyin_login(webview)
            .await
            .map(|snapshot| format!("{:?}", snapshot.state))
            .unwrap_or_else(|error| format!("error:{error}"));
        // Log whether the jar actually holds store login cookies — critical for
        // diagnosing scan-login cards that still paint the login wall.
        let jar_login = read_douyin_cookie(webview)
            .ok()
            .map(|jar| {
                let names = login_cookie_values(&jar)
                    .keys()
                    .cloned()
                    .collect::<Vec<_>>();
                format!("jar_login_names={}", names.join(","))
            })
            .unwrap_or_else(|| "jar_login_names=".into());
        eprintln!(
            "[embedded-browser] session-bootstrap done instance={instance_id} state={login_state} {jar_login}"
        );
    }
    Ok(())
}

/// Write a captured Douyin session into the account-keyed native data store
/// used by browser instances. Scan-login uses a temporary `create-{session}`
/// store; without this seed step the instance opens an empty store and must
/// re-inject cookies, which WKWebView often fails to apply as a live session.
async fn seed_account_browser_data_store(
    app: &tauri::AppHandle,
    window: &tauri::Window,
    runtime: &EngineRuntime,
    account_id: &str,
    raw_cookie: &str,
) -> Result<(), String> {
    let account_id = account_id.trim();
    if account_id.is_empty() {
        return Err("账号标识无效".into());
    }
    if login_cookie_values(raw_cookie).is_empty() {
        return Err("账号 Cookie 缺少登录凭据".into());
    }
    let label = format!(
        "account-seed-{}--{}",
        safe_window_label_part(account_id),
        safe_window_label_part(window.label())
    );
    if let Some(existing) = app.get_webview(&label) {
        let _ = existing.close();
        tokio::time::sleep(Duration::from_millis(250)).await;
    }
    let blank_url = "about:blank"
        .parse::<Url>()
        .map_err(|error| format!("初始化账号数据目录失败：{error}"))?;
    let mut builder = WebviewBuilder::new(label.clone(), WebviewUrl::External(blank_url))
        .focused(false)
        .accept_first_mouse(false)
        .devtools(false)
        .user_agent(DOUYIN_CHROME_USER_AGENT)
        .on_navigation(|url| {
            url.scheme() == "about"
                || url
                    .domain()
                    .is_some_and(|domain| domain == "douyin.com" || domain.ends_with(".douyin.com"))
        });
    #[cfg(any(target_os = "macos", target_os = "ios"))]
    {
        builder = builder.data_store_identifier(browser_data_store_identifier(account_id));
    }
    #[cfg(not(any(target_os = "macos", target_os = "ios")))]
    {
        let data_dir = embedded_browser_data_dir(app, account_id)?;
        std::fs::create_dir_all(&data_dir)
            .map_err(|error| format!("创建账号浏览器数据目录失败：{error}"))?;
        // Wipe residual anonymous WebView2 profile so inject starts clean.
        if data_dir.exists() {
            let _ = std::fs::remove_dir_all(&data_dir);
            let _ = std::fs::create_dir_all(&data_dir);
        }
        builder = builder.data_directory(data_dir);
    }
    let webview = window
        .add_child(
            builder,
            LogicalPosition::new(-12_000.0, -12_000.0),
            LogicalSize::new(420.0, 320.0),
        )
        .map_err(|error| format!("创建账号登录数据目录失败：{error}"))?;
    let _ = webview.hide();
    let target = "https://www.douyin.com/"
        .parse::<Url>()
        .map_err(|error| format!("解析抖音地址失败：{error}"))?;
    let seed_id = format!("seed-{account_id}");
    let bootstrap = bootstrap_browser_account_session(
        &webview,
        runtime,
        &seed_id,
        raw_cookie,
        target,
    )
    .await;
    let login_state = inspect_douyin_login(&webview)
        .await
        .map(|snapshot| format!("{:?}", snapshot.state))
        .unwrap_or_else(|error| format!("error:{error}"));
    if cfg!(debug_assertions) {
        eprintln!(
            "[embedded-browser] account-store seed account={account_id} state={login_state} bootstrap_ok={}",
            bootstrap.is_ok()
        );
    }
    let _ = webview.hide();
    let _ = webview.close();
    // Prefer a successful bootstrap, but do not fail account creation when the
    // seed WebView only soft-failed — Go still has the Cookie for remount.
    bootstrap
}

fn login_cookie_snapshot_matches(expected: &str, actual: &str) -> bool {
    let expected = login_cookie_values(expected);
    let actual = login_cookie_values(actual);
    !expected.is_empty()
        && expected
            .iter()
            .all(|(name, value)| actual.get(name).is_some_and(|current| current == value))
}

fn login_cookie_snapshot_usable(expected: &str, actual: &str) -> bool {
    if login_cookie_snapshot_matches(expected, actual) {
        return true;
    }
    // WebView2 often surfaces only a subset of login cookies immediately after
    // set_cookie. One matching primary login cookie is enough to proceed.
    let expected = login_cookie_values(expected);
    let actual = login_cookie_values(actual);
    if actual.is_empty() {
        return false;
    }
    expected.iter().any(|(name, value)| {
        actual
            .get(name)
            .is_some_and(|current| current == value && !current.trim().is_empty())
    })
}

async fn inject_douyin_cookie_and_confirm(
    webview: &tauri::Webview,
    raw_cookie: &str,
) -> Result<(), String> {
    if login_cookie_values(raw_cookie).is_empty() {
        return Err("账号 Cookie 缺少登录凭据，请重新扫码登录".into());
    }
    let attempts = if cfg!(target_os = "windows") { 8 } else { 6 };
    let mut last_error = "等待浏览器接收账号 Cookie".to_string();
    for attempt in 0..attempts {
        let _ = inject_douyin_cookie(webview, raw_cookie);
        let wait_ms = if cfg!(target_os = "windows") { 220 } else { 160 };
        tokio::time::sleep(Duration::from_millis(wait_ms)).await;
        match read_douyin_cookie(webview) {
            Ok(actual) if login_cookie_snapshot_usable(raw_cookie, &actual) => return Ok(()),
            Ok(_) => last_error = "浏览器尚未写入完整登录 Cookie".into(),
            Err(error) => last_error = error,
        }
        if attempt + 1 < attempts {
            tokio::time::sleep(Duration::from_millis(if cfg!(target_os = "windows") {
                180
            } else {
                140
            }))
            .await;
        }
    }
    Err(format!("同步账号 Cookie 失败：{last_error}"))
}

fn read_douyin_cookie(webview: &tauri::Webview) -> Result<String, String> {
    let mut values = HashMap::<String, (u8, String)>::new();
    // Wry's macOS `cookies_for_url` currently compares cookie domains using
    // exact equality. Read the native store and reproduce the cookies that
    // apply to https://www.douyin.com/ ourselves. Cookies scoped only to a
    // sibling such as live.douyin.com must not overwrite a same-named parent
    // login cookie in the flat canonical account header.
    for cookie in webview
        .cookies()
        .map_err(|error| format!("读取登录 Cookie 失败：{error}"))?
        .into_iter()
    {
        let Some(domain) = cookie.domain() else {
            continue;
        };
        let domain = domain.trim_start_matches('.').to_ascii_lowercase();
        // Include live.douyin.com so Windows reads still see login cookies that
        // WebView2 only exposes under the live host after a room navigation.
        let priority = match domain.as_str() {
            "douyin.com" => 3,
            "www.douyin.com" => 2,
            "live.douyin.com" => 1,
            _ => continue,
        };
        if cookie.name() == "fubao_login_probe"
            || cookie.name().starts_with("fubao_participation_probe_")
            || cookie.name().starts_with("fubao_following_live_")
        {
            continue;
        }
        let entry = values
            .entry(cookie.name().to_string())
            .or_insert_with(|| (priority, cookie.value().to_string()));
        if priority > entry.0 {
            *entry = (priority, cookie.value().to_string());
        }
    }
    // Douyin does not use one stable login-cookie name across all desktop
    // rollouts. The current web client commonly persists the authenticated
    // session under the sid/uid UCP variants instead of sessionid_ss.
    let logged_in = DOUYIN_LOGIN_COOKIE_NAMES.iter().any(|name| {
        values
            .get(*name)
            .is_some_and(|(_, value)| !value.trim().is_empty())
    });
    if !logged_in {
        if cfg!(debug_assertions) {
            let mut cookie_names = values.keys().cloned().collect::<Vec<_>>();
            cookie_names.sort();
            // Names are useful for compatibility diagnostics; values remain
            // native-only and must never be logged or returned to the UI.
            eprintln!(
                "[embedded-browser] no-login-cookie names={}",
                cookie_names.join(",")
            );
        }
        return Err("尚未检测到登录状态，请先在抖音窗口完成登录".into());
    }
    let mut pairs = values.into_iter().collect::<Vec<_>>();
    pairs.sort_by(|left, right| left.0.cmp(&right.0));
    Ok(pairs
        .into_iter()
        .map(|(name, (_, value))| format!("{name}={value}"))
        .collect::<Vec<_>>()
        .join("; "))
}

fn canonical_cookie_values(raw_cookie: &str) -> HashMap<String, String> {
    raw_cookie
        .split(';')
        .filter_map(|item| item.trim().split_once('='))
        .filter_map(|(name, value)| {
            let name = name.trim();
            if name.is_empty()
                || name == "fubao_login_probe"
                || name.starts_with("fubao_participation_probe_")
                || name.starts_with("fubao_following_live_")
            {
                return None;
            }
            Some((name.to_string(), value.trim().to_string()))
        })
        .collect()
}

fn login_cookie_values(raw_cookie: &str) -> HashMap<String, String> {
    canonical_cookie_values(raw_cookie)
        .into_iter()
        .filter(|(name, _)| DOUYIN_LOGIN_COOKIE_NAMES.contains(&name.as_str()))
        .collect()
}

#[cfg(test)]
mod cookie_tests {
    use super::{
        begin_browser_webview_close, browser_location_matches_live_room,
        browser_login_cookie_is_safe_to_persist, browser_new_window_target,
        browser_restore_location, browser_webview_is_closing, canonical_cookie_values,
        login_cookie_snapshot_matches, login_cookie_values, remember_browser_instance_account,
        remember_browser_location, EngineRuntime, Url,
    };

    #[test]
    fn complete_cookie_comparison_ignores_order_but_detects_auxiliary_changes() {
        let stored = canonical_cookie_values("sessionid_ss=session; ttwid=old; sid_guard=guard");
        let reordered = canonical_cookie_values("sid_guard=guard; sessionid_ss=session; ttwid=old");
        let rotated = canonical_cookie_values("sid_guard=guard; sessionid_ss=session; ttwid=new");
        let with_native_probe = canonical_cookie_values(
            "sid_guard=guard; sessionid_ss=session; ttwid=old; fubao_following_live_test_0=7b7d",
        );

        assert_eq!(stored, reordered);
        assert_eq!(stored, with_native_probe);
        assert_ne!(stored, rotated);
        assert_eq!(
            login_cookie_values("sessionid_ss=session; ttwid=old"),
            login_cookie_values("sessionid_ss=session; ttwid=new")
        );
    }

    #[test]
    fn partial_browser_snapshot_must_not_replace_store_login_cookies() {
        let store = "sessionid_ss=full; sid_guard=guard; uid_tt=uid; ttwid=aux";
        let partial = "sessionid_ss=full; ttwid=aux";
        let complete = "sessionid_ss=rotated; sid_guard=guard; uid_tt=uid; ttwid=new";
        assert!(!browser_login_cookie_is_safe_to_persist(store, partial));
        assert!(browser_login_cookie_is_safe_to_persist(store, complete));
        assert!(!browser_login_cookie_is_safe_to_persist(store, ""));
    }

    #[test]
    fn injected_profile_requires_every_login_cookie_to_match() {
        let expected = "sessionid_ss=new-session; sid_guard=new-guard; ttwid=aux";
        assert!(login_cookie_snapshot_matches(
            expected,
            "sid_guard=new-guard; sessionid_ss=new-session; ttwid=other"
        ));
        assert!(!login_cookie_snapshot_matches(
            expected,
            "sid_guard=old-guard; sessionid_ss=new-session"
        ));
        assert!(!login_cookie_snapshot_matches(
            "ttwid=auxiliary-only",
            "ttwid=auxiliary-only"
        ));
    }

    #[test]
    fn browser_close_tombstone_makes_overlapping_close_idempotent() {
        let runtime = EngineRuntime::default();

        assert!(!browser_webview_is_closing(&runtime, "browser-a--main"));
        assert_eq!(
            begin_browser_webview_close(&runtime, "browser-a--main"),
            Ok(true)
        );
        assert!(browser_webview_is_closing(&runtime, "browser-a--main"));
        assert_eq!(
            begin_browser_webview_close(&runtime, "browser-a--main"),
            Ok(false)
        );
        assert_eq!(
            begin_browser_webview_close(&runtime, "browser-b--main"),
            Ok(true)
        );
    }

    #[test]
    fn browser_location_cache_matches_only_the_requested_live_room() {
        let runtime = EngineRuntime::default();
        let room_url = "https://live.douyin.com/123456789?from=fubao"
            .parse::<Url>()
            .expect("test URL must parse");

        remember_browser_location(&runtime, "instance-a", &room_url);

        assert!(browser_location_matches_live_room(
            &runtime,
            "instance-a",
            "123456789"
        ));
        assert!(!browser_location_matches_live_room(
            &runtime,
            "instance-a",
            "987654321"
        ));
        assert!(!browser_location_matches_live_room(
            &runtime,
            "instance-b",
            "123456789"
        ));
    }

    #[test]
    fn recreated_instance_restores_the_account_location_instead_of_the_home_feed() {
        let runtime = EngineRuntime::default();
        let room_url = "https://live.douyin.com/123456789"
            .parse::<Url>()
            .expect("test URL must parse");

        remember_browser_instance_account(&runtime, "instance-old", "account-1");
        remember_browser_location(&runtime, "instance-old", &room_url);

        // Deleting the card and creating it again yields a new instance id.
        remember_browser_instance_account(&runtime, "instance-new", "account-1");
        assert_eq!(
            browser_restore_location(&runtime, "instance-new").as_str(),
            room_url.as_str()
        );

        // A different account must never inherit another account's page.
        remember_browser_instance_account(&runtime, "instance-other", "account-2");
        assert_eq!(
            browser_restore_location(&runtime, "instance-other").as_str(),
            "https://www.douyin.com/"
        );
    }

    #[test]
    fn browser_popup_navigation_stays_on_safe_douyin_https_targets() {
        let live_room = "https://live.douyin.com/123456789?from=homepage"
            .parse::<Url>()
            .expect("live-room URL must parse");
        let douyin_subdomain = "https://www.douyin.com/follow"
            .parse::<Url>()
            .expect("Douyin URL must parse");
        let insecure = "http://live.douyin.com/123456789"
            .parse::<Url>()
            .expect("insecure URL must parse");
        let unrelated = "https://example.com/123456789"
            .parse::<Url>()
            .expect("external URL must parse");

        assert_eq!(browser_new_window_target(&live_room), Some(live_room));
        assert_eq!(
            browser_new_window_target(&douyin_subdomain),
            Some(douyin_subdomain)
        );
        assert_eq!(browser_new_window_target(&insecure), None);
        assert_eq!(browser_new_window_target(&unrelated), None);
    }
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
enum BrowserLoginState {
    LoggedIn,
    LoggedOut,
    Unknown,
}

struct BrowserLoginSnapshot {
    raw_cookie: Option<String>,
    state: BrowserLoginState,
    /// Douyin rendered its anonymous shell (a visible top-bar 登录 entry) even
    /// though the jar may already hold login cookies. That is a stale document
    /// which loaded before the cookies landed, not CK expiry, so it must drive
    /// a bounded reload instead of a credential verdict.
    anonymous_render: bool,
    /// The document is rendered far enough to be meaningful and carries no login
    /// entry. Only this counts as evidence that a session actually took: a page
    /// mid-hydration also has no login entry yet and must stay undetermined.
    clean_render: bool,
    /// Compact render facts (readyState, body length, header, host) behind the
    /// verdict. Undetermined has several causes that need different handling, so
    /// keep them observable without letting them influence any CK decision.
    render_diagnostics: Option<String>,
}

async fn inspect_douyin_login(webview: &tauri::Webview) -> Result<BrowserLoginSnapshot, String> {
    // The browser itself is the source of truth here. A stale Cookie may still
    // exist after Douyin has rejected it, so cookie presence alone must never
    // be interpreted as a valid session. The short-lived probe contains only
    // a state word and never exposes credentials to the frontend.
    webview
        .eval(
            r#"(() => {
              // innerText is layout-dependent: a hidden or zero-sized child
              // WebView returns '' for a fully loaded document. Keep it for the
              // login-wall verdict, where "not rendered" must stay inconclusive
              // rather than becoming CK expiry, and measure readiness against
              // textContent so a card that was ever hidden cannot get stuck
              // permanently undetermined.
              const text = document.body?.innerText || '';
              const documentText = document.body?.textContent || '';
              // Only the dedicated login wall is authoritative. A lone visible
              // “登录” control appears during SPA transitions and secondary
              // prompts; treating it as logout was wiping freshly imported CK
              // after browser-instance creation.
              const explicitDialog = text.includes('登录后免费畅享高清视频') &&
                (text.includes('扫码登录') || text.includes('验证码登录'));
              const loginWall = explicitDialog ||
                (text.includes('扫码登录') && text.includes('验证码登录') &&
                  (text.includes('登录后免费畅享') || text.includes('手机号登录'))) ||
                // Windows Douyin often shows a compact login gate without the
                // full “登录后免费畅享高清视频” copy.
                ((text.includes('扫码登录') || text.includes('验证码登录')) &&
                  (text.includes('登录后即可') || text.includes('登录后观看') ||
                    text.includes('立即登录') || text.includes('短信登录')));
              // Live SPA after navigate often has document.readyState=complete
              // while body is still the previous www feed / empty shell. Prefer
              // hostname-aware readiness so we do not mis-label mid-nav as ready.
              const host = String(location.hostname || '');
              const path = String(location.pathname || '');
              const onLiveHost = host === 'live.douyin.com' || host.endsWith('.live.douyin.com');
              const bodyLen = String(text || '').trim().length;
              const documentLen = String(documentText || '').trim().length;
              const ready = document.readyState !== 'loading' && Boolean(document.body) &&
                (onLiveHost ? documentLen > 40 || path.length > 1 : documentLen > 20);
              const state = loginWall ? 'out' : ready ? 'ready' : 'unknown';
              // A visible login affordance means this document was rendered for
              // an anonymous visitor. Reported separately from the verdict above
              // so it can trigger a reload without touching CK health. Douyin
              // renders its login entry as a plain div with obfuscated classes
              // and no data-e2e hook, so scan divs/spans too and require the
              // element to be on screen; `#login-panel-new` is the stable id of
              // the inline logged-out panel.
              const onScreen = (node) => {
                const rect = node.getBoundingClientRect();
                return rect.width > 8 && rect.height > 8 &&
                  rect.top < window.innerHeight && rect.bottom > 0 &&
                  rect.left < window.innerWidth && rect.right > 0;
              };
              const anonymousShell = (() => {
                try {
                  const panel = document.querySelector('#login-panel-new');
                  if (panel && onScreen(panel)) return true;
                  const nodes = document.querySelectorAll(
                    'button, [role="button"], a, div, span'
                  );
                  let scanned = 0;
                  for (const node of nodes) {
                    if (scanned++ > 4000) break;
                    const label = String(node.textContent || '').trim();
                    if (label !== '登录' && label !== '登录抖音' && label !== '立即登录') continue;
                    if (onScreen(node)) return true;
                  }
                  return false;
                } catch (_) {
                  return false;
                }
              })();
              // “No login entry” only means something while the header that
               // hosts it is both hydrated and actually on screen. The feed body
              // paints before that header, and scrolling the feed moves the
              // login entry out of view — judging either as authenticated is how
              // an anonymous card kept reading as healthy.
              const headerReady = (() => {
                try {
                  const search = document.querySelector('input[placeholder*="搜索"]');
                  return Boolean(search) && onScreen(search);
                } catch (_) {
                  return false;
                }
              })();
              const marker = `${state}${
                anonymousShell ? '-anon' : headerReady && bodyLen > 200 ? '-auth' : ''
              }`;
              document.cookie = `fubao_login_probe=${marker}; Path=/; Secure; SameSite=None; Max-Age=5`;
              // Separate channel so the verdict parsing above stays suffix-based.
              // An undetermined render has several very different causes (skeleton
              // that never paints, mid-navigation document, unreadable jar) and
              // they need different handling, so record which one it was.
              const diag = `rs.${document.readyState}~bl.${bodyLen}~tl.${documentLen}~hdr.${
                headerReady ? 1 : 0
              }~h.${host.replace(/[^a-z.]/g, '')}`;
              document.cookie = `fubao_login_diag=${diag}; Path=/; Secure; SameSite=None; Max-Age=5`;
            })();"#,
        )
        .map_err(|error| format!("检查浏览器登录页面失败：{error}"))?;
    tokio::time::sleep(Duration::from_millis(140)).await;

    let cookies = webview
        .cookies()
        .map_err(|error| format!("读取浏览器登录状态失败：{error}"))?;
    let probe = cookies
        .iter()
        .find(|cookie| cookie.name() == "fubao_login_probe")
        .map(|cookie| cookie.value().to_string());
    let render_diagnostics = cookies
        .iter()
        .find(|cookie| cookie.name() == "fubao_login_diag")
        .map(|cookie| cookie.value().to_string());
    let raw_cookie = read_douyin_cookie(webview).ok();
    let _ = webview.eval(
        "document.cookie='fubao_login_probe=; Path=/; Secure; SameSite=None; Max-Age=0';\
         document.cookie='fubao_login_diag=; Path=/; Secure; SameSite=None; Max-Age=0'",
    );
    // Login wall is LoggedOut even when injected cookies remain in the jar.
    // Windows WebView2 often keeps store tokens after a rejected session; that
    // must still surface as CK expiry outside the bootstrap grace window.
    // When the probe cookie is lost (SameSite/path races after live navigate)
    // but login cookies are readable and no login wall was reported, treat as
    // LoggedIn — otherwise every red-packet prepare toasted “尚未完成加载”.
    let jar_logged_in = raw_cookie.as_ref().is_some_and(|value| !value.trim().is_empty());
    let anonymous_render = probe.as_deref().is_some_and(|value| value.ends_with("-anon"));
    let clean_render = probe.as_deref().is_some_and(|value| value.ends_with("-auth"));
    let probe_state = probe.as_deref().map(|value| {
        value
            .strip_suffix("-anon")
            .or_else(|| value.strip_suffix("-auth"))
            .unwrap_or(value)
    });
    let state = match probe_state {
        Some("out") => BrowserLoginState::LoggedOut,
        Some("ready") if jar_logged_in => BrowserLoginState::LoggedIn,
        Some("ready") => BrowserLoginState::Unknown,
        Some("unknown") if jar_logged_in => BrowserLoginState::Unknown,
        _ if jar_logged_in => BrowserLoginState::LoggedIn,
        _ => BrowserLoginState::Unknown,
    };
    Ok(BrowserLoginSnapshot {
        raw_cookie,
        state,
        anonymous_render,
        clean_render,
        render_diagnostics,
    })
}

/// True only when the current document is both a confirmed login and free of
/// Douyin's anonymous shell. Used to decide whether another full page load is
/// still needed, so a healthy first load does not pay for a redundant reload.
#[cfg(not(target_os = "windows"))]
async fn douyin_render_is_authenticated(webview: &tauri::Webview) -> bool {
    inspect_douyin_login(webview)
        .await
        .ok()
        .is_some_and(|snapshot| {
            matches!(snapshot.state, BrowserLoginState::LoggedIn) && !snapshot.anonymous_render
        })
}

/// Wait after live-room navigation until login is definitive or the deadline.
/// SPA transitions commonly report Unknown for several seconds even when the
/// jar already holds a valid session.
async fn wait_for_douyin_login_ready(
    webview: &tauri::Webview,
    max_wait: Duration,
) -> Result<BrowserLoginSnapshot, String> {
    let deadline = Instant::now() + max_wait;
    let mut last = inspect_douyin_login(webview).await?;
    while Instant::now() < deadline {
        match last.state {
            BrowserLoginState::LoggedIn | BrowserLoginState::LoggedOut => return Ok(last),
            BrowserLoginState::Unknown => {
                tokio::time::sleep(Duration::from_millis(400)).await;
                last = inspect_douyin_login(webview).await?;
            }
        }
    }
    Ok(last)
}

/// Browser snapshots may miss HttpOnly login cookies briefly after inject or
/// SPA navigation. Never overwrite a complete store Cookie with a partial read.
fn browser_login_cookie_is_safe_to_persist(store_cookie: &str, browser_cookie: &str) -> bool {
    let store_login = login_cookie_values(store_cookie);
    let browser_login = login_cookie_values(browser_cookie);
    if browser_login.is_empty() {
        return false;
    }
    if store_login.is_empty() {
        return true;
    }
    // Every store login key must still be present in the browser jar.
    if !store_login.keys().all(|name| {
        browser_login
            .get(name)
            .is_some_and(|value| !value.trim().is_empty())
    }) {
        return false;
    }
    // Never shrink a complete scan-login / import capture. Inject + SPA jars
    // often omit auxiliary rows; writing that snapshot back to Go permanently
    // corrupts the store Cookie and later remounts fail with a login wall.
    if browser_cookie.len() + 128 < store_cookie.len() {
        return false;
    }
    true
}

async fn read_authenticated_douyin_cookie(webview: &tauri::Webview) -> Result<String, String> {
    // WebView2 / WKWebView can publish the navigation/UI state slightly before
    // CookieManager exposes the final HttpOnly login values. Retry the native
    // read so scan-login does not persist a partial session missing sid_*.
    let mut last_error = "尚未检测到登录状态，请先在抖音窗口完成登录".to_string();
    let mut best_cookie = String::new();
    for attempt in 0..12 {
        match inspect_douyin_login(webview).await {
            Ok(BrowserLoginSnapshot {
                raw_cookie: Some(raw_cookie),
                state: BrowserLoginState::LoggedIn,
                ..
            }) => {
                // Prefer the longest snapshot once LoggedIn — later ticks often
                // add auxiliary cookies that the first tick omitted.
                if raw_cookie.len() >= best_cookie.len() {
                    best_cookie = raw_cookie;
                }
                if attempt >= 3 || login_cookie_values(&best_cookie).len() >= 4 {
                    return Ok(best_cookie);
                }
            }
            Ok(BrowserLoginSnapshot {
                state: BrowserLoginState::LoggedOut,
                ..
            }) => {
                if !best_cookie.is_empty() {
                    return Ok(best_cookie);
                }
                return Err("抖音页面仍处于未登录状态，请完成登录后再更新 CK".into());
            }
            Ok(_) => {
                last_error = "登录页面尚未同步完成，请稍后重试".into();
            }
            Err(error) => last_error = error,
        }
        if attempt < 11 {
            tokio::time::sleep(Duration::from_millis(280)).await;
        }
    }
    if !best_cookie.is_empty() {
        return Ok(best_cookie);
    }
    Err(last_error)
}

fn valid_live_room_id(value: &str) -> bool {
    let value = value.trim();
    (6..=20).contains(&value.len()) && value.bytes().all(|byte| byte.is_ascii_digit())
}

fn live_room_url(web_rid: &str) -> Result<Url, String> {
    if !valid_live_room_id(web_rid) {
        return Err("直播间标识无效".into());
    }
    format!("https://live.douyin.com/{}", web_rid.trim())
        .parse::<Url>()
        .map_err(|error| format!("解析直播间地址失败：{error}"))
}

async fn navigate_browser_to_live_room(
    webview: &tauri::Webview,
    runtime: &EngineRuntime,
    instance_id: &str,
    web_rid: &str,
) -> Result<(), String> {
    let target = live_room_url(web_rid)?;
    // Never query WKWebView.url() from task/teardown races. Wry's macOS
    // implementation unwraps the underlying NSURL and aborts the process if
    // WebKit has already invalidated it. Navigation callbacks maintain this
    // safe native cache without touching a closing page object.
    let already_there = browser_location_matches_live_room(runtime, instance_id, web_rid);
    if !already_there {
        webview
            .navigate(target.clone())
            .map_err(|error| format!("切换到直播间失败：{error}"))?;
        remember_browser_location(runtime, instance_id, &target);
    }
    for _ in 0..40 {
        if browser_location_matches_live_room(runtime, instance_id, web_rid) {
            tokio::time::sleep(Duration::from_millis(if already_there {
                500
            } else {
                1_800
            }))
            .await;
            return Ok(());
        }
        tokio::time::sleep(Duration::from_millis(150)).await;
    }
    Err("直播间页面加载超时，请确认直播间仍在开播".into())
}

fn decode_hex_utf8(value: &str) -> Result<String, String> {
    if value.len() % 2 != 0 {
        return Err("原生页面结果编码无效".into());
    }
    let mut bytes = Vec::with_capacity(value.len() / 2);
    for pair in value.as_bytes().chunks_exact(2) {
        let text = std::str::from_utf8(pair).map_err(|_| "原生页面结果编码无效")?;
        bytes.push(u8::from_str_radix(text, 16).map_err(|_| "原生页面结果编码无效")?);
    }
    String::from_utf8(bytes).map_err(|_| "原生页面结果不是有效文本".into())
}

fn clear_page_probe_chunks(webview: &tauri::Webview, prefix: &str, chunk_count: usize) {
    let names = (0..chunk_count)
        .map(|index| format!("{prefix}_{index}"))
        .chain(std::iter::once(format!("{prefix}_meta")))
        .collect::<Vec<_>>();
    let script = format!(
        "for (const name of {}) document.cookie = `${{name}}=; Path=/; Secure; SameSite=None; Max-Age=0`;",
        serde_json::to_string(&names).unwrap_or_else(|_| "[]".into())
    );
    let _ = webview.eval(&script);
}

async fn read_page_probe_chunks(webview: &tauri::Webview, prefix: &str) -> Result<String, String> {
    let meta_name = format!("{prefix}_meta");
    for _ in 0..100 {
        tokio::time::sleep(Duration::from_millis(150)).await;
        let cookies = match webview.cookies() {
            Ok(cookies) => cookies,
            Err(_) => continue,
        };
        let Some(chunk_count) = cookies
            .iter()
            .find(|cookie| cookie.name() == meta_name)
            .and_then(|cookie| cookie.value().parse::<usize>().ok())
        else {
            continue;
        };
        if chunk_count == 0 || chunk_count > 80 {
            clear_page_probe_chunks(webview, prefix, chunk_count.min(80));
            return Err("关注直播页面返回的数据量异常".into());
        }
        let mut encoded = String::new();
        let mut complete = true;
        for index in 0..chunk_count {
            let name = format!("{prefix}_{index}");
            let Some(value) = cookies
                .iter()
                .find(|cookie| cookie.name() == name)
                .map(|cookie| cookie.value())
            else {
                complete = false;
                break;
            };
            encoded.push_str(value);
        }
        if !complete {
            continue;
        }
        clear_page_probe_chunks(webview, prefix, chunk_count);
        return decode_hex_utf8(&encoded);
    }
    Err("等待登录页面返回关注直播数据超时".into())
}

async fn execute_following_live_in_page(
    webview: &tauri::Webview,
) -> Result<NativeFollowingLivePageResult, String> {
    let prefix = format!("fubao_following_live_{}", Uuid::new_v4().simple());
    let prefix_json = serde_json::to_string(&prefix)
        .map_err(|error| format!("准备关注直播页面请求失败：{error}"))?;
    let script = r#"(() => {
      const prefix = __FUBAO_PROBE_PREFIX__;
      const compact = (value, limit = 240) => String(value ?? '').trim().slice(0, limit);
      const finish = (result) => {
        try {
          const text = JSON.stringify(result);
          const bytes = new TextEncoder().encode(text);
          const hex = Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('');
          const chunks = hex.match(/.{1,2200}/g) || ['7b7d'];
          chunks.forEach((chunk, index) => {
            document.cookie = `${prefix}_${index}=${chunk}; Path=/; Secure; SameSite=None; Max-Age=20`;
          });
          document.cookie = `${prefix}_meta=${chunks.length}; Path=/; Secure; SameSite=None; Max-Age=20`;
        } catch (_) {}
      };
      const safeItems = (payload) => {
        const rows = Array.isArray(payload?.data?.data) ? payload.data.data : [];
        const seen = new Set();
        const items = [];
        for (const record of rows.slice(0, 200)) {
          const room = record && typeof record.room === 'object' ? record.room : {};
          const owner = room && typeof room.owner === 'object' ? room.owner : {};
          const roomID = compact(room.id_str || room.id, 32);
          const webRID = compact(record?.web_rid || room.web_rid || owner.web_rid, 32);
          const key = roomID || webRID;
          if (!key || seen.has(key)) continue;
          seen.add(key);
          const avatarList = owner?.avatar_thumb?.url_list;
          items.push({
            room_id: roomID,
            web_rid: webRID,
            user_id: compact(owner.id_str || owner.id, 64),
            sec_uid: compact(owner.sec_uid, 160),
            nickname: compact(owner.nickname, 120),
            avatar_url: compact(Array.isArray(avatarList) ? avatarList[0] : '', 800),
            title: compact(room.title, 240),
            viewer_count: compact(room.user_count_str || room?.stats?.user_count_str, 48),
          });
        }
        return items;
      };
      const unsignedURL = () => {
        const url = new URL('https://www.douyin.com/webcast/web/feed/follow/');
        const values = {
          device_platform: 'webapp', aid: '6383', channel: 'channel_pc_web',
          pc_client_type: '1', version_code: '290100', version_name: '29.1.0',
          cookie_enabled: String(navigator.cookieEnabled),
          screen_width: String(screen.width || 1920), screen_height: String(screen.height || 1080),
          browser_language: navigator.language || 'zh-CN', from_user_page: '1',
          locate_query: 'false', need_time_list: '1', pc_libra_divert: 'Mac',
          publish_video_strategy_type: '2', round_trip_time: '0',
          show_live_replay_strategy: '1', time_list_query: '0',
          update_version_code: '170400', whale_cut_token: '', scene: 'aweme_pc_follow_top'
        };
        for (const [key, value] of Object.entries(values)) url.searchParams.set(key, value);
        return url;
      };
      const requestURLs = () => {
        const urls = [];
        try {
          const entries = performance.getEntriesByType('resource').slice().reverse();
          const matched = entries.find((entry) => {
            try {
              const candidate = new URL(String(entry.name || ''), location.href);
              return candidate.hostname === 'www.douyin.com' && candidate.pathname === '/webcast/web/feed/follow/';
            } catch (_) {
              return false;
            }
          });
          if (matched) {
            const captured = new URL(String(matched.name), location.href);
            urls.push(captured.toString());
            for (const key of ['a_bogus', 'X-Bogus', '__ac_signature', '__ac_nonce']) captured.searchParams.delete(key);
            urls.push(captured.toString());
          }
        } catch (_) {}
        urls.push(unsignedURL().toString());
        return [...new Set(urls)];
      };
      (async () => {
        let last = null;
        try {
          for (const url of requestURLs()) {
            const response = await fetch(url, {
              method: 'GET', credentials: 'include', cache: 'no-store',
              headers: {'accept': 'application/json, text/plain, */*'},
              referrer: 'https://www.douyin.com/follow',
              referrerPolicy: 'strict-origin-when-cross-origin'
            });
            const payload = await response.json();
            const statusCode = Number(payload?.status_code || 0);
            last = {
              status_code: statusCode,
              status_msg: compact(payload?.status_msg || (response.ok ? '' : `HTTP ${response.status}`)),
              items: statusCode === 0 ? safeItems(payload) : []
            };
            if (statusCode === 0) break;
          }
          finish(last || {status_code: -1, status_msg: '关注直播接口没有返回数据', items: []});
        } catch (error) {
          finish({
            status_code: -1,
            status_msg: compact(error && (error.message || error) || '关注直播页面请求失败'),
            items: []
          });
        }
      })();
    })();"#
        .replace("__FUBAO_PROBE_PREFIX__", &prefix_json);
    webview
        .eval(&script)
        .map_err(|error| format!("启动登录页面关注直播请求失败：{error}"))?;
    let encoded = read_page_probe_chunks(webview, &prefix).await?;
    serde_json::from_str(&encoded).map_err(|error| format!("解析关注直播页面结果失败：{error}"))
}

async fn execute_page_participation(
    webview: &tauri::Webview,
    task: &NativePageParticipationTask,
) -> NativePageParticipationResult {
    // The capture hook is normally installed from the page-load callback, but
    // scheduled/background tasks can be admitted after a SPA navigation has
    // already completed. Re-evaluate the idempotent hook immediately before
    // every native action so the page's own signed receive request is never
    // missed merely because this task arrived late.
    let _ = webview.eval(RED_PACKET_RECEIVE_CAPTURE_SCRIPT);
    let cookie_name = format!(
        "fubao_participation_probe_{}",
        task.task_id
            .chars()
            .filter(|character| character.is_ascii_alphanumeric())
            .take(20)
            .collect::<String>()
    );
    let payload = json!({
        "action": task.action,
        "web_rid": task.web_rid,
        "actual_room_id": task.actual_room_id,
        "box_id": task.box_id,
        "packet_id": task.packet_id.as_deref().unwrap_or_default(),
        "user_id": task.user_id.as_deref().unwrap_or_default(),
        "sec_uid": task.sec_uid.as_deref().unwrap_or_default(),
        "anchor_id": task.anchor_id.as_deref().unwrap_or_default(),
        "box_type": task.box_type.as_deref().unwrap_or_default(),
        "send_time": task.send_time.as_deref().unwrap_or_default(),
        "delay_time": task.delay_time.as_deref().unwrap_or_default(),
    });
    let script = format!(
        r#"(() => {{
          const task = {payload};
          const cookieName = {cookie_name};
          const finish = (result) => {{
            try {{
              // Prefer in-page storage: document.cookie is size-limited and can
              // silently drop large hex payloads, which looked like page timeouts.
              if (!window.__fubaoPagePartResults) window.__fubaoPagePartResults = {{}};
              const safe = Object.assign({{}}, result || {{}});
              if (typeof safe.body === 'string' && safe.body.length > 900) {{
                safe.body = safe.body.slice(0, 900);
              }}
              window.__fubaoPagePartResults[cookieName] = safe;
            }} catch (_) {{}}
            try {{
              const text = JSON.stringify(result || {{}});
              // Keep cookie backup tiny so WebView cookie jars always accept it.
              const compact = text.length > 1800 ? text.slice(0, 1800) : text;
              let hex = '';
              const bytes = new TextEncoder().encode(compact);
              for (let index = 0; index < bytes.length; index += 1) {{
                const part = bytes[index].toString(16);
                hex += part.length === 1 ? ('0' + part) : part;
              }}
              document.cookie = cookieName + '=' + hex + '; Path=/; Secure; SameSite=None; Max-Age=20';
            }} catch (_) {{}}
          }};
          const challengeCopy = /请完成(?:安全)?验证|请拖动滑块|拖动滑块|滑块验证|验证码拦截|安全验证拦截/;
          const visibleElement = (element) => {{
            if (!element) return false;
            const style = window.getComputedStyle(element);
            const rect = element.getBoundingClientRect();
            return style.display !== 'none' && style.visibility !== 'hidden' &&
              Number(style.opacity || 1) > 0.05 && rect.width > 2 && rect.height > 2;
          }};
          const detectsVisibleChallenge = () => {{
            try {{
              const selectors = [
                'iframe[src*="captcha"]', 'iframe[src*="verify"]',
                '[id*="captcha"]', '[class*="captcha"]',
                '[id*="verify"]', '[class*="verify"]',
                '[data-e2e*="captcha"]', '[data-testid*="captcha"]'
              ].join(',');
              for (const element of document.querySelectorAll(selectors)) {{
                if (!visibleElement(element)) continue;
                const copy = String(element.innerText || element.getAttribute('aria-label') || element.getAttribute('title') || '');
                if (challengeCopy.test(copy) || /secsdk-captcha|verifycenter|verify_center/.test(String(element.outerHTML || '').toLowerCase())) return true;
              }}
              return challengeCopy.test(String(document.body && document.body.innerText || ''));
            }} catch (error) {{ return false; }}
          }};
          const detectsResponseChallenge = (value) => {{
            const source = String(value || '').slice(0, 16000);
            const lowered = source.toLowerCase();
            if (challengeCopy.test(source) || /secsdk-captcha|captcha_required|challenge_required|verifycenter|verify_center/.test(lowered)) return true;
            try {{
              const parsed = JSON.parse(source);
              const inspect = (item) => {{
                if (!item || typeof item !== 'object') return false;
                if (Array.isArray(item)) return item.some(inspect);
                for (const key of ['need_verify', 'needVerify', 'captcha_required', 'captchaRequired', 'challenge_required', 'challengeRequired']) {{
                  const value = item[key];
                  if (value === true || (typeof value === 'number' && value !== 0) || (typeof value === 'string' && !['', '0', 'false', 'none', 'null'].includes(value.toLowerCase()))) return true;
                }}
                for (const key of ['status_msg', 'message', 'msg', 'toast', 'error']) {{
                  if (challengeCopy.test(String(item[key] || ''))) return true;
                }}
                return Object.values(item).some(inspect);
              }};
              return inspect(parsed);
            }} catch (error) {{ return false; }}
          }};
          // Real live-page join/rush traffic (captured in DY-KIRO) carries the
          // current webcast identity (user_id/sec_user_id/msToken base) plus a
          // full browser fingerprint. A minimal room_id+box_id URL is accepted
          // with status_code=0 but almost always soft-denies succeed=false.
          // Reuse the latest same-origin webcast resource for session fields,
          // strip list/signature params, then set the action-specific fields so
          // bdms.js can re-sign on fetch.
          const looksNumericID = (value) => /^\\d{{5,}}$/.test(String(value || '').trim());
          const pickNumeric = (value) => {{
            const text = String(value || '').trim();
            return looksNumericID(text) ? text : '';
          }};
          const walkAnchorID = (node, depth) => {{
            if (!node || depth > 6) return '';
            if (Array.isArray(node)) {{
              for (const item of node) {{
                const found = walkAnchorID(item, depth + 1);
                if (found) return found;
              }}
              return '';
            }}
            if (typeof node !== 'object') return '';
            for (const key of ['anchor_id', 'anchorId', 'anchor_user_id', 'anchorUserId', 'owner_user_id', 'ownerUserId']) {{
              const value = pickNumeric(node[key]);
              if (value) return value;
            }}
            for (const nestKey of ['owner', 'anchor', 'author', 'user', 'room_owner', 'roomOwner']) {{
              const nest = node[nestKey];
              if (!nest || typeof nest !== 'object') continue;
              const value = pickNumeric(nest.id_str || nest.idStr || nest.uid || nest.user_id || nest.userId || nest.id);
              if (value) return value;
            }}
            for (const child of Object.values(node)) {{
              if (child && typeof child === 'object') {{
                const found = walkAnchorID(child, depth + 1);
                if (found) return found;
              }}
            }}
            return '';
          }};
          const detectRuntimeFingerprint = () => {{
            const ua = String(navigator.userAgent || '');
            const platform = String(navigator.platform || '');
            let os_name = 'Windows';
            let os_version = '10';
            if (/Android/i.test(ua)) {{ os_name = 'Android'; os_version = '10'; }}
            else if (/iPhone|iPad|iPod/i.test(ua)) {{ os_name = 'iOS'; os_version = '16'; }}
            else if (/Mac OS X|Macintosh/i.test(ua) || /Mac/i.test(platform)) {{
              os_name = 'Mac OS';
              const mac = ua.match(/Mac OS X ([0-9_]+)/i);
              os_version = mac ? mac[1].replace(/_/g, '.') : '10.15.7';
            }} else if (/Windows NT ([0-9.]+)/i.test(ua) || /Win/i.test(platform)) {{
              os_name = 'Windows';
              const win = ua.match(/Windows NT ([0-9.]+)/i);
              os_version = win ? win[1] : '10';
            }}
            let browser_version = '124.0.0.0';
            let browser_name = 'Chrome';
            try {{
              // Prefer indexOf over slash-heavy regex literals in this embedded script.
              const chromeAt = ua.indexOf('Chrome/');
              const edgeAt = ua.indexOf('Edg/');
              const takeVersion = (at, token) => {{
                if (at < 0) return '';
                const start = at + token.length;
                let end = start;
                while (end < ua.length && /[0-9.]/.test(ua.charAt(end))) end += 1;
                return ua.slice(start, end);
              }};
              if (edgeAt >= 0) {{
                browser_name = 'Edge';
                browser_version = takeVersion(edgeAt, 'Edg/') || browser_version;
              }} else if (chromeAt >= 0) {{
                browser_name = 'Chrome';
                browser_version = takeVersion(chromeAt, 'Chrome/') || browser_version;
              }}
            }} catch (_) {{}}
            return {{
              os_name,
              os_version,
              browser_name,
              browser_version,
              browser_platform: platform || (os_name === 'Windows' ? 'Win32' : 'MacIntel')
            }};
          }};
          const pageIdentity = () => {{
            const found = {{}};
            try {{
              const entries = performance.getEntriesByType('resource').slice().reverse();
              for (const entry of entries) {{
                const candidate = new URL(String(entry.name || ''), location.href);
                if (!/douyin\\.com$/i.test(candidate.hostname)) continue;
                for (const key of ['user_id', 'sec_user_id', 'user_unique_id', 'sec_anchor_id', 'anchor_id', 'anchor_id_str', 'verifyFp', 'fp', 's_v_web_id', 'device_id', 'msToken']) {{
                  const value = String(candidate.searchParams.get(key) || '').trim();
                  if (value && !found[key]) found[key] = value;
                }}
                if (found.user_id && found.sec_user_id && found.anchor_id) break;
              }}
            }} catch (_) {{}}
            try {{
              const cookieText = String(document.cookie || '');
              for (const part of cookieText.split(';')) {{
                const index = part.indexOf('=');
                if (index < 0) continue;
                const key = part.slice(0, index).trim();
                const value = part.slice(index + 1).trim();
                if (!value) continue;
                if ((key === 'uid_tt' || key === 'uid_tt_ss') && !found.user_id && looksNumericID(value)) found.user_id = value;
                if ((key === 's_v_web_id' || key === 'fp' || key === 'verifyFp' || key === 'msToken') && !found[key]) found[key] = value;
                if (key === 'sessionid' && !found.session_hint) found.session_hint = '1';
              }}
            }} catch (_) {{}}
            // Live room SPA often keeps the streamer id in RENDER_DATA / page state
            // even when the task metadata from center-library rows omits anchor_id.
            if (!found.anchor_id) {{
              try {{
                for (const id of ['RENDER_DATA', '__NEXT_DATA__', 'SIGI_STATE']) {{
                  const el = document.getElementById(id) || document.querySelector(`script#${{id}}`);
                  if (!el || !el.textContent) continue;
                  let text = el.textContent;
                  try {{ text = decodeURIComponent(text); }} catch (_) {{}}
                  const parsed = JSON.parse(text);
                  const anchor = walkAnchorID(parsed, 0);
                  if (anchor) {{ found.anchor_id = anchor; break; }}
                }}
              }} catch (_) {{}}
            }}
            if (!found.anchor_id) {{
              try {{
                for (const key of Object.keys(window)) {{
                  if (!/ROOM|ROOM_INFO|INIT|STATE|STORE|ANCHOR|OWNER/i.test(key)) continue;
                  const anchor = walkAnchorID(window[key], 0);
                  if (anchor) {{ found.anchor_id = anchor; break; }}
                }}
              }} catch (_) {{}}
            }}
            if (!found.anchor_id) {{
              try {{
                const html = String(document.documentElement && document.documentElement.innerHTML || '').slice(0, 250000);
                const match = html.match(/"(?:owner_user_id|anchor_id|anchor_id_str)"\\s*:\\s*"?(\\d{{6,}})"?/);
                if (match && match[1]) found.anchor_id = match[1];
              }} catch (_) {{}}
            }}
            // sec_anchor_id is MS4w... (not digits). Digit-only regex always missed it.
            if (!found.sec_anchor_id) {{
              try {{
                const html = String(document.documentElement && document.documentElement.innerHTML || '').slice(0, 400000);
                let m = html.match(/"sec_anchor_id"\\s*:\\s*"([^"]{{12,}})"/);
                if (!m) m = html.match(/"sec_uid"\\s*:\\s*"(MS4w[^"]{{8,}})"/);
                if (m && m[1]) found.sec_anchor_id = m[1];
              }} catch (_) {{}}
            }}
            if (!found.sec_anchor_id) {{
              try {{
                const walkSec = (node, depth) => {{
                  if (!node || depth > 8) return '';
                  if (Array.isArray(node)) {{
                    for (const item of node) {{
                      const foundSec = walkSec(item, depth + 1);
                      if (foundSec) return foundSec;
                    }}
                    return '';
                  }}
                  if (typeof node !== 'object') return '';
                  for (const key of ['sec_anchor_id', 'sec_uid', 'secUid', 'sec_user_id']) {{
                    const value = String(node[key] || '').trim();
                    if (value.length >= 12 && (value.indexOf('MS4w') === 0 || value.length > 20)) return value;
                  }}
                  for (const nestKey of ['owner', 'anchor', 'author', 'user', 'room_owner', 'roomOwner', 'data', 'room', 'roomInfo']) {{
                    const nest = node[nestKey];
                    if (nest && typeof nest === 'object') {{
                      const foundSec = walkSec(nest, depth + 1);
                      if (foundSec) return foundSec;
                    }}
                  }}
                  return '';
                }};
                for (const id of ['RENDER_DATA', '__NEXT_DATA__', 'SIGI_STATE']) {{
                  const el = document.getElementById(id) || document.querySelector(`script#${{id}}`);
                  if (!el || !el.textContent) continue;
                  let raw = el.textContent;
                  try {{ raw = decodeURIComponent(raw); }} catch (_) {{}}
                  const parsed = JSON.parse(raw);
                  const sec = walkSec(parsed, 0);
                  if (sec) {{ found.sec_anchor_id = sec; break; }}
                }}
              }} catch (_) {{}}
            }}
            if (!found.device_id) {{
              try {{
                const html = String(document.documentElement && document.documentElement.innerHTML || '').slice(0, 400000);
                const m = html.match(/"device_id"\\s*:\\s*"?(\\d{{6,}})"?/);
                if (m && m[1]) found.device_id = m[1];
              }} catch (_) {{}}
            }}
            if (!found.user_unique_id) {{
              try {{
                const html = String(document.documentElement && document.documentElement.innerHTML || '').slice(0, 400000);
                const m = html.match(/"user_unique_id"\\s*:\\s*"?(\\d{{6,}})"?/);
                if (m && m[1]) found.user_unique_id = m[1];
              }} catch (_) {{}}
            }}
            // Real live-page join uses s_v_web_id as verifyFp/fp when dedicated keys absent.
            if (found.s_v_web_id) {{
              if (!found.verifyFp) found.verifyFp = found.s_v_web_id;
              if (!found.fp) found.fp = found.s_v_web_id;
            }}
            if (found.verifyFp && !found.fp) found.fp = found.verifyFp;
            if (found.fp && !found.verifyFp) found.verifyFp = found.fp;
            try {{
              const live = typeof window.__fubaoGetLiveIdentity === 'function' ? window.__fubaoGetLiveIdentity() : (window.__fubaoLiveIdentity || {{}});
              for (const key of ['room_id', 'anchor_id', 'anchor_id_str', 'sec_anchor_id', 'user_id', 'sec_user_id', 'user_unique_id', 'msToken', 'device_id', 'verifyFp', 'fp', 's_v_web_id']) {{
                if (!found[key] && live[key]) found[key] = live[key];
              }}
            }} catch (_) {{}}
            return found;
          }};
          // pageRoomID: only what the live SPA is using right now (no task fallback).
          // join_diag showed pageRoom=0 while msToken/a_bogus=1 — stale task room_id
          // then causes succeed=false. Prefer live identity from hooked traffic.
          const pageRoomID = () => {{
            try {{
              const live = typeof window.__fubaoGetLiveIdentity === 'function' ? window.__fubaoGetLiveIdentity() : (window.__fubaoLiveIdentity || {{}});
              const rid = String(live.room_id || '').trim();
              if (looksNumericID(rid) && rid.length >= 10) return rid;
            }} catch (_) {{}}
            try {{
              const entries = performance.getEntriesByType('resource').slice().reverse();
              for (const entry of entries) {{
                try {{
                  const candidate = new URL(String(entry.name || ''), location.href);
                  if (!/douyin\\.com$/i.test(candidate.hostname)) continue;
                  if (!candidate.pathname.includes('/webcast/')) continue;
                  const rid = String(candidate.searchParams.get('room_id') || '').trim();
                  if (looksNumericID(rid) && rid.length >= 10) return rid;
                }} catch (_) {{}}
              }}
            }} catch (_) {{}}
            try {{
              for (const id of ['RENDER_DATA', '__NEXT_DATA__', 'SIGI_STATE']) {{
                const el = document.getElementById(id) || document.querySelector(`script#${{id}}`);
                if (!el || !el.textContent) continue;
                let text = el.textContent;
                try {{ text = decodeURIComponent(text); }} catch (_) {{}}
                const parsed = JSON.parse(text);
                const walk = (node, depth) => {{
                  if (!node || depth > 8) return '';
                  if (Array.isArray(node)) {{
                    for (const item of node) {{
                      const found = walk(item, depth + 1);
                      if (found) return found;
                    }}
                    return '';
                  }}
                  if (typeof node !== 'object') return '';
                  for (const key of ['room_id', 'roomId', 'enter_room_id', 'enterRoomId', 'id_str', 'idStr']) {{
                    const value = pickNumeric(node[key]);
                    if (value && value.length >= 10) return value;
                  }}
                  for (const nestKey of ['room', 'roomInfo', 'room_info', 'data', 'roomStore']) {{
                    const nest = node[nestKey];
                    if (nest && typeof nest === 'object') {{
                      const found = walk(nest, depth + 1);
                      if (found) return found;
                    }}
                  }}
                  return '';
                }};
                const found = walk(parsed, 0);
                if (found) return found;
              }}
            }} catch (_) {{}}
            try {{
              const html = String(document.documentElement && document.documentElement.innerHTML || '').slice(0, 400000);
              const match = html.match(/"(?:room_id|enter_room_id)"\\s*:\\s*"?(\\d{{10,}})"?/);
              if (match && match[1]) return match[1];
            }} catch (_) {{}}
            return '';
          }};
          // Page-sniffed live room_id when available, else the task room. Never
          // block the join on the sniffed value: it is often unavailable in the
          // embedded WebView, and the verified succeed:true path used the task room.
          const liveRoomID = () => pageRoomID() || String(task.actual_room_id || '').trim();
          const pickWebcastTemplate = () => {{
            try {{
              const entries = performance.getEntriesByType('resource').slice().reverse();
              let fallback = null;
              for (const entry of entries) {{
                try {{
                  const candidate = new URL(String(entry.name || ''), location.href);
                  if (candidate.hostname !== 'live.douyin.com') continue;
                  if (!candidate.pathname.startsWith('/webcast/')) continue;
                  const path = candidate.pathname;
                  // Prefer room enter / im / rank — avoid box list pollution templates.
                  if (/\/webcast\/room\//.test(path) || /\/webcast\/im\//.test(path) ||
                      /\/webcast\/ranklist\//.test(path) || /\/webcast\/gift\//.test(path)) {{
                    return candidate;
                  }}
                  if (!fallback && !/\/luckybox\/box\/list/.test(path) && !/\/lottery\//.test(path)) {{
                    fallback = candidate;
                  }}
                }} catch (_) {{}}
              }}
              return fallback;
            }} catch (_) {{
              return null;
            }}
          }};
          // Clone a real same-room webcast request as the template so the join
          // carries exactly the session/fingerprint query keys this live page has
          // already used successfully, then override the join fields. Strip only
          // signatures and list cursors: bdms re-signs msToken/a_bogus on send, so
          // never attach a sniffed msToken here — a token that does not match the
          // signature is a soft-deny cause.
          const requestURL = (endpoint) => {{
            let url = new URL(`https://live.douyin.com/webcast/luckybox/${{endpoint}}/`);
            try {{
              const template = pickWebcastTemplate();
              if (template) url = new URL(template.toString());
            }} catch (_) {{}}
            url.protocol = 'https:';
            url.hostname = 'live.douyin.com';
            url.pathname = `/webcast/luckybox/${{endpoint}}/`;
            for (const key of [
              'msToken', 'a_bogus', 'X-Bogus', '__ac_signature', '__ac_nonce',
              'cursor', 'count', 'offset', 'fetch_time', 'last_id', 'room_ids',
              'box_list_type', 'is_draw', 'identity', 'Rangelist'
            ]) {{
              url.searchParams.delete(key);
            }}
            const identity = pageIdentity();
            const runtime = detectRuntimeFingerprint();
            let live = {{}};
            try {{
              live = typeof window.__fubaoGetLiveIdentity === 'function' ? window.__fubaoGetLiveIdentity() : (window.__fubaoLiveIdentity || {{}});
            }} catch (_) {{}}
            const userId = String(task.user_id || identity.user_id || live.user_id || '').trim();
            const secUserId = String(task.sec_uid || identity.sec_user_id || live.sec_user_id || '').trim();
            // Match real live-page join captures: enter_from=link_share, no
            // web_rid/box_type on join, and empty pc_client_version is fine.
            const values = {{
              aid: '6383',
              app_name: 'douyin_web',
              live_id: '1',
              device_platform: 'web',
              language: 'zh-CN',
              enter_from: 'link_share',
              enter_from_merge: 'link_share',
              enter_method: 'direct_open',
              cookie_enabled: String(navigator.cookieEnabled !== false),
              screen_width: String(window.screen && window.screen.width || 1920),
              screen_height: String(window.screen && window.screen.height || 1080),
              browser_language: navigator.language || 'zh-CN',
              browser_platform: runtime.browser_platform,
              browser_name: runtime.browser_name,
              browser_version: runtime.browser_version,
              os_name: runtime.os_name,
              os_version: runtime.os_version,
              platform: 'PC',
              pc_client_type: '1',
              pc_client_version: '',
              version_code: '170400',
              update_version_code: '170400',
              version_name: '17.4.0',
              action_type: 'click',
              is_ad: '0',
              room_id: liveRoomID(),
              box_id: task.box_id,
              user_id: userId,
              sec_user_id: secUserId,
              user_unique_id: live.user_unique_id || identity.user_unique_id || '',
              user_agent: navigator.userAgent || ''
            }};
            const anchor = String(task.anchor_id || identity.anchor_id || live.anchor_id || live.anchor_id_str || '').trim();
            if (anchor) {{
              values.anchor_id = anchor;
              values.anchor_id_str = anchor;
            }}
            const secAnchor = String(live.sec_anchor_id || identity.sec_anchor_id || '').trim();
            if (secAnchor) values.sec_anchor_id = secAnchor;
            // receive keeps optional room-entry fingerprint keys when present.
            if (endpoint === 'receive') {{
              if (task.web_rid) values.web_rid = task.web_rid;
              if (identity.verifyFp || identity.fp) {{
                values.verifyFp = identity.verifyFp || identity.fp;
                values.fp = identity.fp || identity.verifyFp;
              }}
              if (identity.s_v_web_id) values.s_v_web_id = identity.s_v_web_id;
              if (identity.device_id) values.device_id = identity.device_id;
            }}
            for (const [key, value] of Object.entries(values)) {{
              // Allow intentionally empty pc_client_version (real Chrome capture).
              if (key === 'pc_client_version' || String(value || '').trim()) {{
                url.searchParams.set(key, String(value ?? ''));
              }}
            }}
            return url;
          }};
          const requestBody = (endpoint) => {{
            if (endpoint === 'receive') return '';
            const body = {{
              box_id: String(task.box_id || ''),
              room_id: String(liveRoomID() || task.actual_room_id || '')
            }};
            let live = {{}};
            try {{
              live = typeof window.__fubaoGetLiveIdentity === 'function' ? window.__fubaoGetLiveIdentity() : (window.__fubaoLiveIdentity || {{}});
            }} catch (_) {{}}
            const anchor = String(task.anchor_id || pageIdentity().anchor_id || live.anchor_id || '').trim();
            if (anchor) body.anchor_id = anchor;
            return JSON.stringify(body);
          }};
          const parseLuckyboxBody = (text) => {{
            try {{ return JSON.parse(String(text || '')); }} catch (_) {{ return null; }}
          }};
          const luckyboxLooksJoined = (text) => {{
            const parsed = parseLuckyboxBody(text);
            if (!parsed || Number(parsed.status_code ?? -1) !== 0) return false;
            const data = parsed.data && typeof parsed.data === 'object' ? parsed.data : {{}};
            if (data.succeed === true || data.succeeded === true || data.joined === true ||
              data.has_joined === true || data.hasJoined === true || data.success === true ||
              data.is_success === true || data.isSuccess === true) return true;
            if (data.rush_too_much || data.rush_too_often) return true;
            const message = String(parsed.status_msg || data.message || data.prompts || data.toast || '');
            return /参与成功|成功参与|已参与|等待开奖|已抢|领取成功/.test(message);
          }};
          const takeCapturedReceive = () => {{
            try {{
              if (typeof window.__fubaoTakeRedPacketReceiveResult !== 'function') return null;
              const item = window.__fubaoTakeRedPacketReceiveResult(task.box_id, task.packet_id);
              if (!item || !item.body) return null;
              return {{endpoint: 'receive', status: Number(item.status || 200), text: String(item.body)}};
            }} catch (_) {{ return null; }}
          }};
          // Pull room_id / user_unique_id from any webcast traffic the SPA already sent.
          const harvestSessionIdentity = () => {{
            try {{
              const live = window.__fubaoLiveIdentity || (window.__fubaoLiveIdentity = {{}});
              const take = (key, value, minLen) => {{
                const raw = String(value == null ? '' : value).trim();
                if (!raw) return;
                if (minLen && raw.length < minLen) return;
                if (!live[key]) live[key] = raw;
              }};
              const entries = performance.getEntriesByType('resource').slice().reverse();
              for (const entry of entries.slice(0, 80)) {{
                try {{
                  const candidate = new URL(String(entry.name || ''), location.href);
                  if (!/douyin\\.com$/i.test(candidate.hostname)) continue;
                  if (!candidate.pathname.includes('/webcast/')) continue;
                  take('room_id', candidate.searchParams.get('room_id'), 10);
                  take('user_unique_id', candidate.searchParams.get('user_unique_id'), 5);
                  take('sec_anchor_id', candidate.searchParams.get('sec_anchor_id'), 10);
                  take('anchor_id', candidate.searchParams.get('anchor_id'), 5);
                  take('msToken', candidate.searchParams.get('msToken'), 8);
                }} catch (_) {{}}
              }}
              live.saved_at = Date.now();
              window.__fubaoLiveIdentity = live;
            }} catch (_) {{}}
          }};
          const clickResultSurface = () => {{
            try {{
              const exact = /^(查看结果|开奖结果|开红包|拆红包|拆开红包|打开红包|未中奖|已中奖)$/;
              const visible = (element) => {{
                const style = window.getComputedStyle(element);
                const rect = element.getBoundingClientRect();
                return style.display !== 'none' && style.visibility !== 'hidden' &&
                  Number(style.opacity || 1) > 0.05 && rect.width > 4 && rect.height > 4;
              }};
              const candidates = Array.from(document.querySelectorAll('button,[role="button"],a'))
                .map((element) => {{
                  const text = String(element.innerText || element.textContent || '').replace(/\\s+/g, ' ').trim();
                  return {{element, text}};
                }})
                .filter((item) => visible(item.element) && exact.test(item.text));
              const candidate = candidates[0];
              if (!candidate) return false;
              candidate.element.click();
              return true;
            }} catch (_) {{ return false; }}
          }};
          const send = async (endpoint) => {{
            // Page fetch only for join, so bdms.js re-signs the URL (msToken +
            // a_bogus). The XHR fallback is reserved for receive: it bypasses the
            // fetch hook, so every join it ever sent was soft-denied, and re-sending
            // the same join is exactly the double-request shape that earns
            // rush_spam. Bound both so a hung request cannot outlive the rush window.
            try {{
              if (typeof window.__fubaoEnsureFetchHook === 'function') window.__fubaoEnsureFetchHook();
            }} catch (_) {{}}
            const bodyText = requestBody(endpoint);
            let urlText = requestURL(endpoint).toString();
            const contentType = endpoint === 'receive'
              ? 'application/x-www-form-urlencoded'
              : 'application/json';
            const requestTimeoutMs = 6000;
            let status = 0;
            let text = '';
            let transport = 'none';
            const headers = {{
              'accept': 'application/json, text/plain, */*',
              'content-type': contentType
            }};
            const doFetch = async (url) => {{
              const controller = typeof AbortController === 'function' ? new AbortController() : null;
              const timer = controller ? setTimeout(() => controller.abort(), requestTimeoutMs) : 0;
              try {{
                const response = await window.fetch(url, {{
                  method: 'POST', credentials: 'include', cache: 'no-store',
                  headers,
                  body: bodyText,
                  referrer: `https://live.douyin.com/${{task.web_rid}}`,
                  referrerPolicy: 'strict-origin-when-cross-origin',
                  mode: 'cors',
                  signal: controller ? controller.signal : undefined
                }});
                status = response.status;
                text = await response.text();
                transport = 'fetch';
                try {{
                  const finalURL = String(response && response.url || url);
                  if (finalURL && finalURL.indexOf('http') === 0) urlText = finalURL;
                }} catch (_) {{}}
              }} finally {{
                if (timer) clearTimeout(timer);
              }}
            }};
            const doXHR = async (url) => {{
              if (typeof XMLHttpRequest !== 'function') return;
              await new Promise((resolve) => {{
                try {{
                  const xhr = new XMLHttpRequest();
                  xhr.open('POST', url, true);
                  xhr.withCredentials = true;
                  xhr.setRequestHeader('Accept', 'application/json, text/plain, */*');
                  xhr.setRequestHeader('Content-Type', contentType);
                  xhr.timeout = requestTimeoutMs;
                  xhr.onload = () => {{
                    status = xhr.status;
                    text = String(xhr.responseText || '');
                    transport = 'xhr';
                    try {{ if (xhr.responseURL) urlText = String(xhr.responseURL); }} catch (_) {{}}
                    resolve();
                  }};
                  xhr.onerror = () => {{ status = 0; text = ''; resolve(); }};
                  xhr.ontimeout = () => {{ status = 0; text = ''; resolve(); }};
                  xhr.onabort = () => {{ status = 0; text = ''; resolve(); }};
                  setTimeout(() => {{ try {{ xhr.abort(); }} catch (_) {{}} resolve(); }}, requestTimeoutMs + 200);
                  xhr.send(bodyText);
                }} catch (_) {{ resolve(); }}
              }});
            }};
            try {{ await doFetch(urlText); }} catch (_) {{ status = 0; text = ''; }}
            if ((!text || status === 0) && endpoint === 'receive') {{ await doXHR(urlText); }}
            if (endpoint === 'receive') {{
              try {{
                const parsed = JSON.parse(text);
                const infos = parsed && parsed.data && parsed.data.receive_info;
                let reduced = infos;
                if (Array.isArray(infos)) {{
                  const targets = new Set([String(task.box_id || ''), String(task.packet_id || '')].filter(Boolean));
                  const matched = infos.find((item) => {{
                    const id = String(item && (item.box_id_str || item.boxIdStr || item.box_id || item.boxId || item.activity_id || item.activityId || item.red_packet_id || item.redPacketId || item.lottery_id || item.lotteryId) || '');
                    return targets.has(id);
                  }});
                  const onlyDefinitive = infos.length === 1 && infos[0] &&
                    (Object.prototype.hasOwnProperty.call(infos[0], 'succeed') || Object.prototype.hasOwnProperty.call(infos[0], 'success'));
                  const onlyIdless = infos.length === 1 && !String(infos[0] && (infos[0].box_id_str || infos[0].boxIdStr || infos[0].box_id || infos[0].boxId || infos[0].activity_id || infos[0].activityId || infos[0].red_packet_id || infos[0].redPacketId || infos[0].lottery_id || infos[0].lotteryId) || '');
                  reduced = matched ? [matched] : onlyIdless || onlyDefinitive ? [infos[0]] : infos.length === 0 ? [] : undefined;
                }}
                text = JSON.stringify({{status_code: parsed.status_code, status_msg: parsed.status_msg, data: {{receive_info: reduced}}}});
              }} catch (error) {{}}
            }}
            let diag = '';
            try {{
              const u = new URL(urlText);
              const hasTemplate = Boolean(pickWebcastTemplate());
              diag = [
                `msToken=${{u.searchParams.get('msToken') ? 1 : 0}}`,
                `a_bogus=${{u.searchParams.get('a_bogus') ? 1 : 0}}`,
                `pageRoom=${{pageRoomID() ? 1 : 0}}`,
                `roomSrc=${{pageRoomID() ? 'page' : (String(task.actual_room_id || '').trim() ? 'task' : 'none')}}`,
                `tpl=${{hasTemplate ? 1 : 0}}`,
                `room=${{u.searchParams.get('room_id') || ''}}`,
                `box=${{u.searchParams.get('box_id') || ''}}`,
                `anchor=${{u.searchParams.get('anchor_id') || ''}}`,
                `sec_anchor=${{u.searchParams.get('sec_anchor_id') ? 1 : 0}}`,
                `uid=${{u.searchParams.get('user_id') || ''}}`,
                `vfp=${{u.searchParams.get('verifyFp') || u.searchParams.get('fp') || u.searchParams.get('s_v_web_id') ? 1 : 0}}`,
                `did=${{u.searchParams.get('device_id') ? 1 : 0}}`,
                `uuniq=${{u.searchParams.get('user_unique_id') ? 1 : 0}}`,
                `sv=${{u.searchParams.get('s_v_web_id') ? 1 : 0}}`,
                `bv=${{u.searchParams.get('browser_version') || ''}}`,
                `bdms=${{window.bdms || window.byted_acrawler ? 1 : 0}}`,
                `xport=${{transport}}`
              ].join(' ');
            }} catch (_) {{ diag = 'diag_parse_fail'; }}
            return {{endpoint, status, text, diag, url_has_abogus: /[?&]a_bogus=/.test(urlText)}};
          }};
          (async () => {{
            try {{
              if (location.hostname !== 'live.douyin.com' || location.pathname.replace(/^\/+|\/+$/g, '') !== String(task.web_rid)) {{
                finish({{endpoint: 'page', http_status: 0, body: '', error: '浏览器实例未进入目标直播间', attempts: 0, context_missing: true}});
                return;
              }}
              if (detectsVisibleChallenge()) {{
                finish({{endpoint: 'page', http_status: 0, body: '', error: '验证码/安全验证拦截，已暂停接收新任务', attempts: 0, context_missing: false, login_expired: false, challenge_blocked: true}});
                return;
              }}
              if (task.action === 'receive') {{
                const captured = takeCapturedReceive();
                if (captured) {{
                  finish({{endpoint: captured.endpoint, http_status: captured.status, body: captured.text, error: '', attempts: 1, context_missing: false, login_expired: false, challenge_blocked: false}});
                  return;
                }}
              }}
              // Keep this pre-join budget very small. Go admits the account only
              // inside the final-countdown window, so every second spent here is
              // taken straight out of the packet's remaining rush time. Do not
              // gate on page-sniffed room_id: it is frequently unavailable in the
              // embedded WebView and waiting for it burned the whole window.
              if (task.action !== 'receive') {{
                const readyDeadline = Date.now() + 1600;
                while (Date.now() < readyDeadline) {{
                  harvestSessionIdentity();
                  const head = String(document.body && document.body.innerText || '').slice(0, 400);
                  const stillEntering = /正在进入直播间|加载中|连接中/.test(head);
                  let hasWebcast = false;
                  try {{
                    // Plain string match — a regex literal with over-escaped
                    // slashes once threw SyntaxError and aborted the whole script.
                    hasWebcast = performance.getEntriesByType('resource').some((entry) =>
                      String(entry.name || '').includes('live.douyin.com/webcast/')
                    );
                  }} catch (_) {{}}
                  if (hasWebcast) {{
                    try {{ if (typeof window.__fubaoEnsureFetchHook === 'function') window.__fubaoEnsureFetchHook(); }} catch (_) {{}}
                  }}
                  if (!stillEntering && (hasWebcast || pageIdentity().anchor_id || task.anchor_id)) break;
                  await new Promise((resolve) => setTimeout(resolve, 100));
                }}
                harvestSessionIdentity();
              }}
              // Pure API join, one request. No DOM click fallback: a synthetic
              // click after our own join makes the page issue a second request
              // for the same event, which Douyin answers with rush_spam.
              const action = task.action === 'receive' ? 'receive' : 'join';
              const response = await send(action);
              const attempts = 1;
              if (action === 'receive' && /"receive_info"\s*:\s*\[\s*\]/.test(response.text || '')) {{
                // Keep the page-native path alive: if the result panel is
                // already present, opening it causes Douyin to issue its own
                // signed receive request. The capture hook above then returns
                // the definitive personal result on the next poll.
                clickResultSurface();
                await new Promise((resolve) => setTimeout(resolve, 700));
                const captured = takeCapturedReceive();
                if (captured) {{
                  finish({{endpoint: captured.endpoint, http_status: captured.status, body: captured.text, error: '', attempts: 1, context_missing: false, login_expired: false, challenge_blocked: false, diag: ''}});
                  return;
                }}
              }}
              // An unanswered fetch leaves empty text with status 0. Whether the
              // join reached Douyin is genuinely unknown, so report a page-layer
              // timeout: never a soft-deny, and never a second join for the same
              // packet just to obtain a readable response.
              if (!String(response.text || '').trim() && Number(response.status || 0) === 0) {{
                finish({{
                  endpoint: action,
                  http_status: 0,
                  body: '',
                  error: '直播页面红包请求超时（页面接口无响应）',
                  attempts,
                  context_missing: false,
                  login_expired: false,
                  challenge_blocked: false,
                  diag: String(response.diag || '')
                }});
                return;
              }}
              const challengeBlocked = detectsResponseChallenge(response.text);
              finish({{
                endpoint: response.endpoint,
                http_status: response.status,
                body: String(response.text || '').slice(0, 1200),
                error: challengeBlocked ? '验证码/安全验证拦截，已暂停接收新任务' : '',
                attempts,
                context_missing: false,
                login_expired: false,
                challenge_blocked: challengeBlocked,
                diag: String(response.diag || '')
              }});
            }} catch (error) {{
              const message = String(error && (error.message || error) || '直播页面红包请求失败');
              const challengeBlocked = detectsResponseChallenge(message);
              finish({{endpoint: 'page', http_status: 0, body: '', error: challengeBlocked ? '验证码/安全验证拦截，已暂停接收新任务' : message, attempts: 1, context_missing: false, login_expired: false, challenge_blocked: challengeBlocked}});
            }}
          }})();
        }})();"#,
        payload = payload,
        cookie_name = serde_json::to_string(&cookie_name)
            .unwrap_or_else(|_| "\"fubao_participation_probe\"".into()),
    );
    if let Err(error) = webview.eval(&script) {
        return NativePageParticipationResult {
            endpoint: "page".into(),
            http_status: 0,
            body: String::new(),
            error: format!("启动直播页面红包请求失败：{error}"),
            attempts: 0,
            context_missing: false,
            login_expired: false,
            challenge_blocked: false,
            diag: String::new(),
        };
    }
    // Prefer window storage (reliable). Cookie hex is a small backup only.
    let poll_script = format!(
        r#"(function(){{
          try {{
            const key = {cookie_name};
            const store = window.__fubaoPagePartResults;
            if (store && store[key]) {{
              const value = store[key];
              try {{ delete store[key]; }} catch (_) {{}}
              return JSON.stringify(value);
            }}
          }} catch (_) {{}}
          return null;
        }})()"#,
        cookie_name = serde_json::to_string(&cookie_name).unwrap_or_else(|_| "\"\"".into()),
    );
    for _ in 0..160 {
        tokio::time::sleep(Duration::from_millis(150)).await;
        if let Some(value) = eval_json_value(webview, &poll_script).await {
            return native_page_result_from_value(value);
        }
        // Cookie backup path for older builds / partial page contexts.
        let cookies = match webview.cookies() {
            Ok(cookies) => cookies,
            Err(_) => continue,
        };
        let Some(encoded) = cookies
            .iter()
            .find(|cookie| cookie.name() == cookie_name)
            .map(|cookie| cookie.value().to_string())
        else {
            continue;
        };
        let _ = webview.eval(&format!(
            "document.cookie={};",
            serde_json::to_string(&format!(
                "{cookie_name}=; Path=/; Secure; SameSite=None; Max-Age=0"
            ))
            .unwrap_or_else(|_| "\"\"".into())
        ));
        let decoded = match decode_hex_utf8(&encoded) {
            Ok(value) => value,
            Err(_) => continue,
        };
        if let Ok(value) = serde_json::from_str::<Value>(&decoded) {
            return native_page_result_from_value(value);
        }
    }
    NativePageParticipationResult {
        endpoint: "page".into(),
        http_status: 0,
        body: String::new(),
        error: "等待直播页面红包接口响应超时".into(),
        attempts: 1,
        context_missing: false,
        login_expired: false,
        challenge_blocked: false,
        diag: String::new(),
    }
}

fn native_page_result_from_value(value: Value) -> NativePageParticipationResult {
    NativePageParticipationResult {
        endpoint: value
            .get("endpoint")
            .and_then(Value::as_str)
            .unwrap_or("page")
            .to_string(),
        http_status: value
            .get("http_status")
            .and_then(Value::as_i64)
            .unwrap_or(0),
        body: value
            .get("body")
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_string(),
        error: value
            .get("error")
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_string(),
        attempts: value.get("attempts").and_then(Value::as_i64).unwrap_or(1),
        context_missing: value
            .get("context_missing")
            .and_then(Value::as_bool)
            .unwrap_or(false),
        login_expired: value
            .get("login_expired")
            .and_then(Value::as_bool)
            .unwrap_or(false),
        challenge_blocked: value
            .get("challenge_blocked")
            .and_then(Value::as_bool)
            .unwrap_or(false),
        diag: value
            .get("diag")
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_string(),
    }
}

async fn eval_json_value(webview: &tauri::Webview, script: &str) -> Option<Value> {
    let (sender, receiver) = oneshot::channel::<String>();
    let sender = std::sync::Mutex::new(Some(sender));
    if webview
        .eval_with_callback(script, move |result| {
            if let Ok(mut guard) = sender.lock() {
                if let Some(sender) = guard.take() {
                    let _ = sender.send(result);
                }
            }
        })
        .is_err()
    {
        return None;
    }
    let raw = tokio::time::timeout(Duration::from_millis(250), receiver)
        .await
        .ok()?
        .ok()?;
    let trimmed = raw.trim();
    if trimmed.is_empty() || trimmed == "null" || trimmed == "undefined" {
        return None;
    }
    // eval_with_callback may wrap strings as JSON strings.
    if let Ok(value) = serde_json::from_str::<Value>(trimmed) {
        if let Some(text) = value.as_str() {
            return serde_json::from_str(text).ok();
        }
        return Some(value);
    }
    serde_json::from_str(trimmed).ok()
}

/// Seconds left before the packet's draw deadline, derived from the packet's own
/// `send_time + delay_time` exactly like the Go engine's authoritative rule.
/// Returns `None` when the packet carries no usable timing, so a missing field
/// can never be read as "already expired".
fn page_participation_remaining_seconds(task: &NativePageParticipationTask) -> Option<f64> {
    let send: f64 = task.send_time.as_deref()?.trim().parse().ok()?;
    let delay: f64 = task.delay_time.as_deref()?.trim().parse().ok()?;
    if send <= 0.0 || delay < 0.0 {
        return None;
    }
    let mut seconds = send;
    while seconds > 1e12 {
        seconds /= 1000.0;
    }
    // Millisecond timestamps are ~1e12; second timestamps are ~1e9.
    if seconds > 1e11 {
        seconds /= 1000.0;
    }
    // Placeholder or truncated send_time values must not collapse the window
    // into 1970 and make every packet look expired.
    if seconds < 1_577_836_800.0 {
        return None;
    }
    let now = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .ok()?
        .as_secs_f64();
    Some(seconds + delay - now)
}

/// A join whose draw window already closed must never be sent: it can only earn
/// a soft-deny while still exposing the account to risk control. `receive` stays
/// allowed after the deadline because the personal draw result exists only then.
fn page_participation_window_closed(task: &NativePageParticipationTask) -> bool {
    task.action.trim() == "join"
        && page_participation_remaining_seconds(task).is_some_and(|remaining| remaining <= 0.0)
}

async fn handle_page_participation_task(
    app: tauri::AppHandle,
    runtime: Arc<EngineRuntime>,
    task: NativePageParticipationTask,
) {
    let result = if task.account_id.trim().is_empty()
        || !valid_live_room_id(&task.web_rid)
        || task.actual_room_id.trim().is_empty()
        || task.box_id.trim().is_empty()
    {
        NativePageParticipationResult::context_missing("红包参与任务缺少直播间参数")
    } else if page_participation_window_closed(&task) {
        // Reuse the no-request path so the durable reservation is released
        // instead of persisting a misleading 参与失败 row, and skip the live-room
        // navigation entirely for a packet that can no longer be joined.
        NativePageParticipationResult::context_missing("红包已过期，未发送参与请求")
    } else if let Some(webview) =
        browser_webview_for_instance(&app, runtime.as_ref(), &task.instance_id)
    {
        match navigate_browser_to_live_room(
            &webview,
            runtime.as_ref(),
            &task.instance_id,
            &task.web_rid,
        )
        .await
        {
            // Tight budget on the participation path: the account is admitted
            // only inside the packet's final-countdown window, so a long login
            // readiness wait here spends the rush time itself. The user-initiated
            // prepare path keeps the generous wait.
            Ok(()) => match wait_for_douyin_login_ready(&webview, Duration::from_millis(1_200))
                .await
            {
                Ok(snapshot) if snapshot.state == BrowserLoginState::LoggedOut => {
                    NativePageParticipationResult::login_expired("CK 已失效：直播页面要求重新登录")
                }
                Ok(snapshot)
                    if snapshot.state == BrowserLoginState::LoggedIn
                        || snapshot
                            .raw_cookie
                            .as_ref()
                            .is_some_and(|value| !value.trim().is_empty()) =>
                {
                    // Navigation plus the login-readiness wait are the largest
                    // slice of the countdown window, so re-check the deadline
                    // here rather than trusting the admission decision.
                    if page_participation_window_closed(&task) {
                        NativePageParticipationResult::context_missing(
                            "红包已过期，未发送参与请求",
                        )
                    } else {
                        execute_page_participation(&webview, &task).await
                    }
                }
                Ok(_) => NativePageParticipationResult::context_missing(
                    "直播页面尚未完成加载，请稍后重试",
                ),
                Err(error) => NativePageParticipationResult::context_missing(error),
            },
            Err(error) => NativePageParticipationResult::context_missing(error),
        }
    } else {
        NativePageParticipationResult::context_missing("浏览器实例页面未挂载，请先点击卡片红包图标")
    };
    // Always log safe join diagnostics so soft-deny without a_bogus is visible.
    let diag_line = format!(
        "[redpacket-page] task={} account={} endpoint={} http={} attempts={} context_missing={} login_expired={} error={} diag={} body_head={}",
        task.task_id.chars().take(8).collect::<String>(),
        task.account_id.chars().take(8).collect::<String>(),
        result.endpoint,
        result.http_status,
        result.attempts,
        result.context_missing,
        result.login_expired,
        if result.error.is_empty() {
            "none"
        } else {
            result.error.as_str()
        },
        if result.diag.is_empty() {
            "none"
        } else {
            result.diag.as_str()
        },
        result.body.chars().take(160).collect::<String>().replace('\n', " "),
    );
    eprintln!("{diag_line}");
    // Persist to app data so we can inspect after soft-deny without hunting stderr.
    if let Ok(dir) = app.path().app_data_dir() {
        let path = dir.join("data").join("join_diag.log");
        if let Ok(mut file) = std::fs::OpenOptions::new()
            .create(true)
            .append(true)
            .open(&path)
        {
            use std::io::Write;
            let ts = std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .map(|d| d.as_secs())
                .unwrap_or(0);
            let _ = writeln!(file, "ts={ts} {diag_line}");
        }
    }
    let challenge_blocked = result.challenge_blocked;
    let challenge_instance_id = task.instance_id.clone();
    let _ = native_engine_request(
        runtime.clone(),
        "red_packet_participation.native_complete",
        json!({
            "task_id": task.task_id,
            "endpoint": result.endpoint,
            "http_status": result.http_status,
            "body": result.body,
            "error": result.error,
            "attempts": result.attempts,
            "context_missing": result.context_missing,
            "login_expired": result.login_expired,
            "challenge_blocked": result.challenge_blocked,
            "secret": runtime.native_secret,
        }),
    )
    .await;
    if challenge_blocked {
        // Go persists the safe account block and releases its participation
        // lease. Mirror that terminal state in Rust immediately so the native
        // WebView is no longer treated as an active participation context.
        if let Ok(mut contexts) = runtime.browser_red_packet_contexts.lock() {
            contexts.remove(&challenge_instance_id);
        }
    }
}

async fn poll_page_participation_tasks(app: tauri::AppHandle, runtime: Arc<EngineRuntime>) {
    tokio::time::sleep(Duration::from_millis(350)).await;
    while runtime.online.load(Ordering::SeqCst) {
        let has_prepared_context = runtime
            .browser_red_packet_contexts
            .lock()
            .map(|contexts| !contexts.is_empty())
            .unwrap_or(false);
        if !has_prepared_context {
            tokio::time::sleep(Duration::from_millis(500)).await;
            continue;
        }
        let next = native_engine_request(
            runtime.clone(),
            "red_packet_participation.native_next",
            json!({"secret": runtime.native_secret}),
        )
        .await;
        match next {
            Ok(value) if !value.is_null() => {
                if let Ok(task) = serde_json::from_value::<NativePageParticipationTask>(value) {
                    let task_app = app.clone();
                    let task_runtime = runtime.clone();
                    tauri::async_runtime::spawn(async move {
                        handle_page_participation_task(task_app, task_runtime, task).await;
                    });
                }
                tokio::time::sleep(Duration::from_millis(60)).await;
            }
            _ => tokio::time::sleep(Duration::from_millis(250)).await,
        }
    }
}

fn build_login_webview(label: String) -> WebviewBuilder<tauri::Wry> {
    let blank_url = "about:blank".parse().expect("about:blank must parse");
    WebviewBuilder::new(label, WebviewUrl::External(blank_url))
        .focused(true)
        .accept_first_mouse(true)
        .devtools(cfg!(debug_assertions))
        .user_agent(DOUYIN_CHROME_USER_AGENT)
        .on_navigation(|url| {
            url.scheme() == "about"
                || url
                    .domain()
                    .is_some_and(|domain| domain == "douyin.com" || domain.ends_with(".douyin.com"))
        })
}

#[cfg(not(any(target_os = "macos", target_os = "ios")))]
fn webview_data_root(app: &tauri::AppHandle) -> Result<std::path::PathBuf, String> {
    #[cfg(target_os = "windows")]
    {
        // WebView2 user-data folders are machine-local runtime state. Keeping
        // them out of roaming AppData avoids Windows profile synchronization
        // and environment-lock stalls while a child WebView is being created.
        return app
            .path()
            .app_local_data_dir()
            .map_err(|error| format!("读取本机浏览器数据目录失败：{error}"));
    }
    #[cfg(not(target_os = "windows"))]
    {
        app.path()
            .app_data_dir()
            .map_err(|error| format!("读取浏览器数据目录失败：{error}"))
    }
}

#[tauri::command]
async fn open_account_rebind(
    app: tauri::AppHandle,
    window: tauri::Window,
    runtime: tauri::State<'_, Arc<EngineRuntime>>,
    account_id: String,
    bounds: BrowserBounds,
) -> Result<String, String> {
    let account_id = account_id.trim().to_string();
    if account_id.is_empty() {
        return Err("账号标识不能为空".into());
    }
    let label = rebind_webview_label(&account_id, window.label());
    if let Some(webview) = app.get_webview(&label) {
        webview
            .set_position(LogicalPosition::new(bounds.x, bounds.y))
            .map_err(|error| format!("定位登录页面失败：{error}"))?;
        webview
            .set_size(LogicalSize::new(
                bounds.width.max(360.0),
                bounds.height.max(280.0),
            ))
            .map_err(|error| format!("调整登录页面失败：{error}"))?;
        webview
            .show()
            .map_err(|error| format!("显示登录页面失败：{error}"))?;
        return Ok(label);
    }

    let runtime = runtime.inner().clone();
    let result = native_engine_request(
        runtime.clone(),
        "account.native_credential",
        json!({ "account_id": account_id, "secret": runtime.native_secret }),
    )
    .await?;
    let credential: NativeAccountCredential =
        serde_json::from_value(result).map_err(|error| format!("解析账号凭据失败：{error}"))?;
    if credential.account_id != account_id {
        return Err("账号凭据不匹配".into());
    }

    let blank_url = "about:blank"
        .parse()
        .map_err(|error| format!("初始化登录地址失败：{error}"))?;
    let mut builder = WebviewBuilder::new(label.clone(), WebviewUrl::External(blank_url))
        .focused(true)
        .accept_first_mouse(true)
        .devtools(cfg!(debug_assertions))
        .user_agent(DOUYIN_CHROME_USER_AGENT)
        .on_navigation(|url| {
            url.scheme() == "about"
                || url
                    .domain()
                    .is_some_and(|domain| domain == "douyin.com" || domain.ends_with(".douyin.com"))
        });
    #[cfg(any(target_os = "macos", target_os = "ios"))]
    {
        builder = builder.data_store_identifier(rebind_data_store_identifier(&account_id));
    }
    #[cfg(not(any(target_os = "macos", target_os = "ios")))]
    {
        let data_dir = webview_data_root(&app)?
            .join("embedded-browser")
            .join(&account_id);
        std::fs::create_dir_all(&data_dir)
            .map_err(|error| format!("创建登录数据目录失败：{error}"))?;
        builder = builder.data_directory(data_dir);
    }
    let webview = window
        .add_child(
            builder,
            LogicalPosition::new(bounds.x, bounds.y),
            LogicalSize::new(bounds.width.max(360.0), bounds.height.max(280.0)),
        )
        .map_err(|error| format!("创建登录页面失败：{error}"))?;
    if !credential.cookie.trim().is_empty() {
        inject_douyin_cookie_and_confirm(&webview, &credential.cookie).await?;
    }
    let douyin_url: Url = "https://www.douyin.com/"
        .parse()
        .map_err(|error| format!("解析抖音地址失败：{error}"))?;
    webview
        .navigate(douyin_url)
        .map_err(|error| format!("加载抖音登录页失败：{error}"))?;
    webview
        .show()
        .map_err(|error| format!("显示登录页面失败：{error}"))?;
    let _ = credential.account_name;
    Ok(label)
}

#[tauri::command]
fn sync_account_rebind(
    app: tauri::AppHandle,
    window: tauri::Window,
    account_id: String,
    bounds: BrowserBounds,
) -> Result<(), String> {
    let webview = app
        .get_webview(&rebind_webview_label(account_id.trim(), window.label()))
        .ok_or("登录页面尚未创建")?;
    webview
        .set_position(LogicalPosition::new(bounds.x, bounds.y))
        .map_err(|error| format!("定位登录页面失败：{error}"))?;
    webview
        .set_size(LogicalSize::new(
            bounds.width.max(360.0),
            bounds.height.max(280.0),
        ))
        .map_err(|error| format!("调整登录页面失败：{error}"))
}

#[tauri::command]
async fn complete_account_rebind(
    app: tauri::AppHandle,
    window: tauri::Window,
    runtime: tauri::State<'_, Arc<EngineRuntime>>,
    account_id: String,
) -> Result<Value, String> {
    let account_id = account_id.trim().to_string();
    let label = rebind_webview_label(&account_id, window.label());
    let webview = app
        .get_webview(&label)
        .ok_or("登录页面已关闭，请重新打开")?;
    let raw_cookie = read_authenticated_douyin_cookie(&webview).await?;
    let runtime = runtime.inner().clone();
    native_engine_request(
        runtime.clone(),
        "account.native_replace_cookie",
        json!({ "account_id": account_id, "cookie": raw_cookie, "secret": runtime.native_secret }),
    )
    .await?;
    // The user has just completed a fresh authenticated native-page check.
    // Persist that authoritative browser signal so a transient online API
    // failure cannot leave the previous expired badge visible on Windows.
    let account = native_engine_request(
        runtime.clone(),
        "account.native_set_browser_login_state",
        json!({
            "account_id": account_id,
            "logged_in": true,
            "promote_native_surface": true,
            "secret": runtime.native_secret
        }),
    )
    .await?;
    // Rebind authenticates inside the account-id data store. Point the browser
    // profile key back at the account id so instances stop using any older
    // create-{session} store from a prior scan-login.
    let _ = native_engine_request(
        runtime.clone(),
        "account.native_set_browser_profile_key",
        json!({
            "account_id": account_id,
            "profile_key": "",
            "secret": runtime.native_secret
        }),
    )
    .await;
    webview
        .hide()
        .map_err(|error| format!("隐藏登录页面失败：{error}"))?;
    // Hiding is the visual guarantee. Closing releases the native WebView;
    // if WebKit delays teardown, it must never remain over the main UI.
    let _ = webview.close();
    Ok(account)
}

#[tauri::command]
fn cancel_account_rebind(
    app: tauri::AppHandle,
    window: tauri::Window,
    account_id: String,
) -> Result<(), String> {
    if let Some(webview) = app.get_webview(&rebind_webview_label(account_id.trim(), window.label()))
    {
        webview
            .hide()
            .map_err(|error| format!("隐藏登录页面失败：{error}"))?;
        let _ = webview.close();
    }
    Ok(())
}

#[tauri::command]
async fn open_account_create(
    app: tauri::AppHandle,
    window: tauri::Window,
    session_id: String,
    bounds: BrowserBounds,
) -> Result<String, String> {
    let session_id = session_id.trim().to_string();
    if session_id.is_empty() {
        return Err("登录会话标识不能为空".into());
    }
    let label = create_account_webview_label(&session_id, window.label());
    if let Some(webview) = app.get_webview(&label) {
        webview
            .set_position(LogicalPosition::new(bounds.x, bounds.y))
            .map_err(|error| format!("定位登录页面失败：{error}"))?;
        webview
            .set_size(LogicalSize::new(
                bounds.width.max(360.0),
                bounds.height.max(280.0),
            ))
            .map_err(|error| format!("调整登录页面失败：{error}"))?;
        webview
            .show()
            .map_err(|error| format!("显示登录页面失败：{error}"))?;
        return Ok(label);
    }
    let mut builder = build_login_webview(label.clone());
    #[cfg(any(target_os = "macos", target_os = "ios"))]
    {
        builder = builder.data_store_identifier(rebind_data_store_identifier(&format!(
            "create-{session_id}"
        )));
    }
    #[cfg(not(any(target_os = "macos", target_os = "ios")))]
    {
        let data_dir = webview_data_root(&app)?
            .join("account-create")
            .join(&session_id);
        std::fs::create_dir_all(&data_dir)
            .map_err(|error| format!("创建登录数据目录失败：{error}"))?;
        builder = builder.data_directory(data_dir);
    }
    let webview = window
        .add_child(
            builder,
            LogicalPosition::new(bounds.x, bounds.y),
            LogicalSize::new(bounds.width.max(360.0), bounds.height.max(280.0)),
        )
        .map_err(|error| format!("创建登录页面失败：{error}"))?;
    let douyin_url = "https://www.douyin.com/"
        .parse()
        .map_err(|error| format!("解析抖音地址失败：{error}"))?;
    webview
        .navigate(douyin_url)
        .map_err(|error| format!("加载抖音登录页失败：{error}"))?;
    webview
        .show()
        .map_err(|error| format!("显示登录页面失败：{error}"))?;
    Ok(label)
}

#[tauri::command]
fn sync_account_create(
    app: tauri::AppHandle,
    window: tauri::Window,
    session_id: String,
    bounds: BrowserBounds,
) -> Result<(), String> {
    let webview = app
        .get_webview(&create_account_webview_label(
            session_id.trim(),
            window.label(),
        ))
        .ok_or("登录页面尚未创建")?;
    webview
        .set_position(LogicalPosition::new(bounds.x, bounds.y))
        .map_err(|error| format!("定位登录页面失败：{error}"))?;
    webview
        .set_size(LogicalSize::new(
            bounds.width.max(360.0),
            bounds.height.max(280.0),
        ))
        .map_err(|error| format!("调整登录页面失败：{error}"))
}

#[tauri::command]
async fn complete_account_create(
    app: tauri::AppHandle,
    window: tauri::Window,
    runtime: tauri::State<'_, Arc<EngineRuntime>>,
    session_id: String,
    role: String,
    group_id: Option<String>,
) -> Result<Value, String> {
    let label = create_account_webview_label(session_id.trim(), window.label());
    let webview = app
        .get_webview(&label)
        .ok_or("登录页面已关闭，请重新打开")?;
    let raw_cookie = read_authenticated_douyin_cookie(&webview).await?;
    let runtime = runtime.inner().clone();
    let session_id = session_id.trim().to_string();
    let mut result = native_engine_request(
        runtime.clone(),
        "account.native_create_from_cookie",
        json!({
            "cookie": raw_cookie,
            "role": role,
            "group_id": group_id.unwrap_or_default(),
            "session_id": session_id,
            "secret": runtime.native_secret
        }),
    )
    .await?;
    let account_id = result
        .pointer("/account/id")
        .and_then(Value::as_str)
        .map(str::to_owned)
        .ok_or("新增账号结果缺少账号标识")?;
    let account = native_engine_request(
        runtime.clone(),
        "account.native_set_browser_login_state",
        json!({
            "account_id": account_id,
            "logged_in": true,
            "promote_native_surface": true,
            "secret": runtime.native_secret
        }),
    )
    .await?;
    if let Some(object) = result.as_object_mut() {
        object.insert("account".into(), account);
    }
    // Do NOT close the create WebView before Go has bound browser_profile_key
    // to create-{session}. Instances open that same data store and inherit the
    // live scan-login session — no cookie re-inject required.
    if cfg!(debug_assertions) {
        eprintln!(
            "[embedded-browser] scan-login bound account={account_id} profile_key=create-{session_id}"
        );
    }
    webview
        .hide()
        .map_err(|error| format!("隐藏登录页面失败：{error}"))?;
    let _ = webview.close();
    Ok(result)
}

#[tauri::command]
fn cancel_account_create(
    app: tauri::AppHandle,
    window: tauri::Window,
    session_id: String,
) -> Result<(), String> {
    if let Some(webview) = app.get_webview(&create_account_webview_label(
        session_id.trim(),
        window.label(),
    )) {
        webview
            .hide()
            .map_err(|error| format!("隐藏登录页面失败：{error}"))?;
        let _ = webview.close();
    }
    Ok(())
}

fn apply_browser_geometry(webview: &tauri::Webview, bounds: &BrowserBounds) -> Result<(), String> {
    let width = bounds.width.max(120.0);
    let height = bounds.height.max(90.0);
    // Base the desktop-page scale only on available width. Height changes as
    // cards reflow, so including it caused visible scale jumps while resizing.
    let raw_zoom = (width / 1440.0).clamp(0.26, 0.50);
    let zoom = (raw_zoom * 100.0).round() / 100.0;
    webview
        .set_position(LogicalPosition::new(bounds.x, bounds.y))
        .map_err(|error| format!("定位嵌入浏览器失败：{error}"))?;
    webview
        .set_size(LogicalSize::new(width, height))
        .map_err(|error| format!("调整嵌入浏览器大小失败：{error}"))?;
    webview
        .set_zoom(zoom)
        .map_err(|error| format!("设置嵌入浏览器缩放失败：{error}"))
}

fn apply_browser_bounds(webview: &tauri::Webview, bounds: &BrowserBounds) -> Result<(), String> {
    apply_browser_geometry(webview, bounds)?;
    webview
        .show()
        .map_err(|error| format!("显示嵌入浏览器失败：{error}"))
}

/// How long a not-yet-painted page may stay hidden before the card is told the
/// load is slow, and the hard bound after which it is shown regardless.
const BROWSER_REVEAL_SLOW_AFTER: Duration = Duration::from_secs(6);
const BROWSER_REVEAL_CONTENT_MAX_WAIT: Duration = Duration::from_secs(12);

/// Cheap "has this document actually painted anything?" probe.
///
/// Runs on the async runtime, like the login probe, and deliberately does no
/// DOM traversal — it only measures text length. Readiness must be measured
/// against `textContent`, never `innerText`: innerText is layout-dependent and
/// returns '' for a hidden or zero-sized child WebView, which is exactly the
/// state a not-yet-revealed card is in, so it would never report content.
async fn douyin_page_has_content(webview: &tauri::Webview) -> bool {
    if webview
        .eval(
            r#"(() => {
              try {
                const text = document.body ? document.body.textContent : '';
                const painted = document.readyState !== 'loading' &&
                  Boolean(document.body) &&
                  String(text || '').trim().length > 20;
                document.cookie = 'fubao_paint_probe=' + (painted ? '1' : '0') +
                  '; Path=/; Secure; SameSite=None';
              } catch (_) {
                // An inconclusive frame is "not yet painted", never an error.
              }
            })();"#,
        )
        .is_err()
    {
        return false;
    }
    tokio::time::sleep(Duration::from_millis(90)).await;
    let painted = webview
        .cookies()
        .map(|cookies| {
            cookies
                .iter()
                .find(|cookie| cookie.name() == "fubao_paint_probe")
                .is_some_and(|cookie| cookie.value() == "1")
        })
        .unwrap_or(false);
    let _ = webview
        .eval("document.cookie='fubao_paint_probe=; Path=/; Secure; SameSite=None; Max-Age=0'");
    painted
}

fn schedule_browser_webview_reveal(
    webview: tauri::Webview,
    app: tauri::AppHandle,
    runtime: Arc<EngineRuntime>,
    webview_label: String,
    instance_id: String,
    revealed: Arc<AtomicBool>,
    delay: std::time::Duration,
) {
    // Never call WKWebView/WebView2 show() from a raw OS thread — on macOS that
    // can freeze the whole app. Sleep on the async runtime, then hop to main.
    tauri::async_runtime::spawn(async move {
        tokio::time::sleep(delay).await;
        if revealed
            .compare_exchange(false, true, Ordering::SeqCst, Ordering::SeqCst)
            .is_err()
        {
            return;
        }
        if browser_webview_is_closing(&runtime, &webview_label) {
            return;
        }
        // PageLoadEvent::Finished fires when navigation completes, which for
        // Douyin's SPA is long before first paint. Showing at that point is
        // what put a blank white native surface over the card's own loading
        // state — with no feedback and no way to tell loading from stuck. Wait
        // for real content here, in the async runtime that is already idling,
        // rather than polling from the frontend: an IPC round trip plus a
        // main-thread hop every few hundred milliseconds competes with the very
        // page render being waited on and makes the blank period longer.
        let started = Instant::now();
        let mut slow_reported = false;
        loop {
            if browser_webview_is_closing(&runtime, &webview_label) {
                return;
            }
            if douyin_page_has_content(&webview).await {
                break;
            }
            let waited = started.elapsed();
            if waited >= BROWSER_REVEAL_CONTENT_MAX_WAIT {
                // Show anyway: a partially painted but interactive page is more
                // useful than one that stays hidden indefinitely.
                break;
            }
            if !slow_reported && waited >= BROWSER_REVEAL_SLOW_AFTER {
                slow_reported = true;
                let _ = app.emit(
                    "browser-webview://slow",
                    json!({ "instance_id": instance_id.clone() }),
                );
            }
            tokio::time::sleep(Duration::from_millis(400)).await;
        }
        let (tx, rx) = std::sync::mpsc::channel();
        let dispatch = app.run_on_main_thread(move || {
            let result = webview.show();
            let _ = tx.send(result);
        });
        let show_result = match dispatch {
            Ok(()) => rx
                .recv()
                .unwrap_or_else(|_| Err(tauri::Error::FailedToReceiveMessage)),
            Err(error) => Err(error),
        };
        match show_result {
            Ok(()) => {
                let _ = app.emit(
                    "browser-webview://ready",
                    json!({ "instance_id": instance_id }),
                );
            }
            Err(error) => {
                let _ = app.emit(
                    "browser-webview://load-error",
                    json!({
                        "instance_id": instance_id,
                        "message": format!("显示嵌入浏览器失败：{error}"),
                    }),
                );
            }
        }
    });
}

/// Recovers a card whose Douyin document stays undetermined: rendered far enough
/// to answer script, yet never painting a header or a login entry. Douyin's own
/// skeleton can persist indefinitely in that state, and because the probe is
/// deliberately three-state, neither the anonymous heal nor the login-wall path
/// ever touches it — the card would sit on a gray skeleton forever. Reload it at
/// most once per throttle window and never publish a CK verdict from it.
async fn heal_stalled_browser_render(
    runtime: &Arc<EngineRuntime>,
    webview: &tauri::Webview,
    credential: &NativeBrowserCredential,
    instance_id: &str,
) {
    // A prepared participation context owns this document; its signed request
    // template cannot survive a reload. Room discovery can retry, a rush cannot.
    let participating = runtime
        .browser_red_packet_contexts
        .lock()
        .map(|contexts| contexts.contains(instance_id))
        .unwrap_or(false);
    if participating {
        return;
    }
    let stalled_for = runtime
        .browser_stalled_render_since
        .lock()
        .ok()
        .map(|mut since| {
            since
                .entry(instance_id.to_string())
                .or_insert_with(Instant::now)
                .elapsed()
        })
        .unwrap_or_default();
    if stalled_for < Duration::from_secs(90) {
        return;
    }
    let heal_due = runtime
        .browser_stalled_render_healed_at
        .lock()
        .ok()
        .and_then(|healed| healed.get(instance_id).copied())
        .map_or(true, |last| last.elapsed() >= Duration::from_secs(180));
    if !heal_due {
        return;
    }
    if let Ok(mut healed) = runtime.browser_stalled_render_healed_at.lock() {
        healed.insert(instance_id.to_string(), Instant::now());
    }
    if cfg!(debug_assertions) {
        eprintln!(
            "[embedded-browser] stalled render heal instance={instance_id} stalled_for={}s",
            stalled_for.as_secs()
        );
    }
    let _ = inject_douyin_cookie_and_confirm(webview, &credential.cookie).await;
    let _ = inject_douyin_cookie(webview, &credential.cookie);
    if let Ok(mut injected) = runtime.browser_cookie_injected_at.lock() {
        injected.insert(instance_id.to_string(), Instant::now());
    }
    let _ = webview.eval(
        r#"(() => {
          try { localStorage.clear(); } catch (_) {}
          try { sessionStorage.clear(); } catch (_) {}
          try { location.reload(); } catch (_) {}
        })();"#,
    );
}

fn is_safe_douyin_location(url: &Url) -> bool {
    url.scheme() == "https"
        && url
            .domain()
            .is_some_and(|domain| domain == "douyin.com" || domain.ends_with(".douyin.com"))
}

fn browser_new_window_target(url: &Url) -> Option<Url> {
    is_safe_douyin_location(url).then(|| url.clone())
}

/// Record which account owns a mounted instance so its last safe location can
/// survive the instance record being deleted and created again.
fn remember_browser_instance_account(runtime: &EngineRuntime, instance_id: &str, account_id: &str) {
    if account_id.trim().is_empty() {
        return;
    }
    if let Ok(mut owners) = runtime.browser_instance_accounts.lock() {
        owners.insert(instance_id.to_owned(), account_id.trim().to_owned());
    }
}

fn browser_instance_account(runtime: &EngineRuntime, instance_id: &str) -> Option<String> {
    runtime
        .browser_instance_accounts
        .lock()
        .ok()
        .and_then(|owners| owners.get(instance_id).cloned())
}

fn remember_browser_location(runtime: &EngineRuntime, instance_id: &str, url: &Url) {
    if !is_safe_douyin_location(url) {
        return;
    }
    if let Ok(mut locations) = runtime.browser_locations.lock() {
        locations.insert(instance_id.to_owned(), url.as_str().to_owned());
    }
    if let Some(account_id) = browser_instance_account(runtime, instance_id) {
        if let Ok(mut locations) = runtime.browser_account_locations.lock() {
            locations.insert(account_id, url.as_str().to_owned());
        }
    }
}

fn browser_restore_location(runtime: &EngineRuntime, instance_id: &str) -> Url {
    let remembered = runtime
        .browser_locations
        .lock()
        .ok()
        .and_then(|locations| locations.get(instance_id).cloned())
        .or_else(|| {
            let account_id = browser_instance_account(runtime, instance_id)?;
            runtime
                .browser_account_locations
                .lock()
                .ok()
                .and_then(|locations| locations.get(&account_id).cloned())
        });
    remembered
        .and_then(|location| location.parse::<Url>().ok())
        .filter(is_safe_douyin_location)
        .unwrap_or_else(|| {
            "https://www.douyin.com/"
                .parse::<Url>()
                .expect("static Douyin URL must be valid")
        })
}

fn browser_location_matches_live_room(
    runtime: &EngineRuntime,
    instance_id: &str,
    web_rid: &str,
) -> bool {
    runtime
        .browser_locations
        .lock()
        .ok()
        .and_then(|locations| locations.get(instance_id).cloned())
        .and_then(|location| location.parse::<Url>().ok())
        .is_some_and(|current| {
            current.domain() == Some("live.douyin.com")
                && current.path().trim_matches('/') == web_rid.trim()
        })
}

async fn ensure_browser_webview(
    app: &tauri::AppHandle,
    window: &tauri::Window,
    runtime: Arc<EngineRuntime>,
    instance_id: &str,
    bounds: &BrowserBounds,
    reveal_when_ready: bool,
) -> Result<tauri::Webview, String> {
    let label = browser_webview_label(&instance_id, window.label());
    let was_closing = browser_webview_is_closing(&runtime, &label);
    if was_closing {
        if app.get_webview(&label).is_some() {
            return Err("浏览器实例正在释放，请稍后重试".into());
        }
        runtime
            .browser_webviews_closing
            .lock()
            .map_err(|_| "浏览器实例销毁状态锁不可用")?
            .remove(&label);
    }
    if let Some(webview) = app.get_webview(&label) {
        if reveal_when_ready {
            apply_browser_bounds(&webview, bounds)?;
        }
        return Ok(webview);
    }

    let result = native_engine_request(
        runtime.clone(),
        "browser.native_credential",
        json!({
            "instance_id": instance_id,
            "secret": runtime.native_secret,
        }),
    )
    .await?;
    let credential: NativeBrowserCredential =
        serde_json::from_value(result).map_err(|error| format!("解析浏览器凭据失败：{error}"))?;
    if credential.instance_id != instance_id || credential.account_id.trim().is_empty() {
        return Err("浏览器实例凭据不匹配".into());
    }

    let blank_url = "about:blank"
        .parse()
        .map_err(|error| format!("初始化浏览器地址失败：{error}"))?;
    let ready_app = app.clone();
    let ready_instance_id = instance_id.to_string();
    let ready_once = Arc::new(AtomicBool::new(false));
    let ready_once_for_page = ready_once.clone();
    // Suppress page-load reveals until cookie bootstrap finishes. Intermediate
    // Finished events during bootstrap used to clear the HTML loader and leave
    // a long blank surface while later reloads were still running.
    let bootstrap_active = Arc::new(AtomicBool::new(true));
    let bootstrap_active_for_page = bootstrap_active.clone();
    let navigation_runtime = runtime.clone();
    let navigation_instance_id = instance_id.to_string();
    let new_window_app = app.clone();
    let new_window_runtime = runtime.clone();
    let new_window_instance_id = instance_id.to_string();
    let new_window_label = label.clone();
    let page_runtime = runtime.clone();
    let page_instance_id = instance_id.to_string();
    // Register ownership before resolving the landing page so a card recreated
    // for this account can resume its last location instead of the home feed.
    remember_browser_instance_account(&runtime, instance_id, &credential.account_id);
    let restore_url = browser_restore_location(&runtime, &instance_id);
    let restore_url_text = restore_url.to_string();
    let mut builder = WebviewBuilder::new(label.clone(), WebviewUrl::External(blank_url))
        .focused(false)
        .accept_first_mouse(true)
        .devtools(cfg!(debug_assertions))
        // Match the Chromium identity used by the legacy 福宝/DY-KIRO CDP
        // browser. Douyin's page-level login gate is UA-sensitive even when
        // its self-profile API accepts the same Cookie.
        .user_agent(DOUYIN_CHROME_USER_AGENT)
        .on_navigation(move |url| {
            if url.scheme() == "about" {
                return true;
            }
            if is_safe_douyin_location(url) {
                remember_browser_location(&navigation_runtime, &navigation_instance_id, url);
                return true;
            }
            false
        })
        .on_new_window(move |url, _features| {
            let Some(target) = browser_new_window_target(&url) else {
                return NewWindowResponse::Deny;
            };
            if browser_webview_is_closing(&new_window_runtime, &new_window_label) {
                return NewWindowResponse::Deny;
            }

            // Douyin opens live-room cards with window.open/target=_blank.
            // Keep the navigation inside the originating account WebView so
            // it retains the isolated profile, Cookie, and page context.
            // Queue it after denying native popup creation to avoid creating
            // an unmanaged WKWebView/WebView2 window.
            let app_for_navigation = new_window_app.clone();
            let runtime_for_navigation = new_window_runtime.clone();
            let instance_for_navigation = new_window_instance_id.clone();
            let label_for_navigation = new_window_label.clone();
            let target_for_navigation = target.clone();
            let _ = new_window_app.run_on_main_thread(move || {
                if browser_webview_is_closing(&runtime_for_navigation, &label_for_navigation) {
                    return;
                }
                if let Some(webview) = app_for_navigation.get_webview(&label_for_navigation) {
                    remember_browser_location(
                        &runtime_for_navigation,
                        &instance_for_navigation,
                        &target_for_navigation,
                    );
                    let _ = webview.navigate(target_for_navigation);
                }
            });
            NewWindowResponse::Deny
        })
        .on_page_load(move |webview, payload| {
            let is_douyin = is_safe_douyin_location(payload.url());
            if is_douyin {
                remember_browser_location(&page_runtime, &page_instance_id, payload.url());
                if payload.event() == PageLoadEvent::Finished {
                    // Install after every real Douyin document load. SPA room
                    // switches keep the same document and therefore retain
                    // the hook; full navigations get a fresh one here.
                    let _ = webview.eval(RED_PACKET_RECEIVE_CAPTURE_SCRIPT);
                }
            }
            if reveal_when_ready
                && payload.event() == PageLoadEvent::Finished
                && is_douyin
                && !bootstrap_active_for_page.load(Ordering::SeqCst)
            {
                // Only post-bootstrap loads may reveal. A short settle lets the
                // SPA replace the blank document without a multi-second gap.
                schedule_browser_webview_reveal(
                    webview.clone(),
                    ready_app.clone(),
                    page_runtime.clone(),
                    webview.label().to_string(),
                    ready_instance_id.clone(),
                    ready_once_for_page.clone(),
                    std::time::Duration::from_millis(350),
                );
            }
        });
    // Prefer scan-login profile key (create-{session}) so the instance opens
    // the same WKWebView store the user just authenticated in.
    let store_key =
        account_browser_store_key(&credential.account_id, &credential.browser_profile_key);
    if cfg!(debug_assertions) {
        eprintln!(
            "[embedded-browser] open instance={} account={} store_key={}",
            instance_id, credential.account_id, store_key
        );
    }
    #[cfg(any(target_os = "macos", target_os = "ios"))]
    {
        builder = builder.data_store_identifier(browser_data_store_identifier(&store_key));
    }
    #[cfg(not(any(target_os = "macos", target_os = "ios")))]
    {
        let data_dir = webview_data_root(&app)?
            .join("embedded-browser")
            .join(&store_key);
        std::fs::create_dir_all(&data_dir)
            .map_err(|error| format!("创建实例数据目录失败：{error}"))?;
        builder = builder.data_directory(data_dir);
    }

    let webview = match window.add_child(
        builder,
        // Create the native surface outside the visible window first. A
        // newly-created WKWebView paints white before its first page
        // frame; keeping it off-card lets the HTML loading state remain
        // visible until `on_page_load(Finished)` reveals the real page.
        LogicalPosition::new(-10_000.0, -10_000.0),
        LogicalSize::new(bounds.width.max(120.0), bounds.height.max(90.0)),
    ) {
        Ok(webview) => webview,
        Err(error) => {
            // Concurrent mount from resize/ready observers can race past the
            // pre-check above. Reuse the winner instead of flashing an error
            // over an already-working Douyin surface.
            let message = error.to_string();
            if message.contains("already exists") {
                if let Some(existing) = app.get_webview(&label) {
                    if reveal_when_ready {
                        apply_browser_bounds(&existing, bounds)?;
                    }
                    return Ok(existing);
                }
            }
            return Err(format!("创建嵌入浏览器失败：{message}"));
        }
    };

    // WebView2 can stall cookie/network init when created fully off-screen.
    // Keep it hidden but on a valid on-screen geometry for Windows profiles.
    #[cfg(target_os = "windows")]
    {
        let safe = BrowserBounds {
            x: bounds.x.max(0.0),
            y: bounds.y.max(0.0),
            width: bounds.width.max(120.0),
            height: bounds.height.max(90.0),
        };
        apply_browser_geometry(&webview, &safe)?;
    }
    #[cfg(not(target_os = "windows"))]
    {
        apply_browser_geometry(&webview, &bounds)?;
    }
    webview
        .hide()
        .map_err(|error| format!("隐藏待加载浏览器失败：{error}"))?;
    let scan_shared_store = credential
        .browser_profile_key
        .trim()
        .starts_with("create-");
    let bootstrap_result = if scan_shared_store {
        // Scan-login instances reuse the create WebView data store that already
        // holds a live Douyin session. Open the page first; only fall back to
        // cookie inject when the store is cold.
        webview
            .navigate(restore_url.clone())
            .map_err(|error| format!("加载抖音页面失败：{error}"))?;
        tokio::time::sleep(Duration::from_millis(1_200)).await;
        let already_in = matches!(
            inspect_douyin_login(&webview).await.map(|s| s.state),
            Ok(BrowserLoginState::LoggedIn)
        );
        if already_in {
            if cfg!(debug_assertions) {
                eprintln!(
                    "[embedded-browser] scan-store warm instance={instance_id} store_key={store_key}"
                );
            }
            if let Ok(mut injected) = runtime.browser_cookie_injected_at.lock() {
                injected.insert(instance_id.to_string(), Instant::now());
            }
            Ok(())
        } else {
            if cfg!(debug_assertions) {
                eprintln!(
                    "[embedded-browser] scan-store cold, inject fallback instance={instance_id}"
                );
            }
            bootstrap_browser_account_session(
                &webview,
                runtime.as_ref(),
                instance_id,
                &credential.cookie,
                restore_url,
            )
            .await
        }
    } else {
        bootstrap_browser_account_session(
            &webview,
            runtime.as_ref(),
            instance_id,
            &credential.cookie,
            restore_url,
        )
        .await
    };
    if let Err(error) = bootstrap_result {
        // Still expose the native surface so the card is not permanently dead.
        // Cookie/login can recover on the next sync or user rebind.
        if cfg!(debug_assertions) {
            eprintln!(
                "[embedded-browser] bootstrap soft-failed instance={instance_id}: {error}"
            );
        }
        if let Ok(url) = restore_url_text.parse::<Url>() {
            let _ = webview.navigate(url);
        }
    }
    bootstrap_active.store(false, Ordering::SeqCst);
    // Reveal only after bootstrap is finished. A brief settle covers the final
    // SPA paint without the previous multi-second blank after the HTML loader.
    if reveal_when_ready {
        schedule_browser_webview_reveal(
            webview.clone(),
            app.clone(),
            runtime.clone(),
            webview.label().to_string(),
            instance_id.to_string(),
            ready_once,
            std::time::Duration::from_millis(400),
        );
    }
    if cfg!(debug_assertions) {
        eprintln!(
            "[embedded-browser] mounted label={label} account={} visibility={} url={restore_url_text}",
            credential.account_id,
            if reveal_when_ready {
                "card"
            } else {
                "background"
            }
        );
    }
    let _ = credential.account_name;
    Ok(webview)
}

#[tauri::command]
async fn mount_browser_webview(
    app: tauri::AppHandle,
    window: tauri::Window,
    runtime: tauri::State<'_, Arc<EngineRuntime>>,
    instance_id: String,
    bounds: BrowserBounds,
) -> Result<String, String> {
    let existed = app
        .get_webview(&browser_webview_label(&instance_id, window.label()))
        .is_some();
    let webview = ensure_browser_webview(
        &app,
        &window,
        runtime.inner().clone(),
        &instance_id,
        &bounds,
        true,
    )
    .await?;
    if existed {
        // An off-screen WebView may have been created earlier by a scheduled
        // task. It is already loaded and has no pending first-paint callback,
        // so tell the card to leave its HTML loading placeholder immediately.
        let _ = app.emit(
            "browser-webview://ready",
            json!({ "instance_id": instance_id }),
        );
    }
    Ok(webview.label().to_string())
}

#[tauri::command]
fn sync_browser_webview(
    app: tauri::AppHandle,
    window: tauri::Window,
    runtime: tauri::State<'_, Arc<EngineRuntime>>,
    instance_id: String,
    bounds: BrowserBounds,
    reveal: bool,
) -> Result<(), String> {
    let label = browser_webview_label(&instance_id, window.label());
    if browser_webview_is_closing(runtime.inner().as_ref(), &label) {
        return Err("嵌入浏览器正在释放".into());
    }
    let webview = app.get_webview(&label).ok_or("嵌入浏览器尚未创建")?;
    // Geometry synchronization must not reveal an initial white native
    // surface. Once the frontend has observed the first page-load event,
    // however, a grid-column change needs to re-show the surface that was
    // deliberately hidden during drag. Applying bounds and visibility in one
    // command prevents the card from being stranded on its HTML placeholder.
    if reveal {
        apply_browser_bounds(&webview, &bounds)
    } else {
        apply_browser_geometry(&webview, &bounds)
    }
}

#[tauri::command]
fn hide_browser_webview(
    app: tauri::AppHandle,
    window: tauri::Window,
    runtime: tauri::State<'_, Arc<EngineRuntime>>,
    instance_id: String,
) -> Result<(), String> {
    let label = browser_webview_label(&instance_id, window.label());
    if browser_webview_is_closing(runtime.inner().as_ref(), &label) {
        return Ok(());
    }
    if let Some(webview) = app.get_webview(&label) {
        webview
            .hide()
            .map_err(|error| format!("隐藏嵌入浏览器失败：{error}"))?;
    }
    Ok(())
}

#[tauri::command]
fn close_browser_webview(
    app: tauri::AppHandle,
    window: tauri::Window,
    runtime: tauri::State<'_, Arc<EngineRuntime>>,
    instance_id: String,
) -> Result<(), String> {
    let label = browser_webview_label(&instance_id, window.label());
    let should_close = begin_browser_webview_close(runtime.inner().as_ref(), &label)?;
    if should_close {
        if let Some(webview) = app.get_webview(&label) {
            // The last safe location is already captured by on_navigation and
            // on_page_load. Calling webview.url() here can abort on macOS when
            // WKWebView is concurrently invalidating, so teardown deliberately
            // performs no page inspection and is safe to request repeatedly.
            webview
                .close()
                .map_err(|error| format!("关闭嵌入浏览器失败：{error}"))?;
        }
    }
    if let Ok(mut contexts) = runtime.browser_red_packet_contexts.lock() {
        contexts.remove(&instance_id);
    }
    let runtime = runtime.inner().clone();
    let closed_instance_id = instance_id.clone();
    tauri::async_runtime::spawn(async move {
        let _ = native_engine_request(
            runtime.clone(),
            "red_packet_participation.native_context",
            json!({
                "instance_id": closed_instance_id,
                "ready": false,
                "secret": runtime.native_secret,
            }),
        )
        .await;
    });
    Ok(())
}

#[tauri::command]
async fn prepare_browser_red_packet_context(
    app: tauri::AppHandle,
    window: tauri::Window,
    runtime: tauri::State<'_, Arc<EngineRuntime>>,
    instance_id: String,
    web_rid: String,
    result_only: Option<bool>,
    allow_challenge_recovery: Option<bool>,
    batch_activity_id: Option<String>,
    ready_timeout_seconds: Option<u64>,
) -> Result<String, String> {
    let instance_id = instance_id.trim().to_string();
    let web_rid = web_rid.trim().to_string();
    // Scheduled participation must not depend on whether the browser page is
    // currently visible or whether its card has ever intersected the viewport.
    // Reuse any account WebView already alive in another app page/window; when
    // none exists, create an off-screen native WebView backed by the exact same
    // account-keyed data store and Cookie injection path as a visible card.
    let (webview, created_background) = if let Some(webview) =
        browser_webview_for_instance(&app, runtime.inner().as_ref(), &instance_id)
    {
        if cfg!(debug_assertions) {
            eprintln!("[redpacket-schedule] reused page context instance={instance_id}");
        }
        (webview, false)
    } else {
        let webview = ensure_browser_webview(
            &app,
            &window,
            runtime.inner().clone(),
            &instance_id,
            &BrowserBounds {
                x: -10_000.0,
                y: -10_000.0,
                width: 1280.0,
                height: 720.0,
            },
            false,
        )
        .await?;
        if cfg!(debug_assertions) {
            eprintln!(
                "[redpacket-schedule] created background page context instance={instance_id}"
            );
        }
        (webview, true)
    };
    navigate_browser_to_live_room(&webview, runtime.inner().as_ref(), &instance_id, &web_rid)
        .await?;
    // After www.douyin.com → live.douyin.com navigation the SPA commonly sits
    // in Unknown for several seconds even with a valid jar. Wait longer so the
    // red-packet icon does not immediately toast “尚未完成加载”.
    //
    // A batch/rotation caller passes a shorter bound: the packet window is only
    // a minute or two, so holding a rotation slot for the full manual wait
    // costs more than simply giving the slot to the next account. A manual
    // “准备页面上下文” keeps the long wait.
    let ready_timeout = Duration::from_secs(ready_timeout_seconds.unwrap_or(18).clamp(4, 60));
    let snapshot = wait_for_douyin_login_ready(&webview, ready_timeout).await?;
    match snapshot.state {
        BrowserLoginState::LoggedIn => {}
        BrowserLoginState::LoggedOut => return Err("当前实例尚未登录，请先重新绑定 CK".into()),
        BrowserLoginState::Unknown => {
            // Last chance: if login cookies are present, allow prepare. Join
            // itself will still re-check; a false Unknown must not block start.
            if snapshot.raw_cookie.as_ref().is_some_and(|c| !c.trim().is_empty()) {
                if cfg!(debug_assertions) {
                    eprintln!(
                        "[redpacket-prepare] accept jar-present Unknown as ready instance={instance_id}"
                    );
                }
            } else {
                return Err("直播页面尚未完成加载，请稍后重试".into());
            }
        }
    }
    let runtime = runtime.inner().clone();
    if let Err(error) = native_engine_request(
        runtime.clone(),
        "red_packet_participation.native_context",
        json!({
            "instance_id": instance_id,
            "ready": true,
            "result_only": result_only.unwrap_or(false),
            "allow_challenge_recovery": allow_challenge_recovery.unwrap_or(false),
            "batch_activity_id": batch_activity_id.unwrap_or_default(),
            "secret": runtime.native_secret,
        }),
    )
    .await
    {
        // A context created only for a scheduled/background run must not stay
        // alive without the matching Go participation lease. Existing visible
        // card WebViews remain untouched when admission is temporarily denied.
        if created_background {
            let label = webview.label().to_string();
            if begin_browser_webview_close(runtime.as_ref(), &label).unwrap_or(false) {
                let _ = webview.close();
            }
        }
        return Err(error);
    }
    runtime
        .browser_red_packet_contexts
        .lock()
        .map_err(|_| "红包页面上下文状态锁不可用")?
        .insert(instance_id);
    Ok(format!("https://live.douyin.com/{web_rid}"))
}

#[tauri::command]
/// Points a finished participation instance's landing-page memory back at the
/// Douyin home page.
///
/// A live room is the heaviest page this app ever hosts — video plus gift
/// animations — and it is also what the landing-page memory would restore on
/// the next mount, so a finished account would otherwise resume a stale room.
///
/// This deliberately only rewrites the remembered location and does **not**
/// navigate the live document. The `bdms.js` fetch hook that re-signs webcast
/// requests lives in the `live.douyin.com` document; navigating away destroys
/// it while the cached request template and the registered context survive, so
/// a join dispatched in that window goes out unsigned and is bare soft-denied.
/// The card is closed or reused shortly afterwards anyway, and the next mount
/// then lands on the light home feed — which was the whole point.
async fn reset_browser_landing_page(
    runtime: tauri::State<'_, Arc<EngineRuntime>>,
    instance_id: String,
) -> Result<(), String> {
    let instance_id = instance_id.trim().to_string();
    if instance_id.is_empty() {
        return Err("浏览器实例参数无效".into());
    }
    let runtime = runtime.inner().clone();
    let target: Url = "https://www.douyin.com/"
        .parse()
        .map_err(|error| format!("解析抖音首页地址失败：{error}"))?;
    remember_browser_location(&runtime, &instance_id, &target);
    Ok(())
}

#[tauri::command]
async fn stop_browser_red_packet_context(
    runtime: tauri::State<'_, Arc<EngineRuntime>>,
    instance_id: String,
) -> Result<(), String> {
    let instance_id = instance_id.trim().to_string();
    if instance_id.is_empty() {
        return Err("浏览器实例参数无效".into());
    }
    let runtime = runtime.inner().clone();
    native_engine_request(
        runtime.clone(),
        "red_packet_participation.native_context",
        json!({
            "instance_id": instance_id,
            "ready": false,
            "secret": runtime.native_secret,
        }),
    )
    .await?;
    runtime
        .browser_red_packet_contexts
        .lock()
        .map_err(|_| "红包页面上下文状态锁不可用")?
        .remove(&instance_id);
    Ok(())
}

#[cfg(not(any(target_os = "macos", target_os = "ios")))]
fn embedded_browser_data_dir(
    app: &tauri::AppHandle,
    account_id: &str,
) -> Result<std::path::PathBuf, String> {
    Ok(webview_data_root(app)?
        .join("embedded-browser")
        .join(account_id.trim()))
}

/// Close account WebViews and wipe the Windows WebView2 user-data folder so the
/// next mount cannot resume an anonymous jar that rejected the imported CK.
async fn rebuild_browser_account_session_inner(
    app: &tauri::AppHandle,
    runtime: Arc<EngineRuntime>,
    instance_id: &str,
) -> Result<(), String> {
    let instance_id = instance_id.trim();
    if instance_id.is_empty() {
        return Err("浏览器实例标识无效".into());
    }
    let result = native_engine_request(
        runtime.clone(),
        "browser.native_credential",
        json!({
            "instance_id": instance_id,
            "secret": runtime.native_secret,
        }),
    )
    .await?;
    let credential: NativeBrowserCredential =
        serde_json::from_value(result).map_err(|error| format!("解析浏览器凭据失败：{error}"))?;

    for webview in browser_webviews_for_instance(app, runtime.as_ref(), instance_id) {
        let label = webview.label().to_string();
        let _ = begin_browser_webview_close(runtime.as_ref(), &label);
        let _ = webview.close();
    }
    if let Ok(mut contexts) = runtime.browser_red_packet_contexts.lock() {
        contexts.remove(instance_id);
    }
    if let Ok(mut locations) = runtime.browser_locations.lock() {
        locations.remove(instance_id);
    }
    if let Ok(mut injected) = runtime.browser_cookie_injected_at.lock() {
        injected.remove(instance_id);
    }
    if let Ok(mut synced) = runtime.browser_cookie_synced_at.lock() {
        synced.remove(instance_id);
    }

    // Give WebView2 time to release profile file locks before deleting.
    tokio::time::sleep(Duration::from_millis(450)).await;

    #[cfg(not(any(target_os = "macos", target_os = "ios")))]
    {
        let data_dir = embedded_browser_data_dir(app, &credential.account_id)?;
        if data_dir.exists() {
            let mut last_error = String::new();
            for attempt in 0..14 {
                match std::fs::remove_dir_all(&data_dir) {
                    Ok(()) => {
                        last_error.clear();
                        break;
                    }
                    Err(error) => {
                        last_error = error.to_string();
                        if attempt + 1 < 14 {
                            tokio::time::sleep(Duration::from_millis(220)).await;
                        }
                    }
                }
            }
            if data_dir.exists() {
                return Err(format!(
                    "清理内嵌浏览器数据目录失败：{}",
                    if last_error.is_empty() {
                        "目录仍被占用".to_string()
                    } else {
                        last_error
                    }
                ));
            }
        }
        std::fs::create_dir_all(&data_dir)
            .map_err(|error| format!("重建内嵌浏览器数据目录失败：{error}"))?;
    }

    if cfg!(debug_assertions) {
        eprintln!(
            "[embedded-browser] profile rebuilt instance={instance_id} account={}",
            credential.account_id
        );
    }
    // Re-seed the account-keyed store from the Go Cookie before the next
    // mount. This is what makes scan-login instances recover after delete /
    // recreate without relying only on inject-from-blank.
    if !credential.cookie.trim().is_empty() {
        if let Some(window) = app
            .get_window(MAIN_WINDOW_LABEL)
            .or_else(|| app.windows().into_values().next())
        {
            if let Err(error) = seed_account_browser_data_store(
                app,
                &window,
                runtime.as_ref(),
                &credential.account_id,
                &credential.cookie,
            )
            .await
            {
                if cfg!(debug_assertions) {
                    eprintln!(
                        "[embedded-browser] rebuild seed soft-failed instance={instance_id}: {error}"
                    );
                }
            }
        }
    }
    let _ = credential.account_name;
    Ok(())
}

#[tauri::command]
async fn rebuild_browser_account_session(
    app: tauri::AppHandle,
    runtime: tauri::State<'_, Arc<EngineRuntime>>,
    instance_id: String,
) -> Result<(), String> {
    rebuild_browser_account_session_inner(&app, runtime.inner().clone(), instance_id.trim()).await
}

#[tauri::command]
async fn refresh_browser_account_cookie(
    app: tauri::AppHandle,
    runtime: tauri::State<'_, Arc<EngineRuntime>>,
    instance_id: String,
    force_profile_reset: Option<bool>,
) -> Result<(), String> {
    let instance_id = instance_id.trim().to_string();
    if force_profile_reset.unwrap_or(false) {
        // Destroy anonymous WebView2 state first; the next mount re-injects
        // the canonical store Cookie into a clean profile.
        return rebuild_browser_account_session_inner(
            &app,
            runtime.inner().clone(),
            &instance_id,
        )
        .await;
    }
    let webviews = browser_webviews_for_instance(&app, runtime.inner().as_ref(), &instance_id);
    if webviews.is_empty() {
        // An unmounted instance receives the latest canonical Cookie during
        // its next mount, so there is nothing to refresh now.
        return Ok(());
    }
    let runtime = runtime.inner().clone();
    let result = native_engine_request(
        runtime.clone(),
        "browser.native_credential",
        json!({
            "instance_id": instance_id,
            "secret": runtime.native_secret,
        }),
    )
    .await?;
    let credential: NativeBrowserCredential =
        serde_json::from_value(result).map_err(|error| format!("解析浏览器凭据失败：{error}"))?;
    let douyin_url: Url = "https://www.douyin.com/"
        .parse()
        .map_err(|error| format!("解析抖音地址失败：{error}"))?;
    for webview in webviews {
        // Clear residual anonymous rows before re-applying the store Cookie.
        clear_douyin_session_cookies(&webview).await;
        bootstrap_browser_account_session(
            &webview,
            runtime.as_ref(),
            &instance_id,
            &credential.cookie,
            douyin_url.clone(),
        )
        .await
        .map_err(|error| format!("刷新账号登录状态失败：{error}"))?;
    }
    Ok(())
}

#[tauri::command]
async fn sync_browser_account_cookie(
    app: tauri::AppHandle,
    window: tauri::Window,
    runtime: tauri::State<'_, Arc<EngineRuntime>>,
    instance_id: String,
    require_logged_in: Option<bool>,
) -> Result<bool, String> {
    let require_logged_in = require_logged_in.unwrap_or(false);
    let label = browser_webview_label(&instance_id, window.label());
    let Some(webview) = app
        .get_webview(&label)
        .or_else(|| browser_webview_for_instance(&app, runtime.inner().as_ref(), &instance_id))
    else {
        if require_logged_in {
            return Err("当前卡片登录页面尚未就绪，请等待卡片加载完成后重试".into());
        }
        if cfg!(debug_assertions) {
            eprintln!(
                "[embedded-browser] cookie-sync skipped instance={instance_id} reason=not-mounted"
            );
        }
        return Ok(false);
    };
    let mut snapshot = inspect_douyin_login(&webview).await?;
    let runtime = runtime.inner().clone();
    let result = native_engine_request(
        runtime.clone(),
        "browser.native_credential",
        json!({
            "instance_id": instance_id,
            "secret": runtime.native_secret,
        }),
    )
    .await?;
    let credential: NativeBrowserCredential =
        serde_json::from_value(result).map_err(|error| format!("解析浏览器凭据失败：{error}"))?;
    // Fresh mounts inject the store Cookie then navigate. Douyin SPA can paint
    // a login shell briefly while the session is still restoring. Only during
    // this grace may a login wall be ignored — afterwards Windows often keeps
    // rejected inject cookies in the jar and must show CK 已失效.
    let grace = if cfg!(target_os = "windows") {
        Duration::from_secs(20)
    } else {
        Duration::from_secs(30)
    };
    let within_injection_grace = runtime
        .browser_cookie_injected_at
        .lock()
        .ok()
        .and_then(|injected| injected.get(&instance_id).copied())
        .is_some_and(|injected_at| injected_at.elapsed() < grace);
    if snapshot.state == BrowserLoginState::LoggedOut && within_injection_grace {
        if !require_logged_in {
            if cfg!(debug_assertions) {
                eprintln!(
                    "[embedded-browser] login-state deferred instance={instance_id} reason=profile-bootstrap"
                );
            }
            return Ok(false);
        }
        // An explicit open action needs a definitive result. Give the newly
        // injected Windows profile a bounded page-restoration window instead
        // of failing on its first transient login-dialog frame.
        for _ in 0..10 {
            tokio::time::sleep(Duration::from_millis(500)).await;
            snapshot = inspect_douyin_login(&webview).await?;
            if snapshot.state != BrowserLoginState::LoggedOut {
                break;
            }
        }
    }
    // During bootstrap only: a login wall while store cookies are still present
    // may be a transient SPA frame. After the grace window, login wall means
    // the session did not take — keep LoggedOut so the UI can show CK 已失效
    // even though inject left tokens in the WebView2 jar.
    if snapshot.state == BrowserLoginState::LoggedOut && within_injection_grace {
        if let Ok(current) = read_douyin_cookie(&webview) {
            if login_cookie_snapshot_matches(&credential.cookie, &current)
                || browser_login_cookie_is_safe_to_persist(&credential.cookie, &current)
            {
                if cfg!(debug_assertions) {
                    eprintln!(
                        "[embedded-browser] login-state demoted-to-unknown instance={instance_id} reason=login-cookies-still-present"
                    );
                }
                snapshot = BrowserLoginSnapshot {
                    raw_cookie: Some(current),
                    state: BrowserLoginState::Unknown,
                    anonymous_render: snapshot.anonymous_render,
                    clean_render: snapshot.clean_render,
                    render_diagnostics: snapshot.render_diagnostics,
                };
            }
        }
    }
    // Only a rendered, login-free page ends a wall episode. Clearing it on any
    // non-wall frame would let a mid-reload frame reset the timer forever.
    if snapshot.state == BrowserLoginState::LoggedIn && snapshot.clean_render {
        if let Ok(mut since) = runtime.browser_login_wall_since.lock() {
            since.remove(&instance_id);
        }
    }
    // Douyin can render its anonymous shell when the document loaded before the
    // injected cookies landed. The jar still holds the store session, so this is
    // a stale render rather than CK expiry. Reload it (throttled) and report
    // Unknown meanwhile: claiming a healthy login here is exactly what hid a
    // visibly logged-out card behind “浏览器实例登录状态正常”. Explicit actions
    // keep their existing verdict so a user-initiated open is never blocked.
    if !snapshot.anonymous_render {
        let last_anonymous = runtime
            .browser_anonymous_last_at
            .lock()
            .ok()
            .and_then(|last| last.get(&instance_id).copied());
        // Leaving the anonymous state needs positive evidence — a fully rendered
        // page without a login entry — sustained past the quiet period. An
        // undetermined frame keeps the history untouched, because a reloading
        // document has no login entry yet and would otherwise read as recovery.
        let recovered = last_anonymous
            .is_none_or(|last| snapshot.clean_render && last.elapsed() >= Duration::from_secs(120));
        if recovered {
            if let Ok(mut since) = runtime.browser_anonymous_since.lock() {
                since.remove(&instance_id);
            }
            if let Ok(mut last) = runtime.browser_anonymous_last_at.lock() {
                last.remove(&instance_id);
            }
            if let Ok(mut healed) = runtime.browser_anonymous_healed_at.lock() {
                healed.remove(&instance_id);
            }
        } else if !require_logged_in {
            // An anonymous history is still outstanding. Publishing a healthy
            // login here is what let a visibly logged-out card keep reporting
            // CK 正常 between two anonymous frames.
            return Ok(false);
        }
    } else if !require_logged_in
        && !within_injection_grace
        // A definitive login wall must reach the verdict logic below. Healing it
        // here would swallow the one page signal Douyin gives us that is strong
        // enough to expire a credential.
        && snapshot.state != BrowserLoginState::LoggedOut
    {
        let jar_has_store_login = snapshot
            .raw_cookie
            .as_deref()
            .is_some_and(|current| login_cookie_snapshot_usable(&credential.cookie, current));
        if jar_has_store_login {
            // A prepared participation context owns this page: the live room was
            // navigated and its signed request template lives in that document.
            // Clearing SPA storage and reloading underneath it destroys exactly
            // the context the running task depends on, which surfaces as
            // “浏览器实例未进入目标直播间”. An anonymous frame during participation is
            // for the participation path itself to resolve, not for this poller.
            let participating = runtime
                .browser_red_packet_contexts
                .lock()
                .map(|contexts| contexts.contains(&instance_id))
                .unwrap_or(false);
            if participating {
                return Ok(false);
            }
            let anonymous_for = runtime
                .browser_anonymous_since
                .lock()
                .ok()
                .map(|mut since| {
                    since
                        .entry(instance_id.clone())
                        .or_insert_with(Instant::now)
                        .elapsed()
                })
                .unwrap_or_default();
            if let Ok(mut last) = runtime.browser_anonymous_last_at.lock() {
                last.insert(instance_id.clone(), Instant::now());
            }
            let heal_due = runtime
                .browser_anonymous_healed_at
                .lock()
                .ok()
                .and_then(|healed| healed.get(&instance_id).copied())
                .map_or(true, |last| last.elapsed() >= Duration::from_secs(180));
            if heal_due {
                if let Ok(mut healed) = runtime.browser_anonymous_healed_at.lock() {
                    healed.insert(instance_id.clone(), Instant::now());
                }
                if cfg!(debug_assertions) {
                    eprintln!(
                        "[embedded-browser] anonymous render heal instance={instance_id} anonymous_for={}s",
                        anonymous_for.as_secs()
                    );
                }
                let _ = inject_douyin_cookie_and_confirm(&webview, &credential.cookie).await;
                let _ = inject_douyin_cookie(&webview, &credential.cookie);
                if let Ok(mut injected) = runtime.browser_cookie_injected_at.lock() {
                    injected.insert(instance_id.clone(), Instant::now());
                }
                let _ = webview.eval(
                    r#"(() => {
                      try { localStorage.clear(); } catch (_) {}
                      try { sessionStorage.clear(); } catch (_) {}
                      try { location.reload(); } catch (_) {}
                    })();"#,
                );
            }
            // Deliberately no CK verdict here. This DOM signal is reliable enough
            // to trigger a reload, but not to expire a credential: the card page
            // is small, scrollable, and intermittently shows Douyin's own login
            // panel, so it cannot distinguish “session rejected” from “header not
            // painted”. An authenticated business request is the only evidence
            // allowed to expire CK.
            return Ok(false);
        }
    }
    let mut cookie_persisted = false;
    // Never overwrite the Go store from a card still inside the inject grace
    // window — the jar is often a partial inject snapshot and would destroy a
    // complete scan-login Cookie (observed as shrinking 8k→5k captures).
    if snapshot.state == BrowserLoginState::LoggedIn && !within_injection_grace {
        if let Some(raw_cookie) = snapshot.raw_cookie.as_deref() {
            let cookie_changed =
                canonical_cookie_values(raw_cookie) != canonical_cookie_values(&credential.cookie);
            let login_cookie_changed =
                login_cookie_values(raw_cookie) != login_cookie_values(&credential.cookie);
            let auxiliary_sync_due = runtime
                .browser_cookie_synced_at
                .lock()
                .ok()
                .and_then(|synced| synced.get(&instance_id).copied())
                .map_or(true, |last_sync| {
                    last_sync.elapsed() >= Duration::from_secs(60)
                });
            let safe_to_persist =
                browser_login_cookie_is_safe_to_persist(&credential.cookie, raw_cookie);
            // Login-cookie changes are persisted immediately. Douyin rotates
            // auxiliary browser cookies frequently, so complete snapshots are
            // rate-limited to once per minute unless an explicit open action
            // needs the freshest possible account state. Never persist a
            // partial browser read that would drop store login cookies.
            if safe_to_persist
                && cookie_changed
                && (require_logged_in || login_cookie_changed || auxiliary_sync_due)
            {
                native_engine_request(
                    runtime.clone(),
                    "account.native_replace_cookie",
                    json!({
                        "account_id": credential.account_id,
                        "cookie": raw_cookie,
                        "secret": runtime.native_secret,
                    }),
                )
                .await?;
                cookie_persisted = true;
                if let Ok(mut synced) = runtime.browser_cookie_synced_at.lock() {
                    synced.insert(instance_id.clone(), Instant::now());
                }
            } else if cfg!(debug_assertions) && cookie_changed && !safe_to_persist {
                eprintln!(
                    "[embedded-browser] cookie-replace skipped instance={instance_id} reason=partial-browser-snapshot"
                );
            }
        }
    }
    if snapshot.state == BrowserLoginState::Unknown {
        if cfg!(debug_assertions) {
            eprintln!(
                "[embedded-browser] login-state unknown instance={instance_id} render={} jar={}",
                snapshot.render_diagnostics.as_deref().unwrap_or("none"),
                snapshot.raw_cookie.as_deref().map_or(0, str::len),
            );
        }
        if !require_logged_in && !within_injection_grace {
            heal_stalled_browser_render(&runtime, &webview, &credential, &instance_id).await;
        }
        return Ok(false);
    }
    if let Ok(mut since) = runtime.browser_stalled_render_since.lock() {
        since.remove(&instance_id);
    }
    if let Ok(mut healed) = runtime.browser_stalled_render_healed_at.lock() {
        healed.remove(&instance_id);
    }
    if require_logged_in && snapshot.state != BrowserLoginState::LoggedIn {
        // Explicit open failures must not permanently expire a still-valid
        // store Cookie when the page is merely still bootstrapping.
        if within_injection_grace || snapshot.raw_cookie.is_some() {
            return Err("当前卡片登录页面尚未就绪，请等待加载完成后重试".into());
        }
        native_engine_request(
            runtime.clone(),
            "account.native_set_browser_login_state",
            json!({
                "account_id": credential.account_id,
                "logged_in": false,
                "secret": runtime.native_secret,
            }),
        )
        .await?;
        return Err("当前卡片没有检测到有效登录状态，请先完成登录或重新绑定 CK".into());
    }
    let desired_status = if snapshot.state == BrowserLoginState::LoggedIn {
        "valid"
    } else {
        "expired"
    };
    // Only a definitive logout wall without usable login cookies may expire CK.
    // Scan-login cards often paint a transient login shell while injected
    // sessionid_* rows are already in the jar — treating that as CK expiry
    // permanently breaks the next remount.
    if snapshot.state == BrowserLoginState::LoggedOut {
        if within_injection_grace {
            return Ok(false);
        }
        let jar = snapshot.raw_cookie.as_deref().unwrap_or("");
        let store_has_login = !login_cookie_values(&credential.cookie).is_empty();
        let jar_has_login = !login_cookie_values(jar).is_empty()
            || login_cookie_snapshot_usable(&credential.cookie, jar);
        // An already-expired account needs no deferral: the verdict it would
        // reach is the one it already carries.
        if store_has_login && jar_has_login && credential.cookie_status != "expired" {
            // Give one re-assert per wall episode, then accept the verdict. The
            // previous unbounded deferral also re-armed the inject grace on every
            // pass, so an account Douyin had permanently rejected could never be
            // reported and its card kept offering no way out.
            let mut first_frame = false;
            let wall_for = runtime
                .browser_login_wall_since
                .lock()
                .ok()
                .map(|mut since| {
                    let entry = since.entry(instance_id.clone()).or_insert_with(|| {
                        first_frame = true;
                        Instant::now()
                    });
                    entry.elapsed()
                })
                .unwrap_or_default();
            if wall_for < Duration::from_secs(90) {
                if cfg!(debug_assertions) {
                    eprintln!(
                        "[embedded-browser] login-state defer expire instance={instance_id} reason=login-cookies-present wall_for={}s",
                        wall_for.as_secs()
                    );
                }
                if first_frame {
                    let _ = inject_douyin_cookie(&webview, &credential.cookie);
                    if let Ok(mut injected) = runtime.browser_cookie_injected_at.lock() {
                        injected.insert(instance_id.clone(), Instant::now());
                    }
                }
                return Ok(false);
            }
            // Expired instances keep the fast 5s cadence, so log the verdict only
            // when it is actually a transition.
            if cfg!(debug_assertions) && credential.cookie_status != "expired" {
                eprintln!(
                    "[embedded-browser] login-state expire instance={instance_id} reason=persistent-login-wall wall_for={}s",
                    wall_for.as_secs()
                );
            }
        }
        if store_has_login && !jar_has_login {
            // Jar lost login rows — re-inject instead of expiring a fresh scan CK.
            if cfg!(debug_assertions) {
                eprintln!(
                    "[embedded-browser] login-state re-inject instance={instance_id} reason=jar-missing-login"
                );
            }
            let _ = inject_douyin_cookie_and_confirm(&webview, &credential.cookie).await;
            let _ = inject_douyin_cookie(&webview, &credential.cookie);
            if let Ok(mut injected) = runtime.browser_cookie_injected_at.lock() {
                injected.insert(instance_id.clone(), Instant::now());
            }
            return Ok(false);
        }
    }
    // Clearing an expired badge needs authoritative proof, not a DOM frame. A
    // client restart renders www.douyin.com before Douyin paints its login
    // dialog, so a page probe momentarily looks signed in and used to promote a
    // session the follow-live interface was still rejecting with `20003`. The
    // authoritative probe and an explicit rebind remain able to clear it.
    if desired_status == "valid" && credential.cookie_status == "expired" && !cookie_persisted {
        return Ok(false);
    }
    if !cookie_persisted && credential.cookie_status == desired_status {
        if cfg!(debug_assertions) {
            eprintln!(
                "[embedded-browser] login-state unchanged instance={instance_id} state={:?}",
                snapshot.state
            );
        }
        return Ok(false);
    }
    native_engine_request(
        runtime.clone(),
        "account.native_set_browser_login_state",
        json!({
            "account_id": credential.account_id,
            "logged_in": snapshot.state == BrowserLoginState::LoggedIn,
            "secret": runtime.native_secret,
        }),
    )
    .await?;
    if cfg!(debug_assertions) {
        eprintln!(
            "[embedded-browser] login-state synced instance={instance_id} state={:?}",
            snapshot.state
        );
    }
    Ok(true)
}

#[tauri::command]
async fn sync_browser_following_live(
    app: tauri::AppHandle,
    window: tauri::Window,
    runtime: tauri::State<'_, Arc<EngineRuntime>>,
    instance_id: String,
) -> Result<Value, String> {
    let instance_id = instance_id.trim().to_string();
    if instance_id.is_empty() {
        return Err("浏览器实例参数无效".into());
    }
    // Followed-live discovery evaluates its request inside the same account
    // document a participation task runs in. Room discovery can be retried at
    // any time; a red-packet rush window cannot, so never compete for it.
    if runtime
        .browser_red_packet_contexts
        .lock()
        .map(|contexts| contexts.contains(&instance_id))
        .unwrap_or(false)
    {
        return Err("该实例正在参与红包，稍后再读取关注直播".into());
    }
    let label = browser_webview_label(&instance_id, window.label());
    let webview = app
        .get_webview(&label)
        .or_else(|| browser_webview_for_instance(&app, runtime.inner().as_ref(), &instance_id))
        .ok_or("浏览器实例页面尚未就绪，请等待卡片加载完成后重试")?;
    let mut login_state = BrowserLoginState::Unknown;
    for _ in 0..25 {
        login_state = inspect_douyin_login(&webview).await?.state;
        if login_state != BrowserLoginState::Unknown {
            break;
        }
        tokio::time::sleep(Duration::from_millis(200)).await;
    }
    match login_state {
        BrowserLoginState::LoggedIn => {}
        BrowserLoginState::LoggedOut => return Err("当前实例尚未登录，请先重新绑定 CK".into()),
        BrowserLoginState::Unknown => return Err("抖音页面尚未完成加载，请稍后重试".into()),
    }

    let page_result = execute_following_live_in_page(&webview).await?;
    if cfg!(debug_assertions) {
        eprintln!(
            "[following-live-page] instance={} status={} items={} error={}",
            instance_id,
            page_result.status_code,
            page_result.items.len(),
            if page_result.status_msg.trim().is_empty() {
                "none"
            } else {
                "present"
            },
        );
    }
    let runtime = runtime.inner().clone();
    // This is an authenticated business request, so its verdict outranks every
    // DOM heuristic: `20003` is Douyin rejecting the session, `0` proves it took.
    // Publish it so a page that merely looks signed in cannot keep an account
    // marked valid, nor a stale expired badge survive a working session.
    if page_result.status_code == 0 || page_result.status_code == 20003 {
        let logged_in = page_result.status_code == 0;
        let known = native_engine_request(
            runtime.clone(),
            "browser.native_credential",
            json!({
                "instance_id": instance_id,
                "secret": runtime.native_secret,
            }),
        )
        .await
        .ok()
        .and_then(|value| serde_json::from_value::<NativeBrowserCredential>(value).ok());
        let desired = if logged_in { "valid" } else { "expired" };
        if let Some(known) = known.filter(|known| known.cookie_status != desired) {
            if cfg!(debug_assertions) {
                eprintln!(
                    "[following-live-page] login-state authoritative instance={instance_id} status={} -> {desired}",
                    page_result.status_code
                );
            }
            let _ = native_engine_request(
                runtime.clone(),
                "account.native_set_browser_login_state",
                json!({
                    "account_id": known.account_id,
                    "logged_in": logged_in,
                    "secret": runtime.native_secret,
                }),
            )
            .await;
        }
        if logged_in {
            if let Ok(mut since) = runtime.browser_login_wall_since.lock() {
                since.remove(&instance_id);
            }
        }
    }
    if page_result.status_code != 0 {
        let detail = page_result.status_msg.trim();
        let message = if page_result.status_code == 20003 {
            "抖音关注直播接口未识别当前登录状态，请在实例中重新登录后重试".to_string()
        } else if detail.is_empty() {
            format!("关注直播页面请求失败：状态码 {}", page_result.status_code)
        } else {
            format!("关注直播页面请求失败：{detail}")
        };
        return Err(message);
    }

    native_engine_request(
        runtime.clone(),
        "browser.native_following_live",
        json!({
            "instance_id": instance_id,
            "items": page_result.items,
            "secret": runtime.native_secret,
        }),
    )
    .await
}

#[tauri::command]
fn engine_status(runtime: tauri::State<'_, Arc<EngineRuntime>>) -> bool {
    runtime.online.load(Ordering::SeqCst)
}

#[tauri::command]
fn engine_send(
    payload: String,
    runtime: tauri::State<'_, Arc<EngineRuntime>>,
) -> Result<(), String> {
    if payload.len() > 2 * 1024 * 1024 {
        return Err("IPC 消息超过 2 MB 上限".into());
    }
    let mut guard = runtime.child.lock().map_err(|_| "引擎状态锁不可用")?;
    let child = guard.as_mut().ok_or("Go 引擎未运行")?;
    let line = format!("{}\n", payload.trim());
    child
        .write(line.as_bytes())
        .map_err(|error| format!("发送 IPC 消息失败：{error}"))
}

#[tauri::command]
fn engine_restart(
    app: tauri::AppHandle,
    runtime: tauri::State<'_, Arc<EngineRuntime>>,
) -> Result<(), String> {
    if let Some(child) = runtime.child.lock().map_err(|_| "引擎状态锁不可用")?.take() {
        let _ = child.kill();
    }
    runtime.online.store(false, Ordering::SeqCst);
    start_engine(app, runtime.inner().clone())
}

#[tauri::command]
fn start_window_drag(window: tauri::Window) -> Result<(), String> {
    window
        .start_dragging()
        .map_err(|error| format!("开始拖动窗口失败：{error}"))
}

#[tauri::command]
fn toggle_window_maximize(window: tauri::Window) -> Result<(), String> {
    let is_maximized = window
        .is_maximized()
        .map_err(|error| format!("读取窗口大小失败：{error}"))?;
    if is_maximized {
        window
            .unmaximize()
            .map_err(|error| format!("还原窗口失败：{error}"))
    } else {
        window
            .maximize()
            .map_err(|error| format!("放大窗口失败：{error}"))
    }
}

#[tauri::command]
fn frontend_log(level: String, message: String) {
    eprintln!("[frontend:{level}] {message}");
}

#[tauri::command]
fn refresh_window_surface(window: tauri::WebviewWindow) -> Result<(), String> {
    let size = window
        .inner_size()
        .map_err(|error| format!("读取窗口尺寸失败：{error}"))?;
    if size.height > 1 {
        window
            .set_size(tauri::PhysicalSize::new(size.width, size.height - 1))
            .map_err(|error| format!("刷新窗口绘制层失败：{error}"))?;
        window
            .set_size(size)
            .map_err(|error| format!("恢复窗口尺寸失败：{error}"))?;
    }
    window
        .set_focus()
        .map_err(|error| format!("恢复窗口焦点失败：{error}"))
}

#[tauri::command]
async fn open_monitor_log(app: tauri::AppHandle) -> Result<(), String> {
    if let Some(window) = app.get_webview_window("monitor-log") {
        window
            .show()
            .map_err(|error| format!("显示运行日志窗口失败：{error}"))?;
        window
            .set_focus()
            .map_err(|error| format!("聚焦运行日志窗口失败：{error}"))?;
        return Ok(());
    }

    let builder = WebviewWindowBuilder::new(
        &app,
        "monitor-log",
        WebviewUrl::App("index.html?window=monitor-log".into()),
    )
    .title("红包监测运行日志")
    .inner_size(680.0, 520.0)
    .min_inner_size(400.0, 240.0)
    .resizable(true)
    .decorations(true)
    .center()
    .focused(true);

    #[cfg(target_os = "macos")]
    let builder = {
        // Let the HTML header share the white surface with the native traffic
        // lights instead of showing a second gray title strip.
        builder
            .title_bar_style(tauri::TitleBarStyle::Overlay)
            .hidden_title(true)
            .traffic_light_position(LogicalPosition::new(15.0, 20.0))
            .background_color(tauri::webview::Color(255, 255, 255, 255))
    };

    builder
        .build()
        .map(|_| ())
        .map_err(|error| format!("打开运行日志窗口失败：{error}"))
}

#[tauri::command]
async fn open_participation_log(app: tauri::AppHandle) -> Result<(), String> {
    if let Some(window) = app.get_webview_window("participation-log") {
        window
            .show()
            .map_err(|error| format!("显示参与日志窗口失败：{error}"))?;
        window
            .set_focus()
            .map_err(|error| format!("聚焦参与日志窗口失败：{error}"))?;
        return Ok(());
    }
    let builder = WebviewWindowBuilder::new(
        &app,
        "participation-log",
        WebviewUrl::App("index.html?window=participation-log".into()),
    )
    .title("红包参与详细日志")
    .inner_size(780.0, 560.0)
    .min_inner_size(460.0, 280.0)
    .resizable(true)
    .decorations(true)
    .center()
    .focused(true);
    #[cfg(target_os = "macos")]
    let builder = builder
        .title_bar_style(tauri::TitleBarStyle::Overlay)
        .hidden_title(true)
        .traffic_light_position(LogicalPosition::new(15.0, 20.0))
        .background_color(tauri::webview::Color(255, 255, 255, 255));
    builder
        .build()
        .map(|_| ())
        .map_err(|error| format!("打开参与日志窗口失败：{error}"))
}

#[tauri::command]
async fn browser_instance_window_metadata(
    runtime: tauri::State<'_, Arc<EngineRuntime>>,
    instance_id: String,
) -> Result<Value, String> {
    let result = native_engine_request(
        runtime.inner().clone(),
        "browser.native_credential",
        json!({
            "instance_id": instance_id,
            "secret": runtime.native_secret,
        }),
    )
    .await?;
    let credential: NativeBrowserCredential = serde_json::from_value(result)
        .map_err(|error| format!("解析浏览器实例信息失败：{error}"))?;
    Ok(json!({
        "instance_id": credential.instance_id,
        "account_id": credential.account_id,
        "account_name": credential.account_name,
        "cookie_status": credential.cookie_status,
        "surface": credential.surface,
    }))
}

#[tauri::command]
async fn launch_external_chrome_instance(
    runtime: tauri::State<'_, Arc<EngineRuntime>>,
    instance_id: String,
) -> Result<Value, String> {
    let instance_id = instance_id.trim().to_string();
    if instance_id.is_empty() {
        return Err("浏览器实例标识无效".into());
    }
    // Rescue path for import accounts: bypass the embedded-only browser.open
    // gate while leaving the card surface on embedded WebView after sync.
    native_engine_request(
        runtime.inner().clone(),
        "browser.repair_login",
        json!({ "instance_id": instance_id }),
    )
    .await
}

#[tauri::command]
async fn open_browser_instance_window(
    app: tauri::AppHandle,
    runtime: tauri::State<'_, Arc<EngineRuntime>>,
    instance_id: String,
    surface: Option<String>,
) -> Result<(), String> {
    let instance_id = instance_id.trim().to_string();
    if instance_id.is_empty()
        || !instance_id
            .chars()
            .all(|character| character.is_ascii_alphanumeric() || character == '-')
    {
        return Err("浏览器实例标识无效".into());
    }

    let credential_result = native_engine_request(
        runtime.inner().clone(),
        "browser.native_credential",
        json!({
            "instance_id": instance_id,
            "secret": runtime.native_secret,
        }),
    )
    .await?;
    let credential: NativeBrowserCredential = serde_json::from_value(credential_result)
        .map_err(|error| format!("解析浏览器实例信息失败：{error}"))?;
    // Frontend surface is authoritative when the card already classified the
    // account (import vs QR). Fall back to the engine credential for re-open.
    let requested = surface.unwrap_or_default();
    let external_chrome =
        requested.trim() == "external_chrome" || credential.surface.trim() == "external_chrome";
    // Chrome repair is allowed even when the stored CK is marked expired so
    // import accounts can re-establish a live session and sync cookies back.
    if credential.cookie_status == "expired" && !external_chrome {
        return Err("参与账号 CK 已失效，请先重新绑定".into());
    }

    // Separate labels so an earlier embedded open cannot reuse a window that
    // still mounts a child Douyin WebView for an import account.
    let label = if external_chrome {
        format!("external-chrome-{}", safe_window_label_part(&instance_id))
    } else {
        format!("instance-window-{}", safe_window_label_part(&instance_id))
    };
    // Look up the native window rather than only the WebviewWindow wrapper.
    // On Windows the native window can already be registered while the
    // webview lookup is briefly unavailable, which previously sent this path
    // into a duplicate build and surfaced an `already exists` error.
    if let Some(window) = app.get_window(&label) {
        reveal_window(&window)?;
        return Ok(());
    }

    // Embedded instances consume a runtime lease for the native WebView. External
    // Chrome instances take their lease inside browser.open instead.
    if !external_chrome {
        let admission = native_engine_request(
            runtime.inner().clone(),
            "browser.runtime.acquire",
            json!({ "instance_id": instance_id }),
        )
        .await?;
        if admission.get("granted").and_then(Value::as_bool) != Some(true) {
            let position = admission
                .get("queue_position")
                .and_then(Value::as_i64)
                .unwrap_or(1);
            let limit = admission
                .pointer("/capacity/effective_limit")
                .and_then(Value::as_i64)
                .unwrap_or(0);
            return Err(format!(
                "当前设备安全并发为 {limit}，实例已进入等待队列（第 {position} 位）"
            ));
        }
    }

    let route = if external_chrome {
        format!("index.html?window=browser-external&instance={instance_id}")
    } else {
        format!("index.html?window=browser-instance&instance={instance_id}")
    };
    let builder = WebviewWindowBuilder::new(&app, &label, WebviewUrl::App(route.into()))
        .title(format!("福宝浏览器实例 · {}", credential.account_name))
        .inner_size(
            if external_chrome { 420.0 } else { 1080.0 },
            if external_chrome { 320.0 } else { 760.0 },
        )
        .min_inner_size(
            if external_chrome { 360.0 } else { 640.0 },
            if external_chrome { 260.0 } else { 460.0 },
        )
        .resizable(true)
        .decorations(true)
        .center()
        .focused(true);

    #[cfg(target_os = "macos")]
    let builder = builder
        .title_bar_style(tauri::TitleBarStyle::Overlay)
        .hidden_title(true)
        // Align traffic lights with the compact 28px HTML title row.
        .traffic_light_position(LogicalPosition::new(14.0, 14.0))
        .background_color(tauri::webview::Color(255, 255, 255, 255));

    let window = match builder.build() {
        Ok(window) => window,
        Err(error) => {
            // A concurrent second click may race with the first window build.
            // Reuse and activate the winner instead of treating it as a user-
            // visible creation failure.
            if let Some(window) = app.get_window(&label) {
                reveal_window(&window)?;
                return Ok(());
            }
            if !external_chrome {
                let _ = native_engine_request(
                    runtime.inner().clone(),
                    "browser.runtime.release",
                    json!({ "instance_id": instance_id }),
                )
                .await;
            }
            return Err(format!("打开实例窗口失败：{error}"));
        }
    };
    #[cfg(windows)]
    apply_windows_titlebar_palette(&window);
    if let Some(native_window) = app.get_window(&label) {
        reveal_window(&native_window)?;
    } else {
        window
            .show()
            .map_err(|error| format!("显示实例窗口失败：{error}"))?;
        let _ = window.unminimize();
        window
            .set_focus()
            .map_err(|error| format!("聚焦实例窗口失败：{error}"))?;
    }

    // Embedded instance windows own a runtime lease that must be released when
    // the shell is destroyed. Chrome repair shells stop the temporary Chrome
    // process and notify the main UI so cards do not stay on “修复登录中”.
    if external_chrome {
        let close_app = app.clone();
        let close_runtime = runtime.inner().clone();
        let close_instance_id = instance_id.clone();
        let released = Arc::new(AtomicBool::new(false));
        window.on_window_event(move |event| {
            if !matches!(event, WindowEvent::Destroyed) || released.swap(true, Ordering::SeqCst) {
                return;
            }
            let runtime = close_runtime.clone();
            let instance_id = close_instance_id.clone();
            let app = close_app.clone();
            tauri::async_runtime::spawn(async move {
                let _ = native_engine_request(
                    runtime.clone(),
                    "browser.repair_stop",
                    json!({ "instance_id": instance_id }),
                )
                .await;
                let _ = app.emit(
                    "chrome-repair://closed",
                    json!({ "instance_id": instance_id }),
                );
            });
        });
        return Ok(());
    }

    let close_app = app.clone();
    let close_runtime = runtime.inner().clone();
    let close_instance_id = instance_id.clone();
    let close_window_label = label.clone();
    let released = Arc::new(AtomicBool::new(false));
    window.on_window_event(move |event| {
        if !matches!(event, WindowEvent::Destroyed) || released.swap(true, Ordering::SeqCst) {
            return;
        }
        let child_label = browser_webview_label(&close_instance_id, &close_window_label);
        let _ = begin_browser_webview_close(close_runtime.as_ref(), &child_label);
        let _ = close_app.emit(
            "browser-instance-window://closed",
            json!({ "instance_id": close_instance_id }),
        );
        let runtime = close_runtime.clone();
        let instance_id = close_instance_id.clone();
        tauri::async_runtime::spawn(async move {
            let _ = native_engine_request(
                runtime.clone(),
                "browser.runtime.release",
                json!({ "instance_id": instance_id }),
            )
            .await;
        });
    });
    Ok(())
}

#[tauri::command]
async fn open_page_window(app: tauri::AppHandle, view: String) -> Result<(), String> {
    let view = view.trim();
    let title = match view {
        "browsers" => "浏览器实例",
        "accounts" => "账号与红包池",
        _ => return Err("不支持在新窗口打开该页面".into()),
    };
    let label = format!("page-{view}");
    if let Some(window) = app.get_webview_window(&label) {
        window
            .show()
            .map_err(|error| format!("显示页面窗口失败：{error}"))?;
        let _ = window.unminimize();
        window
            .set_focus()
            .map_err(|error| format!("聚焦页面窗口失败：{error}"))?;
        return Ok(());
    }

    let route = format!("index.html?window=page&view={view}");
    let builder = WebviewWindowBuilder::new(&app, &label, WebviewUrl::App(route.into()))
        .title(format!("福宝控制台 · {title}"))
        .inner_size(1080.0, 720.0)
        .min_inner_size(680.0, 500.0)
        .resizable(true)
        .decorations(true)
        .center()
        .focused(true);

    #[cfg(target_os = "macos")]
    let builder = {
        builder
            .title_bar_style(tauri::TitleBarStyle::Overlay)
            .hidden_title(true)
            .traffic_light_position(LogicalPosition::new(15.0, 20.0))
            .background_color(tauri::webview::Color(255, 255, 255, 255))
    };

    builder
        .build()
        .map(|_| ())
        .map_err(|error| format!("打开页面窗口失败：{error}"))
}

#[tauri::command]
fn open_live_room(app: tauri::AppHandle, web_rid: String) -> Result<(), String> {
    let web_rid = web_rid.trim();
    if !(6..=24).contains(&web_rid.len())
        || !web_rid.chars().all(|character| character.is_ascii_digit())
    {
        return Err("直播间标识无效，无法打开".to_string());
    }
    let url = format!("https://live.douyin.com/{web_rid}");
    #[allow(deprecated)]
    app.shell()
        .open(url, None)
        .map_err(|error| format!("打开直播间失败：{error}"))
}

#[tauri::command]
fn close_monitor_log(app: tauri::AppHandle) -> Result<(), String> {
    if let Some(window) = app.get_webview_window("monitor-log") {
        window
            .close()
            .map_err(|error| format!("关闭运行日志窗口失败：{error}"))?;
    }
    Ok(())
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .plugin(tauri_plugin_process::init())
        .plugin(tauri_plugin_shell::init())
        .plugin(tauri_plugin_updater::Builder::new().build())
        .setup(|app| {
            let runtime = Arc::new(EngineRuntime::default());
            app.manage(runtime.clone());
            if let Err(error) = start_engine(app.handle().clone(), runtime) {
                eprintln!("{error}");
            }
            setup_system_tray(app.handle())?;
            #[cfg(windows)]
            if let Some(window) = app.get_webview_window("main") {
                apply_windows_titlebar_palette(&window);
            }
            #[cfg(debug_assertions)]
            if std::env::var("FUBAO_OPEN_DEVTOOLS").as_deref() == Ok("1") {
                if let Some(window) = app.get_webview_window("main") {
                    window.open_devtools();
                }
            }
            Ok(())
        })
        .on_window_event(|window, event| {
            if window.label() != MAIN_WINDOW_LABEL {
                return;
            }
            match event {
                WindowEvent::CloseRequested { api, .. } => {
                    // Close-to-tray: keep the Go engine and tray alive. The
                    // surface is restored via tray left-click / “打开福宝控制台”.
                    api.prevent_close();
                    if let Err(error) = window.hide() {
                        eprintln!("[fubao-tray] hide main window failed: {error}");
                    } else {
                        eprintln!("[fubao-tray] main window hidden to tray");
                    }
                }
                WindowEvent::Destroyed => {
                    // Should not happen when prevent_close works. Log so a
                    // future tray open can rebuild via ensure_main_window.
                    eprintln!("[fubao-tray] main window destroyed unexpectedly");
                }
                _ => {}
            }
        })
        .invoke_handler(tauri::generate_handler![
            engine_status,
            engine_send,
            engine_restart,
            prepare_app_update,
            start_window_drag,
            toggle_window_maximize,
            frontend_log,
            refresh_window_surface,
            open_monitor_log,
            open_participation_log,
            open_browser_instance_window,
            browser_instance_window_metadata,
            launch_external_chrome_instance,
            rebuild_browser_account_session,
            open_page_window,
            open_live_room,
            close_monitor_log,
            mount_browser_webview,
            sync_browser_webview,
            hide_browser_webview,
            close_browser_webview,
            prepare_browser_red_packet_context,
            stop_browser_red_packet_context,
            reset_browser_landing_page,
            refresh_browser_account_cookie,
            sync_browser_account_cookie,
            sync_browser_following_live,
            open_account_rebind,
            sync_account_rebind,
            complete_account_rebind,
            cancel_account_rebind,
            open_account_create,
            sync_account_create,
            complete_account_create,
            cancel_account_create
        ])
        .build(tauri::generate_context!())
        .expect("福宝控制台初始化失败")
        .run(|app, event| match event {
            #[cfg(target_os = "macos")]
            RunEvent::Reopen { .. } => show_main_window(app),
            // Closing the last visible window must not tear down a tray app.
            // Explicit tray “彻底退出” calls app.exit(0) with a code.
            RunEvent::ExitRequested { api, code, .. } => {
                if code.is_none() {
                    api.prevent_exit();
                }
            }
            RunEvent::Exit => stop_engine(app),
            _ => {}
        });
}
