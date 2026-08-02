use std::{
    collections::HashMap,
    path::{Path, PathBuf},
    process::{Command, Stdio},
    sync::{Arc, Mutex},
    time::Duration,
};

use futures_util::StreamExt;
use semver::Version;
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use tauri::{AppHandle, Emitter, Manager};
use tokio::io::AsyncWriteExt;

const UPDATE_MANIFEST_URL: &str =
    "https://raw.githubusercontent.com/ccvar/fubao-v2-releases/main/latest.json";

#[derive(Default)]
pub struct UpdaterRuntime {
    candidate: Mutex<Option<UpdateCandidate>>,
    downloaded: Mutex<Option<DownloadedPackage>>,
}

#[derive(Clone, Deserialize)]
struct UpdateManifest {
    version: String,
    #[serde(default)]
    notes: String,
    #[serde(default)]
    force: bool,
    packages: HashMap<String, UpdatePackage>,
}

#[derive(Clone, Deserialize)]
struct UpdatePackage {
    url: String,
    filename: String,
    sha256: String,
    #[serde(default)]
    size: u64,
}

#[derive(Clone)]
struct UpdateCandidate {
    manifest: UpdateManifest,
    package: UpdatePackage,
}

#[derive(Clone)]
struct DownloadedPackage {
    path: PathBuf,
}

#[derive(Serialize)]
pub struct UpdateStatus {
    current_version: String,
    latest_version: String,
    available: bool,
    notes: String,
    force: bool,
    filename: String,
    size: u64,
}

#[derive(Clone, Serialize)]
struct UpdateProgress {
    downloaded: u64,
    total: u64,
    percent: u8,
}

#[derive(Serialize)]
pub struct DownloadResult {
    version: String,
    filename: String,
}

#[tauri::command]
pub async fn check_app_update(
    app: AppHandle,
    runtime: tauri::State<'_, Arc<UpdaterRuntime>>,
) -> Result<UpdateStatus, String> {
    let current_version = app.package_info().version.to_string();
    let manifest = update_client()?
        .get(UPDATE_MANIFEST_URL)
        .send()
        .await
        .map_err(|error| format!("检查更新失败：{error}"))?
        .error_for_status()
        .map_err(|error| format!("更新服务返回异常：{error}"))?
        .json::<UpdateManifest>()
        .await
        .map_err(|error| format!("解析更新信息失败：{error}"))?;

    let package = manifest
        .packages
        .get(platform_key())
        .cloned()
        .ok_or_else(|| "更新清单没有当前平台的安装包".to_string())?;
    validate_package(&package)?;
    let available = is_newer_version(&manifest.version, &current_version)?;

    let status = UpdateStatus {
        current_version,
        latest_version: manifest.version.trim_start_matches('v').to_string(),
        available,
        notes: manifest.notes.clone(),
        force: manifest.force,
        filename: package.filename.clone(),
        size: package.size,
    };

    let mut candidate = runtime
        .candidate
        .lock()
        .map_err(|_| "更新状态锁不可用".to_string())?;
    *candidate = available.then_some(UpdateCandidate { manifest, package });
    if !available {
        if let Ok(mut downloaded) = runtime.downloaded.lock() {
            *downloaded = None;
        }
    }
    Ok(status)
}

