<script lang="ts">
  import { invoke } from "@tauri-apps/api/core";
  import { getVersion } from "@tauri-apps/api/app";
  import { emit, listen } from "@tauri-apps/api/event";
  import { tick, onMount } from "svelte";
  import ConfirmDialog from "./lib/ConfirmDialog.svelte";
  import appIconUrl from "../src-tauri/icons/64x64.png";
  import {
    PulseIcon as Activity,
    ArrowClockwiseIcon as ArrowClockwise,
    ArrowSquareOutIcon as ArrowSquareOut,
    BrowserIcon as Browser,
    CaretDownIcon as CaretDown,
    CheckCircleIcon as CheckCircle,
    ClockCountdownIcon as ClockCountdown,
    ClipboardTextIcon as ClipboardText,
    DotsThreeIcon as DotsThree,
    DownloadSimpleIcon as DownloadSimple,
    FileArrowUpIcon as FileArrowUp,
    FolderOpenIcon as FolderOpen,
    GearSixIcon as GearSix,
    GiftIcon as Gift,
    MagnifyingGlassIcon as MagnifyingGlass,
    MonitorIcon as Monitor,
    PauseIcon as Pause,
    PlayIcon as Play,
    PlusIcon as Plus,
    QrCodeIcon as QrCode,
    RadioIcon as Radio,
    ShieldCheckIcon as ShieldCheck,
    SidebarSimpleIcon as SidebarSimple,
    SlidersHorizontalIcon as SlidersHorizontal,
    TerminalWindowIcon as TerminalWindow,
    TrashIcon as Trash,
    UploadSimpleIcon as UploadSimple,
    UserCircleIcon as UserCircle,
    UserFocusIcon as UserFocus,
    WarningCircleIcon as WarningCircle,
    WifiHighIcon as WifiHigh,
    XIcon as X,
  } from "phosphor-svelte";

  type NavKey = "overview" | "tasks" | "browsers" | "accounts";
  type MonitorState = "running" | "paused" | "warning";
  type AccountRole = "monitoring" | "participation";
  type ManagementTab = "redpackets" | "rooms" | AccountRole;
  type AccountStatusFilter = "all" | "available" | "expired" | "cooldown";

  type LicenseStatus = {
    state: "inactive" | "active" | "expired" | "stale" | "machine_mismatch";
    edition: "免费版" | "专业版";
    label: string;
    tone: "neutral" | "success" | "warning" | "danger";
    detail: string;
    machine_code: string;
    plan?: string;
    expires_at?: string;
    last_validated_at?: string;
    license_key_masked?: string;
  };

  type LicenseOperation = { success: boolean; message: string; status: LicenseStatus };

  type UpdateStatus = {
    current_version: string;
    latest_version: string;
    available: boolean;
    notes: string;
    force: boolean;
    filename: string;
    size: number;
  };

  type UpdateProgress = {
    downloaded: number;
    total: number;
    percent: number;
  };

  type AccountProfile = {
    enabled: boolean;
	cookie_status?: "unknown" | "valid" | "expired";
	cookie_message?: string;
	cookie_checked_at?: string;
    last_error?: string;
    last_used_at?: string;
    last_use_status?: string;
    total_request_count?: number;
    today_request_count?: number;
    join_count?: number;
    win_count?: number;
    last_join_at?: string;
    proxy_id?: number;
    fingerprint_profile_id?: number;
    tags?: string[];
  };

  type AccountItem = {
    id: string;
    name: string;
    nickname?: string;
    user_id?: string;
    cookie_status?: "unknown" | "valid" | "expired";
    cookie_message?: string;
    cookie_checked_at?: string;
    source?: string;
    roles: AccountRole[];
    monitoring?: AccountProfile;
    participation?: AccountProfile;
    created_at: string;
    updated_at: string;
  };

  type CookieValidation = {
    account_id: string;
    status: "unknown" | "valid" | "expired";
    message: string;
    checked_at: string;
  };

  type BrowserInstance = {
    id: string;
    name: string;
    account_id: string;
    account_name: string;
    status: "online" | "stopped";
    browser: string;
    created_at: string;
    updated_at: string;
    opened_at?: string;
    credential_updated_at?: string;
    runtime_state?: "stopped" | "waiting" | "running";
    queue_position?: number;
  };

  type BrowserCapacity = {
    mode: "auto";
    total: number;
    running: number;
    waiting: number;
    recommended_limit: number;
    effective_limit: number;
    available_slots: number;
    estimated_per_instance_bytes: number;
    resources: {
      cpu_count: number;
      memory_total_bytes: number;
      memory_available_bytes: number;
      pressure: "unknown" | "normal" | "constrained" | "critical";
    };
    message: string;
  };

  type BrowserAdmission = {
    granted: boolean;
    state: "stopped" | "waiting" | "running";
    queue_position?: number;
    capacity: BrowserCapacity;
  };

  type RoomItem = {
    id: string;
    web_rid?: string;
    actual_room_id?: string;
    name?: string;
    streamer_name?: string;
    monitor_status: string;
    connection_status: string;
    enabled: boolean;
    source?: string;
    created_at: string;
    updated_at: string;
  };

  type RedPacketMonitor = {
    id: string;
    room_id: string;
    web_rid?: string;
    actual_room_id?: string;
    name?: string;
    streamer_name?: string;
    account_id?: string;
    account_name?: string;
    status: string;
    connection_status: string;
    live_status: "unknown" | "live" | "offline" | "error" | string;
    live_status_source?: string;
    live_raw_status?: string;
    enabled: boolean;
    last_checked_at?: string;
    last_live_checked_at?: string;
    last_red_packet_checked_at?: string;
    last_event_at?: string;
    last_error?: string;
    last_packet_id?: string;
    last_packet_title?: string;
    last_participant_count?: number;
    packet_count: number;
    created_at: string;
    updated_at: string;
  };

  type RedPacketEvent = {
    id: string;
    monitor_id: string;
	account_id?: string;
	account_name?: string;
    room_id: string;
    room_name?: string;
    streamer_name?: string;
    web_rid?: string;
    packet_id: string;
    title?: string;
    prize?: string;
    source: string;
    detected_at: string;
    draw_at?: string;
	expires_at?: string;
    participant_count?: number;
  };

  type MonitorRuntimeLog = {
    id: string;
    at: string;
    level: "info" | "success" | "warning" | "error";
    message: string;
  };

  type EngineEnvelope<T = unknown> = {
    v: number;
    id?: string;
    ok?: boolean;
    result?: T;
    error?: { code: string; message: string };
    event?: string;
  };

  type MonitorItem = {
    id: number;
    name: string;
    room: string;
    anchor: string;
    state: MonitorState;
    lastSeen: string;
    events: number;
    account: string;
    accent: string;
  };

  const navItems = [
    { key: "overview" as NavKey, label: "监测总览", icon: Monitor },
    { key: "tasks" as NavKey, label: "红包任务", icon: Gift },
    { key: "browsers" as NavKey, label: "浏览器实例", icon: Browser },
    { key: "accounts" as NavKey, label: "账号与直播间", icon: UserFocus },
  ];

  const viewMeta: Record<NavKey, { title: string; subtitle: string }> = {
    overview: { title: "监测总览", subtitle: "4 个直播间 · 3 个运行中" },
    tasks: { title: "红包任务", subtitle: "12 个候选 · 2 个等待结果" },
    browsers: { title: "浏览器实例", subtitle: "3 个实例 · 2 个在线" },
    accounts: { title: "账号与直播间", subtitle: "直播间与账号数据" },
  };

  let activeView: NavKey = "overview";
  let clientVersion = __APP_VERSION__;
  let updateStatus: UpdateStatus | null = null;
  let updateChecking = false;
  let updateModalOpen = false;
  let updateDownloading = false;
  let updateDownloaded = false;
  let updateInstalling = false;
  let updateProgress: UpdateProgress = { downloaded: 0, total: 0, percent: 0 };
  let updateError = "";
  let licenseStatus: LicenseStatus = {
    state: "inactive",
    edition: "免费版",
    label: "免费版",
    tone: "neutral",
    detail: "输入激活码可升级为专业版。",
    machine_code: "",
  };
  let licenseModalOpen = false;
  let licenseKey = "";
  let licenseBusy = false;
  let licenseError = "";
  let query = "";
  let searchOpen = false;
  let topbarSearchInput: HTMLInputElement;
  let modalOpen = false;
  let toast = "";
  let refreshing = false;
  let newRoom = "";
  let newName = "";
  let instanceModalOpen = false;
  let browserInstances: BrowserInstance[] = [];
  let browserCapacity: BrowserCapacity | null = null;
  let browserLoading = false;
  let browserStatusPolling = false;
  let browserCookieSyncing = false;
  let browserError = "";
  let browserCreating = false;
  let browserOpeningId = "";
  let browserClosingId = "";
  let browserWebviewMountingIds: string[] = [];
  let browserWebviewMountedIds: string[] = [];
  let browserWebviewReleasingIds: string[] = [];
  let browserWebviewErrors: Record<string, string> = {};
  let browserWebviewSyncFrame = 0;
  let browserViewSettled = false;
  let browserColumns = 2;
  let browserColumnSyncTimer = 0;
  let browserLayoutRevision = 0;
  let browserLayoutChanging = false;
  let selectedParticipationAccountIds: string[] = [];
  let managementTab: ManagementTab = "rooms";
  let rooms: RoomItem[] = [];
  let roomsLoading = false;
  let roomsMigrating = false;
  let roomError = "";
  let roomRenderLimit = 300;
  let roomImportModalOpen = false;
  let roomImportText = "";
  let roomImportBusy = false;
  let roomImportFileInput: HTMLInputElement;
  let redPacketMonitors: RedPacketMonitor[] = [];
  let redPacketMonitorOverrides: Record<string, { status: string; connectionStatus: string }> = {};
  let redPacketMonitorListRequestSeq = 0;
  let redPacketMonitorsLoading = false;
  let redPacketMonitorError = "";
  let redPacketEvents: RedPacketEvent[] = [];
  let redPacketEventsInitialized = false;
  let redPacketEventsLoading = false;
  let redPacketEventError = "";
  let redPacketRenderLimit = 300;
  let redPacketClock = Date.now();
  let redPacketBatchAction: "start" | "stop" | "" = "";
  let redPacketMonitorActionId = "";
  let monitorRuntimeLogs: MonitorRuntimeLog[] = [];
  let accountRole: AccountRole = "participation";
  let accounts: AccountItem[] = [];
  let accountsLoading = false;
  let accountsMigrating = false;
  let accountError = "";
  let accountStatusFilter: AccountStatusFilter = "all";
  let statusMenuOpen = false;
  let importMenuOpen = false;
  let accountFileInput: HTMLInputElement;
  let accountFolderInput: HTMLInputElement;
  let accountPendingDelete: AccountItem | null = null;
  let accountDeleting = false;
  let accountRebinding: AccountItem | null = null;
	let accountRebindRole: AccountRole = "participation";
  let accountCreateSessionId = "";
  let accountCreateRole: AccountRole = "participation";
  let accountRebindOpeningId = "";
  let accountRebindCompleting = false;
  let accountRebindViewport: HTMLDivElement;
  let accountRebindSyncFrame = 0;
  let cookieValidatingAccountIds: string[] = [];
  let engineListenerReady = false;
  let sidebarWidth = 252;
	let sidebarCollapsed = false;
  let resizingSidebar = false;
  let resizeStartX = 0;
  let resizeStartWidth = 252;
  let monitors: MonitorItem[] = [
    {
      id: 1,
      name: "星选福利直播间",
      room: "712936518204",
      anchor: "星选好物",
      state: "running",
      lastSeen: "刚刚",
      events: 18,
      account: "监测账号 01",
      accent: "#d7553f",
    },
    {
      id: 2,
      name: "数码新品频道",
      room: "884203716925",
      anchor: "科技现场",
      state: "running",
      lastSeen: "8 秒前",
      events: 6,
      account: "监测账号 02",
      accent: "#5879d8",
    },
    {
      id: 3,
      name: "晚间红包专场",
      room: "519740268331",
      anchor: "城市优选",
      state: "warning",
      lastSeen: "2 分钟前",
      events: 3,
      account: "等待账号",
      accent: "#b9812f",
    },
    {
      id: 4,
      name: "品牌日常直播",
      room: "307185946122",
      anchor: "生活研究所",
      state: "paused",
      lastSeen: "18 分钟前",
      events: 0,
      account: "监测账号 03",
      accent: "#6c8c80",
    },
  ];

  const taskCards = [
    { title: "进行中的参与", value: "3", detail: "分布在 2 个直播间", tone: "coral" },
    { title: "等待开奖结果", value: "2", detail: "最近开奖 42 秒后", tone: "blue" },
    { title: "今日已完成", value: "27", detail: "成功参与 24 次", tone: "green" },
  ];

  $: visibleMonitors = monitors.filter((item) => {
    const haystack = `${item.name} ${item.anchor} ${item.room}`.toLowerCase();
    return haystack.includes(query.trim().toLowerCase());
  });
  $: monitoringAccounts = accounts.filter((account) => account.roles.includes("monitoring"));
  $: participationAccounts = accounts.filter((account) => account.roles.includes("participation"));
  $: accountSubtitle = `${rooms.length} 个直播间 · ${participationAccounts.length} 个参与 · ${monitoringAccounts.length} 个监测`;
  $: filteredRooms = rooms.filter((room) => {
    const haystack = `${room.name ?? ""} ${room.streamer_name ?? ""} ${room.web_rid ?? ""} ${room.actual_room_id ?? ""} ${room.id}`.toLowerCase();
    return haystack.includes(query.trim().toLowerCase());
  });
  $: visibleRooms = filteredRooms.slice(0, roomRenderLimit);
  $: enabledRedPacketMonitors = redPacketMonitors.filter((monitor) => monitor.enabled);
  $: runningRedPacketMonitorCount = enabledRedPacketMonitors.filter((monitor) => redPacketMonitorUiStatus(monitor) === "running").length;
  $: canStartAnyRedPacketMonitor = enabledRedPacketMonitors.some((monitor) => redPacketMonitorUiStatus(monitor) !== "running");
  $: canStopAnyRedPacketMonitor = runningRedPacketMonitorCount > 0;
  $: activeRedPacketCount = redPacketEvents.filter(redPacketEventIsActive).length;
	$: expiredParticipationAccountCount = participationAccounts.filter((account) => accountCookieStatus(account, "participation") === "expired").length;
	$: expiredMonitoringAccountCount = monitoringAccounts.filter((account) => accountCookieStatus(account, "monitoring") === "expired").length;
  $: filteredRedPacketEvents = redPacketEvents.filter((event) => {
    const needle = query.trim().toLowerCase();
    if (!needle) return true;
    return [event.title, event.prize, event.room_name, event.streamer_name, event.web_rid, event.room_id]
      .filter(Boolean)
      .some((value) => String(value).toLowerCase().includes(needle));
  });
  $: visibleRedPacketEvents = filteredRedPacketEvents.slice(0, redPacketRenderLimit);
  $: browserSubtitle = browserCapacity
    ? `${browserInstances.length} 个实例 · ${browserCapacity.running} 个运行 · 建议上限 ${browserCapacity.recommended_limit}${browserCapacity.waiting > 0 ? ` · ${browserCapacity.waiting} 个等待` : ""}`
    : `${browserInstances.length} 个实例 · ${browserInstances.filter((item) => item.status === "online").length} 个在线 · ${browserInstances.filter(browserCookieExpired).length} 个失效`;
  $: visibleBrowserInstances = browserInstances.filter((item) => {
    const haystack = `${item.name} ${item.account_name} ${item.browser}`.toLowerCase();
    return haystack.includes(query.trim().toLowerCase());
  });
  $: browserWebviewLayoutKey = `${activeView}:${browserViewSettled}:${instanceModalOpen}:${licenseModalOpen}:${sidebarCollapsed}:${query}:${visibleBrowserInstances
    .map((instance) => instance.id)
    .join(",")}`;
  $: if (browserWebviewLayoutKey) scheduleEmbeddedBrowserSync();
  $: selectableParticipationAccounts = participationAccounts.filter(
    (account) =>
      account.participation?.enabled &&
      !browserInstances.some((instance) => instance.account_id === account.id),
  );
  $: visibleAccounts = (accountRole === "monitoring" ? monitoringAccounts : participationAccounts).filter(
    (account) => {
      const haystack = `${account.name} ${account.nickname ?? ""} ${account.user_id ?? ""}`.toLowerCase();
      const searchMatches = haystack.includes(query.trim().toLowerCase());
      const status = accountStatus(account);
      const statusMatches =
        accountStatusFilter === "all" ||
        (accountStatusFilter === "available" && status === "可用") ||
        (accountStatusFilter === "expired" && status === "CK 失效") ||
        (accountStatusFilter === "cooldown" && status === "冷却中");
      return searchMatches && statusMatches;
    },
  );

  const pendingRequests = new Map<
    string,
    { resolve: (value: unknown) => void; reject: (error: Error) => void; timer: number }
  >();

  function handleEngineMessage(raw: string) {
    let message: EngineEnvelope;
    try {
      message = JSON.parse(raw);
    } catch {
      return;
    }
    if (!message.id) return;
    const pending = pendingRequests.get(message.id);
    if (!pending) return;
    window.clearTimeout(pending.timer);
    pendingRequests.delete(message.id);
    if (message.ok) {
      pending.resolve(message.result);
    } else {
      pending.reject(new Error(message.error?.message || "Go 引擎请求失败"));
    }
  }

  async function engineRequest<T>(method: string, params: Record<string, unknown> = {}): Promise<T> {
    const id = `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 9)}`;
    const result = new Promise<T>((resolve, reject) => {
      const timer = window.setTimeout(() => {
        pendingRequests.delete(id);
        reject(new Error("Go 引擎响应超时"));
      }, method.startsWith("license.") ? 35000 : 12000);
      pendingRequests.set(id, {
        resolve: (value) => resolve(value as T),
        reject,
        timer,
      });
    });
    try {
      await invoke("engine_send", {
        payload: JSON.stringify({ v: 1, id, method, params }),
      });
    } catch (error) {
      const pending = pendingRequests.get(id);
      if (pending) {
        window.clearTimeout(pending.timer);
        pendingRequests.delete(id);
        pending.reject(error instanceof Error ? error : new Error(String(error)));
      }
    }
    return result;
  }

  async function loadLicenseStatus() {
    if (!engineListenerReady) return;
    try {
      licenseStatus = await engineRequest<LicenseStatus>("license.status");
    } catch (error) {
      licenseError = error instanceof Error ? error.message : String(error);
    }
  }

  async function openLicenseModal() {
    await hideEmbeddedBrowsers();
    licenseError = "";
    licenseModalOpen = true;
    void loadLicenseStatus();
  }

  function closeLicenseModal() {
    if (licenseBusy) return;
    licenseModalOpen = false;
    licenseKey = "";
    licenseError = "";
    scheduleEmbeddedBrowserSync();
  }

  async function activateLicense() {
    if (!licenseKey.trim() || licenseBusy) return;
    licenseBusy = true;
    licenseError = "";
    try {
      const result = await engineRequest<LicenseOperation>("license.activate", { license_key: licenseKey.trim() });
      licenseStatus = result.status;
      if (!result.success) {
        licenseError = result.message;
        return;
      }
      licenseKey = "";
      toast = result.message;
      window.setTimeout(() => (toast = ""), 2200);
    } catch (error) {
      licenseError = error instanceof Error ? error.message : String(error);
    } finally {
      licenseBusy = false;
    }
  }

  async function refreshLicense() {
    if (licenseBusy) return;
    licenseBusy = true;
    licenseError = "";
    try {
      const result = await engineRequest<LicenseOperation>("license.refresh");
      licenseStatus = result.status;
      if (!result.success) licenseError = result.message;
      else {
        toast = result.message;
        window.setTimeout(() => (toast = ""), 2200);
      }
    } catch (error) {
      licenseError = error instanceof Error ? error.message : String(error);
    } finally {
      licenseBusy = false;
    }
  }

  function formatFileSize(size: number) {
    let value = Math.max(0, Number(size) || 0);
    for (const unit of ["B", "KB", "MB", "GB"]) {
      if (value < 1024 || unit === "GB") {
        return unit === "B" ? `${Math.round(value)} ${unit}` : `${value.toFixed(1)} ${unit}`;
      }
      value /= 1024;
    }
    return `${value.toFixed(1)} GB`;
  }

  async function checkForAppUpdate(silent = false, openWhenAvailable = false) {
    if (!isTauriDesktop() || updateChecking) return;
    updateChecking = true;
    if (!silent) updateError = "";
    try {
      updateStatus = await invoke<UpdateStatus>("check_app_update");
      if (updateStatus.available) {
        if (openWhenAvailable) await openUpdateModal();
      } else if (!silent) {
        showToast(`当前 v${clientVersion} 已是最新版本`);
      }
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      if (!silent) {
        updateError = message;
        showToast(message);
      }
    } finally {
      updateChecking = false;
    }
  }

  async function openUpdateModal() {
    if (!updateStatus?.available) {
      await checkForAppUpdate(false, true);
      return;
    }
    await hideEmbeddedBrowsers();
    updateError = "";
    updateModalOpen = true;
  }

  function closeUpdateModal() {
    if (updateDownloading || updateInstalling) return;
    updateModalOpen = false;
    scheduleEmbeddedBrowserSync();
  }

  async function downloadAppUpdate() {
    if (updateDownloading || updateDownloaded) return;
    updateDownloading = true;
    updateError = "";
    updateProgress = { downloaded: 0, total: updateStatus?.size || 0, percent: 0 };
    try {
      await invoke("download_app_update");
      updateDownloaded = true;
      updateProgress = {
        downloaded: updateStatus?.size || updateProgress.downloaded,
        total: updateStatus?.size || updateProgress.total,
        percent: 100,
      };
    } catch (error) {
      updateError = error instanceof Error ? error.message : String(error);
    } finally {
      updateDownloading = false;
    }
  }

  async function installAppUpdate() {
    if (!updateDownloaded || updateInstalling) return;
    updateInstalling = true;
    updateError = "";
    try {
      await invoke("install_app_update");
    } catch (error) {
      updateInstalling = false;
      updateError = error instanceof Error ? error.message : String(error);
    }
  }

  function stateLabel(state: MonitorState) {
    return state === "running" ? "监测中" : state === "warning" ? "需处理" : "已暂停";
  }

  function eventTimestamp(value?: string) {
    const text = value?.trim();
    if (!text) return 0;
    if (/^\d+(?:\.\d+)?$/.test(text)) {
      const numeric = Number(text);
      if (!Number.isFinite(numeric) || numeric <= 0) return 0;
      return numeric < 1_000_000_000_000 ? numeric * 1000 : numeric;
    }
    const parsed = Date.parse(text);
    return Number.isFinite(parsed) ? parsed : 0;
  }

  function redPacketEventIsActive(event: RedPacketEvent) {
    const drawAt = eventTimestamp(event.expires_at || event.draw_at);
    return drawAt > redPacketClock;
  }

  function redPacketEventExpiryParts(event: RedPacketEvent) {
    const value = event.expires_at || event.draw_at;
    const timestamp = eventTimestamp(value);
    if (!timestamp) return { countdown: "", absolute: "", text: "过期时间待解析" };
    const absolute = new Date(timestamp).toLocaleString("zh-CN", {
      month: "numeric",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
      hour12: false,
    });
    const seconds = Math.max(0, Math.ceil((timestamp - redPacketClock) / 1000));
    const hours = Math.floor(seconds / 3600);
    const minutes = Math.floor((seconds % 3600) / 60);
    const remainingSeconds = seconds % 60;
    const pad = (value: number) => String(value).padStart(2, "0");
    const countdown = hours > 0
      ? `${pad(hours)}:${pad(minutes)}:${pad(remainingSeconds)}`
      : `${pad(minutes)}:${pad(remainingSeconds)}`;
    return { countdown, absolute, text: `${countdown} · ${absolute}` };
  }

  function redPacketEventExpiry(event: RedPacketEvent) {
    return redPacketEventExpiryParts(event).text;
  }

  async function openRedPacketLiveRoom(event: RedPacketEvent) {
    const webRID = (event.web_rid || event.room_id || "").trim();
    if (!/^\d{6,24}$/.test(webRID)) {
      showToast("暂未读取到可打开的直播间地址");
      return;
    }
    if (!("__TAURI_INTERNALS__" in window)) {
      window.open(`https://live.douyin.com/${webRID}`, "_blank", "noopener,noreferrer");
      return;
    }
    try {
      await invoke("open_live_room", { webRid: webRID });
    } catch (error) {
      showToast(error instanceof Error ? error.message : String(error));
    }
  }

  function showExpiredAccounts(role: AccountRole) {
    switchView("accounts");
    managementTab = role;
    accountRole = role;
    accountStatusFilter = "expired";
  }

  function browserAccountDouyinId(instance: BrowserInstance) {
    const userId = accounts.find((account) => account.id === instance.account_id)?.user_id?.trim();
    return userId || "尚未读取";
  }

  function switchView(key: NavKey) {
    if (activeView === "browsers" && key !== "browsers") {
      void releaseEmbeddedBrowsers();
    }
    browserViewSettled = false;
    activeView = key;
    query = "";
    searchOpen = false;
    statusMenuOpen = false;
    importMenuOpen = false;
    if (engineListenerReady && (key === "accounts" || key === "browsers")) {
      void loadAccounts();
    }
    if (key === "accounts" && engineListenerReady) {
      void loadRooms();
      void loadRedPacketMonitors();
      void loadRedPacketEvents();
    }
    if (key === "browsers" && engineListenerReady) {
      void loadBrowserInstances();
    }
  }

  function isTauriDesktop() {
    return "__TAURI_INTERNALS__" in window;
  }

  function browserMountBounds(element: HTMLElement) {
    const rect = element.getBoundingClientRect();
    return {
      x: rect.left,
      y: rect.top,
      width: rect.width,
      height: rect.height,
    };
  }

  function embeddedBrowserBounds(element: HTMLElement) {
    const rect = element.getBoundingClientRect();
    const topbar = document.querySelector<HTMLElement>(".topbar");
    const content = document.querySelector<HTMLElement>(".content");
    const columnControl = document.querySelector<HTMLElement>(".browser-column-control");
    const topBoundary = Math.max(
      topbar?.getBoundingClientRect().bottom ?? 0,
      content?.getBoundingClientRect().top ?? 0,
      0,
    );
    let bottomBoundary = Math.min(
      content?.getBoundingClientRect().bottom ?? window.innerHeight,
      window.innerHeight,
    );
    const leftBoundary = Math.max(content?.getBoundingClientRect().left ?? 0, 0);
    const rightBoundary = Math.min(
      content?.getBoundingClientRect().right ?? window.innerWidth,
      window.innerWidth,
    );
    if (columnControl) {
      const controlRect = columnControl.getBoundingClientRect();
      const tooltipReserve = columnControl.matches(":hover, :focus-within") ? 32 : 0;
      const controlTop = controlRect.top - tooltipReserve - 6;
      const overlapsControl =
        rect.right > controlRect.left - 6 &&
        rect.left < controlRect.right + 6 &&
        rect.bottom > controlTop &&
        rect.top < controlRect.bottom + 6;
      if (overlapsControl) bottomBoundary = Math.min(bottomBoundary, controlTop);
    }
    const left = Math.max(rect.left, leftBoundary);
    const top = Math.max(rect.top, topBoundary);
    const right = Math.min(rect.right, rightBoundary);
    const bottom = Math.min(rect.bottom, bottomBoundary);
    return {
      x: left,
      y: top,
      width: Math.max(0, right - left),
      height: Math.max(0, bottom - top),
    };
  }

  function browserMountIsVisible(element: HTMLElement) {
    const bounds = embeddedBrowserBounds(element);
    return bounds.width >= 120 && bounds.height >= 90;
  }

  function observeBrowserMount(element: HTMLElement) {
    const root = document.querySelector<HTMLElement>(".content");
    const observer = new IntersectionObserver(
      () => scheduleEmbeddedBrowserSync(),
      { root, threshold: [0, 0.01, 0.25] },
    );
    observer.observe(element);
    window.requestAnimationFrame(scheduleEmbeddedBrowserSync);
    return {
      destroy() {
        observer.disconnect();
      },
    };
  }

  async function hideEmbeddedBrowser(instanceId: string) {
    if (!isTauriDesktop()) return;
    try {
      await invoke("hide_browser_webview", { instanceId });
    } catch {
      // A not-yet-mounted child WebView is already effectively hidden.
    }
  }

  async function hideEmbeddedBrowsers() {
    if (!isTauriDesktop()) return;
    await Promise.all(browserInstances.map((instance) => hideEmbeddedBrowser(instance.id)));
  }

  function updateBrowserRuntimeState(
    instanceId: string,
    state: "stopped" | "waiting" | "running",
    queuePosition = 0,
  ) {
    browserInstances = browserInstances.map((instance) =>
      instance.id === instanceId
        ? { ...instance, runtime_state: state, queue_position: queuePosition }
        : instance,
    );
  }

  async function releaseEmbeddedBrowser(instance: BrowserInstance) {
    if (browserWebviewReleasingIds.includes(instance.id)) return;
    const mounted = browserWebviewMountedIds.includes(instance.id) || browserWebviewMountingIds.includes(instance.id);
    const reserved = instance.runtime_state === "running" || instance.runtime_state === "waiting";
    if (!mounted && !reserved) return;
    browserWebviewReleasingIds = [...browserWebviewReleasingIds, instance.id];
    try {
      if (isTauriDesktop() && mounted) {
        await invoke("close_browser_webview", { instanceId: instance.id }).catch(() => undefined);
      }
      browserWebviewMountedIds = browserWebviewMountedIds.filter((id) => id !== instance.id);
      browserWebviewMountingIds = browserWebviewMountingIds.filter((id) => id !== instance.id);
      if (engineListenerReady) {
        browserCapacity = await engineRequest<BrowserCapacity>("browser.runtime.release", {
          instance_id: instance.id,
        }).catch(() => browserCapacity);
      }
      if (instance.status !== "online") updateBrowserRuntimeState(instance.id, "stopped");
    } finally {
      browserWebviewReleasingIds = browserWebviewReleasingIds.filter((id) => id !== instance.id);
    }
  }

  async function releaseEmbeddedBrowsers() {
    await Promise.all(browserInstances.map((instance) => releaseEmbeddedBrowser(instance)));
  }

  async function mountEmbeddedBrowser(instance: BrowserInstance, element: HTMLElement) {
    if (
      browserWebviewMountingIds.includes(instance.id) ||
      activeView !== "browsers" ||
      instanceModalOpen ||
      licenseModalOpen ||
      !browserMountIsVisible(element)
    ) {
      return;
    }
    browserWebviewMountingIds = [...browserWebviewMountingIds, instance.id];
    const nextErrors = { ...browserWebviewErrors };
    delete nextErrors[instance.id];
    browserWebviewErrors = nextErrors;
    try {
      const admission = await engineRequest<BrowserAdmission>("browser.runtime.acquire", {
        instance_id: instance.id,
      });
      browserCapacity = admission.capacity;
      updateBrowserRuntimeState(instance.id, admission.state, admission.queue_position ?? 0);
      if (!admission.granted) return;
      await invoke("mount_browser_webview", {
        instanceId: instance.id,
        bounds: embeddedBrowserBounds(element),
      });
      // The native mount can finish after the user has switched views, opened
      // a modal, or scrolled the card away. Re-check the final UI state before
      // publishing the mounted surface so a late response cannot leave an
      // orphan WebView consuming a runtime slot.
      if (
        activeView !== "browsers" ||
        instanceModalOpen ||
        licenseModalOpen ||
        !element.isConnected ||
        !browserMountIsVisible(element)
      ) {
        await invoke("close_browser_webview", { instanceId: instance.id }).catch(() => undefined);
        browserCapacity = await engineRequest<BrowserCapacity>("browser.runtime.release", {
          instance_id: instance.id,
        }).catch(() => browserCapacity);
        updateBrowserRuntimeState(instance.id, "stopped");
        return;
      }
      if (!browserWebviewMountedIds.includes(instance.id)) {
        browserWebviewMountedIds = [...browserWebviewMountedIds, instance.id];
      }
      // A persistent account profile can already contain a fresh login from
      // the prior session. Synchronize immediately on mount instead of
      // leaving the stale CK badge visible until the polling interval fires.
      const loginStateUpdated = await invoke<boolean>("sync_browser_account_cookie", {
        instanceId: instance.id,
      }).catch(() => false);
      if (loginStateUpdated) {
        accounts = await engineRequest<AccountItem[]>("account.list");
      }
    } catch (error) {
      browserWebviewErrors = {
        ...browserWebviewErrors,
        [instance.id]: error instanceof Error ? error.message : String(error),
      };
      await invoke("close_browser_webview", { instanceId: instance.id }).catch(() => undefined);
      await engineRequest<BrowserCapacity>("browser.runtime.release", {
        instance_id: instance.id,
      }).then((capacity) => {
        browserCapacity = capacity;
        updateBrowserRuntimeState(instance.id, "stopped");
      }).catch(() => undefined);
    } finally {
      browserWebviewMountingIds = browserWebviewMountingIds.filter((id) => id !== instance.id);
    }
  }

  async function syncEmbeddedBrowsers() {
    if (!isTauriDesktop() || browserLayoutChanging) return;
    await tick();
    const visibleIds = new Set(visibleBrowserInstances.map((instance) => instance.id));
    await Promise.all(
      browserInstances.map(async (instance) => {
        const element = document.querySelector<HTMLElement>(
          `[data-browser-instance="${instance.id}"]`,
        );
        if (
          activeView !== "browsers" ||
          !browserViewSettled ||
          instanceModalOpen ||
          licenseModalOpen ||
          !visibleIds.has(instance.id) ||
          !element ||
          !browserMountIsVisible(element) ||
          instance.status === "online"
        ) {
          await releaseEmbeddedBrowser(instance);
          return;
        }
        if (!browserWebviewMountedIds.includes(instance.id)) {
          await mountEmbeddedBrowser(instance, element);
          return;
        }
        try {
          const admission = await engineRequest<BrowserAdmission>("browser.runtime.acquire", {
            instance_id: instance.id,
          });
          browserCapacity = admission.capacity;
          updateBrowserRuntimeState(instance.id, admission.state, admission.queue_position ?? 0);
          if (!admission.granted) {
            await releaseEmbeddedBrowser({ ...instance, runtime_state: "running" });
            return;
          }
          await invoke("sync_browser_webview", {
            instanceId: instance.id,
            bounds: embeddedBrowserBounds(element),
          });
        } catch {
          browserWebviewMountedIds = browserWebviewMountedIds.filter((id) => id !== instance.id);
          await mountEmbeddedBrowser(instance, element);
        }
      }),
    );
  }

  function scheduleEmbeddedBrowserSync() {
    if (!isTauriDesktop() || browserLayoutChanging) return;
    window.cancelAnimationFrame(browserWebviewSyncFrame);
    browserWebviewSyncFrame = window.requestAnimationFrame(() => {
      void syncEmbeddedBrowsers();
    });
  }

  function scheduleBrowserColumnControlSync() {
    window.requestAnimationFrame(scheduleEmbeddedBrowserSync);
    window.setTimeout(scheduleEmbeddedBrowserSync, 170);
  }

  function scheduleAccountRebindSync() {
    if (!isTauriDesktop() || (!accountRebinding && !accountCreateSessionId) || accountRebindOpeningId || !accountRebindViewport) return;
    window.cancelAnimationFrame(accountRebindSyncFrame);
    accountRebindSyncFrame = window.requestAnimationFrame(() => {
      if (!accountRebindViewport) return;
      if (accountRebinding) {
        void invoke("sync_account_rebind", {
          accountId: accountRebinding.id,
          bounds: browserMountBounds(accountRebindViewport),
        }).catch(() => undefined);
      } else if (accountCreateSessionId) {
        void invoke("sync_account_create", {
          sessionId: accountCreateSessionId,
          bounds: browserMountBounds(accountRebindViewport),
        }).catch(() => undefined);
      }
    });
  }

  function closeInstanceModal() {
    instanceModalOpen = false;
    selectedParticipationAccountIds = [];
    void tick().then(scheduleEmbeddedBrowserSync);
  }

  function toggleParticipationAccount(accountId: string) {
    selectedParticipationAccountIds = selectedParticipationAccountIds.includes(accountId)
      ? selectedParticipationAccountIds.filter((id) => id !== accountId)
      : [...selectedParticipationAccountIds, accountId];
  }

  function selectManagementTab(tab: ManagementTab) {
    managementTab = tab;
    query = "";
    roomRenderLimit = 300;
    redPacketRenderLimit = 300;
    accountStatusFilter = "all";
    if (tab === "participation" || tab === "monitoring") accountRole = tab;
    if (tab === "redpackets" && engineListenerReady) void loadRedPacketEvents();
    if (tab === "rooms" && engineListenerReady) {
      void loadRooms();
      void loadRedPacketMonitors();
    }
  }

  function searchPlaceholder() {
    if (activeView === "accounts") {
      if (managementTab === "redpackets") return "搜索红包、直播间或房间号";
      if (managementTab === "rooms") return "搜索直播间、主播或房间号";
      return "搜索账号昵称或抖音号";
    }
    if (activeView === "browsers") return "搜索实例或参与账号";
    return "搜索直播间、主播或房间号";
  }

  async function openTopbarSearch() {
    searchOpen = true;
    await tick();
    topbarSearchInput?.focus();
  }

  function closeTopbarSearch() {
    query = "";
    searchOpen = false;
  }

  function hasAccountRole(account: AccountItem, role: AccountRole) {
    return account.roles.includes(role);
  }

  function oppositeRole(role: AccountRole): AccountRole {
    return role === "monitoring" ? "participation" : "monitoring";
  }

  function roleLabel(role: AccountRole) {
    return role === "monitoring" ? "监测账号" : "参与账号";
  }

  function accountStatus(account: AccountItem) {
    const profile = accountRole === "monitoring" ? account.monitoring : account.participation;
    if (accountCookieStatus(account, accountRole) === "expired") return "CK 失效";
    if (!profile?.enabled || profile.last_error || /冷却|等待|cooldown/i.test(profile.last_use_status || "")) return "冷却中";
    return "可用";
  }

  function accountCookieStatus(account: AccountItem, role: AccountRole) {
    return role === "monitoring" ? account.monitoring?.cookie_status || "unknown" : account.cookie_status || "unknown";
  }

  function accountCookieMessage(account: AccountItem, role: AccountRole) {
    return role === "monitoring" ? account.monitoring?.cookie_message : account.cookie_message;
  }

  function browserCookieExpired(instance: BrowserInstance) {
    return accounts.find((account) => account.id === instance.account_id)?.cookie_status === "expired";
  }

  function browserCookieMessage(instance: BrowserInstance) {
    const message = accounts.find((account) => account.id === instance.account_id)?.cookie_message;
    return (message || "CK 已失效，请重新登录或导入").replaceAll("CK 已过期", "CK 已失效");
  }

  function accountStatusFilterLabel(filter: AccountStatusFilter) {
    if (filter === "available") return "可用";
    if (filter === "expired") return "CK 失效";
    if (filter === "cooldown") return "冷却中";
    return "全部状态";
  }

  function selectAccountStatus(filter: AccountStatusFilter) {
    accountStatusFilter = filter;
    statusMenuOpen = false;
  }

  function toggleImportMenu() {
    importMenuOpen = !importMenuOpen;
    statusMenuOpen = false;
  }

  function toggleStatusMenu() {
    statusMenuOpen = !statusMenuOpen;
    importMenuOpen = false;
  }

  function closeFloatingMenus(event: PointerEvent) {
    const target = event.target as HTMLElement;
    if (!target.closest(".menu-anchor")) {
      statusMenuOpen = false;
      importMenuOpen = false;
    }
  }

  async function pasteAccountCookie() {
    importMenuOpen = false;
    try {
      const cookie = await navigator.clipboard.readText();
      if (!cookie.trim()) {
        showToast("剪贴板里没有 Cookie");
        return;
      }
      showToast("已读取 Cookie，等待识别账号");
    } catch {
      showToast("无法读取剪贴板，请检查系统权限");
    }
  }

  async function startQrLogin() {
    importMenuOpen = false;
    if (!isTauriDesktop()) {
      showToast("扫码登录仅支持桌面客户端");
      return;
    }
    if (accountRebindOpeningId || accountRebindCompleting) return;
    accountCreateRole = managementTab === "monitoring" || managementTab === "participation" ? managementTab : accountRole;
    const sessionId = crypto.randomUUID();
    accountCreateSessionId = sessionId;
    accountRebinding = null;
    accountRebindOpeningId = sessionId;
    await hideEmbeddedBrowsers();
    try {
      await tick();
      if (!accountRebindViewport) throw new Error("登录区域尚未准备完成");
      await invoke("open_account_create", {
        sessionId,
        bounds: browserMountBounds(accountRebindViewport),
      });
    } catch (error) {
      try {
        await invoke("cancel_account_create", { sessionId });
      } catch {
        // Preserve the original open error.
      }
      accountCreateSessionId = "";
      void tick().then(scheduleEmbeddedBrowserSync);
      showToast(error instanceof Error ? error.message : String(error));
    } finally {
      accountRebindOpeningId = "";
    }
  }

  function chooseAccountFiles(folder = false) {
    importMenuOpen = false;
    (folder ? accountFolderInput : accountFileInput)?.click();
  }

  function openRoomImportModal() {
    importMenuOpen = false;
    roomImportText = "";
    roomImportModalOpen = true;
  }

  async function readRoomImportFile(event: Event) {
    const input = event.currentTarget as HTMLInputElement;
    const files = Array.from(input.files ?? []);
    if (files.length === 0) return;
    try {
      const text = await Promise.all(files.map((file) => file.text()));
      roomImportText = text.filter(Boolean).join("\n");
    } catch {
      showToast("无法读取直播间文件");
    } finally {
      input.value = "";
    }
  }

  async function importRoomIDs() {
    if (roomImportBusy || !roomImportText.trim()) return;
    roomImportBusy = true;
    roomError = "";
    try {
      const result = await engineRequest<{ imported: number; merged: number; invalid?: number; total: number }>("room.import_ids", {
        ids: roomImportText,
      });
      rooms = await engineRequest<RoomItem[]>("room.list");
      await loadRedPacketMonitors();
      roomImportModalOpen = false;
      managementTab = "rooms";
      query = "";
      roomRenderLimit = 300;
      const invalidText = result.invalid ? `，${result.invalid} 条无效` : "";
      showToast(`已导入 ${result.imported} 个直播间${result.merged ? `，${result.merged} 个已存在` : ""}${invalidText}`);
    } catch (error) {
      roomError = error instanceof Error ? error.message : String(error);
      showToast(roomError);
    } finally {
      roomImportBusy = false;
    }
  }

  function handleAccountFiles(event: Event, folder = false) {
    const input = event.currentTarget as HTMLInputElement;
    const count = input.files?.length ?? 0;
    if (count > 0) showToast(folder ? `已选择包含 ${count} 个文件的文件夹` : `已选择 ${count} 个账号文件`);
    input.value = "";
  }

  function setDirectoryInput(node: HTMLInputElement) {
    node.setAttribute("webkitdirectory", "");
    node.setAttribute("directory", "");
  }

  function accountMeta(account: AccountItem) {
    if (accountRole === "participation") {
      const profile = account.participation;
      const binding = profile?.fingerprint_profile_id ? `指纹 ${profile.fingerprint_profile_id}` : "未绑定指纹";
      return `参与 ${profile?.join_count ?? 0} 次 · 中奖 ${profile?.win_count ?? 0} 次 · ${binding}`;
    }
    const profile = account.monitoring;
    return `${profile?.total_request_count ?? 0} / ${profile?.today_request_count ?? 0} · ${accountLastRequestAgo(profile?.last_used_at)}`;
  }

  function accountLastRequestAgo(value?: string) {
    if (!value) return "暂无请求";
    const time = new Date(value).getTime();
    if (Number.isNaN(time)) return "暂无请求";
    const seconds = Math.max(0, Math.floor((Date.now() - time) / 1000));
    if (seconds < 60) return `${seconds} 秒前`;
    const minutes = Math.floor(seconds / 60);
    if (minutes < 60) return `${minutes} 分钟前`;
    const hours = Math.floor(minutes / 60);
    if (hours < 24) return `${hours} 小时前`;
    return `${Math.floor(hours / 24)} 天前`;
  }

  function accountMonitoringUsageTip(account: AccountItem) {
    const profile = account.monitoring;
    const lastRequest = profile?.last_used_at
      ? new Date(profile.last_used_at).toLocaleString("zh-CN", { hour12: false })
      : "暂无本客户端请求";
    return `本客户端请求统计\n请求总数：${profile?.total_request_count ?? 0}\n今日次数：${profile?.today_request_count ?? 0}\n最后请求：${lastRequest}`;
  }

  function roomDisplayName(room: RoomItem) {
    return room.name || room.streamer_name || `直播间 ${room.web_rid || room.id}`;
  }

  function roomStatus(room: RoomItem) {
    if (!room.enabled) return "已停用";
    const monitor = roomMonitorFor(room);
    if (monitor) return redPacketMonitorStatus(monitor, redPacketMonitorUiStatus(monitor));
    if (room.monitor_status === "running") return "监测中";
    return "待监测";
  }

  function roomMonitorFor(room: RoomItem, monitors: RedPacketMonitor[] = redPacketMonitors) {
    return monitors.find((item) => item.room_id === room.id || item.web_rid === room.web_rid);
  }

  function redPacketMonitorName(monitor: RedPacketMonitor) {
    return monitor.name || monitor.streamer_name || `直播间 ${monitor.web_rid || monitor.room_id}`;
  }

  function redPacketMonitorAccountName(monitor: RedPacketMonitor) {
    return monitor.account_name || monitoringAccounts.find((account) => account.id === monitor.account_id)?.nickname || monitoringAccounts.find((account) => account.id === monitor.account_id)?.name || monitor.account_id || "未分配监测账号";
  }

  function applyRedPacketMonitorOverrides(items: RedPacketMonitor[]) {
    const overrides = { ...redPacketMonitorOverrides };
    const next = items.map((item) => {
      const override = overrides[item.id];
      if (!override) return item;
      if (item.status === override.status || item.status === "error") {
        delete overrides[item.id];
        return item;
      }
      return { ...item, status: override.status, connection_status: override.connectionStatus };
    });
    redPacketMonitorOverrides = overrides;
    return next;
  }

  function setRedPacketMonitorOverride(id: string, status: string, connectionStatus: string) {
    redPacketMonitorOverrides = { ...redPacketMonitorOverrides, [id]: { status, connectionStatus } };
  }

  // The row must render the action result immediately, even while the engine
  // is persisting the snapshot or a background list request is still in flight.
  // Keep this lookup separate from the monitor object so a stale object captured
  // by an each-block cannot put the play icon back after a successful start.
  function redPacketMonitorUiStatus(
    monitor: RedPacketMonitor,
    overrides: Record<string, { status: string; connectionStatus: string }> = redPacketMonitorOverrides,
  ) {
    return overrides[monitor.id]?.status || monitor.status;
  }

  function redPacketMonitorStatus(monitor: RedPacketMonitor, status = redPacketMonitorUiStatus(monitor)) {
    if (!monitor.enabled) return "已停用";
    if (status === "running") return "监测中";
    if (status === "error") return "需处理";
    return "未启动";
  }

  function roomLiveStatus(monitor: RedPacketMonitor, status = redPacketMonitorUiStatus(monitor)) {
    if (!monitor.enabled) return "已停用";
    if (status !== "running") return "未监测";
    if (monitor.connection_status === "connecting") return "检测中";
    if (monitor.live_status === "live") return "已开播";
    if (monitor.live_status === "offline") return "未开播";
    if (monitor.live_status === "error" || monitor.connection_status === "error") return "探测异常";
    return "检测中";
  }

  function roomMonitorPhase(monitor: RedPacketMonitor, status = redPacketMonitorUiStatus(monitor)) {
    if (!monitor.enabled) return "直播间已停用";
    if (status !== "running") return "红包监测未启动";
    if (monitor.live_status === "live") {
      return monitor.last_red_packet_checked_at
        ? `红包检测 ${formatMonitorTime(monitor.last_red_packet_checked_at)}`
        : "正在进入红包监测";
    }
    if (monitor.live_status === "offline") {
      return monitor.last_live_checked_at
        ? `等待开播 · ${formatMonitorTime(monitor.last_live_checked_at)}`
        : "等待开播";
    }
    if (monitor.live_status === "error" || monitor.connection_status === "error") return "开播状态探测失败";
    return "正在判断直播状态";
  }

  function formatMonitorTime(value?: string) {
    if (!value) return "暂无";
    const timestamp = Date.parse(value);
    if (!Number.isFinite(timestamp)) return value.replace("T", " ").slice(0, 16);
    const seconds = Math.max(0, Math.floor((Date.now() - timestamp) / 1000));
    if (seconds < 10) return "刚刚";
    if (seconds < 60) return `${seconds} 秒前`;
    if (seconds < 3600) return `${Math.floor(seconds / 60)} 分钟前`;
    return new Date(timestamp).toLocaleString("zh-CN", { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" });
  }

  function appendMonitorLog(message: string, level: MonitorRuntimeLog["level"] = "info") {
    const now = new Date();
    monitorRuntimeLogs = [
      ...monitorRuntimeLogs,
      {
        id: `${now.getTime()}-${Math.random().toString(16).slice(2)}`,
        at: now.toLocaleTimeString("zh-CN", { hour12: false }),
        level,
        message,
      },
    ].slice(-200);
    void emitMonitorLogState();
  }

  function monitorLogStatePayload() {
    return {
      logs: monitorRuntimeLogs,
      running: runningRedPacketMonitorCount,
      stopped: Math.max(0, enabledRedPacketMonitors.length - runningRedPacketMonitorCount),
      error: redPacketMonitors.filter((item) => redPacketMonitorUiStatus(item) === "error").length,
    };
  }

  async function emitMonitorLogState() {
    if (!("__TAURI_INTERNALS__" in window)) return;
    try {
      await emit("monitor-log://state", monitorLogStatePayload());
    } catch {
      // The native log window may have been closed between updates.
    }
  }

  function recordMonitorStateChanges(previous: RedPacketMonitor[], next: RedPacketMonitor[]) {
    if (previous.length === 0) {
      appendMonitorLog(`已载入 ${next.length} 个直播间，${next.filter((item) => item.status === "running").length} 个正在监测`);
      return;
    }
    const previousByID = new Map(previous.map((item) => [item.id, item]));
    for (const item of next) {
      const old = previousByID.get(item.id);
      if (!old) continue;
	  const roomLabel = redPacketMonitorName(item);
	  const accountLabel = redPacketMonitorAccountName(item);
      if (old.status !== item.status || old.connection_status !== item.connection_status) {
		if (item.status === "running") appendMonitorLog(`监测账号「${accountLabel}」正在监测直播间「${roomLabel}」，连接状态：${item.connection_status || "连接中"}`, "success");
		else if (item.status === "error") appendMonitorLog(`监测账号「${accountLabel}」监测直播间「${roomLabel}」异常：${item.last_error || "未知错误"}`, "error");
		else appendMonitorLog(`监测账号「${accountLabel}」已停止监测直播间「${roomLabel}」`, "warning");
      } else if (item.last_error && item.last_error !== old.last_error) {
		appendMonitorLog(`监测账号「${accountLabel}」监测直播间「${roomLabel}」时发生错误：${item.last_error}`, "error");
      }
	  if (old.live_status !== item.live_status && item.status === "running") {
		if (item.live_status === "live") appendMonitorLog(`监测账号「${accountLabel}」确认直播间「${roomLabel}」已开播，开始检测红包`, "success");
		else if (item.live_status === "offline") appendMonitorLog(`监测账号「${accountLabel}」确认直播间「${roomLabel}」未开播，等待下一轮探测`);
	  }
    }
  }

  async function openMonitorRuntimeLog() {
    appendMonitorLog(`运行概况：${runningRedPacketMonitorCount}/${enabledRedPacketMonitors.length} 个直播间正在监测`);
    if (!("__TAURI_INTERNALS__" in window)) {
      showToast("运行日志窗口仅支持桌面端");
      return;
    }
    try {
      await invoke("open_monitor_log");
      // The window sends a ready event after its listener is mounted. This
      // second emission also covers the first paint when opening is very fast.
      window.setTimeout(() => void emitMonitorLogState(), 120);
    } catch (error) {
      showToast(error instanceof Error ? error.message : String(error));
    }
  }

  async function loadRedPacketMonitors(force = false) {
    if (redPacketMonitorsLoading && !force) return;
    const requestSeq = ++redPacketMonitorListRequestSeq;
    redPacketMonitorsLoading = true;
    redPacketMonitorError = "";
    try {
      const items = await engineRequest<RedPacketMonitor[]>("red_packet_monitor.list");
      // A polling request may have started before a start/stop action. Ignore
      // that older snapshot so it cannot put the row back into its old state.
      if (requestSeq !== redPacketMonitorListRequestSeq) return;
      const previous = redPacketMonitors;
      const next = applyRedPacketMonitorOverrides(items);
      recordMonitorStateChanges(previous, next);
      redPacketMonitors = next;
    } catch (error) {
      if (requestSeq !== redPacketMonitorListRequestSeq) return;
      redPacketMonitorError = error instanceof Error ? error.message : String(error);
    } finally {
      if (requestSeq === redPacketMonitorListRequestSeq) redPacketMonitorsLoading = false;
    }
  }

  async function loadRedPacketEvents() {
    if (redPacketEventsLoading) return;
    redPacketEventsLoading = true;
    redPacketEventError = "";
    try {
	  const previous = redPacketEvents;
	  const next = await engineRequest<RedPacketEvent[]>("red_packet_event.list");
	  if (redPacketEventsInitialized) {
		const previousIDs = new Set(previous.map((event) => event.id));
		for (const event of next.filter((item) => !previousIDs.has(item.id)).reverse()) {
		  const accountLabel = event.account_name || event.account_id || "未分配监测账号";
		  const roomLabel = event.room_name || event.streamer_name || `直播间 ${event.web_rid || event.room_id}`;
		  appendMonitorLog(`监测账号「${accountLabel}」在直播间「${roomLabel}」发现红包「${event.title || event.packet_id}」；奖品：${event.prize || "待解析"}；${redPacketEventExpiry(event)}`, "success");
		}
	  }
	  redPacketEvents = next;
	  redPacketEventsInitialized = true;
    } catch (error) {
      redPacketEventError = error instanceof Error ? error.message : String(error);
    } finally {
      redPacketEventsLoading = false;
    }
  }

  async function toggleRoomRedPacketMonitor(monitor: RedPacketMonitor) {
    if (redPacketMonitorActionId || redPacketBatchAction) return;
    redPacketMonitorActionId = monitor.id;
    const starting = monitor.status !== "running";
    const optimisticStatus = starting ? "running" : "stopped";
    const optimisticConnectionStatus = starting ? "connecting" : "disconnected";
    setRedPacketMonitorOverride(monitor.id, optimisticStatus, optimisticConnectionStatus);
    redPacketMonitors = redPacketMonitors.map((item) =>
      item.id === monitor.id
        ? { ...item, status: optimisticStatus, connection_status: optimisticConnectionStatus, last_error: "" }
        : item,
    );
    try {
      appendMonitorLog(`${starting ? "正在启动" : "正在停止"}「${redPacketMonitorName(monitor)}」`);
      if (!starting) {
        const result = await engineRequest<{ monitor?: RedPacketMonitor }>("red_packet_monitor.stop", { monitor_id: monitor.id });
        const nextMonitor = result.monitor;
        redPacketMonitors = redPacketMonitors.map((item) => item.id === monitor.id ? { ...item, ...nextMonitor, status: "stopped", connection_status: "disconnected", last_error: "" } : item);
        showToast(`已停止「${redPacketMonitorName(monitor)}」的红包监测`);
		appendMonitorLog(`监测账号「${redPacketMonitorAccountName(nextMonitor || monitor)}」已停止监测直播间「${redPacketMonitorName(nextMonitor || monitor)}」`, "warning");
      } else {
        const result = await engineRequest<{ monitor?: RedPacketMonitor }>("red_packet_monitor.start", { monitor_id: monitor.id });
        const nextMonitor = result.monitor;
        redPacketMonitors = redPacketMonitors.map((item) => item.id === monitor.id ? { ...item, ...nextMonitor, status: "running", connection_status: nextMonitor?.connection_status || "connecting", last_error: "" } : item);
        showToast(`已启动「${redPacketMonitorName(monitor)}」的红包监测`);
		appendMonitorLog(`监测账号「${redPacketMonitorAccountName(nextMonitor || monitor)}」已开始监测直播间「${redPacketMonitorName(nextMonitor || monitor)}」`, "success");
      }
      void loadRedPacketMonitors(true);
    } catch (error) {
      const nextOverrides = { ...redPacketMonitorOverrides };
      delete nextOverrides[monitor.id];
      redPacketMonitorOverrides = nextOverrides;
      const message = error instanceof Error ? error.message : String(error);
      appendMonitorLog(`「${redPacketMonitorName(monitor)}」操作失败：${message}`, "error");
      showToast(message);
      void loadRedPacketMonitors(true);
    } finally {
      redPacketMonitorActionId = "";
    }
  }

  async function toggleAllRedPacketMonitors(action: "start" | "stop") {
    if (redPacketBatchAction) return;
    redPacketBatchAction = action;
    try {
      appendMonitorLog(action === "start" ? "正在批量启动直播间监测" : "正在批量停止直播间监测");
	  const result = await engineRequest<{ started?: number; stopped?: number; account_id?: string; account_name?: string }>(
        action === "start" ? "red_packet_monitor.start_all" : "red_packet_monitor.stop_all",
      );
      showToast(action === "start" ? `已启动 ${result.started ?? 0} 个直播间红包监测` : `已停止 ${result.stopped ?? 0} 个直播间红包监测`);
      appendMonitorLog(
		action === "start"
		  ? `监测账号「${result.account_name || result.account_id || "默认监测账号"}」已开始监测 ${result.started ?? 0} 个直播间`
		  : `批量停止完成：${result.stopped ?? 0} 个直播间`,
        action === "start" ? "success" : "warning",
      );
      const nextStatus = action === "start" ? "running" : "stopped";
      const nextConnectionStatus = action === "start" ? "connecting" : "disconnected";
      const nextOverrides = { ...redPacketMonitorOverrides };
      for (const item of redPacketMonitors) {
        if (action === "start" && !item.enabled) continue;
        nextOverrides[item.id] = { status: nextStatus, connectionStatus: nextConnectionStatus };
      }
      redPacketMonitorOverrides = nextOverrides;
      redPacketMonitors = redPacketMonitors.map((item) =>
        action === "start" && !item.enabled
          ? item
          : { ...item, status: nextStatus, connection_status: nextConnectionStatus, last_error: "" },
      );
      void loadRedPacketMonitors(true);
      void loadRedPacketEvents();
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      appendMonitorLog(`批量操作失败：${message}`, "error");
      showToast(message);
      void loadRedPacketMonitors(true);
    } finally {
      redPacketBatchAction = "";
    }
  }

  async function loadRooms(autoMigrate = true) {
    if (roomsLoading) return;
    roomsLoading = true;
    roomError = "";
    try {
      const items = await engineRequest<RoomItem[]>("room.list");
      rooms = items;
      if (items.length === 0 && autoMigrate) {
        await migrateLegacyRooms(true);
      }
    } catch (error) {
      roomError = error instanceof Error ? error.message : String(error);
    } finally {
      roomsLoading = false;
    }
  }

  async function migrateLegacyRooms(silent = false) {
    if (roomsMigrating) return;
    importMenuOpen = false;
    roomsMigrating = true;
    roomError = "";
    try {
      const result = await engineRequest<{ imported: number; merged: number; total: number }>("room.migrate_legacy");
      rooms = await engineRequest<RoomItem[]>("room.list");
      if (!silent || result.imported > 0) {
        showToast(`已导入 ${result.imported} 个直播间，当前共 ${result.total} 个`);
      }
      managementTab = "rooms";
      query = "";
      roomRenderLimit = 300;
    } catch (error) {
      roomError = error instanceof Error ? error.message : String(error);
      showToast(roomError);
    } finally {
      roomsMigrating = false;
    }
  }

  async function loadAccounts(autoMigrate = true) {
    if (accountsLoading) return;
    accountsLoading = true;
    accountError = "";
    try {
      const items = await engineRequest<AccountItem[]>("account.list");
      accounts = items;
      // The Go sidecar reads RPC messages serially. Revalidating every imported
      // account here can occupy that queue for minutes and block interactive
      // actions such as opening the native CK rebind window. The list already
      // contains the last persisted CK health; fresh checks are performed for
      // active browser instances and immediately after a successful rebind.
      if (items.length === 0 && autoMigrate) {
        await migrateLegacyAccounts(true);
      }
    } catch (error) {
      accountError = error instanceof Error ? error.message : String(error);
    } finally {
      accountsLoading = false;
    }
  }

  async function validateAccountCookie(accountId: string, role: AccountRole, force = false) {
    if (cookieValidatingAccountIds.includes(accountId)) return;
    cookieValidatingAccountIds = [...cookieValidatingAccountIds, accountId];
    try {
      const result = await engineRequest<CookieValidation>("account.validate_cookie", {
        account_id: accountId,
		role,
        force,
      });
	  accounts = accounts.map((account) => {
		if (account.id !== accountId) return account;
		if (role === "monitoring") {
		  return { ...account, monitoring: { ...account.monitoring, enabled: account.monitoring?.enabled ?? true, cookie_status: result.status, cookie_message: result.message, cookie_checked_at: result.checked_at } };
		}
		return { ...account, cookie_status: result.status, cookie_message: result.message, cookie_checked_at: result.checked_at };
	  });
    } catch {
      // A temporary network failure must not be presented as an expired CK.
    } finally {
      cookieValidatingAccountIds = cookieValidatingAccountIds.filter((id) => id !== accountId);
    }
  }

  async function openAccountRebind(account: AccountItem) {
    if (accountRebindOpeningId || accountRebindCompleting) return;
    accountRebindOpeningId = account.id;
	accountRebindRole = accountRole;
    await hideEmbeddedBrowsers();
    accountCreateSessionId = "";
    accountRebinding = account;
    try {
      await tick();
      if (!accountRebindViewport) throw new Error("登录区域尚未准备完成");
      await invoke("open_account_rebind", {
        accountId: account.id,
        bounds: browserMountBounds(accountRebindViewport),
      });
    } catch (error) {
      // Creation can fail after WebKit has already mounted the native child.
      // Always ask Rust to hide/close it before removing the HTML modal.
      try {
        await invoke("cancel_account_rebind", { accountId: account.id });
      } catch {
        // Preserve the original open error for the user.
      }
      accountRebinding = null;
      void tick().then(scheduleEmbeddedBrowserSync);
      showToast(error instanceof Error ? error.message : String(error));
    } finally {
      accountRebindOpeningId = "";
    }
  }

  async function cancelAccountRebind() {
    if ((!accountRebinding && !accountCreateSessionId) || accountRebindCompleting || accountRebindOpeningId) return;
    try {
      if (accountRebinding) {
        await invoke("cancel_account_rebind", { accountId: accountRebinding.id });
      } else {
        await invoke("cancel_account_create", { sessionId: accountCreateSessionId });
      }
      // The native child must be hidden before its HTML mount disappears.
      accountRebinding = null;
      accountCreateSessionId = "";
      void tick().then(scheduleEmbeddedBrowserSync);
    } catch (error) {
      // Keep the modal in place so a failed native close never exposes an
      // orphan WebView over the rest of the application.
      showToast(`关闭登录实例失败：${error instanceof Error ? error.message : String(error)}`);
    }
  }

  async function completeAccountRebind() {
    if ((!accountRebinding && !accountCreateSessionId) || accountRebindCompleting) return;
    const account = accountRebinding;
    const createSessionId = accountCreateSessionId;
    accountRebindCompleting = true;
    try {
      if (account) {
        await invoke("complete_account_rebind", { accountId: account.id });
        accountRebinding = null;
        const accountInstances = browserInstances.filter((instance) => instance.account_id === account.id);
        await Promise.all(
          accountInstances.map((instance) =>
            invoke("refresh_browser_account_cookie", { instanceId: instance.id }),
          ),
        );
		await validateAccountCookie(account.id, accountRebindRole, true);
        await loadBrowserInstances();
        showToast(`已重新绑定「${account.nickname || account.name}」并更新 CK`);
      } else {
        const result = await invoke<{ created: boolean; account: AccountItem }>("complete_account_create", {
          sessionId: createSessionId,
          role: accountCreateRole,
        });
        accountCreateSessionId = "";
        await loadAccounts(false);
        managementTab = accountCreateRole;
        accountRole = accountCreateRole;
        showToast(result.created ? `已添加「${result.account.nickname || result.account.name}」` : `账号已存在，已更新登录信息并加入${roleLabel(accountCreateRole)}`);
      }
      void tick().then(scheduleEmbeddedBrowserSync);
    } catch (error) {
      showToast(error instanceof Error ? error.message : String(error));
    } finally {
      accountRebindCompleting = false;
    }
  }

  async function migrateLegacyAccounts(silent = false) {
    if (accountsMigrating) return;
    accountsMigrating = true;
    accountError = "";
    try {
      const result = await engineRequest<{
        imported: number;
        merged: number;
        monitoring_assignments: number;
        participation_assignments: number;
        total: number;
      }>("account.migrate_legacy");
      accounts = await engineRequest<AccountItem[]>("account.list");
      if (!silent || result.imported > 0) {
        showToast(`已迁移 ${result.imported} 个账号，当前共 ${result.total} 个`);
      }
    } catch (error) {
      accountError = error instanceof Error ? error.message : String(error);
    } finally {
      accountsMigrating = false;
    }
  }

  async function addAccountToRole(account: AccountItem, role: AccountRole) {
    try {
      await engineRequest("account.add_role", { account_id: account.id, role });
      await loadAccounts(false);
      showToast(`已添加到${roleLabel(role)}`);
    } catch (error) {
      showToast(error instanceof Error ? error.message : String(error));
    }
  }

  async function removeAccountRole(account: AccountItem, role: AccountRole) {
    if (account.roles.length <= 1) {
      showToast("账号至少需要保留一个分类");
      return;
    }
    try {
      await engineRequest("account.remove_role", { account_id: account.id, role });
      await loadAccounts(false);
      showToast(`已移除${roleLabel(role)}`);
    } catch (error) {
      showToast(error instanceof Error ? error.message : String(error));
    }
  }

  async function loadBrowserInstances() {
    if (browserLoading) return;
    browserLoading = true;
    browserError = "";
    try {
      const [instances, capacity] = await Promise.all([
        engineRequest<BrowserInstance[]>("browser.list"),
        engineRequest<BrowserCapacity>("browser.capacity"),
      ]);
      browserInstances = instances;
      browserCapacity = capacity;
      await tick();
      if (activeView === "browsers" && isTauriDesktop()) {
        try {
          await invoke("refresh_window_surface");
        } catch {
          // The browser-only preview has no native surface to refresh.
        }
      }
      browserViewSettled = activeView === "browsers";
      window.setTimeout(scheduleEmbeddedBrowserSync, 120);
    } catch (error) {
      browserError = error instanceof Error ? error.message : String(error);
    } finally {
      browserLoading = false;
    }
  }

  function updateBrowserColumns(event: Event) {
    const value = Number((event.currentTarget as HTMLInputElement).value);
    browserColumns = Math.max(1, Math.min(10, Math.round(value)));
    localStorage.setItem("fubao.browserColumns", String(browserColumns));
    browserLayoutChanging = true;
    browserLayoutRevision += 1;
    const revision = browserLayoutRevision;
    window.cancelAnimationFrame(browserWebviewSyncFrame);
    window.clearTimeout(browserColumnSyncTimer);
    void hideEmbeddedBrowsers();
    browserColumnSyncTimer = window.setTimeout(async () => {
      if (revision !== browserLayoutRevision) return;
      // Flush any native bounds request that may have completed after the
      // first hide, then measure only the final settled grid.
      await hideEmbeddedBrowsers();
      await tick();
      await new Promise<void>((resolve) => window.requestAnimationFrame(() => resolve()));
      await new Promise<void>((resolve) => window.requestAnimationFrame(() => resolve()));
      if (revision !== browserLayoutRevision) return;
      browserLayoutChanging = false;
      await syncEmbeddedBrowsers();
    }, 180);
  }

  async function pollBrowserInstanceStatuses() {
    if (browserStatusPolling || browserLoading || activeView !== "browsers" || !engineListenerReady) return;
    browserStatusPolling = true;
    try {
      const previousById = new Map(browserInstances.map((instance) => [instance.id, instance]));
      const [items, capacity] = await Promise.all([
        engineRequest<BrowserInstance[]>("browser.list"),
        engineRequest<BrowserCapacity>("browser.capacity"),
      ]);
      const syncedInstance = items.find((instance) => {
        const previous = previousById.get(instance.id);
        return Boolean(
          previous &&
            instance.credential_updated_at &&
            instance.credential_updated_at !== previous.credential_updated_at,
        );
      });
      browserInstances = items;
      browserCapacity = capacity;
      if (syncedInstance) {
        if (isTauriDesktop()) {
          try {
            await invoke("refresh_browser_account_cookie", { instanceId: syncedInstance.id });
          } catch {
            // The canonical account is already updated. An unmounted card
            // receives the latest CK on its next native WebView mount.
          }
        }
        accounts = await engineRequest<AccountItem[]>("account.list");
        showToast(`已同步「${syncedInstance.account_name}」的新登录状态和 CK`);
      }
    } catch {
      // Keep the last visible state; the normal refresh action reports errors.
    } finally {
      browserStatusPolling = false;
      scheduleEmbeddedBrowserSync();
    }
  }

  async function syncEmbeddedBrowserCookies() {
    if (
      browserCookieSyncing ||
      activeView !== "browsers" ||
      !engineListenerReady ||
      !isTauriDesktop()
    ) return;
    browserCookieSyncing = true;
    try {
      const results = await Promise.all(
        visibleBrowserInstances.map((instance) =>
          invoke<boolean>("sync_browser_account_cookie", { instanceId: instance.id }).catch(() => false),
        ),
      );
      if (results.some(Boolean)) {
        accounts = await engineRequest<AccountItem[]>("account.list");
        showToast("已将浏览器实例的新登录状态同步到账号 CK");
      }
    } finally {
      browserCookieSyncing = false;
    }
  }

  async function openInstanceModal() {
    selectedParticipationAccountIds = [];
    await hideEmbeddedBrowsers();
    instanceModalOpen = true;
    if (!engineListenerReady) {
      browserError = "Go 引擎尚未连接";
      return;
    }
    await Promise.all([loadAccounts(false), loadBrowserInstances()]);
  }

  async function createBrowserInstance() {
    const selectedIds = [...selectedParticipationAccountIds];
    if (selectedIds.length === 0 || browserCreating) return;
    browserCreating = true;
    browserError = "";
    const created: BrowserInstance[] = [];
    const failed: string[] = [];
    for (const accountId of selectedIds) {
      try {
        created.push(await engineRequest<BrowserInstance>("browser.create", { account_id: accountId }));
      } catch (error) {
        const account = selectableParticipationAccounts.find((item) => item.id === accountId);
        failed.push(`${account?.nickname || account?.name || accountId}：${error instanceof Error ? error.message : String(error)}`);
      }
    }
    for (const instance of created) {
      browserInstances = browserInstances.some((item) => item.id === instance.id)
        ? browserInstances.map((item) => (item.id === instance.id ? instance : item))
        : [...browserInstances, instance];
    }
    try {
      if (failed.length > 0) {
        selectedParticipationAccountIds = selectedIds.filter((id) => !created.some((item) => item.account_id === id));
        browserError = failed.join("；");
        if (created.length > 0) showToast(`已创建 ${created.length} 个实例，${failed.length} 个未完成`);
        return;
      }
      closeInstanceModal();
      showToast(`已创建 ${created.length} 个独立实例，账号 CK 已共享`);
    } finally {
      browserCreating = false;
    }
  }

  async function closeBrowserInstance(instance: BrowserInstance) {
    if (browserClosingId) return;
    browserClosingId = instance.id;
    browserError = "";
    try {
      if (isTauriDesktop()) {
        await invoke("close_browser_webview", { instanceId: instance.id });
      }
      await engineRequest<BrowserCapacity>("browser.runtime.release", {
        instance_id: instance.id,
      }).catch(() => undefined);
      await engineRequest<BrowserInstance>("browser.close", { instance_id: instance.id });
      browserInstances = browserInstances.filter((item) => item.id !== instance.id);
      browserWebviewMountedIds = browserWebviewMountedIds.filter((id) => id !== instance.id);
      browserWebviewMountingIds = browserWebviewMountingIds.filter((id) => id !== instance.id);
      showToast(`已关闭「${instance.account_name}」的实例，账号环境已保留`);
    } catch (error) {
      browserError = error instanceof Error ? error.message : String(error);
      await loadBrowserInstances();
      showToast(browserError);
    } finally {
      browserClosingId = "";
      void tick().then(scheduleEmbeddedBrowserSync);
    }
  }

  async function openBrowserInstance(instance: BrowserInstance) {
    if (browserOpeningId) return;
    browserOpeningId = instance.id;
    browserError = "";
    try {
      await releaseEmbeddedBrowser(instance);
      const opened = await engineRequest<BrowserInstance>("browser.open", {
        instance_id: instance.id,
      });
      browserInstances = browserInstances.map((item) => (item.id === opened.id ? opened : item));
      showToast(`已打开「${opened.account_name}」的独立抖音窗口`);
      await loadBrowserInstances();
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      browserError = message;
      await loadBrowserInstances();
      browserError = message;
      showToast(message);
    } finally {
      browserOpeningId = "";
    }
  }

  async function deleteAccount() {
    if (!accountPendingDelete || accountDeleting) return;
    const account = accountPendingDelete;
    accountDeleting = true;
    try {
      await engineRequest("account.delete", { account_id: account.id });
      accountPendingDelete = null;
      await loadAccounts(false);
      showToast(`已删除账号「${account.nickname || account.name}」`);
    } catch (error) {
      showToast(error instanceof Error ? error.message : String(error));
    } finally {
      accountDeleting = false;
    }
  }

  function toggleMonitor(id: number) {
    monitors = monitors.map((item) =>
      item.id === id
        ? { ...item, state: item.state === "running" ? "paused" : "running", lastSeen: "刚刚" }
        : item,
    );
    showToast("监测状态已更新");
  }

  function refresh() {
    if (refreshing) return;
    refreshing = true;
    window.setTimeout(() => {
      refreshing = false;
      monitors = monitors.map((item) =>
        item.state === "running" ? { ...item, lastSeen: "刚刚" } : item,
      );
      showToast("运行状态已刷新");
    }, 650);
  }

  function addMonitor() {
    if (!newRoom.trim()) return;
    monitors = [
      ...monitors,
      {
        id: Date.now(),
        name: newName.trim() || `直播间 ${newRoom.trim().slice(-4)}`,
        room: newRoom.trim(),
        anchor: "等待首次识别",
        state: "running",
        lastSeen: "准备连接",
        events: 0,
        account: "自动分配",
        accent: "#8b6db3",
      },
    ];
    newName = "";
    newRoom = "";
    modalOpen = false;
    activeView = "overview";
    showToast("新监测已创建");
  }

  function showToast(message: string) {
    toast = message;
    window.setTimeout(() => {
      if (toast === message) toast = "";
    }, 3000);
  }

  function clampSidebarWidth(width: number) {
    return Math.min(360, Math.max(220, width));
  }

  function toggleSidebar() {
    sidebarCollapsed = !sidebarCollapsed;
    void tick().then(scheduleEmbeddedBrowserSync);
  }

  async function startWindowDrag(event: PointerEvent) {
    if (event.button !== 0) return;

    const target = event.target as HTMLElement;
    if (target.closest("button, input, label, [role='slider']")) return;

    event.preventDefault();
    try {
      await invoke(event.detail >= 2 ? "toggle_window_maximize" : "start_window_drag");
    } catch {
      // Window chrome commands are available only inside the Tauri desktop runtime.
    }
  }

  function startSidebarResize(event: PointerEvent) {
    if (sidebarCollapsed || event.button !== 0) return;
    event.preventDefault();
    resizingSidebar = true;
    resizeStartX = event.clientX;
    resizeStartWidth = sidebarWidth;
    document.body.classList.add("is-resizing-sidebar");
  }

  function resizeSidebar(event: PointerEvent) {
    if (!resizingSidebar) return;
    sidebarWidth = clampSidebarWidth(resizeStartWidth + event.clientX - resizeStartX);
    scheduleEmbeddedBrowserSync();
  }

  function stopSidebarResize() {
    if (!resizingSidebar) return;
    resizingSidebar = false;
    document.body.classList.remove("is-resizing-sidebar");
    localStorage.setItem("fubao.sidebarWidth", String(Math.round(sidebarWidth)));
    scheduleEmbeddedBrowserSync();
  }

  function resizeSidebarByKeyboard(event: KeyboardEvent) {
    if (sidebarCollapsed) return;
    if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
    event.preventDefault();
    sidebarWidth = clampSidebarWidth(sidebarWidth + (event.key === "ArrowRight" ? 12 : -12));
    localStorage.setItem("fubao.sidebarWidth", String(Math.round(sidebarWidth)));
    scheduleEmbeddedBrowserSync();
  }

  onMount(() => {
    if (isTauriDesktop()) {
      void getVersion().then((version) => {
        if (version.trim()) clientVersion = version.trim();
        window.setTimeout(() => void checkForAppUpdate(true), 1200);
      });
    }
    const savedWidth = Number(localStorage.getItem("fubao.sidebarWidth"));
    if (Number.isFinite(savedWidth) && savedWidth > 0) {
      sidebarWidth = clampSidebarWidth(savedWidth);
    }
    const savedBrowserColumns = Number(localStorage.getItem("fubao.browserColumns"));
    if (Number.isFinite(savedBrowserColumns) && savedBrowserColumns >= 1 && savedBrowserColumns <= 10) {
      browserColumns = Math.round(savedBrowserColumns);
    }

    let unlistenEngine: (() => void) | undefined;
    let unlistenMonitorLogReady: (() => void) | undefined;
    let unlistenMonitorLogClear: (() => void) | undefined;
    let unlistenUpdateProgress: (() => void) | undefined;
    if ("__TAURI_INTERNALS__" in window) {
      void listen<string>("engine://message", (event) => handleEngineMessage(event.payload))
        .then((unlisten) => {
          unlistenEngine = unlisten;
          engineListenerReady = true;
          void loadLicenseStatus();
          void loadAccounts(false);
          void loadRedPacketMonitors();
          void loadRedPacketEvents();
          if (activeView === "accounts") {
            void loadRooms();
          }
          if (activeView === "browsers") {
            void loadAccounts();
            void loadBrowserInstances();
          }
        })
        .catch((error) => {
          engineListenerReady = false;
          accountError = error instanceof Error ? error.message : String(error);
        });
      void listen("monitor-log://ready", () => void emitMonitorLogState()).then((unlisten) => {
        unlistenMonitorLogReady = unlisten;
      });
      void listen("monitor-log://clear", () => {
        monitorRuntimeLogs = [];
        void emitMonitorLogState();
      }).then((unlisten) => {
        unlistenMonitorLogClear = unlisten;
      });
      void listen<UpdateProgress>("update://progress", (event) => {
        updateProgress = event.payload;
      }).then((unlisten) => {
        unlistenUpdateProgress = unlisten;
      });
    }

    window.addEventListener("pointermove", resizeSidebar);
    window.addEventListener("pointerdown", closeFloatingMenus);
    window.addEventListener("pointerup", stopSidebarResize);
    window.addEventListener("pointercancel", stopSidebarResize);
    window.addEventListener("blur", stopSidebarResize);
    window.addEventListener("resize", scheduleEmbeddedBrowserSync);
    window.addEventListener("resize", scheduleAccountRebindSync);
    document.addEventListener("scroll", scheduleEmbeddedBrowserSync, true);
    const shell = document.querySelector<HTMLElement>(".app-shell");
    const shellResizeObserver = new ResizeObserver(scheduleEmbeddedBrowserSync);
    if (shell) shellResizeObserver.observe(shell);
    const redPacketRefreshTimer = window.setInterval(() => {
      if (!engineListenerReady) return;
      void loadAccounts(false);
      void loadRedPacketMonitors();
      void loadRedPacketEvents();
    }, 5000);
    const redPacketClockTimer = window.setInterval(() => {
      redPacketClock = Date.now();
    }, 1000);
    const browserStatusTimer = window.setInterval(() => {
      void pollBrowserInstanceStatuses();
    }, 2000);
    const browserCookieSyncTimer = window.setInterval(() => {
      void syncEmbeddedBrowserCookies();
    }, 5000);

    return () => {
      window.removeEventListener("pointermove", resizeSidebar);
      window.removeEventListener("pointerdown", closeFloatingMenus);
      window.removeEventListener("pointerup", stopSidebarResize);
      window.removeEventListener("pointercancel", stopSidebarResize);
      window.removeEventListener("blur", stopSidebarResize);
      window.removeEventListener("resize", scheduleEmbeddedBrowserSync);
      window.removeEventListener("resize", scheduleAccountRebindSync);
      document.removeEventListener("scroll", scheduleEmbeddedBrowserSync, true);
      shellResizeObserver.disconnect();
      window.clearInterval(redPacketRefreshTimer);
      window.clearInterval(redPacketClockTimer);
      window.clearInterval(browserStatusTimer);
      window.clearInterval(browserCookieSyncTimer);
      window.clearTimeout(browserColumnSyncTimer);
      window.cancelAnimationFrame(browserWebviewSyncFrame);
      window.cancelAnimationFrame(accountRebindSyncFrame);
      void releaseEmbeddedBrowsers();
      unlistenEngine?.();
      unlistenMonitorLogReady?.();
      unlistenMonitorLogClear?.();
      unlistenUpdateProgress?.();
      for (const pending of pendingRequests.values()) {
        window.clearTimeout(pending.timer);
        pending.reject(new Error("页面已关闭"));
      }
      pendingRequests.clear();
      document.body.classList.remove("is-resizing-sidebar");
    };
  });
</script>

<svelte:head>
  <meta name="theme-color" content="#f8f7f4" />
</svelte:head>

<div
  class:sidebar-collapsed={sidebarCollapsed}
  class:sidebar-resizing={resizingSidebar}
  class="app-shell"
  style={`--sidebar-width:${sidebarCollapsed ? 0 : sidebarWidth}px`}
>
  <aside class="sidebar">
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div class="window-strip" data-tauri-drag-region onpointerdown={startWindowDrag}>
      <button
        class="icon-button sidebar-toggle"
        aria-label={sidebarCollapsed ? "展开侧栏" : "收起侧栏"}
        data-tooltip={sidebarCollapsed ? "展开侧栏" : "收起侧栏"}
        data-tooltip-placement="right"
        onclick={toggleSidebar}
      >
        <SidebarSimple size={15} weight="regular" />
      </button>
    </div>

    <nav class="main-nav" aria-label="主要导航">
      {#each navItems as item}
        <button
          class:active={activeView === item.key}
          class="nav-item"
          onclick={() => switchView(item.key)}
        >
          <svelte:component this={item.icon} size={19} weight="regular" />
          <span>{item.label}</span>
          {#if item.key === "tasks"}<span class="nav-count">12</span>{/if}
        </button>
      {/each}
    </nav>

    <div class="quick-list">
      <p class="section-label">最近活动</p>
      <button class="quick-row" onclick={() => switchView("overview")}>
        <span class="quick-status live"><Radio size={14} weight="fill" /></span>
        <span>{runningRedPacketMonitorCount} 个直播间正在监测</span>
      </button>
      <button class="quick-row" onclick={() => switchView("tasks")}>
        <span class="quick-status live"><Gift size={14} weight="fill" /></span>
        <span>{activeRedPacketCount} 个红包发放中</span>
      </button>
      <button class:warning={expiredParticipationAccountCount > 0} class="quick-row" onclick={() => showExpiredAccounts("participation")}>
        <span class="quick-status"><UserCircle size={15} /></span>
        <span>{expiredParticipationAccountCount} 个参与账号 CK 失效</span>
      </button>
      <button class:warning={expiredMonitoringAccountCount > 0} class="quick-row" onclick={() => showExpiredAccounts("monitoring")}>
        <span class="quick-status"><UserFocus size={15} /></span>
        <span>{expiredMonitoringAccountCount} 个监测账号 CK 失效</span>
      </button>
    </div>

    <div class="sidebar-footer">
      <span class="brand-mark"><img src={appIconUrl} alt="" /></span>
      <span class="brand-copy">
        <strong>福宝控制台</strong>
        <span class="brand-meta">
          <button
            class:update-available={Boolean(updateStatus?.available)}
            class="client-version-button"
            aria-label={updateStatus?.available ? `发现新版本 ${updateStatus.latest_version}` : "检查客户端更新"}
            data-tooltip={updateChecking ? "正在检查更新…" : updateStatus?.available ? `发现新版本 v${updateStatus.latest_version}` : "点击检查更新"}
            data-tooltip-placement="top"
            disabled={updateChecking}
            onclick={() => void openUpdateModal()}
          >v{clientVersion}</button>
          {#if updateStatus?.available}
            <button
              class="update-available-badge"
              aria-label={`升级到 v${updateStatus.latest_version}`}
              data-tooltip={`升级到 v${updateStatus.latest_version}`}
              data-tooltip-placement="top"
              onclick={() => void openUpdateModal()}
            >可升级</button>
          {/if}
          <button
            class:professional={licenseStatus.state === "active"}
            class="edition-badge"
            aria-label={`当前为${licenseStatus.edition}，打开授权管理`}
            data-tooltip="授权管理"
            data-tooltip-placement="top"
            onclick={openLicenseModal}
          >{licenseStatus.edition}</button>
        </span>
      </span>
      <button
        class="icon-button"
        aria-label="授权与设置"
        data-tooltip="授权与设置"
        data-tooltip-placement="top"
        onclick={openLicenseModal}
      ><GearSix size={16} /></button>
    </div>

    <div
      class="sidebar-resizer"
      class:active={resizingSidebar}
      role="slider"
      aria-label="调整侧栏宽度"
      aria-orientation="vertical"
      aria-valuemin="220"
      aria-valuemax="360"
      aria-valuenow={Math.round(sidebarWidth)}
      tabindex={sidebarCollapsed ? -1 : 0}
      onpointerdown={startSidebarResize}
      onkeydown={resizeSidebarByKeyboard}
    ></div>
  </aside>

  <main class="main-panel">
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <header class="topbar" data-tauri-drag-region onpointerdown={startWindowDrag}>
      {#if sidebarCollapsed}
        <button
          class="icon-button collapsed-sidebar-toggle"
          aria-label="展开侧栏"
          data-tooltip="展开侧栏"
          data-tooltip-placement="bottom"
          onclick={toggleSidebar}
        >
          <SidebarSimple size={15} weight="regular" />
        </button>
      {/if}
      <div class="title-group" data-tauri-drag-region>
        <div class="title-line" data-tauri-drag-region>
          <span class="title-icon" data-tauri-drag-region>
            {#if activeView === "overview"}<Monitor size={18} />
            {:else if activeView === "tasks"}<Gift size={18} />
            {:else if activeView === "browsers"}<Browser size={18} />
            {:else}<UserFocus size={18} />{/if}
          </span>
          <h1 data-tauri-drag-region>{viewMeta[activeView].title}</h1>
        </div>
        <p data-tauri-drag-region>
          {activeView === "accounts"
            ? accountSubtitle
            : activeView === "browsers"
              ? browserSubtitle
              : viewMeta[activeView].subtitle}
          <span data-tauri-drag-region>·</span>本机运行
        </p>
      </div>

      <div class="toolbar">
        <div class:open={searchOpen} class="topbar-search">
          {#if searchOpen}
            <label class="topbar-search-box">
              <MagnifyingGlass size={14} />
              <input
                bind:this={topbarSearchInput}
                bind:value={query}
                placeholder={searchPlaceholder()}
                onkeydown={(event) => event.key === "Escape" && closeTopbarSearch()}
              />
              <button aria-label="关闭搜索" data-tooltip="关闭搜索" data-tooltip-placement="bottom" onclick={closeTopbarSearch}>
                <X size={12} />
              </button>
            </label>
          {:else}
            <button class="icon-button" aria-label="搜索" data-tooltip="搜索" data-tooltip-placement="bottom" onclick={openTopbarSearch}>
              <MagnifyingGlass size={16} />
            </button>
          {/if}
        </div>
        <button class="icon-button" aria-label="刷新" data-tooltip="刷新" data-tooltip-placement="bottom" onclick={refreshing ? undefined : refresh}>
          <ArrowClockwise class={refreshing ? "spinning" : ""} size={16} />
        </button>
        {#if activeView === "accounts"}
          <div class="menu-anchor import-anchor">
            <button
              class="primary-action import-trigger"
              aria-haspopup="menu"
              aria-expanded={importMenuOpen}
              onclick={toggleImportMenu}
            >
              <UploadSimple size={15} />
              <span>导入数据</span>
              <CaretDown size={11} />
            </button>
            {#if importMenuOpen}
              <div class="floating-menu import-menu" role="menu">
                <button role="menuitem" onclick={openRoomImportModal}>
                  <Radio size={17} /><span>导入直播间</span>
                </button>
                <span class="menu-divider"></span>
                <button role="menuitem" onclick={pasteAccountCookie}><ClipboardText size={17} /><span>粘贴 Cookie</span></button>
                <button role="menuitem" onclick={startQrLogin}><QrCode size={17} /><span>扫码登录</span></button>
                <button role="menuitem" onclick={() => chooseAccountFiles(false)}><FileArrowUp size={17} /><span>导入文件</span></button>
                <button role="menuitem" onclick={() => chooseAccountFiles(true)}><FolderOpen size={17} /><span>导入文件夹</span></button>
              </div>
            {/if}
          </div>
          <input
            class="visually-hidden"
            bind:this={accountFileInput}
            type="file"
            accept=".json,.txt,.cookie"
            multiple
            onchange={(event) => handleAccountFiles(event)}
          />
          <input
            class="visually-hidden"
            bind:this={accountFolderInput}
            type="file"
            multiple
            onchange={(event) => handleAccountFiles(event, true)}
            use:setDirectoryInput
          />
          <input
            class="visually-hidden"
            bind:this={roomImportFileInput}
            type="file"
            accept=".txt,.csv,text/plain,text/csv"
            multiple
            onchange={readRoomImportFile}
          />
        {:else if activeView === "browsers"}
          <button class="primary-action" onclick={openInstanceModal}>
            <Plus size={14} weight="bold" />
            <span>新建实例</span>
          </button>
        {:else}
          <button class="primary-action" onclick={() => (modalOpen = true)}>
            <Plus size={14} weight="bold" />
            <span>新建监测</span>
          </button>
        {/if}
      </div>
    </header>

    <section class:account-content={activeView === "accounts"} class="content">
      {#if activeView !== "accounts" && activeView !== "browsers"}
        <div class="view-tools">
          <button class="filter-button">
            <SlidersHorizontal size={16} />
            <span>全部状态</span>
            <CaretDown size={12} />
          </button>
        </div>
      {/if}

      {#if activeView === "overview"}
        <div class="card-grid monitor-grid">
          {#each visibleMonitors as item}
            <article class="monitor-card">
              <div class="card-head">
                <span class="room-mark" style={`--accent:${item.accent}`}>
                  <Activity size={23} weight="bold" />
                </span>
                <div class="card-title">
                  <h2>{item.name}</h2>
                  <p>{item.anchor} · {item.events} 个红包事件</p>
                </div>
                <span class:running={item.state === "running"} class:warning={item.state === "warning"} class:paused={item.state === "paused"} class="status-pill">
                  <span></span>{stateLabel(item.state)}
                </span>
              </div>

              <div class="card-row">
                <div class="row-label"><WifiHigh size={17} /><strong>实时状态</strong></div>
                <span>{item.state === "warning" ? "等待账号恢复" : `上次响应 ${item.lastSeen}`}</span>
              </div>
              <div class="card-row">
                <div class="row-label"><UserCircle size={17} /><strong>运行账号</strong></div>
                <span>{item.account}</span>
              </div>
              <div class="card-foot">
                <span>房间号 {item.room}</span>
                <div class="card-actions">
                  <button aria-label="更多操作"><DotsThree size={18} weight="bold" /></button>
                  <button
                    class:resume={item.state !== "running"}
                    class="play-button"
                    aria-label={item.state === "running" ? "暂停监测" : "继续监测"}
                    onclick={() => toggleMonitor(item.id)}
                  >
                    {#if item.state === "running"}<Pause size={15} weight="fill" />
                    {:else}<Play size={15} weight="fill" />{/if}
                  </button>
                </div>
              </div>
            </article>
          {/each}
        </div>
        {#if visibleMonitors.length === 0}
          <div class="empty-state"><MagnifyingGlass size={30} /><strong>没有找到匹配的监测</strong><span>换个关键词试试</span></div>
        {/if}
      {:else if activeView === "tasks"}
        <div class="summary-grid">
          {#each taskCards as task}
            <article class={`summary-card ${task.tone}`}>
              <span class="summary-icon"><Gift size={20} weight="fill" /></span>
              <div><p>{task.title}</p><strong>{task.value}</strong><small>{task.detail}</small></div>
              <CaretDown class="side-caret" size={15} />
            </article>
          {/each}
        </div>
        <section class="table-card">
          <div class="table-title"><h2>最近任务</h2><span>任务状态会由 Go 引擎实时推送</span></div>
          {#each monitors.slice(0, 3) as item, index}
            <div class="task-row">
              <span class={`task-state state-${index}`}><Gift size={17} weight="fill" /></span>
              <div><strong>{item.name}</strong><small>{index === 0 ? "等待参与窗口" : index === 1 ? "等待开奖结果" : "已完成"}</small></div>
              <span>{index === 0 ? "42 秒后" : index === 1 ? "1 分 18 秒后" : "今天 18:40"}</span>
            </div>
          {/each}
        </section>
      {:else if activeView === "browsers"}
        {#if browserError}
          <div class="account-notice error browser-notice">
            <WarningCircle size={17} />
            <span>{browserError}</span>
            <button onclick={loadBrowserInstances}>重试</button>
          </div>
        {/if}
        {#if browserLoading && browserInstances.length === 0}
          <div class="empty-state"><ArrowClockwise class="spinning" size={25} /><strong>正在读取浏览器实例</strong><span>由 Go 引擎加载本机独立配置</span></div>
        {:else if visibleBrowserInstances.length > 0}
          {#if browserCapacity && (browserCapacity.waiting > 0 || browserCapacity.resources.pressure === "constrained" || browserCapacity.resources.pressure === "critical")}
            <div class:critical={browserCapacity.resources.pressure === "critical"} class="browser-capacity-note">
              <ClockCountdown size={14} />
              <span>{browserCapacity.message}</span>
              <small>运行 {browserCapacity.running}/{browserCapacity.effective_limit} · 建议 {browserCapacity.recommended_limit}</small>
            </div>
          {/if}
          <div
            class="card-grid simple-grid browser-instance-grid"
            style={`--browser-columns:${browserColumns}`}
          >
            {#each visibleBrowserInstances as item, index}
              <article class="simple-card browser-instance-card">
                <div class="browser-card-preview" aria-label={`${item.account_name} 的真实抖音浏览器`}>
                  <div
                    class="browser-webview-mount"
                    data-browser-instance={item.id}
                    use:observeBrowserMount
                  >
                    {#if item.status === "online"}
                      <span class="browser-preview-loading external">
                        <ArrowSquareOut size={18} />
                        <strong>正在独立窗口运行</strong>
                        <small>关闭独立窗口后会自动回到卡片</small>
                      </span>
                    {:else if item.runtime_state === "waiting"}
                      <span class="browser-preview-loading waiting">
                        <ClockCountdown size={18} />
                        <strong>等待运行资源</strong>
                        <small>队列第 {item.queue_position || 1} 位，资源释放后自动启动</small>
                      </span>
                    {:else if browserWebviewMountingIds.includes(item.id)}
                      <span class="browser-preview-loading">
                        <ArrowClockwise class="spinning" size={18} />
                        <strong>正在嵌入真实浏览器</strong>
                        <small>正在同步该账号的独立登录态</small>
                      </span>
                    {:else if browserWebviewErrors[item.id]}
                      <span class="browser-preview-loading error">
                        <WarningCircle size={18} />
                        <strong>真实浏览器暂不可用</strong>
                        <small>{browserWebviewErrors[item.id]}</small>
                      </span>
                    {:else}
                      <span class="browser-preview-loading">
                        <Browser size={18} />
                        <strong>真实浏览器准备就绪</strong>
                        <small>页面加载后可直接点击、输入和滚动</small>
                      </span>
                    {/if}
                  </div>
                </div>

                <div class="browser-account-row">
                  <span class={`simple-icon browser-tone-${index % 3}`}><Browser size={14} /></span>
                  <div class="simple-title browser-account-title">
                    <h2>
                      <span>{item.account_name}</span>
                      <small>{browserAccountDouyinId(item)}</small>
                      {#if browserCookieExpired(item)}
                        <em class="browser-cookie-expired" data-tooltip={browserCookieMessage(item)} data-tooltip-placement="top">CK 已失效</em>
                      {/if}
                    </h2>
                  </div>
                  <div class="browser-card-actions">
                    <button
                      class="secondary-button browser-close-button"
                      aria-label="关闭实例"
                      data-tooltip="关闭实例"
                      data-tooltip-placement="left"
                      disabled={browserClosingId === item.id}
                      onclick={() => closeBrowserInstance(item)}
                    >
                      {#if browserClosingId === item.id}<ArrowClockwise class="spinning" size={13} />
                      {:else}<X size={13} weight="bold" />{/if}
                    </button>
                    <button
                      class="secondary-button browser-open-button"
                      aria-label={item.status === "online" ? "重新打开实例" : "打开实例"}
                      data-tooltip={item.status === "online" ? "重新打开实例" : "打开实例"}
                      data-tooltip-placement="left"
                      disabled={browserOpeningId === item.id || browserClosingId === item.id}
                      onclick={() => openBrowserInstance(item)}
                    >
                      {#if browserOpeningId === item.id}
                        <ArrowClockwise class="spinning" size={14} />
                      {:else}
                        <ArrowSquareOut size={14} />
                      {/if}
                    </button>
                  </div>
                </div>
              </article>
            {/each}
          </div>
        {:else}
          <div class="empty-state browser-empty">
            <Browser size={31} />
            <strong>{query ? "没有匹配的浏览器实例" : "还没有浏览器实例"}</strong>
            <span>{query ? "换个关键词试试" : "选择一个参与账号，创建独立的抖音登录窗口"}</span>
            {#if !query}<button class="secondary-button" onclick={openInstanceModal}><Plus size={14} />新建实例</button>{/if}
          </div>
        {/if}
        {#if browserInstances.length > 0}
          <label
            class="browser-column-control"
            style={`--range-progress:${((browserColumns - 1) / 9) * 100}%`}
            data-tooltip={`每行显示 ${browserColumns} 列`}
            data-tooltip-placement="top"
            onpointerenter={scheduleBrowserColumnControlSync}
            onpointerleave={scheduleBrowserColumnControlSync}
            onfocusin={scheduleBrowserColumnControlSync}
            onfocusout={scheduleBrowserColumnControlSync}
            ontransitionend={scheduleBrowserColumnControlSync}
          >
            <input
              type="range"
              min="1"
              max="10"
              step="1"
              value={browserColumns}
              aria-label="浏览器实例每行列数"
              aria-valuetext={`${browserColumns} 列`}
              oninput={updateBrowserColumns}
            />
          </label>
        {/if}
      {:else}
        <div class="account-tabs" role="tablist" aria-label="直播间与账号分类">
          <button
            class:active={managementTab === "redpackets"}
            role="tab"
            aria-selected={managementTab === "redpackets"}
            onclick={() => selectManagementTab("redpackets")}
          >
            红包 <span>{redPacketEvents.length}</span>
          </button>
          <button
            class:active={managementTab === "rooms"}
            role="tab"
            aria-selected={managementTab === "rooms"}
            onclick={() => selectManagementTab("rooms")}
          >
            直播间 <span>{rooms.length}</span>
          </button>
          <button
            class:active={managementTab === "participation"}
            role="tab"
            aria-selected={managementTab === "participation"}
            onclick={() => selectManagementTab("participation")}
          >
            参与账号 <span>{participationAccounts.length}</span>
          </button>
          <button
            class:active={managementTab === "monitoring"}
            role="tab"
            aria-selected={managementTab === "monitoring"}
            onclick={() => selectManagementTab("monitoring")}
          >
            监测账号 <span>{monitoringAccounts.length}</span>
          </button>
          {#if managementTab === "redpackets"}
            <p>仅展示直播间实时发现的红包</p>
          {:else if managementTab === "rooms"}
            <div class="room-monitor-bulk-actions" aria-label="直播间红包监测批量操作">
              <button class="monitor-log-button" aria-label="查看红包监测运行日志" data-tooltip="查看红包监测运行日志" data-tooltip-placement="top" onclick={openMonitorRuntimeLog}>
                <TerminalWindow size={13} />
              </button>
              {#if canStartAnyRedPacketMonitor || redPacketBatchAction === "start"}
                <button
                  class="secondary-button compact-action monitor-bulk-start"
                  disabled={redPacketBatchAction !== "" || enabledRedPacketMonitors.length === 0}
                  onclick={() => toggleAllRedPacketMonitors("start")}
                >
                  <Play size={12} weight="fill" />{redPacketBatchAction === "start" ? "启动中…" : "全部启动"}
                </button>
              {/if}
              {#if canStopAnyRedPacketMonitor || redPacketBatchAction === "stop"}
                <button
                  class="secondary-button compact-action monitor-bulk-stop"
                  disabled={redPacketBatchAction !== ""}
                  onclick={() => toggleAllRedPacketMonitors("stop")}
                >
                  <Pause size={12} weight="fill" />{redPacketBatchAction === "stop" ? "停止中…" : "全部停止"}
                </button>
              {/if}
            </div>
          {:else}
            <div class="menu-anchor filter-anchor account-filter-anchor">
              <button
                class="filter-button"
                aria-haspopup="menu"
                aria-expanded={statusMenuOpen}
                onclick={toggleStatusMenu}
              >
                <SlidersHorizontal size={16} />
                <span>{accountStatusFilterLabel(accountStatusFilter)}</span>
                <CaretDown size={12} />
              </button>
              {#if statusMenuOpen}
                <div class="floating-menu status-menu" role="menu">
                  {#each [
                    ["all", "全部状态"],
                    ["available", "可用"],
                    ["expired", "CK 失效"],
                    ["cooldown", "冷却中"],
                  ] as option}
                    <button
                      class:active={accountStatusFilter === option[0]}
                      role="menuitemradio"
                      aria-checked={accountStatusFilter === option[0]}
                      onclick={() => selectAccountStatus(option[0] as AccountStatusFilter)}
                    >
                      <span>{option[1]}</span>
                      {#if accountStatusFilter === option[0]}<CheckCircle size={15} weight="fill" />{/if}
                    </button>
                  {/each}
                </div>
              {/if}
            </div>
          {/if}
        </div>
        <section
          class:fill-panel={managementTab === "redpackets" || managementTab === "rooms" || visibleAccounts.length > 8}
          class="account-panel"
        >
          {#if managementTab === "redpackets"}
            {#if redPacketEventError}
              <div class="account-notice error">
                <WarningCircle size={17} />
                <span>{redPacketEventError}</span>
                <button onclick={loadRedPacketEvents}>重试</button>
              </div>
            {:else if redPacketEventsLoading && redPacketEvents.length === 0}
              <div class="account-empty"><ArrowClockwise class="spinning" size={22} /><span>正在读取实时红包…</span></div>
            {:else if visibleRedPacketEvents.length === 0}
              <div class="account-empty">
                <Gift size={28} />
                <strong>{query ? "没有匹配的红包" : "暂未发现实时红包"}</strong>
                <span>{query ? "换个关键词试试" : "请先在“直播间”点击“全部启动”，发现红包后会显示在这里"}</span>
              </div>
            {:else}
              <div class="red-packet-event-list">
                <div class="red-packet-event-head">
				  <span>红包</span><span>直播间 / 监测账号</span><span>奖品与时效</span><span>发现时间</span><span>参与人数</span>
                </div>
                {#each visibleRedPacketEvents as event}
                  {@const expiry = redPacketEventExpiryParts(event)}
                  <article class="red-packet-event-row">
                    <div class="red-packet-event-identity">
                      <span class="room-avatar red-packet-avatar"><Gift size={17} weight="fill" /></span>
                      <div>
                        <strong>{event.title || "直播间红包"}</strong>
                        <small>{event.packet_id || "红包事件"} · {event.source === "luckybox_api" ? "红包接口" : "实时检测"}</small>
                      </div>
                    </div>
                    <div class="red-packet-event-room">
					  <button
						class="red-packet-room-link"
						type="button"
						title="在浏览器中打开直播间"
						onclick={() => openRedPacketLiveRoom(event)}
					  >
						<span>{event.room_name || event.streamer_name || `直播间 ${event.web_rid || event.room_id}`}</span>
						<ArrowSquareOut size={11} />
					  </button>
					  <small>{event.streamer_name || "尚未读取主播"} · 账号 {event.account_name || event.account_id || "待解析"}</small>
                    </div>
					<div class="red-packet-event-prize">
					  <strong>{event.prize || "奖品待解析"}</strong>
					  <small class="red-packet-event-expiry">
						{#if expiry.countdown}
						  <span class="red-packet-event-countdown">{expiry.countdown}</span>
						  <span class="red-packet-event-expiry-absolute">· {expiry.absolute}</span>
						{:else}
						  <span>{expiry.text}</span>
						{/if}
					  </small>
					</div>
                    <span class="red-packet-event-time">{formatMonitorTime(event.detected_at)}</span>
                    <span class="red-packet-event-participants">{event.participant_count ? `${event.participant_count} 人` : "—"}</span>
                  </article>
                {/each}
                {#if visibleRedPacketEvents.length < filteredRedPacketEvents.length}
                  <div class="room-list-more">
                    <span>已显示 {visibleRedPacketEvents.length} / {filteredRedPacketEvents.length} 条</span>
                    <button onclick={() => (redPacketRenderLimit += 300)}>继续显示</button>
                  </div>
                {/if}
              </div>
            {/if}
          {:else if managementTab === "rooms"}
            {#if roomError || redPacketMonitorError}
              <div class="account-notice error">
                <WarningCircle size={17} />
                <span>{roomError || redPacketMonitorError}</span>
                <button onclick={() => { void loadRooms(false); void loadRedPacketMonitors(); }}>重试</button>
              </div>
            {:else if roomsLoading && rooms.length === 0}
              <div class="account-empty"><ArrowClockwise class="spinning" size={22} /><span>正在读取本机直播间…</span></div>
            {:else if visibleRooms.length === 0}
              <div class="account-empty">
                <Radio size={28} />
                <strong>{query ? "没有匹配的直播间" : "还没有直播间数据"}</strong>
                <span>{query ? "换个关键词试试" : "点击右上角“导入数据”，从旧福宝复制直播间列表"}</span>
              </div>
            {:else}
              <div class="room-list">
                <div class="room-list-head">
                  <span>直播间</span><span>主播 / 房间标识</span><span>直播与红包状态</span><span>操作</span>
                </div>
                {#each visibleRooms as room}
                    {@const roomMonitor = roomMonitorFor(room, redPacketMonitors)}
                    {@const roomMonitorStatus = roomMonitor ? redPacketMonitorUiStatus(roomMonitor, redPacketMonitorOverrides) : ""}
                    {@const roomStatusValue = roomMonitor ? roomLiveStatus(roomMonitor, roomMonitorStatus) : room.enabled ? "未监测" : "已停用"}
                    <article class="room-row">
                    <div class="room-identity">
                      <span class="room-avatar"><Radio size={18} weight="fill" /></span>
                      <div>
                        <strong>{roomDisplayName(room)}</strong>
                        <small>{room.web_rid ? `房间号 ${room.web_rid}` : `记录号 ${room.id}`}</small>
                      </div>
                    </div>
                    <div class="room-streamer-info">
                      <strong>{roomMonitor?.streamer_name || room.streamer_name || "尚未读取主播"}</strong>
                      <small>房间标识 {roomMonitor?.actual_room_id || room.actual_room_id || room.id} · {room.source === "dy-kiro" ? "旧福宝导入" : "本机创建"}</small>
                    </div>
                    <div class="room-live-state">
                      <span
                        class:running={roomStatusValue === "已开播"}
                        class:warning={roomStatusValue === "检测中" || roomStatusValue === "探测异常"}
                        class:muted={roomStatusValue === "已停用" || roomStatusValue === "未监测" || roomStatusValue === "未开播"}
                        class="status-pill"
                      ><span></span>{roomStatusValue}</span>
                      <small>{roomMonitor ? roomMonitorPhase(roomMonitor, roomMonitorStatus) : "红包监测未准备"}</small>
                    </div>
                    {#if roomMonitor}
                      <button
                        class:running={roomMonitorStatus === "running"}
                        class="icon-button room-row-action room-monitor-icon-action"
                        disabled={!roomMonitor.enabled || redPacketMonitorActionId === roomMonitor.id || redPacketBatchAction !== ""}
                        aria-label={redPacketMonitorActionId === roomMonitor.id
                          ? "正在处理红包监测"
                          : roomMonitorStatus === "running"
                            ? "停止红包监测"
                            : "启动红包监测"}
                        data-tooltip={redPacketMonitorActionId === roomMonitor.id
                          ? "正在处理红包监测"
                          : roomMonitorStatus === "running"
                            ? "停止红包监测"
                            : "启动红包监测"}
                        data-tooltip-placement="left"
                        onclick={() => toggleRoomRedPacketMonitor(roomMonitor)}
                      >
                        {#if redPacketMonitorActionId === roomMonitor.id}
                          <ArrowClockwise class="spinning" size={13} />
                        {:else if roomMonitorStatus === "running"}
                          <Pause size={13} weight="fill" />
                        {:else}
                          <Play size={13} weight="fill" />
                        {/if}
                      </button>
                    {:else}
                      <span class="room-row-action-muted">准备中</span>
                    {/if}
                    </article>
                {/each}
                {#if visibleRooms.length < filteredRooms.length}
                  <div class="room-list-more">
                    <span>已显示 {visibleRooms.length} / {filteredRooms.length} 条</span>
                    <button onclick={() => (roomRenderLimit += 300)}>继续显示</button>
                  </div>
                {/if}
              </div>
            {/if}
          {:else if accountError}
            <div class="account-notice error">
              <WarningCircle size={17} />
              <span>{accountError}</span>
              <button onclick={() => loadAccounts()}>重试</button>
            </div>
          {:else if accountsLoading && accounts.length === 0}
            <div class="account-empty"><ArrowClockwise class="spinning" size={22} /><span>正在读取本机账号…</span></div>
          {:else if visibleAccounts.length === 0}
            <div class="account-empty">
              <UserCircle size={28} />
              <strong>{query ? "没有匹配的账号" : `还没有${roleLabel(accountRole)}`}</strong>
              <span>{query || accountStatusFilter !== "all" ? "调整搜索或状态筛选后再试" : "可以从另一个分类添加，或通过“导入数据”导入账号"}</span>
            </div>
          {:else}
            <div class="account-list">
              <div class="account-list-head">
                <span>账号</span><span>分类</span><span>状态与数据</span><span>操作</span>
              </div>
              {#each visibleAccounts as account}
                <article class="account-row">
                  <div class="account-identity">
                    <span class="account-avatar"><UserCircle size={20} weight="fill" /></span>
                    <div>
                      <strong>{account.nickname || account.name}</strong>
                      <small>{account.user_id ? `抖音号 ${account.user_id}` : "尚未读取抖音号"}</small>
                    </div>
                  </div>
                  <div class="role-badges">
                    {#if hasAccountRole(account, "participation")}
                      <span class="role-badge participation">
                        <span class="role-badge-label">参与</span>
                        {#if account.roles.length > 1}
                          <button
                            aria-label="移除参与分类"
                            data-tooltip="移除参与分类"
                            data-tooltip-placement="top"
                            onclick={() => removeAccountRole(account, "participation")}
                          ><X size={8} weight="bold" /></button>
                        {/if}
                      </span>
                    {/if}
                    {#if hasAccountRole(account, "monitoring")}
                      <span class="role-badge monitoring">
                        <span class="role-badge-label">监测</span>
                        {#if account.roles.length > 1}
                          <button
                            aria-label="移除监测分类"
                            data-tooltip="移除监测分类"
                            data-tooltip-placement="top"
                            onclick={() => removeAccountRole(account, "monitoring")}
                          ><X size={8} weight="bold" /></button>
                        {/if}
                      </span>
                    {/if}
                    {#if account.roles.length === 1}
                      <button
                        class="add-role-button"
                        aria-label={`同时加入${roleLabel(oppositeRole(accountRole))}`}
                        data-tooltip={`同时加入${roleLabel(oppositeRole(accountRole))}`}
                        data-tooltip-placement="top"
                        onclick={() => addAccountToRole(account, oppositeRole(accountRole))}
                      ><Plus size={10} weight="bold" /></button>
                    {/if}
                  </div>
                  <div class="account-health">
                    <div class="account-health-status">
					  <span class:warning={accountStatus(account) === "冷却中"} class:expired={accountStatus(account) === "CK 失效"} class="status-pill" data-tooltip={accountCookieMessage(account, accountRole) || undefined} data-tooltip-placement="top">
                        <span></span>{accountStatus(account)}
                      </span>
                      {#if accountStatus(account) === "CK 失效"}
                        <button
                          class="account-rebind-button"
                          class:spinning={accountRebindOpeningId === account.id}
                          aria-label={`重新绑定 ${account.nickname || account.name}`}
                          data-tooltip="重新绑定 CK"
                          data-tooltip-placement="top"
                          disabled={Boolean(accountRebindOpeningId) || accountRebindCompleting}
                          onclick={() => openAccountRebind(account)}
                        ><ArrowClockwise size={12} weight="bold" /></button>
                      {/if}
                    </div>
                    <small
                      data-tooltip={accountRole === "monitoring" ? accountMonitoringUsageTip(account) : undefined}
                      data-tooltip-placement="top"
                    >{accountMeta(account)}</small>
                  </div>
                  <div class="account-actions">
                    <button
                      class="delete-account-button"
                      aria-label={`删除账号 ${account.nickname || account.name}`}
                      data-tooltip="删除账号"
                      data-tooltip-placement="left"
                      onclick={() => (accountPendingDelete = account)}
                    ><Trash size={15} /></button>
                  </div>
                </article>
              {/each}
            </div>
          {/if}
        </section>
      {/if}
    </section>
  </main>
</div>

{#if updateModalOpen && updateStatus}
  <div
    class="modal-backdrop"
    role="presentation"
    onclick={(event) => event.currentTarget === event.target && closeUpdateModal()}
  >
    <dialog class="modal update-modal" open aria-labelledby="update-modal-title">
      <div class="modal-head">
        <div>
          <span class="modal-icon update-modal-icon"><DownloadSimple size={18} weight="bold" /></span>
          <h2 id="update-modal-title">软件更新</h2>
        </div>
        <button class="icon-button" aria-label="关闭" disabled={updateDownloading || updateInstalling} onclick={closeUpdateModal}><X size={17} /></button>
      </div>

      <div class="update-version-summary">
        <span><small>当前版本</small><strong>v{clientVersion}</strong></span>
        <ArrowClockwise size={15} />
        <span class="latest"><small>最新版本</small><strong>v{updateStatus.latest_version}</strong></span>
      </div>

      <div class="update-notes">
        <small>更新说明</small>
        <p>{updateStatus.notes || "新版本已准备好，建议升级后继续使用。"}</p>
      </div>

      {#if updateDownloading || updateDownloaded}
        <div class="update-progress-block">
          <div>
            <span>{updateDownloaded ? "下载完成，安装包校验通过" : "正在下载并校验安装包…"}</span>
            <strong>{updateProgress.percent}%</strong>
          </div>
          <progress max="100" value={updateProgress.percent}></progress>
          <small>
            {formatFileSize(updateProgress.downloaded)}
            {#if updateProgress.total > 0} / {formatFileSize(updateProgress.total)}{/if}
          </small>
        </div>
      {:else}
        <p class="update-package-meta">安装包 {formatFileSize(updateStatus.size)} · 下载后自动校验完整性</p>
      {/if}

      {#if updateError}<div class="license-error"><WarningCircle size={14} />{updateError}</div>{/if}
      <div class="modal-actions">
        <button class="secondary-button" disabled={updateDownloading || updateInstalling} onclick={closeUpdateModal}>稍后更新</button>
        {#if updateDownloaded}
          <button class="primary-action" disabled={updateInstalling} onclick={installAppUpdate}>
            <ArrowClockwise class={updateInstalling ? "spinning" : undefined} size={14} />
            {updateInstalling ? "正在启动…" : "安装并重启"}
          </button>
        {:else}
          <button class="primary-action" disabled={updateDownloading} onclick={downloadAppUpdate}>
            <DownloadSimple size={14} />{updateDownloading ? `下载中 ${updateProgress.percent}%` : "立即升级"}
          </button>
        {/if}
      </div>
    </dialog>
  </div>
{/if}

{#if licenseModalOpen}
  <div
    class="modal-backdrop"
    role="presentation"
    onclick={(event) => event.currentTarget === event.target && closeLicenseModal()}
  >
    <dialog class="modal license-modal" open aria-labelledby="license-modal-title">
      <div class="modal-head">
        <div>
          <span class:professional={licenseStatus.state === "active"} class="modal-icon license-modal-icon"><ShieldCheck size={18} weight="bold" /></span>
          <h2 id="license-modal-title">授权管理</h2>
        </div>
        <button class="icon-button" aria-label="关闭" disabled={licenseBusy} onclick={closeLicenseModal}><X size={17} /></button>
      </div>

      <div class:professional={licenseStatus.state === "active"} class="license-summary">
        <div>
          <span class="license-edition">{licenseStatus.edition}</span>
          <strong>{licenseStatus.state === "active" ? "当前设备已激活" : "当前设备未激活"}</strong>
        </div>
        <p>{licenseStatus.detail}</p>
        {#if licenseStatus.machine_code}<small>设备码 {licenseStatus.machine_code}</small>{/if}
      </div>

      {#if licenseStatus.state === "active"}
        <div class="license-details">
          <span><small>激活码</small><strong>{licenseStatus.license_key_masked || "已保存"}</strong></span>
          <span><small>最近验证</small><strong>{licenseStatus.last_validated_at ? new Date(licenseStatus.last_validated_at).toLocaleString("zh-CN", { hour12: false }) : "刚刚"}</strong></span>
        </div>
      {:else}
        <label class="field license-key-field">
          <span>激活码</span>
          <input bind:value={licenseKey} placeholder="输入福宝激活码" autocomplete="off" spellcheck="false" onkeydown={(event) => event.key === "Enter" && activateLicense()} />
        </label>
      {/if}

      {#if licenseError}<div class="license-error"><WarningCircle size={14} />{licenseError}</div>{/if}
      <div class="modal-actions">
        <button class="secondary-button" disabled={licenseBusy} onclick={closeLicenseModal}>关闭</button>
        {#if licenseStatus.state === "active"}
          <button class="primary-action" disabled={licenseBusy} onclick={refreshLicense}>
            <ArrowClockwise class={licenseBusy ? "spinning" : undefined} size={14} />{licenseBusy ? "刷新中…" : "刷新授权"}
          </button>
        {:else}
          <button class="primary-action" disabled={licenseBusy || !licenseKey.trim()} onclick={activateLicense}>
            {#if licenseBusy}<ArrowClockwise class="spinning" size={14} />{:else}<ShieldCheck size={14} />{/if}
            {licenseBusy ? "激活中…" : "激活专业版"}
          </button>
        {/if}
      </div>
    </dialog>
  </div>
{/if}

{#if instanceModalOpen}
  <div
    class="modal-backdrop"
    role="presentation"
    onclick={(event) => event.currentTarget === event.target && !browserCreating && closeInstanceModal()}
  >
    <dialog class="modal instance-modal" open aria-labelledby="instance-modal-title">
      <div class="modal-head">
        <div>
          <span class="modal-icon browser-modal-icon"><Browser size={18} /></span>
          <h2 id="instance-modal-title">新建浏览器实例</h2>
        </div>
        <button
          class="icon-button"
          aria-label="关闭"
          disabled={browserCreating}
          onclick={closeInstanceModal}
        ><X size={17} /></button>
      </div>
      <p class="modal-intro">
        选择一个或多个参与账号。创建后先显示在实例卡片中，点击卡片时才打开独立浏览器窗口。
      </p>

      <div class="instance-account-heading">
        <strong>选择参与账号</strong>
        <span>{selectedParticipationAccountIds.length > 0 ? `${selectedParticipationAccountIds.length} 个已选 · ` : ""}{selectableParticipationAccounts.length} 个可创建</span>
      </div>

      {#if accountsLoading}
        <div class="instance-account-empty"><ArrowClockwise class="spinning" size={20} /><span>正在读取参与账号…</span></div>
      {:else if selectableParticipationAccounts.length === 0}
        <div class="instance-account-empty">
          <UserFocus size={24} />
          <strong>没有可用的参与账号</strong>
          <span>已有实例的账号不会重复显示</span>
          <button
            class="secondary-button"
            onclick={() => {
              closeInstanceModal();
              switchView("accounts");
              accountRole = "participation";
            }}
          >前往账号管理</button>
        </div>
      {:else}
        <div class="instance-account-list" role="group" aria-label="选择参与账号，可多选">
          {#each selectableParticipationAccounts as account}
            <button
              class:selected={selectedParticipationAccountIds.includes(account.id)}
              class="instance-account-option"
              role="checkbox"
              aria-checked={selectedParticipationAccountIds.includes(account.id)}
              onclick={() => toggleParticipationAccount(account.id)}
            >
              <span class="account-avatar"><UserCircle size={20} weight="fill" /></span>
              <span class="instance-account-copy">
                <strong>{account.nickname || account.name}</strong>
                <small>{account.user_id ? `抖音号 ${account.user_id}` : "尚未读取抖音号"}</small>
              </span>
              <span class="instance-account-state">
                <span class="status-pill"><span></span>可用</span>
                <span class:selected={selectedParticipationAccountIds.includes(account.id)} class="radio-mark">
                  {#if selectedParticipationAccountIds.includes(account.id)}<CheckCircle size={15} weight="fill" />{/if}
                </span>
              </span>
            </button>
          {/each}
        </div>
      {/if}

      {#if browserError}<div class="instance-error"><WarningCircle size={15} />{browserError}</div>{/if}
      <div class="notice instance-notice">
        <ShieldCheck size={17} />
        <span>Cookie 仅在 Go 引擎与实例配置目录之间同步，不会返回界面；每个实例的缓存和登录态完全隔离。</span>
      </div>
      <div class="modal-actions">
        <button class="secondary-button" disabled={browserCreating} onclick={closeInstanceModal}>取消</button>
        <button
          class="primary-action instance-create-action"
          disabled={selectedParticipationAccountIds.length === 0 || browserCreating}
          onclick={createBrowserInstance}
        >
          {#if browserCreating}<ArrowClockwise class="spinning" size={15} />
          {:else}<Plus size={15} weight="bold" />{/if}
          {browserCreating ? "正在创建…" : selectedParticipationAccountIds.length > 0 ? `创建 ${selectedParticipationAccountIds.length} 个实例` : "创建实例"}
        </button>
      </div>
    </dialog>
  </div>
{/if}

{#if roomImportModalOpen}
  <div
    class="modal-backdrop"
    role="presentation"
    onclick={(event) => event.currentTarget === event.target && !roomImportBusy && (roomImportModalOpen = false)}
  >
    <dialog class="modal room-import-modal" open aria-labelledby="room-import-modal-title">
      <div class="modal-head">
        <div>
          <span class="modal-icon room-import-icon"><Radio size={18} /></span>
          <h2 id="room-import-modal-title">批量导入直播间</h2>
        </div>
        <button class="icon-button" aria-label="关闭" disabled={roomImportBusy} onclick={() => (roomImportModalOpen = false)}><X size={17} /></button>
      </div>
      <p class="modal-intro room-import-intro">粘贴直播间 ID，或上传 .txt / .csv 文件。系统会自动识别、去重并加入直播间列表。</p>
      <label class="room-import-label" for="room-import-text">直播间 ID</label>
      <textarea
        id="room-import-text"
        class="room-import-textarea"
        bind:value={roomImportText}
        placeholder="示例：\n123456789012\n987654321098"
        spellcheck="false"
      ></textarea>
      <div class="room-import-help">支持一行一个 ID，也支持逗号、空格分隔和直播间链接；仅接受 6–20 位数字。</div>
      <div class="modal-actions room-import-actions">
        <button class="secondary-button room-upload-button" disabled={roomImportBusy} onclick={() => roomImportFileInput?.click()}>
          <UploadSimple size={15} />上传文件
        </button>
        <span class="room-import-action-spacer"></span>
        <button class="secondary-button" disabled={roomImportBusy} onclick={() => (roomImportModalOpen = false)}>取消</button>
        <button class="primary-action" disabled={roomImportBusy || !roomImportText.trim()} onclick={importRoomIDs}>
          {#if roomImportBusy}<ArrowClockwise class="spinning" size={15} />{:else}<UploadSimple size={15} />{/if}
          {roomImportBusy ? "正在导入…" : "导入"}
        </button>
      </div>
    </dialog>
  </div>
{/if}

{#if modalOpen}
  <div class="modal-backdrop" role="presentation" onclick={(event) => event.currentTarget === event.target && (modalOpen = false)}>
    <dialog class="modal" open aria-labelledby="modal-title">
      <div class="modal-head">
        <div><span class="modal-icon"><Radio size={19} weight="fill" /></span><h2 id="modal-title">新建直播间监测</h2></div>
        <button class="icon-button" aria-label="关闭" onclick={() => (modalOpen = false)}><X size={18} /></button>
      </div>
      <p class="modal-intro">添加一个直播间到监测队列。创建后由 Go 引擎负责连接、调度和状态恢复。</p>
      <label class="field"><span>直播间名称</span><input bind:value={newName} placeholder="例如：晚间红包专场" /></label>
      <label class="field"><span>直播间地址或房间号</span><input bind:value={newRoom} placeholder="粘贴直播间链接或输入房间号" /></label>
      <div class="notice"><WarningCircle size={17} /><span>基础架子暂使用演示数据，Go 引擎接入后这里会执行真实连接检查。</span></div>
      <div class="modal-actions">
        <button class="secondary-button" onclick={() => (modalOpen = false)}>取消</button>
        <button class="primary-action" disabled={!newRoom.trim()} onclick={addMonitor}><Plus size={17} />创建监测</button>
      </div>
    </dialog>
  </div>
{/if}

{#if accountRebinding || accountCreateSessionId}
  <div class="modal-backdrop" role="presentation">
    <dialog class="modal account-rebind-modal" open aria-labelledby="account-rebind-title">
      <div class="modal-head">
        <div class="account-rebind-heading">
          <span class="modal-icon account-rebind-icon">{#if accountRebinding}<ArrowClockwise size={18} weight="bold" />{:else}<QrCode size={18} weight="bold" />{/if}</span>
          <div>
            <h2 id="account-rebind-title">{accountRebinding ? "重新绑定 CK" : "扫码添加账号"}</h2>
            <p class="modal-intro">
              {accountRebindOpeningId
                ? "正在准备抖音登录页面…"
                : accountRebinding
                  ? `请完成「${accountRebinding.nickname || accountRebinding.name}」的抖音登录，再更新 CK。`
                  : `请完成抖音登录，账号将新增到${roleLabel(accountCreateRole)}。`}
            </p>
          </div>
        </div>
        <button class="icon-button" aria-label="关闭" disabled={accountRebindCompleting || Boolean(accountRebindOpeningId)} onclick={cancelAccountRebind}><X size={17} /></button>
      </div>
      <div class="account-rebind-browser-shell">
        <div class="account-rebind-loading" aria-hidden="true"><ArrowClockwise class={accountRebindOpeningId ? "spinning" : ""} size={18} /></div>
        <div class="account-rebind-browser" bind:this={accountRebindViewport}></div>
      </div>
      <div class="account-rebind-footer">
        <div class="account-rebind-notice">
          <ShieldCheck size={15} />
          <span>{accountRebinding ? "新 CK 由原生端直接写入 Go 账号存储，不会显示或传递到页面中。" : "登录信息由原生端直接写入 Go 账号存储，不会显示或传递到页面中。"}</span>
        </div>
        <div class="modal-actions">
          <button class="secondary-button" disabled={accountRebindCompleting || Boolean(accountRebindOpeningId)} onclick={cancelAccountRebind}>取消</button>
          <button class="primary-action" disabled={accountRebindCompleting || Boolean(accountRebindOpeningId)} onclick={completeAccountRebind}>
            {#if accountRebindOpeningId}<ArrowClockwise class="spinning" size={15} />正在打开{:else if accountRebindCompleting}<ArrowClockwise class="spinning" size={15} />{:else}<CheckCircle size={15} weight="fill" />{/if}
            {accountRebindOpeningId ? "登录窗口" : accountRebinding ? "登录完成，更新 CK" : "登录完成，添加账号"}
          </button>
        </div>
      </div>
    </dialog>
  </div>
{/if}

{#if accountPendingDelete}
  <ConfirmDialog
    title="删除账号"
    message={`确定删除「${accountPendingDelete.nickname || accountPendingDelete.name}」吗？\n账号将从参与和监测分类中永久移除，本机保存的登录信息与运行配置也会一并删除。`}
    confirmText="确认删除"
    busy={accountDeleting}
    onCancel={() => !accountDeleting && (accountPendingDelete = null)}
    onConfirm={deleteAccount}
  />
{/if}

{#if toast}<div class:browser-toast={activeView === "browsers"} class="toast" role="status" aria-live="polite"><CheckCircle size={16} weight="fill" />{toast}</div>{/if}
