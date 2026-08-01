use std::{
    collections::HashMap,
    hash::{DefaultHasher, Hash, Hasher},
    sync::{
        atomic::{AtomicBool, Ordering},
        Arc, Mutex,
    },
    time::Duration,
};

use serde::Deserialize;
use serde_json::{json, Value};
use tauri::{
    webview::{Cookie, WebviewBuilder},
    Emitter, LogicalPosition, LogicalSize, Manager, WebviewUrl, WebviewWindowBuilder,
};
use tauri_plugin_shell::{
    process::{CommandChild, CommandEvent},
    ShellExt,
};
use tokio::sync::oneshot;
use uuid::Uuid;

mod updater;
use updater::{check_app_update, download_app_update, install_app_update, UpdaterRuntime};

struct EngineRuntime {
    child: Mutex<Option<CommandChild>>,
    online: AtomicBool,
    native_secret: String,
    pending: Mutex<HashMap<String, oneshot::Sender<String>>>,
}

impl Default for EngineRuntime {
    fn default() -> Self {
        Self {
            child: Mutex::new(None),
            online: AtomicBool::new(false),
            native_secret: Uuid::new_v4().to_string(),
            pending: Mutex::new(HashMap::new()),
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

const DOUYIN_CHROME_USER_AGENT: &str = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36";

fn browser_webview_label(instance_id: &str) -> String {
    format!(
        "browser-{}",
        instance_id.replace(|character: char| !character.is_ascii_alphanumeric(), "-")
    )
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

fn rebind_webview_label(account_id: &str) -> String {
    format!(
        "account-rebind-{}",
        account_id.replace(|character: char| !character.is_ascii_alphanumeric(), "-")
    )
}

fn create_account_webview_label(session_id: &str) -> String {
    format!(
        "account-create-{}",
        session_id.replace(|character: char| !character.is_ascii_alphanumeric(), "-")
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

fn read_douyin_cookie(webview: &tauri::Webview) -> Result<String, String> {
    let mut values = HashMap::<String, String>::new();
    // Wry's macOS `cookies_for_url` currently compares cookie domains using
    // exact equality. Douyin's authenticated HttpOnly cookies live on the
    // parent domain `.douyin.com`, so querying `www.douyin.com` silently
    // excludes the real session. Read the native store and apply proper
    // parent-domain matching ourselves.
    for cookie in webview
        .cookies()
        .map_err(|error| format!("读取登录 Cookie 失败：{error}"))?
        .into_iter()
        .filter(|cookie| {
            cookie.domain().is_some_and(|domain| {
                let domain = domain.trim_start_matches('.').to_ascii_lowercase();
                domain == "douyin.com" || domain.ends_with(".douyin.com")
            })
        })
    {
        if cookie.name() == "fubao_login_probe" {
            continue;
        }
        values.insert(cookie.name().to_string(), cookie.value().to_string());
    }
    // Douyin does not use one stable login-cookie name across all desktop
    // rollouts. The current web client commonly persists the authenticated
    // session under the sid/uid UCP variants instead of sessionid_ss.
    let login_names = [
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
    let logged_in = login_names.iter().any(|name| {
        values
            .get(*name)
            .is_some_and(|value| !value.trim().is_empty())
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
        .map(|(name, value)| format!("{name}={value}"))
        .collect::<Vec<_>>()
        .join("; "))
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

fn login_cookie_signature(raw_cookie: &str) -> Vec<(String, String)> {
    let login_names = [
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
    let mut values = raw_cookie
        .split(';')
        .filter_map(|item| item.trim().split_once('='))
        .filter(|(name, _)| login_names.contains(&name.trim()))
        .map(|(name, value)| (name.trim().to_string(), value.trim().to_string()))
        .collect::<Vec<_>>();
    values.sort_by(|left, right| left.0.cmp(&right.0));
    values
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

#[tauri::command]
async fn open_account_rebind(
    app: tauri::AppHandle,
    runtime: tauri::State<'_, Arc<EngineRuntime>>,
    account_id: String,
    bounds: BrowserBounds,
) -> Result<String, String> {
    let account_id = account_id.trim().to_string();
    if account_id.is_empty() {
        return Err("账号标识不能为空".into());
    }
    let label = rebind_webview_label(&account_id);
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
        let data_dir = app
            .path()
            .app_data_dir()
            .map_err(|error| format!("读取登录数据目录失败：{error}"))?
            .join("embedded-browser")
            .join(&account_id);
        std::fs::create_dir_all(&data_dir)
            .map_err(|error| format!("创建登录数据目录失败：{error}"))?;
        builder = builder.data_directory(data_dir);
    }
    let main_window = app.get_window("main").ok_or("主窗口不存在")?;
    let webview = main_window
        .add_child(
            builder,
            LogicalPosition::new(bounds.x, bounds.y),
            LogicalSize::new(bounds.width.max(360.0), bounds.height.max(280.0)),
        )
        .map_err(|error| format!("创建登录页面失败：{error}"))?;
    if !credential.cookie.trim().is_empty() {
        inject_douyin_cookie(&webview, &credential.cookie)?;
    }
    let douyin_url = "https://www.douyin.com/"
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
    account_id: String,
    bounds: BrowserBounds,
) -> Result<(), String> {
    let webview = app
        .get_webview(&rebind_webview_label(account_id.trim()))
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
    runtime: tauri::State<'_, Arc<EngineRuntime>>,
    account_id: String,
) -> Result<(), String> {
    let account_id = account_id.trim().to_string();
    let label = rebind_webview_label(&account_id);
    let webview = app
        .get_webview(&label)
        .ok_or("登录页面已关闭，请重新打开")?;
    let raw_cookie = read_douyin_cookie(&webview)?;
    let runtime = runtime.inner().clone();
    native_engine_request(
        runtime.clone(),
        "account.native_replace_cookie",
        json!({ "account_id": account_id, "cookie": raw_cookie, "secret": runtime.native_secret }),
    )
    .await?;
    webview
        .hide()
        .map_err(|error| format!("隐藏登录页面失败：{error}"))?;
    // Hiding is the visual guarantee. Closing releases the native WebView;
    // if WebKit delays teardown, it must never remain over the main UI.
    let _ = webview.close();
    Ok(())
}

#[tauri::command]
fn cancel_account_rebind(app: tauri::AppHandle, account_id: String) -> Result<(), String> {
    if let Some(webview) = app.get_webview(&rebind_webview_label(account_id.trim())) {
        webview
            .hide()
            .map_err(|error| format!("隐藏登录页面失败：{error}"))?;
        let _ = webview.close();
    }
    Ok(())
}

#[tauri::command]
fn open_account_create(
    app: tauri::AppHandle,
    session_id: String,
    bounds: BrowserBounds,
) -> Result<String, String> {
    let session_id = session_id.trim().to_string();
    if session_id.is_empty() {
        return Err("登录会话标识不能为空".into());
    }
    let label = create_account_webview_label(&session_id);
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
        let data_dir = app
            .path()
            .app_data_dir()
            .map_err(|error| format!("读取登录数据目录失败：{error}"))?
            .join("account-create")
            .join(&session_id);
        std::fs::create_dir_all(&data_dir)
            .map_err(|error| format!("创建登录数据目录失败：{error}"))?;
        builder = builder.data_directory(data_dir);
    }
    let main_window = app.get_window("main").ok_or("主窗口不存在")?;
    let webview = main_window
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
    session_id: String,
    bounds: BrowserBounds,
) -> Result<(), String> {
    let webview = app
        .get_webview(&create_account_webview_label(session_id.trim()))
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
    runtime: tauri::State<'_, Arc<EngineRuntime>>,
    session_id: String,
    role: String,
) -> Result<Value, String> {
    let label = create_account_webview_label(session_id.trim());
    let webview = app
        .get_webview(&label)
        .ok_or("登录页面已关闭，请重新打开")?;
    let raw_cookie = read_douyin_cookie(&webview)?;
    let runtime = runtime.inner().clone();
    let result = native_engine_request(
        runtime.clone(),
        "account.native_create_from_cookie",
        json!({ "cookie": raw_cookie, "role": role, "secret": runtime.native_secret }),
    )
    .await?;
    webview
        .hide()
        .map_err(|error| format!("隐藏登录页面失败：{error}"))?;
    let _ = webview.close();
    Ok(result)
}

#[tauri::command]
fn cancel_account_create(app: tauri::AppHandle, session_id: String) -> Result<(), String> {
    if let Some(webview) = app.get_webview(&create_account_webview_label(session_id.trim())) {
        webview
            .hide()
            .map_err(|error| format!("隐藏登录页面失败：{error}"))?;
        let _ = webview.close();
    }
    Ok(())
}

fn apply_browser_bounds(webview: &tauri::Webview, bounds: &BrowserBounds) -> Result<(), String> {
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
        .map_err(|error| format!("设置嵌入浏览器缩放失败：{error}"))?;
    webview
        .show()
        .map_err(|error| format!("显示嵌入浏览器失败：{error}"))
}

#[tauri::command]
async fn mount_browser_webview(
    app: tauri::AppHandle,
    runtime: tauri::State<'_, Arc<EngineRuntime>>,
    instance_id: String,
    bounds: BrowserBounds,
) -> Result<String, String> {
    let label = browser_webview_label(&instance_id);
    if let Some(webview) = app.get_webview(&label) {
        apply_browser_bounds(&webview, &bounds)?;
        return Ok(label);
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
    if credential.instance_id != instance_id || credential.account_id.trim().is_empty() {
        return Err("浏览器实例凭据不匹配".into());
    }

    let main_window = app.get_window("main").ok_or("主窗口不存在")?;
    let blank_url = "about:blank"
        .parse()
        .map_err(|error| format!("初始化浏览器地址失败：{error}"))?;
    let mut builder = WebviewBuilder::new(label.clone(), WebviewUrl::External(blank_url))
        .focused(false)
        .accept_first_mouse(true)
        .devtools(cfg!(debug_assertions))
        // Match the Chromium identity used by the legacy 福宝/DY-KIRO CDP
        // browser. Douyin's page-level login gate is UA-sensitive even when
        // its self-profile API accepts the same Cookie.
        .user_agent(DOUYIN_CHROME_USER_AGENT)
        .on_navigation(|url| {
            url.scheme() == "about"
                || url
                    .domain()
                    .is_some_and(|domain| domain == "douyin.com" || domain.ends_with(".douyin.com"))
        });
    #[cfg(any(target_os = "macos", target_os = "ios"))]
    {
        builder =
            builder.data_store_identifier(browser_data_store_identifier(&credential.account_id));
    }
    #[cfg(not(any(target_os = "macos", target_os = "ios")))]
    {
        let data_dir = app
            .path()
            .app_data_dir()
            .map_err(|error| format!("读取实例数据目录失败：{error}"))?
            .join("embedded-browser")
            .join(&credential.account_id);
        std::fs::create_dir_all(&data_dir)
            .map_err(|error| format!("创建实例数据目录失败：{error}"))?;
        builder = builder.data_directory(data_dir);
    }

    let webview = main_window
        .add_child(
            builder,
            LogicalPosition::new(bounds.x, bounds.y),
            LogicalSize::new(bounds.width.max(120.0), bounds.height.max(90.0)),
        )
        .map_err(|error| format!("创建嵌入浏览器失败：{error}"))?;

    inject_douyin_cookie(&webview, &credential.cookie)?;
    let douyin_url = "https://www.douyin.com/"
        .parse()
        .map_err(|error| format!("解析抖音地址失败：{error}"))?;
    webview
        .navigate(douyin_url)
        .map_err(|error| format!("加载抖音页面失败：{error}"))?;
    apply_browser_bounds(&webview, &bounds)?;
    if cfg!(debug_assertions) {
        let current_url = webview
            .url()
            .map(|url| url.to_string())
            .unwrap_or_else(|error| format!("读取地址失败：{error}"));
        eprintln!(
            "[embedded-browser] mounted label={label} account={} url={current_url}",
            credential.account_id
        );
    }
    let _ = credential.account_name;
    Ok(label)
}

#[tauri::command]
fn sync_browser_webview(
    app: tauri::AppHandle,
    instance_id: String,
    bounds: BrowserBounds,
) -> Result<(), String> {
    let label = browser_webview_label(&instance_id);
    let webview = app.get_webview(&label).ok_or("嵌入浏览器尚未创建")?;
    apply_browser_bounds(&webview, &bounds)
}

#[tauri::command]
fn hide_browser_webview(app: tauri::AppHandle, instance_id: String) -> Result<(), String> {
    let label = browser_webview_label(&instance_id);
    if let Some(webview) = app.get_webview(&label) {
        webview
            .hide()
            .map_err(|error| format!("隐藏嵌入浏览器失败：{error}"))?;
    }
    Ok(())
}

#[tauri::command]
fn close_browser_webview(app: tauri::AppHandle, instance_id: String) -> Result<(), String> {
    let label = browser_webview_label(&instance_id);
    if let Some(webview) = app.get_webview(&label) {
        webview
            .close()
            .map_err(|error| format!("关闭嵌入浏览器失败：{error}"))?;
    }
    Ok(())
}

#[tauri::command]
async fn refresh_browser_account_cookie(
    app: tauri::AppHandle,
    runtime: tauri::State<'_, Arc<EngineRuntime>>,
    instance_id: String,
) -> Result<(), String> {
    let label = browser_webview_label(&instance_id);
    let Some(webview) = app.get_webview(&label) else {
        // An unmounted instance receives the latest canonical Cookie during
        // its next mount, so there is nothing to refresh now.
        return Ok(());
    };
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
    inject_douyin_cookie(&webview, &credential.cookie)?;
    let douyin_url = "https://www.douyin.com/"
        .parse()
        .map_err(|error| format!("解析抖音地址失败：{error}"))?;
    webview
        .navigate(douyin_url)
        .map_err(|error| format!("刷新账号登录状态失败：{error}"))
}

#[tauri::command]
async fn sync_browser_account_cookie(
    app: tauri::AppHandle,
    runtime: tauri::State<'_, Arc<EngineRuntime>>,
    instance_id: String,
) -> Result<bool, String> {
    let label = browser_webview_label(&instance_id);
    let Some(webview) = app.get_webview(&label) else {
        if cfg!(debug_assertions) {
            eprintln!(
                "[embedded-browser] cookie-sync skipped instance={instance_id} reason=not-mounted"
            );
        }
        return Ok(false);
    };
    let snapshot = inspect_douyin_login(&webview).await?;
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
    let mut cookie_changed = false;
    if snapshot.state == BrowserLoginState::LoggedIn {
        if let Some(raw_cookie) = snapshot.raw_cookie.as_deref() {
            if login_cookie_signature(raw_cookie) != login_cookie_signature(&credential.cookie) {
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
                cookie_changed = true;
            }
        }
    }
    if snapshot.state == BrowserLoginState::Unknown {
        if cfg!(debug_assertions) {
            eprintln!("[embedded-browser] login-state unknown instance={instance_id}");
        }
        return Ok(false);
    }
    let desired_status = if snapshot.state == BrowserLoginState::LoggedIn {
        "valid"
    } else {
        "expired"
    };
    if !cookie_changed && credential.cookie_status == desired_status {
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

    let mut builder = WebviewWindowBuilder::new(
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
    {
        // Let the HTML header share the white surface with the native traffic
        // lights instead of showing a second gray title strip.
        builder = builder
            .title_bar_style(tauri::TitleBarStyle::Overlay)
            .hidden_title(true)
            .traffic_light_position(LogicalPosition::new(15.0, 20.0))
            .background_color(tauri::webview::Color(255, 255, 255, 255));
    }

    builder
        .build()
        .map(|_| ())
        .map_err(|error| format!("打开运行日志窗口失败：{error}"))
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
        .plugin(tauri_plugin_shell::init())
        .setup(|app| {
            let runtime = Arc::new(EngineRuntime::default());
            app.manage(runtime.clone());
            app.manage(Arc::new(UpdaterRuntime::default()));
            if let Err(error) = start_engine(app.handle().clone(), runtime) {
                eprintln!("{error}");
            }
            #[cfg(debug_assertions)]
            if std::env::var("FUBAO_OPEN_DEVTOOLS").as_deref() == Ok("1") {
                if let Some(window) = app.get_webview_window("main") {
                    window.open_devtools();
                }
            }
            Ok(())
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
            open_live_room,
            close_monitor_log,
            check_app_update,
            download_app_update,
            install_app_update,
            mount_browser_webview,
            sync_browser_webview,
            hide_browser_webview,
            close_browser_webview,
            refresh_browser_account_cookie,
            sync_browser_account_cookie,
            open_account_rebind,
            sync_account_rebind,
            complete_account_rebind,
            cancel_account_rebind,
            open_account_create,
            sync_account_create,
            complete_account_create,
            cancel_account_create
        ])
        .run(tauri::generate_context!())
        .expect("福宝控制台启动失败");
}