#[tauri::command]
pub async fn download_app_update(
    app: AppHandle,
    runtime: tauri::State<'_, Arc<UpdaterRuntime>>,
) -> Result<DownloadResult, String> {
    let candidate = runtime
        .candidate
        .lock()
        .map_err(|_| "更新状态锁不可用".to_string())?
        .clone()
        .ok_or_else(|| "没有可下载的新版本，请先检查更新".to_string())?;

    let response = update_client()?
        .get(&candidate.package.url)
        .send()
        .await
        .map_err(|error| format!("下载安装包失败：{error}"))?
        .error_for_status()
        .map_err(|error| format!("安装包下载地址不可用：{error}"))?;
    let total = response.content_length().unwrap_or(candidate.package.size);
    let download_dir = app
        .path()
        .download_dir()
        .unwrap_or_else(|_| std::env::temp_dir());
    tokio::fs::create_dir_all(&download_dir)
        .await
        .map_err(|error| format!("准备下载目录失败：{error}"))?;
    let destination = unique_destination(&download_dir, &candidate.package.filename);
    let partial = destination.with_extension(format!(
        "{}.part",
        destination
            .extension()
            .and_then(|value| value.to_str())
            .unwrap_or("download")
    ));
    let mut file = tokio::fs::File::create(&partial)
        .await
        .map_err(|error| format!("创建安装包文件失败：{error}"))?;
    let mut downloaded = 0_u64;
    let mut stream = response.bytes_stream();
    while let Some(chunk) = stream.next().await {
        let chunk = chunk.map_err(|error| format!("下载安装包失败：{error}"))?;
        file.write_all(&chunk)
            .await
            .map_err(|error| format!("写入安装包失败：{error}"))?;
        downloaded += chunk.len() as u64;
        let percent = if total > 0 {
            ((downloaded.saturating_mul(100) / total).min(100)) as u8
        } else {
            0
        };
        let _ = app.emit(
            "update://progress",
            UpdateProgress {
                downloaded,
                total,
                percent,
            },
        );
    }
    file.flush()
        .await
        .map_err(|error| format!("保存安装包失败：{error}"))?;
    drop(file);

    let bytes = tokio::fs::read(&partial)
        .await
        .map_err(|error| format!("读取安装包失败：{error}"))?;
    let digest = format!("{:x}", Sha256::digest(&bytes));
    if !digest.eq_ignore_ascii_case(candidate.package.sha256.trim()) {
        let _ = tokio::fs::remove_file(&partial).await;
        return Err("安装包校验失败，已删除不完整文件".to_string());
    }
    tokio::fs::rename(&partial, &destination)
        .await
        .map_err(|error| format!("保存安装包失败：{error}"))?;

    *runtime
        .downloaded
        .lock()
        .map_err(|_| "更新状态锁不可用".to_string())? = Some(DownloadedPackage {
        path: destination.clone(),
    });
    Ok(DownloadResult {
        version: candidate
            .manifest
            .version
            .trim_start_matches('v')
            .to_string(),
        filename: destination
            .file_name()
            .and_then(|value| value.to_str())
            .unwrap_or(&candidate.package.filename)
            .to_string(),
    })
}

#[tauri::command]
pub fn install_app_update(
    app: AppHandle,
    runtime: tauri::State<'_, Arc<UpdaterRuntime>>,
) -> Result<(), String> {
    let package = runtime
        .downloaded
        .lock()
        .map_err(|_| "更新状态锁不可用".to_string())?
        .clone()
        .ok_or_else(|| "请先下载并校验更新包".to_string())?;
    if !package.path.exists() {
        return Err("已下载的安装包不存在，请重新下载".to_string());
    }

    launch_installer(&app, &package)?;
    let exit_app = app.clone();
    std::thread::spawn(move || {
        std::thread::sleep(Duration::from_millis(500));
        exit_app.exit(0);
    });
    Ok(())
}

fn update_client() -> Result<reqwest::Client, String> {
    reqwest::Client::builder()
        .user_agent("FubaoConsole/1.0 (+https://github.com/ccvar/fubao-v2-releases)")
        .connect_timeout(Duration::from_secs(10))
        .timeout(Duration::from_secs(180))
        .build()
        .map_err(|error| format!("初始化更新服务失败：{error}"))
}

fn validate_package(package: &UpdatePackage) -> Result<(), String> {
    if !package
        .url
        .starts_with("https://github.com/ccvar/fubao-v2-releases/")
    {
        return Err("更新包来源不受信任".to_string());
    }
    let checksum = package.sha256.trim();
    if checksum.len() != 64 || !checksum.chars().all(|value| value.is_ascii_hexdigit()) {
        return Err("更新包缺少有效的 SHA-256 校验值".to_string());
    }
    if package.filename.trim().is_empty()
        || Path::new(&package.filename)
            .file_name()
            .and_then(|name| name.to_str())
            != Some(package.filename.as_str())
    {
        return Err("更新包文件名无效".to_string());
    }
    Ok(())
}

fn is_newer_version(latest: &str, current: &str) -> Result<bool, String> {
    let latest = Version::parse(latest.trim().trim_start_matches('v'))
        .map_err(|_| "更新清单版本号无效".to_string())?;
    let current = Version::parse(current.trim().trim_start_matches('v'))
        .map_err(|_| "当前客户端版本号无效".to_string())?;
    Ok(latest > current)
}

fn unique_destination(directory: &Path, filename: &str) -> PathBuf {
    let destination = directory.join(filename);
    if !destination.exists() {
        return destination;
    }
    let path = Path::new(filename);
    let stem = path
        .file_stem()
        .and_then(|value| value.to_str())
        .unwrap_or("fubao-update");
    let extension = path
        .extension()
        .and_then(|value| value.to_str())
        .unwrap_or("");
    for index in 1..1000 {
        let name = if extension.is_empty() {
            format!("{stem} ({index})")
        } else {
            format!("{stem} ({index}).{extension}")
        };
        let candidate = directory.join(name);
        if !candidate.exists() {
            return candidate;
        }
    }
    let fallback = if extension.is_empty() {
        format!("{stem}-{}", std::process::id())
    } else {
        format!("{stem}-{}.{extension}", std::process::id())
    };
    directory.join(fallback)
}

