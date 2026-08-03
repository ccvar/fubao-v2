use std::{
    collections::{HashMap, HashSet},
    hash::{DefaultHasher, Hash, Hasher},
    sync::{
        atomic::{AtomicBool, Ordering},
        Arc, Mutex,
    },
    time::{Duration, Instant},
};

use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use tauri::{
    menu::{Menu, MenuItem},
    tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent},
    webview::{Cookie, PageLoadEvent, WebviewBuilder},
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
    // Closing a child WKWebView is asynchronous at the WebKit/Tauri boundary.
    // Keep a native tombstone so overlapping layout, scroll, and modal cleanup
    // requests cannot reuse or close the same invalidating handle twice.
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

fn show_main_window(app: &tauri::AppHandle) {
    if let Some(window) = app.get_webview_window("main") {
        let _ = window.show();
        let _ = window.unminimize();
        let _ = window.set_focus();
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
    use windows_sys::Win32::UI::WindowsAndMessaging::{
        BringWindowToTop, SetForegroundWindow, SetWindowPos, ShowWindow, HWND_NOTOPMOST,
        HWND_TOPMOST, SWP_NOMOVE, SWP_NOSIZE, SWP_SHOWWINDOW, SW_RESTORE,
    };

    let Ok(hwnd) = window.hwnd() else {
        return;
    };
    let flags = SWP_NOMOVE | SWP_NOSIZE | SWP_SHOWWINDOW;
    unsafe {
        let _ = ShowWindow(hwnd.0, SW_RESTORE);
        let _ = SetWindowPos(hwnd.0, HWND_TOPMOST, 0, 0, 0, 0, flags);
        let _ = BringWindowToTop(hwnd.0);
        let _ = SetForegroundWindow(hwnd.0);
        // Use TOPMOST only as an activation assist. The instance window must
        // not remain above unrelated applications after it receives focus.
        let _ = SetWindowPos(hwnd.0, HWND_NOTOPMOST, 0, 0, 0, 0, flags);
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
        .on_menu_event(|app, event| match event.id.as_ref() {
            "tray-show-main" => show_main_window(app),
            "tray-quit" => {
                stop_engine(app);
                app.exit(0);
            }
            _ => {}
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
                show_main_window(tray.app_handle());
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
        }
    }
}

const DOUYIN_CHROME_USER_AGENT: &str = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36";
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

fn browser_data_store_identifier(account_id: &str) -> [u8; 16] {
    let mut first = DefaultHasher::new();
    "fubao-browser-primary".hash(&mut first);
    account_id.hash(&mut first);
    let mut second = DefaultHasher::new();
    "fubao-browser-secondary".hash(&mut second);
    account_id.hash(&mut second);
    let mut result = [0_u8; 16];
    result[..8].copy_from_slice(&first.finish().to_be_bytes());
    result[8..].copy_from_slice(&second.finish().to_be_bytes());
    result
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
    for item in raw_cookie.split(';') {
        let Some((name, value)) = item.trim().split_once('=') else {
            continue;
        };
        if name.trim().is_empty() {
            continue;
        }
        let cookie = Cookie::build((name.trim().to_string(), value.trim().to_string()))
            .domain(".douyin.com")
            .path("/")
            .secure(true)
            .same_site(cookie::SameSite::None)
            .build();
        webview
            .set_cookie(cookie)
            .map_err(|error| format!("同步账号 Cookie 失败：{error}"))?;
    }
    Ok(())
}

fn login_cookie_snapshot_matches(expected: &str, actual: &str) -> bool {
    let expected = login_cookie_values(expected);
    let actual = login_cookie_values(actual);
    !expected.is_empty()
        && expected
            .iter()
            .all(|(name, value)| actual.get(name).is_some_and(|current| current == value))
}

async fn inject_douyin_cookie_and_confirm(
    webview: &tauri::Webview,
    raw_cookie: &str,
) -> Result<(), String> {
    if login_cookie_values(raw_cookie).is_empty() {
        return Err("账号 Cookie 缺少登录凭据，请重新扫码登录".into());
    }
    let mut last_error = "等待浏览器接收账号 Cookie".to_string();
    for attempt in 0..6 {
        inject_douyin_cookie(webview, raw_cookie)?;
        tokio::time::sleep(Duration::from_millis(160)).await;
        match read_douyin_cookie(webview) {
            Ok(actual) if login_cookie_snapshot_matches(raw_cookie, &actual) => return Ok(()),
            Ok(_) => last_error = "浏览器尚未写入完整登录 Cookie".into(),
            Err(error) => last_error = error,
        }
        if attempt < 5 {
            tokio::time::sleep(Duration::from_millis(140)).await;
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
        let priority = match domain.as_str() {
            "douyin.com" => 2,
            "www.douyin.com" => 1,
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
        browser_webview_is_closing, canonical_cookie_values, login_cookie_snapshot_matches,
        login_cookie_values, remember_browser_location, EngineRuntime, Url,
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
}

async fn inspect_douyin_login(webview: &tauri::Webview) -> Result<BrowserLoginSnapshot, String> {
    // The browser itself is the source of truth here. A stale Cookie may still
    // exist after Douyin has rejected it, so cookie presence alone must never
    // be interpreted as a valid session. The short-lived probe contains only
    // a state word and never exposes credentials to the frontend.
    webview
        .eval(
            r#"(() => {
              const visible = (element) => {
                if (!(element instanceof HTMLElement)) return false;
                const rect = element.getBoundingClientRect();
                const style = getComputedStyle(element);
                return rect.width > 0 && rect.height > 0 && style.display !== 'none' && style.visibility !== 'hidden';
              };
              const text = document.body?.innerText || '';
              const explicitDialog = text.includes('登录后免费畅享高清视频') &&
                (text.includes('扫码登录') || text.includes('验证码登录'));
              const explicitLoginControl = [...document.querySelectorAll('button, a, [role="button"]')]
                .some((element) => visible(element) && element.textContent?.trim() === '登录');
              const ready = document.readyState !== 'loading' && Boolean(document.body);
              const state = explicitDialog || explicitLoginControl ? 'out' : ready ? 'ready' : 'unknown';
              document.cookie = `fubao_login_probe=${state}; Path=/; Secure; SameSite=None; Max-Age=5`;
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
    let raw_cookie = read_douyin_cookie(webview).ok();
    let _ = webview
        .eval("document.cookie='fubao_login_probe=; Path=/; Secure; SameSite=None; Max-Age=0'");
    let state = match probe.as_deref() {
        Some("out") => BrowserLoginState::LoggedOut,
        Some("ready") if raw_cookie.is_some() => BrowserLoginState::LoggedIn,
        _ => BrowserLoginState::Unknown,
    };
    Ok(BrowserLoginSnapshot { raw_cookie, state })
}

async fn read_authenticated_douyin_cookie(webview: &tauri::Webview) -> Result<String, String> {
    // WebView2 can publish the navigation/UI state slightly before its Cookie
    // manager exposes the final HttpOnly login values. Retry the native read
    // briefly instead of saving the previously injected, expired account CK.
    let mut last_error = "尚未检测到登录状态，请先在抖音窗口完成登录".to_string();
    for attempt in 0..8 {
        match inspect_douyin_login(webview).await {
            Ok(BrowserLoginSnapshot {
                raw_cookie: Some(raw_cookie),
                state: BrowserLoginState::LoggedIn,
            }) => return Ok(raw_cookie),
            Ok(BrowserLoginSnapshot {
                state: BrowserLoginState::LoggedOut,
                ..
            }) => return Err("抖音页面仍处于未登录状态，请完成登录后再更新 CK".into()),
            Ok(_) => {
                last_error = "登录页面尚未同步完成，请稍后重试".into();
            }
            Err(error) => last_error = error,
        }
        if attempt < 7 {
            tokio::time::sleep(Duration::from_millis(250)).await;
        }
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
              const text = JSON.stringify(result);
              const bytes = new TextEncoder().encode(text);
              const hex = Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('');
              document.cookie = `${{cookieName}}=${{hex}}; Path=/; Secure; SameSite=None; Max-Age=15`;
            }} catch (error) {{}}
          }};
          const stripSignatures = (url) => {{
            for (const key of ['msToken', 'a_bogus', 'X-Bogus', '__ac_signature', '__ac_nonce']) {{
              url.searchParams.delete(key);
            }}
          }};
          const commonRequestURL = () => {{
            try {{
              const entries = performance.getEntriesByType('resource').slice().reverse();
              for (const entry of entries) {{
                const candidate = new URL(String(entry.name || ''), location.href);
                if (candidate.hostname === 'live.douyin.com' && candidate.pathname.startsWith('/webcast/')) {{
                  return candidate;
                }}
              }}
            }} catch (error) {{}}
            return new URL('https://live.douyin.com/webcast/luckybox/join/');
          }};
          const requestURL = (endpoint) => {{
            const url = commonRequestURL();
            url.protocol = 'https:';
            url.hostname = 'live.douyin.com';
            url.pathname = `/webcast/luckybox/${{endpoint}}/`;
            stripSignatures(url);
            for (const key of ['cursor', 'count', 'offset', 'fetch_time', 'last_id', 'room_ids']) {{
              url.searchParams.delete(key);
            }}
            const values = {{
              aid: '6383', app_name: 'douyin_web', live_id: '1', device_platform: 'web',
              room_id: task.actual_room_id, box_id: task.box_id, anchor_id: task.anchor_id
            }};
            for (const [key, value] of Object.entries(values)) {{
              if (String(value || '').trim()) url.searchParams.set(key, String(value));
            }}
            if (!url.searchParams.has('browser_language')) url.searchParams.set('browser_language', navigator.language || 'zh-CN');
            if (!url.searchParams.has('browser_platform')) url.searchParams.set('browser_platform', navigator.platform || 'MacIntel');
            if (!url.searchParams.has('browser_name')) url.searchParams.set('browser_name', 'Mozilla');
            if (!url.searchParams.has('browser_online')) url.searchParams.set('browser_online', String(navigator.onLine));
            if (!url.searchParams.has('cookie_enabled')) url.searchParams.set('cookie_enabled', String(navigator.cookieEnabled));
            return url;
          }};
          const send = async (endpoint) => {{
            const response = await fetch(requestURL(endpoint).toString(), {{
              method: 'POST', credentials: 'include', cache: 'no-store',
              headers: {{'accept': 'application/json, text/plain, */*', 'content-type': 'application/x-www-form-urlencoded'}},
              body: '', referrer: `https://live.douyin.com/${{task.web_rid}}`,
              referrerPolicy: 'strict-origin-when-cross-origin'
            }});
            let text = await response.text();
            if (endpoint === 'receive') {{
              try {{
                const parsed = JSON.parse(text);
                const infos = parsed && parsed.data && parsed.data.receive_info;
                let reduced = infos;
                if (Array.isArray(infos)) {{
                  const target = String(task.box_id || '');
                  const matched = infos.find((item) => {{
                    const id = String(item && (item.box_id_str || item.boxIdStr || item.box_id || item.boxId || item.activity_id || item.activityId) || '');
                    return id === target;
                  }});
                  const onlyIdless = infos.length === 1 && !String(infos[0] && (infos[0].box_id_str || infos[0].boxIdStr || infos[0].box_id || infos[0].boxId || infos[0].activity_id || infos[0].activityId) || '');
                  reduced = matched ? [matched] : onlyIdless ? [infos[0]] : infos.length === 0 ? [] : undefined;
                }}
                text = JSON.stringify({{status_code: parsed.status_code, status_msg: parsed.status_msg, data: {{receive_info: reduced}}}});
              }} catch (error) {{}}
            }}
            return {{endpoint, status: response.status, text}};
          }};
          (async () => {{
            try {{
              if (location.hostname !== 'live.douyin.com' || location.pathname.replace(/^\/+|\/+$/g, '') !== String(task.web_rid)) {{
                finish({{endpoint: 'page', http_status: 0, body: '', error: '浏览器实例未进入目标直播间', attempts: 0, context_missing: true}});
                return;
              }}
              // A synthetic join -> rush fallback doubles account traffic and
              // Douyin reports the second request as rush_spam. One detected
              // packet therefore issues exactly one page-context join. A rush
              // is only safe when it originates from a real page interaction
              // whose complete request template was captured by the page.
              const action = task.action === 'receive' ? 'receive' : 'join';
              const response = await send(action);
              finish({{
                endpoint: response.endpoint,
                http_status: response.status,
                body: String(response.text || '').slice(0, 1200),
                error: '',
                attempts: 1,
                context_missing: false,
                login_expired: false
              }});
            }} catch (error) {{
              finish({{endpoint: 'page', http_status: 0, body: '', error: String(error && (error.message || error) || '直播页面红包请求失败'), attempts: 1, context_missing: false, login_expired: false}});
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
        };
    }
    for _ in 0..100 {
        tokio::time::sleep(Duration::from_millis(150)).await;
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
            Err(error) => return NativePageParticipationResult::context_missing(error),
        };
        let value: Value = match serde_json::from_str(&decoded) {
            Ok(value) => value,
            Err(error) => {
                return NativePageParticipationResult::context_missing(format!(
                    "解析直播页面红包结果失败：{error}"
                ))
            }
        };
        return NativePageParticipationResult {
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
        };
    }
    NativePageParticipationResult {
        endpoint: "page".into(),
        http_status: 0,
        body: String::new(),
        error: "等待直播页面红包接口响应超时".into(),
        attempts: 1,
        context_missing: false,
        login_expired: false,
    }
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
            Ok(()) => match inspect_douyin_login(&webview).await {
                Ok(snapshot) if snapshot.state == BrowserLoginState::LoggedOut => {
                    NativePageParticipationResult::login_expired("CK 已失效：直播页面要求重新登录")
                }
                Ok(snapshot) if snapshot.state == BrowserLoginState::LoggedIn => {
                    execute_page_participation(&webview, &task).await
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
    if cfg!(debug_assertions) {
        eprintln!(
            "[redpacket-page] task={} instance={} endpoint={} http={} attempts={} context_missing={} login_expired={} error={}",
            task.task_id.chars().take(8).collect::<String>(),
            task.instance_id,
            result.endpoint,
            result.http_status,
            result.attempts,
            result.context_missing,
            result.login_expired,
            if result.error.is_empty() { "none" } else { "present" },
        );
    }
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
            "secret": runtime.native_secret,
        }),
    )
    .await;
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
        json!({ "account_id": account_id, "logged_in": true, "secret": runtime.native_secret }),
    )
    .await?;
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
    let mut result = native_engine_request(
        runtime.clone(),
        "account.native_create_from_cookie",
        json!({
            "cookie": raw_cookie,
            "role": role,
            "group_id": group_id.unwrap_or_default(),
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
            "secret": runtime.native_secret
        }),
    )
    .await?;
    if let Some(object) = result.as_object_mut() {
        object.insert("account".into(), account);
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

fn schedule_browser_webview_reveal(
    webview: tauri::Webview,
    app: tauri::AppHandle,
    runtime: Arc<EngineRuntime>,
    webview_label: String,
    instance_id: String,
    revealed: Arc<AtomicBool>,
    delay: std::time::Duration,
) {
    std::thread::spawn(move || {
        std::thread::sleep(delay);
        if revealed
            .compare_exchange(false, true, Ordering::SeqCst, Ordering::SeqCst)
            .is_err()
        {
            return;
        }
        if browser_webview_is_closing(&runtime, &webview_label) {
            return;
        }
        match webview.show() {
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

fn is_safe_douyin_location(url: &Url) -> bool {
    url.scheme() == "https"
        && url
            .domain()
            .is_some_and(|domain| domain == "douyin.com" || domain.ends_with(".douyin.com"))
}

fn remember_browser_location(runtime: &EngineRuntime, instance_id: &str, url: &Url) {
    if !is_safe_douyin_location(url) {
        return;
    }
    if let Ok(mut locations) = runtime.browser_locations.lock() {
        locations.insert(instance_id.to_owned(), url.as_str().to_owned());
    }
}

fn browser_restore_location(runtime: &EngineRuntime, instance_id: &str) -> Url {
    runtime
        .browser_locations
        .lock()
        .ok()
        .and_then(|locations| locations.get(instance_id).cloned())
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
    let navigation_runtime = runtime.clone();
    let navigation_instance_id = instance_id.to_string();
    let page_runtime = runtime.clone();
    let page_instance_id = instance_id.to_string();
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
        .on_page_load(move |webview, payload| {
            let is_douyin = is_safe_douyin_location(payload.url());
            if is_douyin {
                remember_browser_location(&page_runtime, &page_instance_id, payload.url());
            }
            if reveal_when_ready && payload.event() == PageLoadEvent::Finished && is_douyin {
                // WKWebView can report navigation completion before the
                // remote SPA has committed its first meaningful frame. Keep
                // the native surface hidden for a short paint-stabilization
                // window so the HTML loading state transitions directly into
                // real Douyin content instead of flashing an empty white card.
                schedule_browser_webview_reveal(
                    webview.clone(),
                    ready_app.clone(),
                    page_runtime.clone(),
                    webview.label().to_string(),
                    ready_instance_id.clone(),
                    ready_once_for_page.clone(),
                    std::time::Duration::from_millis(1_200),
                );
            }
        });
    #[cfg(any(target_os = "macos", target_os = "ios"))]
    {
        builder =
            builder.data_store_identifier(browser_data_store_identifier(&credential.account_id));
    }
    #[cfg(not(any(target_os = "macos", target_os = "ios")))]
    {
        let data_dir = webview_data_root(&app)?
            .join("embedded-browser")
            .join(&credential.account_id);
        std::fs::create_dir_all(&data_dir)
            .map_err(|error| format!("创建实例数据目录失败：{error}"))?;
        builder = builder.data_directory(data_dir);
    }

    let webview = window
        .add_child(
            builder,
            // Create the native surface outside the visible window first. A
            // newly-created WKWebView paints white before its first page
            // frame; keeping it off-card lets the HTML loading state remain
            // visible until `on_page_load(Finished)` reveals the real page.
            LogicalPosition::new(-10_000.0, -10_000.0),
            LogicalSize::new(bounds.width.max(120.0), bounds.height.max(90.0)),
        )
        .map_err(|error| format!("创建嵌入浏览器失败：{error}"))?;

    webview
        .hide()
        .map_err(|error| format!("隐藏待加载浏览器失败：{error}"))?;
    apply_browser_geometry(&webview, &bounds)?;
    inject_douyin_cookie_and_confirm(&webview, &credential.cookie).await?;
    if let Ok(mut injected) = runtime.browser_cookie_injected_at.lock() {
        injected.insert(instance_id.to_string(), Instant::now());
    }
    webview
        .navigate(restore_url)
        .map_err(|error| format!("加载抖音页面失败：{error}"))?;
    // Some Douyin SPA navigations keep network work alive long enough that
    // WKWebView postpones its Finished callback even though the first screen
    // is already painted. Avoid an endless loading state while still giving
    // the page enough time to replace its initial native white surface.
    if reveal_when_ready {
        schedule_browser_webview_reveal(
            webview.clone(),
            app.clone(),
            runtime.clone(),
            webview.label().to_string(),
            instance_id.to_string(),
            ready_once,
            std::time::Duration::from_millis(5_000),
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
    let mut snapshot = inspect_douyin_login(&webview).await?;
    // A freshly-created hidden WebView may have committed the live-room URL
    // before Douyin's SPA has exposed its login state. Give it a bounded native
    // readiness window instead of incorrectly consuming the scheduled run.
    for _ in 0..8 {
        if snapshot.state != BrowserLoginState::Unknown {
            break;
        }
        tokio::time::sleep(Duration::from_millis(750)).await;
        snapshot = inspect_douyin_login(&webview).await?;
    }
    match snapshot.state {
        BrowserLoginState::LoggedIn => {}
        BrowserLoginState::LoggedOut => return Err("当前实例尚未登录，请先重新绑定 CK".into()),
        BrowserLoginState::Unknown => return Err("直播页面尚未完成加载，请稍后重试".into()),
    }
    let runtime = runtime.inner().clone();
    if let Err(error) = native_engine_request(
        runtime.clone(),
        "red_packet_participation.native_context",
        json!({
            "instance_id": instance_id,
            "ready": true,
            "result_only": result_only.unwrap_or(false),
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

#[tauri::command]
async fn refresh_browser_account_cookie(
    app: tauri::AppHandle,
    runtime: tauri::State<'_, Arc<EngineRuntime>>,
    instance_id: String,
) -> Result<(), String> {
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
        inject_douyin_cookie_and_confirm(&webview, &credential.cookie).await?;
        if let Ok(mut injected) = runtime.browser_cookie_injected_at.lock() {
            injected.insert(instance_id.clone(), Instant::now());
        }
        webview
            .navigate(douyin_url.clone())
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
    let within_injection_grace = runtime
        .browser_cookie_injected_at
        .lock()
        .ok()
        .and_then(|injected| injected.get(&instance_id).copied())
        .is_some_and(|injected_at| injected_at.elapsed() < Duration::from_secs(15));
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
        for _ in 0..6 {
            tokio::time::sleep(Duration::from_millis(500)).await;
            snapshot = inspect_douyin_login(&webview).await?;
            if snapshot.state != BrowserLoginState::LoggedOut {
                break;
            }
        }
    }
    let mut cookie_persisted = false;
    if snapshot.state == BrowserLoginState::LoggedIn {
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
            // Login-cookie changes are persisted immediately. Douyin rotates
            // auxiliary browser cookies frequently, so complete snapshots are
            // rate-limited to once per minute unless an explicit open action
            // needs the freshest possible account state.
            if cookie_changed && (require_logged_in || login_cookie_changed || auxiliary_sync_due) {
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
            }
        }
    }
    if snapshot.state == BrowserLoginState::Unknown {
        if cfg!(debug_assertions) {
            eprintln!("[embedded-browser] login-state unknown instance={instance_id}");
        }
        return Ok(false);
    }
    if require_logged_in && snapshot.state != BrowserLoginState::LoggedIn {
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

    let runtime = runtime.inner().clone();
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
    }))
}

#[tauri::command]
async fn open_browser_instance_window(
    app: tauri::AppHandle,
    runtime: tauri::State<'_, Arc<EngineRuntime>>,
    instance_id: String,
) -> Result<(), String> {
    let instance_id = instance_id.trim().to_string();
    if instance_id.is_empty()
        || !instance_id
            .chars()
            .all(|character| character.is_ascii_alphanumeric() || character == '-')
    {
        return Err("浏览器实例标识无效".into());
    }
    let label = format!("instance-window-{}", safe_window_label_part(&instance_id));
    // Look up the native window rather than only the WebviewWindow wrapper.
    // On Windows the native window can already be registered while the
    // webview lookup is briefly unavailable, which previously sent this path
    // into a duplicate build and surfaced an `already exists` error.
    if let Some(window) = app.get_window(&label) {
        reveal_window(&window)?;
        return Ok(());
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
    if credential.cookie_status == "expired" {
        return Err("参与账号 CK 已失效，请先重新绑定".into());
    }

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

    let route = format!("index.html?window=browser-instance&instance={instance_id}");
    let builder = WebviewWindowBuilder::new(&app, &label, WebviewUrl::App(route.into()))
        .title(format!("福宝浏览器实例 · {}", credential.account_name))
        .inner_size(1080.0, 760.0)
        .min_inner_size(640.0, 460.0)
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
            let _ = native_engine_request(
                runtime.inner().clone(),
                "browser.runtime.release",
                json!({ "instance_id": instance_id }),
            )
            .await;
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
        "accounts" => "账号与直播间",
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
            if window.label() == "main" {
                if let WindowEvent::CloseRequested { api, .. } = event {
                    api.prevent_close();
                    let _ = window.hide();
                }
            }
        })
        .invoke_handler(tauri::generate_handler![
            engine_status,
            engine_send,
            engine_restart,
            start_window_drag,
            toggle_window_maximize,
            frontend_log,
            refresh_window_surface,
            open_monitor_log,
            open_participation_log,
            open_browser_instance_window,
            browser_instance_window_metadata,
            open_page_window,
            open_live_room,
            close_monitor_log,
            mount_browser_webview,
            sync_browser_webview,
            hide_browser_webview,
            close_browser_webview,
            prepare_browser_red_packet_context,
            stop_browser_red_packet_context,
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
            RunEvent::Exit => stop_engine(app),
            _ => {}
        });
}