#[cfg(target_os = "macos")]
fn platform_key() -> &'static str {
    "macos"
}

#[cfg(target_os = "windows")]
fn platform_key() -> &'static str {
    "windows"
}

#[cfg(not(any(target_os = "macos", target_os = "windows")))]
fn platform_key() -> &'static str {
    "unsupported"
}

#[cfg(target_os = "macos")]
fn launch_installer(_app: &AppHandle, package: &DownloadedPackage) -> Result<(), String> {
    use std::os::unix::fs::PermissionsExt;

    let executable =
        std::env::current_exe().map_err(|error| format!("读取当前客户端路径失败：{error}"))?;
    let target_app = executable
        .ancestors()
        .find(|path| path.extension().and_then(|value| value.to_str()) == Some("app"))
        .ok_or_else(|| "开发模式不能自动替换 App，请使用正式安装版测试升级".to_string())?;
    let helper_dir = std::env::temp_dir().join("fubao-console-updater");
    std::fs::create_dir_all(&helper_dir).map_err(|error| format!("准备升级程序失败：{error}"))?;
    let script_path = helper_dir.join(format!("install-{}.sh", std::process::id()));
    let log_path = std::env::temp_dir().join("fubao-console-updater.log");
    let staged_app = helper_dir.join(format!("staged-{}.app", std::process::id()));
    let script = format!(
        r#"#!/bin/bash
set -u
DMG={dmg}
TARGET_APP={target}
STAGED_APP={staged}
PID={pid}
LOG={log}
MOUNT=""
SUCCESS=0
wait_for_app_exit() {{
  for _i in $(/usr/bin/seq 1 90); do
    if ! /bin/kill -0 "$PID" 2>/dev/null; then return 0; fi
    /bin/sleep 1
  done
  return 1
}}
cleanup() {{
  if [[ -n "$MOUNT" ]]; then /usr/bin/hdiutil detach "$MOUNT" -quiet >/dev/null 2>&1 || true; fi
  /bin/rm -rf "$STAGED_APP" >/dev/null 2>&1 || true
  if [[ "$SUCCESS" -ne 1 && -d "$TARGET_APP" ]]; then
    wait_for_app_exit >/dev/null 2>&1 || true
    /usr/bin/open -n "$TARGET_APP" >> "$LOG" 2>&1 || true
  fi
}}
trap cleanup EXIT
echo "[$(/bin/date '+%F %T')] preparing update $DMG" >> "$LOG"
ATTACH_OUTPUT=$(/usr/bin/hdiutil attach "$DMG" -nobrowse -readonly 2>> "$LOG") || {{ echo "mount failed" >> "$LOG"; exit 1; }}
MOUNT=$(printf '%s\n' "$ATTACH_OUTPUT" | /usr/bin/sed -n 's#^.*\(/Volumes/.*\)$#\1#p' | /usr/bin/tail -n 1)
if [[ -z "$MOUNT" || ! -d "$MOUNT" ]]; then echo "mounted volume not found" >> "$LOG"; exit 1; fi
NEW_APP=$(/usr/bin/find "$MOUNT" -maxdepth 2 -name '*.app' -type d -print | /usr/bin/head -n 1)
if [[ -z "$NEW_APP" || ! -d "$NEW_APP" ]]; then echo "new app not found" >> "$LOG"; exit 1; fi
/bin/rm -rf "$STAGED_APP"
if ! /usr/bin/ditto "$NEW_APP" "$STAGED_APP" >> "$LOG" 2>&1; then echo "staging failed" >> "$LOG"; exit 1; fi
if [[ ! -d "$STAGED_APP/Contents" ]]; then echo "staged app is invalid" >> "$LOG"; exit 1; fi
if ! wait_for_app_exit; then echo "app still running" >> "$LOG"; exit 1; fi
BACKUP="${{TARGET_APP}}.old.$(/bin/date '+%Y%m%d%H%M%S')"
if ! /bin/mv "$TARGET_APP" "$BACKUP" >> "$LOG" 2>&1; then echo "backup failed" >> "$LOG"; exit 1; fi
if /bin/mv "$STAGED_APP" "$TARGET_APP" >> "$LOG" 2>&1; then
  if /usr/bin/open -n "$TARGET_APP" >> "$LOG" 2>&1; then
    /bin/rm -rf "$BACKUP"
    SUCCESS=1
    echo "update success" >> "$LOG"
    exit 0
  fi
  echo "new app launch failed" >> "$LOG"
fi
/bin/rm -rf "$TARGET_APP"
/bin/mv "$BACKUP" "$TARGET_APP"
exit 1
"#,
        dmg = shell_quote(&package.path.to_string_lossy()),
        target = shell_quote(&target_app.to_string_lossy()),
        staged = shell_quote(&staged_app.to_string_lossy()),
        pid = std::process::id(),
        log = shell_quote(&log_path.to_string_lossy()),
    );
    std::fs::write(&script_path, script).map_err(|error| format!("写入升级程序失败：{error}"))?;
    let mut permissions = std::fs::metadata(&script_path)
        .map_err(|error| format!("读取升级程序失败：{error}"))?
        .permissions();
    permissions.set_mode(0o700);
    std::fs::set_permissions(&script_path, permissions)
        .map_err(|error| format!("设置升级程序权限失败：{error}"))?;
    Command::new("/usr/bin/nohup")
        .arg("/bin/bash")
        .arg(script_path)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .map_err(|error| format!("启动升级程序失败：{error}"))?;
    Ok(())
}

#[cfg(target_os = "macos")]
fn shell_quote(value: &str) -> String {
    format!("'{}'", value.replace('\'', "'\"'\"'"))
}

#[cfg(target_os = "windows")]
fn launch_installer(_app: &AppHandle, package: &DownloadedPackage) -> Result<(), String> {
    use std::os::windows::process::CommandExt;

    const CREATE_NO_WINDOW: u32 = 0x0800_0000;
    let helper_dir = std::env::temp_dir().join("fubao-console-updater");
    std::fs::create_dir_all(&helper_dir).map_err(|error| format!("准备升级程序失败：{error}"))?;
    let script_path = helper_dir.join(format!("install-{}.ps1", std::process::id()));
    let script = windows_installer_script(&package.path, std::process::id());
    std::fs::write(&script_path, script).map_err(|error| format!("写入升级程序失败：{error}"))?;
    let mut helper = Command::new("powershell");
    helper
        .args([
            "-NoLogo",
            "-NoProfile",
            "-NonInteractive",
            "-ExecutionPolicy",
            "Bypass",
            "-File",
        ])
        .arg(script_path)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .creation_flags(CREATE_NO_WINDOW)
        .spawn()
        .map_err(|error| format!("启动升级程序失败：{error}"))?;
    Ok(())
}

#[cfg(any(target_os = "windows", test))]
fn windows_installer_script(installer: &Path, pid: u32) -> String {
    format!(
        r#"$ErrorActionPreference = 'Stop'
$installer = {installer}
$pidToWait = {pid}
try {{ Wait-Process -Id $pidToWait -Timeout 90 -ErrorAction SilentlyContinue }} catch {{}}
$arguments = @('/P', '/R', '/UPDATE')
$process = Start-Process -FilePath $installer -ArgumentList $arguments -PassThru -Wait
exit $process.ExitCode
"#,
        installer = powershell_quote(&installer.to_string_lossy()),
    )
}

#[cfg(any(target_os = "windows", test))]
fn powershell_quote(value: &str) -> String {
    format!("'{}'", value.replace('\'', "''"))
}

#[cfg(not(any(target_os = "macos", target_os = "windows")))]
fn launch_installer(_app: &AppHandle, _package: &DownloadedPackage) -> Result<(), String> {
    Err("当前平台暂不支持自动升级".to_string())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn compares_semantic_versions() {
        assert!(is_newer_version("0.2.0", "0.1.9").unwrap());
        assert!(!is_newer_version("v0.1.0", "0.1.0").unwrap());
        assert!(!is_newer_version("0.1.9", "0.2.0").unwrap());
    }

    #[test]
    fn rejects_untrusted_packages() {
        let package = UpdatePackage {
            url: "https://example.com/fubao.exe".to_string(),
            filename: "fubao.exe".to_string(),
            sha256: "a".repeat(64),
            size: 1,
        };
        assert!(validate_package(&package).is_err());
    }

    #[test]
    fn windows_update_uses_tauri_nsis_update_mode() {
        let script = windows_installer_script(Path::new("C:\\Temp\\福宝 setup.exe"), 42);
        assert!(script.contains("@('/P', '/R', '/UPDATE')"));
        assert!(script.contains("Wait-Process -Id $pidToWait"));
        assert!(script.contains("-PassThru -Wait"));
    }
}
