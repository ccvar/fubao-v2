<script lang="ts">
  import { invoke } from "@tauri-apps/api/core";
  import { getVersion } from "@tauri-apps/api/app";
  import { emit, listen } from "@tauri-apps/api/event";
  import { relaunch } from "@tauri-apps/plugin-process";
  import { check as checkUpdater, type DownloadEvent, type Update } from "@tauri-apps/plugin-updater";
  import { tick, onMount } from "svelte";
  import ConfirmDialog from "./lib/ConfirmDialog.svelte";
  import appIconUrl from "../src-tauri/icons/64x64.png";
  import {
    PulseIcon as Activity,
    ArchiveIcon as Archive,
    ArrowClockwiseIcon as ArrowClockwise,
    ArrowsDownUpIcon as ArrowsDownUp,
    ArrowSquareOutIcon as ArrowSquareOut,
    BrowserIcon as Browser,
    CaretDownIcon as CaretDown,
    CaretLeftIcon as CaretLeft,
    CaretRightIcon as CaretRight,
    CheckCircleIcon as CheckCircle,
    ClockCountdownIcon as ClockCountdown,
    ClipboardTextIcon as ClipboardText,
    CloudArrowDownIcon as CloudArrowDown,
    SketchLogoIcon as DiamondGem,
    DotsThreeIcon as DotsThree,
    DownloadSimpleIcon as DownloadSimple,
    FileArrowUpIcon as FileArrowUp,
    FolderOpenIcon as FolderOpen,
    FunnelSimpleIcon as FunnelSimple,
    GearSixIcon as GearSix,
    GiftIcon as Gift,
    MagnifyingGlassIcon as MagnifyingGlass,
    MonitorIcon as Monitor,
    PauseIcon as Pause,
    PencilSimpleIcon as PencilSimple,
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

  type NavKey = "browsers" | "accounts";
  type MonitorState = "running" | "paused" | "warning";
  type AccountRole = "monitoring" | "participation";
  type ManagementTab = "redpackets" | "rooms" | "participation-records" | AccountRole;
  type AccountStatusFilter = "all" | "available" | "expired" | "cooldown";
  type ParticipationGroupFilter = "all" | "ungrouped" | string;
  type RoomSortMode = "default" | "instance-first" | "live-first" | "recent-live" | "recent-redpacket";
  type RoomSourceFilter = "all" | "following" | "imported" | "center";
  type MonitoringAccountSortMode = "default" | "total-requests" | "today-requests" | "last-request" | "created-at";
  type ParticipationAccountSortMode = "default" | "available-first" | "join-count" | "win-count" | "created-at";
  type AccountSortMode = MonitoringAccountSortMode | ParticipationAccountSortMode;
  type SettingsTab = "participation" | "rooms" | "monitoring";

  type RoomSettings = {
    auto_recycle_offline_days: number;
    participation_prewarm_minutes: number;
    auto_recycle_low_live_enabled: boolean;
    auto_recycle_max_live_sessions: number;
    auto_recycle_no_packet_enabled: boolean;
    auto_recycle_no_packet_days: number;
    auto_recycle_imported_no_packet_enabled: boolean;
  };

  type RoomCleanupProgress = {
    total: number;
    scanned: number;
    cleaned: number;
    recycled: number;
    excluded: number;
    skipped: number;
    next_cursor?: string;
    has_more: boolean;
  };

  type MonitoringSettings = {
    global_request_interval_ms: number;
    account_request_interval_ms: number;
    global_concurrency: number;
    account_concurrency: number;
    probe_concurrency: number;
  };

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

  type RemoteSyncStatus = {
    enabled: boolean;
    configured: boolean;
	upload_only?: boolean;
    token_masked?: string;
    endpoint: string;
    fallback_endpoint: string;
    active_endpoint?: string;
    pending: number;
    client_id: string;
    last_success_at?: string;
    last_error?: string;
    last_error_at?: string;
  };

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
	red_packet_api_enabled?: boolean;
	red_packet_cooldown_until?: string;
	last_red_packet_status?: string;
	last_red_packet_message?: string;
	join_count?: number;
	win_count?: number;
	diamond_balance?: number;
	diamond_x10?: number;
	diamond_checked_at?: string;
	diamond_status?: string;
	last_join_at?: string;
    proxy_id?: number;
    fingerprint_profile_id?: number;
    tags?: string[];
    group_id?: string;
  };

  type ParticipationGroup = {
    id: string;
    name: string;
    created_at: string;
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
    cpu_recommended_limit: number;
    memory_recommended_limit: number;
    maximum_auto_instances: number;
    memory_reserve_bytes: number;
    effective_limit: number;
    available_slots: number;
    estimated_per_instance_bytes: number;
    resources: {
      cpu_count: number;
      cpu_usage_percent: number;
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

  type BrowserInstanceWindowEvent = {
    instance_id: string;
  };

  type BrowserParticipationContext = {
	instance_id: string;
	account_id: string;
	prepared: boolean;
	accepting: boolean;
	active?: boolean;
	task_active?: boolean;
	resumable?: boolean;
	task_id?: string;
	stopped: boolean;
	stop_reason?: string;
	waiting_draw?: boolean;
	waiting_reason?: string;
	pending_draw_count?: number;
	pending_result_web_rid?: string;
	cooldown_until?: string;
	join_count: number;
	win_count: number;
  };

  type FollowingLiveItem = {
    room_id: string;
    web_rid: string;
    user_id?: string;
    sec_uid?: string;
    nickname: string;
    avatar_url?: string;
    title?: string;
    viewer_count?: string;
  };

  type FollowingLiveResult = {
    account_id: string;
    total: number;
    items: FollowingLiveItem[];
    refreshed_at: string;
    stale?: boolean;
  };

  type RoomFollowSource = {
    account_id: string;
    account_name: string;
    last_seen_live_at: string;
    is_live?: boolean;
  };

  type BrowserWebviewEvent = {
    instance_id: string;
    message?: string;
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
    follow_sources?: RoomFollowSource[];
    following_live?: boolean;
    last_seen_live_at?: string;
	center_live_status?: string;
	center_live_at?: string;
	center_last_event_at?: string;
	recycled?: boolean;
	recycled_at?: string;
	recycle_reason?: string;
	offline_days?: number;
	last_offline_day?: string;
    created_at: string;
    updated_at: string;
  };

  type RoomPage = { items: RoomItem[]; total: number };

  type CenterExclusionItem = {
    id: string;
    web_rid: string;
    actual_room_id?: string;
    name?: string;
    streamer_name?: string;
    reason?: string;
    excluded_at: string;
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
    live_started_at?: string;
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

  type RedPacketMonitorSummary = {
    total: number;
    enabled: number;
    running: number;
    first_checked: number;
    pending_first: number;
    live_running: number;
    errors: number;
  };

  type RedPacketMonitorPage = { items: RedPacketMonitor[]; summary: RedPacketMonitorSummary };

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
	data_source?: string;
    detected_at: string;
    draw_at?: string;
	expires_at?: string;
    participant_count?: number;
  };

  type ParticipationRecord = {
    id: string;
    event_id: string;
    account_id: string;
    account_name: string;
    room_id?: string;
    web_rid?: string;
    room_name?: string;
    streamer_name?: string;
    packet_id: string;
    title?: string;
    prize?: string;
    award?: string;
    endpoint?: "join" | "rush" | string;
    status: string;
    message?: string;
    attempt_count: number;
	joined: boolean;
	won?: boolean;
	wallet_before_diamond?: number;
	wallet_after_diamond?: number;
	wallet_diamond_delta?: number;
	result_source?: string;
	cooldown_until?: string;
    created_at: string;
    updated_at: string;
  };

  type ParticipationTrace = {
	id: string;
	task_id?: string;
	event_id: string;
	account_id: string;
	account_name?: string;
	action: string;
	endpoint?: string;
	http_status?: number;
	request_params: Record<string, string>;
	response_params?: string;
	error?: string;
	follow_policy?: ParticipationFollowPolicy;
	followed?: boolean;
	follow_match_known?: boolean;
	created_at: string;
  };

	type ParticipationPacketType = "all" | "gift" | "diamond";
	type ParticipationFollowPolicy = "all" | "follow_priority" | "follow_only";

	type ParticipationSettings = {
	stop_after_joins: number;
	cooldown_seconds: number;
	stop_after_wins: number;
	draw_result_delay_seconds: number;
	draw_result_max_attempts: number;
	participation_countdown_seconds: number;
	minimum_diamonds: number;
	packet_type: ParticipationPacketType;
	follow_policy: ParticipationFollowPolicy;
  };

  type ParticipationScheduleMode = "once" | "daily" | "interval";

  type ParticipationSchedule = {
	id: string;
	mode: ParticipationScheduleMode;
	enabled: boolean;
	run_at?: string;
	daily_time?: string;
	interval_seconds?: number;
	next_run_at: string;
	last_run_at?: string;
	created_at: string;
	updated_at: string;
  };

  type ParticipationScheduleExecution = {
	schedule_id: string;
	mode: ParticipationScheduleMode;
	label: string;
	due_at: string;
  };

  type ParticipationOverview = {
	join_count: number;
	win_count: number;
	win_diamonds: number;
  };

  type ActivityAccountSummary = {
	account_id: string;
	account_name?: string;
	task_id: string;
	join_count: number;
	win_count: number;
	win_diamonds: number;
	end_reason?: string;
  };

  type SidebarActivity = {
	id: string;
	kind: "participation_started" | string;
	account_id?: string;
	account_ids?: string[];
	account_summaries?: ActivityAccountSummary[];
	label: string;
	active?: boolean;
	join_count?: number;
	win_count?: number;
	win_diamonds?: number;
	created_at: string;
	finished_at?: string;
	stopped_at?: string;
	end_reason?: string;
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
    { key: "browsers" as NavKey, label: "浏览器实例", icon: Browser },
    { key: "accounts" as NavKey, label: "账号与直播间", icon: UserFocus },
  ];

  const viewMeta: Record<NavKey, { title: string; subtitle: string }> = {
    browsers: { title: "浏览器实例", subtitle: "3 个实例 · 2 个在线" },
    accounts: { title: "账号与直播间", subtitle: "直播间与账号数据" },
  };

  const isWindowsPlatform =
    typeof navigator !== "undefined" &&
    (/Windows/i.test(navigator.userAgent) ||
      (import.meta.env.DEV &&
        typeof window !== "undefined" &&
        new URLSearchParams(window.location.search).get("platform") === "windows"));

  const pageWindowParams =
    typeof window !== "undefined" ? new URLSearchParams(window.location.search) : null;
  const requestedPageView = pageWindowParams?.get("view") || "";
  const detachedPageView = navItems.some((item) => item.key === requestedPageView)
    ? (requestedPageView as NavKey)
    : null;
  const isDetachedPageWindow = pageWindowParams?.get("window") === "page" && Boolean(detachedPageView);

  let activeView: NavKey = detachedPageView ?? "browsers";
  let navContextMenu: { key: NavKey; x: number; y: number } | null = null;
  let clientVersion = __APP_VERSION__;
  let updateStatus: UpdateStatus | null = null;
  let pendingAppUpdate: Update | null = null;
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
  let licenseReplacing = false;
  let remoteSyncStatus: RemoteSyncStatus = {
    enabled: false,
    configured: false,
    endpoint: "https://fbv2.ccvar.com/api/v1",
    fallback_endpoint: "https://fbv2.ccvar.com:8087/api/v1",
    pending: 0,
    client_id: "",
  };
  let remoteSyncStatusLoaded = false;
  let remoteSyncToken = "";
  let remoteSyncBusy = false;
  let remoteSyncError = "";
  let remoteSyncEditing = false;
  let participationSettingsModalOpen = false;
  let participationSettingsBusy = false;
  let participationSettingsError = "";
  let settingsTab: SettingsTab = "participation";
  let roomSettings: RoomSettings = {
    auto_recycle_offline_days: 7,
    participation_prewarm_minutes: 10,
    auto_recycle_low_live_enabled: false,
    auto_recycle_max_live_sessions: 0,
    auto_recycle_no_packet_enabled: false,
    auto_recycle_no_packet_days: 3,
    auto_recycle_imported_no_packet_enabled: false,
  };
  let monitoringSettings: MonitoringSettings = {
	global_request_interval_ms: 80,
	account_request_interval_ms: 750,
	global_concurrency: 32,
	account_concurrency: 3,
	probe_concurrency: 64,
  };
	let participationSettings: ParticipationSettings = {
	stop_after_joins: 0,
	cooldown_seconds: 0,
	stop_after_wins: 0,
	draw_result_delay_seconds: 1,
	draw_result_max_attempts: 3,
	participation_countdown_seconds: 10,
	minimum_diamonds: 1,
	packet_type: "diamond",
	follow_policy: "follow_priority",
  };
  let participationTaskMenuOpen = false;
  let participationScheduleModalOpen = false;
  let participationScheduleManaging = false;
  let participationScheduleBusy = false;
  let participationScheduleError = "";
  let participationScheduleMode: ParticipationScheduleMode = "once";
  let participationScheduleRunAt = "";
  let participationScheduleDailyTime = "20:00";
  let participationScheduleInterval = 10;
  let participationScheduleIntervalUnit: "minutes" | "hours" = "minutes";
  let participationScheduleUnitMenuOpen = false;
  let participationSchedules: ParticipationSchedule[] = [];
  let participationBatchRunning = false;
  let participationScheduleClaiming = false;
  let participationScheduleModalElement: HTMLDialogElement;
  let participationScheduleModalX = 0;
  let participationScheduleModalY = 0;
  let participationScheduleModalWidth = 600;
  let participationScheduleModalHeight = 440;
  let participationScheduleDragPointer = -1;
  let participationScheduleDragStartX = 0;
  let participationScheduleDragStartY = 0;
  let participationScheduleDragOriginX = 0;
  let participationScheduleDragOriginY = 0;
  let participationScheduleResizePointer = -1;
  let participationScheduleResizeStartX = 0;
  let participationScheduleResizeStartY = 0;
  let participationScheduleResizeStartWidth = 600;
  let participationScheduleResizeStartHeight = 440;
  let participationScheduleResizeTarget: HTMLElement | null = null;
  let sidebarActivities: SidebarActivity[] = [];
  let participationOverview: ParticipationOverview = { join_count: 0, win_count: 0, win_diamonds: 0 };
  let recentActivityScroller: HTMLDivElement | null = null;
  let recentActivityScrollTop = 0;
  let recentActivityClientHeight = 0;
  let recentActivityScrollHeight = 0;
  let recentActivityScrollbarDragPointer = -1;
  let recentActivityScrollbarDragStartY = 0;
  let recentActivityScrollbarDragStartTop = 0;
  let recentActivityScrollbarTrack: HTMLDivElement | null = null;
  let settingsModalScroller: HTMLDivElement | null = null;
  let settingsModalScrollTop = 0;
  let settingsModalClientHeight = 0;
  let settingsModalScrollHeight = 0;
  let settingsModalScrollbarDragPointer = -1;
  let settingsModalScrollbarDragStartY = 0;
  let settingsModalScrollbarDragStartTop = 0;
  let settingsModalScrollbarTrack: HTMLDivElement | null = null;
  let sidebarActivityDetailID = "";
  let stoppingSidebarActivityID = "";
  $: licenseDaysRemaining = getLicenseDaysRemaining(licenseStatus.expires_at);
  let query = "";
  let searchOpen = false;
  let topbarSearchInput: HTMLInputElement;
  let toast = "";
  let toastTimer: number | undefined;
  let refreshing = false;
  let instanceModalOpen = false;
  let browserInstances: BrowserInstance[] = [];
  let browserInventoryLoaded = false;
  let browserCapacity: BrowserCapacity | null = null;
  let browserLoading = false;
  let browserStatusPolling = false;
  let browserCookieSyncing = false;
  let browserCookieCheckingIds: string[] = [];
  const browserCookieCheckedAt = new Map<string, number>();
  let browserError = "";
  let browserCreating = false;
  let browserOpeningId = "";
  let browserClosingId = "";
  let browserRedPacketPreparingIds: string[] = [];
  let browserRedPacketContextIds: string[] = [];
  let browserParticipationContexts: Record<string, BrowserParticipationContext> = {};
  let browserPendingClose: BrowserInstance | null = null;
  let browserFollowingLive: Record<string, FollowingLiveResult> = {};
  let browserFollowingLiveLoadingIds: string[] = [];
  let browserFollowingLivePendingNativeIds: string[] = [];
  let browserFollowingLiveErrors: Record<string, string> = {};
  let followingLiveModalInstance: BrowserInstance | null = null;
  let browserWebviewMountingIds: string[] = [];
  let browserWebviewLoadingIds: string[] = [];
  // Keep a lightweight ready marker after the native surface is released so
  // returning to this view can describe the operation as a restore. The real
  // WebView is still destroyed off-screen to release its runtime lease.
  let browserWebviewReadyIds: string[] = [];
  let browserIndependentWindowIds: string[] = [];
  let browserWebviewMountedIds: string[] = [];
  let browserWebviewReleasingIds: string[] = [];
  const browserWebviewMountConcurrency = 2;
  let browserWebviewErrors: Record<string, string> = {};
  let browserWebviewSyncFrame = 0;
  let browserViewSettled = false;
  let browserColumns = 2;
  let browserColumnSyncTimer = 0;
  let browserNativeLayoutChain: Promise<void> = Promise.resolve();
  let browserLayoutRevision = 0;
  let browserLayoutChanging = false;
  let selectedParticipationAccountIds: string[] = [];
  let instanceParticipationGroupFilter: ParticipationGroupFilter = "all";
  let instanceAccountsRefreshing = false;
  let managementTab: ManagementTab = "redpackets";
  let monitoringManagementExpanded = false;
  let rooms: RoomItem[] = [];
  let roomTotalCount = 0;
  let roomsLoading = false;
  let roomsMigrating = false;
  let roomError = "";
  let recycledRooms: RoomItem[] = [];
  let centerExcludedRooms: CenterExclusionItem[] = [];
  let roomRecycleModalOpen = false;
  let roomRecycleView: "recycle" | "center-exclusions" = "recycle";
  let roomRecycleLoading = false;
  let roomRecycleBusyId = "";
  let roomRecycleError = "";
  let roomPendingPermanentDelete: RoomItem | null = null;
  let roomRenderLimit = 300;
  let roomImportModalOpen = false;
  let roomImportText = "";
  let roomImportBusy = false;
  let roomImportCompleted = 0;
  let roomImportTotal = 0;
  let roomImportFileInput: HTMLInputElement;
  let redPacketMonitors: RedPacketMonitor[] = [];
  let redPacketMonitorSummary: RedPacketMonitorSummary = { total: 0, enabled: 0, running: 0, first_checked: 0, pending_first: 0, live_running: 0, errors: 0 };
  let redPacketMonitorOverrides: Record<string, { status: string; connectionStatus: string }> = {};
  let redPacketMonitorListRequestSeq = 0;
  let redPacketMonitorsLoading = false;
  let redPacketMonitorError = "";
  let roomSearchTimer = 0;
  let redPacketEvents: RedPacketEvent[] = [];
  let redPacketEventsInitialized = false;
  let redPacketEventsLoading = false;
  let redPacketEventError = "";
  let redPacketRenderLimit = 300;
  let participationRecords: ParticipationRecord[] = [];
  let participationRuntimeLogs: ParticipationTrace[] = [];
  let participationRecordsLoading = false;
  let participationRecordError = "";
  let participationRecordRenderLimit = 300;
  let redPacketClock = Date.now();
  let redPacketHistoryVisible = false;
  let roomSortMode: RoomSortMode = "default";
  let roomSortMenuOpen = false;
  let roomSourceFilter: RoomSourceFilter = "all";
  let roomSourceMenuOpen = false;
  let roomCleanupMenuOpen = false;
  let roomCleanupSettingsBusy = false;
  let roomCleanupSettingsError = "";
  let roomCleanupProgress: RoomCleanupProgress | null = null;
  let roomCleanupProcessed = 0;
  let roomCleanupTotal = 0;
  let redPacketBatchAction: "start" | "stop" | "" = "";
  let redPacketMonitorActionId = "";
  let monitorRuntimeLogs: MonitorRuntimeLog[] = [];
  let accountRole: AccountRole = "participation";
  let accounts: AccountItem[] = [];
  let participationGroups: ParticipationGroup[] = [];
  let accountsLoading = false;
  let accountsMigrating = false;
  let accountError = "";
  let accountStatusFilter: AccountStatusFilter = "all";
  let participationGroupFilter: ParticipationGroupFilter = "all";
  let participationGroupMenuOpen = false;
  let participationGroupDraft = "";
  let participationGroupCreating = false;
  let accountGroupMenuId = "";
  let importParticipationGroupId = "";
  let importGroupMenuOpen = false;
  let accountCreateGroupId = "";
  let statusMenuOpen = false;
  let accountSortMenuOpen = false;
  let monitoringAccountSortMode: MonitoringAccountSortMode = "default";
  let participationAccountSortMode: ParticipationAccountSortMode = "default";
  let importMenuOpen = false;
  let accountFileInput: HTMLInputElement;
  let accountFolderInput: HTMLInputElement;
  let accountPendingDelete: AccountItem | null = null;
  let accountDeleting = false;
	let redPacketAPITogglingAccountIds: string[] = [];
  let accountRebinding: AccountItem | null = null;
  let accountCreateSessionId = "";
  let accountCreateRole: AccountRole = "participation";
  let accountImportBusy = false;
  let accountRebindOpeningId = "";
  let accountRebindCompleting = false;
  let accountRebindViewport: HTMLDivElement;
  let accountRebindSyncFrame = 0;
  let cookieValidatingAccountIds: string[] = [];
  let engineListenerReady = false;
  let sidebarWidth = 232;
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

  $: recentActivityItems = sidebarActivities.map((activity) => ({
		id: activity.id,
		kind: activity.kind,
		label: activity.label,
		accountIDs: activity.account_ids ?? [],
		accountSummaries: activity.account_summaries ?? [],
		active: Boolean(activity.active),
		joinCount: activity.join_count ?? 0,
		winCount: activity.win_count ?? 0,
		winDiamonds: activity.win_diamonds ?? 0,
		finishedAt: activity.finished_at,
		endReason: activity.end_reason,
		stoppedAt: activity.stopped_at,
		createdAt: activity.created_at,
		time: formatMonitorTime(activity.finished_at || activity.stopped_at || activity.created_at, redPacketClock),
		icon: activity.kind.startsWith("participation_") ? (activity.kind.includes("schedule") ? ClockCountdown : Gift) : Radio,
		tone: activity.kind.startsWith("participation_") ? "live" : "neutral",
		view: activity.kind.startsWith("participation_") ? ("browsers" as NavKey) : ("accounts" as NavKey),
	}));
  $: sidebarActivityDetail = recentActivityItems.find((activity) => activity.id === sidebarActivityDetailID);
  $: filteredRooms = rooms.filter((room) => {
    if (!roomMatchesSourceFilter(room, roomSourceFilter)) return false;
    const followAccounts = (room.follow_sources ?? []).map((source) => source.account_name).join(" ");
    const haystack = `${room.name ?? ""} ${room.streamer_name ?? ""} ${room.web_rid ?? ""} ${room.actual_room_id ?? ""} ${room.id} ${followAccounts}`.toLowerCase();
    return haystack.includes(query.trim().toLowerCase());
  });
  $: sortedRooms = filteredRooms
    .map((room, index) => ({ room, index }))
    .sort((left, right) => {
      if (roomSortMode === "instance-first") {
        const instanceDifference = Number(!roomWasDiscoveredByInstance(left.room))
          - Number(!roomWasDiscoveredByInstance(right.room));
        const currentLiveDifference = Number(!roomIsFollowingLive(left.room, redPacketClock))
          - Number(!roomIsFollowingLive(right.room, redPacketClock));
        const recentDifference = roomFollowingLiveTimestamp(right.room) - roomFollowingLiveTimestamp(left.room);
        return instanceDifference || currentLiveDifference || recentDifference || left.index - right.index;
      }
      if (roomSortMode === "live-first") {
        const liveDifference = Number(!roomIsCurrentlyLive(left.room, redPacketMonitors, redPacketMonitorOverrides, redPacketClock))
          - Number(!roomIsCurrentlyLive(right.room, redPacketMonitors, redPacketMonitorOverrides, redPacketClock));
        return liveDifference || left.index - right.index;
      }
      if (roomSortMode === "recent-live") {
        const recentDifference = roomLastLiveStartedAt(right.room, redPacketMonitors)
          - roomLastLiveStartedAt(left.room, redPacketMonitors);
        return recentDifference || left.index - right.index;
      }
      if (roomSortMode === "recent-redpacket") {
        const recentDifference = roomLastRedPacketAt(right.room, redPacketMonitors, redPacketEvents)
          - roomLastRedPacketAt(left.room, redPacketMonitors, redPacketEvents);
        return recentDifference || left.index - right.index;
      }
      return left.index - right.index;
    })
    .map(({ room }) => room);
  $: visibleRooms = sortedRooms.slice(0, roomRenderLimit);
  $: enabledRedPacketMonitors = redPacketMonitors.filter((monitor) => monitor.enabled);
  $: runningRedPacketMonitorCount = redPacketMonitorSummary.running;
  $: liveRunningRedPacketMonitorCount = redPacketMonitorSummary.live_running;
  $: canStartAnyRedPacketMonitor = redPacketMonitorSummary.enabled > redPacketMonitorSummary.running;
  $: canStopAnyRedPacketMonitor = runningRedPacketMonitorCount > 0;
	$: activeRedPacketCount = redPacketEvents.filter((event) => redPacketEventInLibraryWindow(event, redPacketClock)).length;
	$: historicalRedPacketCount = redPacketEvents.length - activeRedPacketCount;
	$: accountSubtitle = `${activeRedPacketCount} 个红包 · ${runningRedPacketMonitorCount} 个房间正在监测${runningRedPacketMonitorCount > 0 ? ` · ${redPacketMonitorSummary.first_checked} 个已完成首轮检测 · ${redPacketMonitorSummary.pending_first} 个待检测 · ${liveRunningRedPacketMonitorCount} 个正在直播` : ""} · ${participationAccounts.length} 个参与 · ${monitoringAccounts.length} 个监测`;
	$: expiredParticipationAccountCount = participationAccounts.filter((account) => accountCookieStatus(account, "participation") === "expired").length;
	$: expiredMonitoringAccountCount = monitoringAccounts.filter((account) => accountCookieStatus(account, "monitoring") === "expired").length;
	$: scopedRedPacketEvents = redPacketEvents.filter((event) => redPacketHistoryVisible
		? !redPacketEventInLibraryWindow(event, redPacketClock)
		: redPacketEventInLibraryWindow(event, redPacketClock));
  $: filteredRedPacketEvents = scopedRedPacketEvents.filter((event) => {
    const needle = query.trim().toLowerCase();
    if (!needle) return true;
    return [event.title, event.prize, event.room_name, event.streamer_name, event.web_rid, event.room_id]
      .filter(Boolean)
      .some((value) => String(value).toLowerCase().includes(needle));
  });
  $: visibleRedPacketEvents = filteredRedPacketEvents.slice(0, redPacketRenderLimit);
  $: filteredParticipationRecords = participationRecords.filter((record) => {
    const needle = query.trim().toLowerCase();
    if (!needle) return true;
    return [record.account_name, record.room_name, record.streamer_name, record.web_rid, record.room_id, record.title, record.prize, record.message]
      .filter(Boolean)
      .some((value) => String(value).toLowerCase().includes(needle));
  });
  $: visibleParticipationRecords = filteredParticipationRecords.slice(0, participationRecordRenderLimit);
  $: browserSubtitle = `${browserInstances.length} 个实例 · ${browserInstances.filter((item) => item.status === "online").length} 个在线 · ${browserInstances.filter(browserCookieExpired).length} 个失效`;
  // A persisted task flag alone is not evidence that native participation is
  // running. The account must still own a prepared native browser context in
  // this process; this also keeps older engines from producing a stale banner
  // after a desktop restart.
  $: activeBrowserParticipationContexts = Object.values(browserParticipationContexts).filter((context) => context.active && context.prepared);
  $: browserParticipationRuntime = {
    accounts: activeBrowserParticipationContexts.length,
    prepared: activeBrowserParticipationContexts.filter((context) => context.prepared).length,
    accepting: activeBrowserParticipationContexts.filter((context) => context.accepting).length,
    joined: activeBrowserParticipationContexts.reduce((total, context) => total + Math.max(0, context.join_count || 0), 0),
    pending: activeBrowserParticipationContexts.reduce((total, context) => total + Math.max(0, context.pending_draw_count || 0), 0),
    won: activeBrowserParticipationContexts.reduce((total, context) => total + Math.max(0, context.win_count || 0), 0),
  };
  $: browserParticipationTaskRunning = browserParticipationRuntime.accounts > 0;
  $: browserInstanceCreateLimitReached = licenseStatus.state !== "active" && browserInstances.length >= 1;
  $: visibleBrowserInstances = browserInstances.filter((item) => {
    const haystack = `${item.name} ${item.account_name} ${item.browser}`.toLowerCase();
    return haystack.includes(query.trim().toLowerCase());
  });
  $: browserWebviewLayoutKey = `${activeView}:${browserViewSettled}:${instanceModalOpen}:${licenseModalOpen}:${participationSettingsModalOpen}:${participationScheduleModalOpen}:${sidebarActivityDetailID}:${participationTaskMenuOpen}:${sidebarCollapsed}:${query}:${visibleBrowserInstances
    .map((instance) => instance.id)
    .join(",")}`;
  $: if (browserWebviewLayoutKey) scheduleEmbeddedBrowserSync();
  $: eligibleParticipationAccounts = participationAccounts.filter(
    (account) =>
      account.participation?.enabled &&
      !browserInstances.some((instance) => instance.account_id === account.id),
  );
  $: selectableParticipationAccounts = eligibleParticipationAccounts.filter((account) =>
    participationGroupMatches(account, instanceParticipationGroupFilter),
  );
  $: visibleAccounts = (accountRole === "monitoring" ? monitoringAccounts : participationAccounts)
    .filter((account) => {
      const haystack = `${account.name} ${account.nickname ?? ""} ${account.user_id ?? ""}`.toLowerCase();
      const searchMatches = haystack.includes(query.trim().toLowerCase());
      const status = accountStatus(account, redPacketClock);
      const statusMatches =
        accountStatusFilter === "all" ||
        (accountStatusFilter === "available" && status === "可用") ||
        (accountStatusFilter === "expired" && status === "CK 失效") ||
        (accountStatusFilter === "cooldown" && status === "冷却中");
      const groupMatches = accountRole !== "participation" || participationGroupMatches(account, participationGroupFilter);
      return searchMatches && statusMatches && groupMatches;
    })
    .map((account, index) => ({ account, index }))
    .sort((left, right) => accountSortDifference(left.account, right.account) || left.index - right.index)
    .map(({ account }) => account);

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
      }, method.startsWith("account.import_") ? 60000 : method.startsWith("license.") || method.startsWith("remote_sync.") ? 35000 : 12000);
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

  let roomListUsesLegacyRPC = false;
  let redPacketMonitorListUsesLegacyRPC = false;

  function engineMethodIsUnavailable(error: unknown, method: string) {
    const message = error instanceof Error ? error.message : String(error);
    return message.includes(`尚未实现方法：${method}`)
      || message.includes(`method not implemented: ${method}`)
      || message.includes(`unimplemented method: ${method}`);
  }

  function roomMatchesSourceFilter(room: RoomItem, source: RoomSourceFilter) {
    if (source === "all") return true;
    const following = room.source === "following-live" || (room.follow_sources?.length ?? 0) > 0;
    if (source === "following") return following;
    if (source === "center") return !following && room.source === "center";
    return !following && room.source !== "center";
  }

  function legacyRoomPage(items: RoomItem[], offset: number, limit: number, search: string, source: RoomSourceFilter): RoomPage {
    const needle = search.trim().toLowerCase();
    const filtered = items.filter((room) => {
      if (room.recycled) return false;
      if (!roomMatchesSourceFilter(room, source)) return false;
      if (!needle) return true;
      const haystack = [room.name, room.streamer_name, room.web_rid, room.actual_room_id, room.id]
        .filter(Boolean)
        .join(" ")
        .toLowerCase();
      return haystack.includes(needle);
    });
    const safeOffset = Math.max(0, offset);
    const safeLimit = Math.max(1, limit);
    return {
      items: filtered.slice(safeOffset, safeOffset + safeLimit),
      total: filtered.length,
    };
  }

  async function loadRoomPage(offset: number, limit: number, search: string, source: RoomSourceFilter): Promise<RoomPage> {
    if (!roomListUsesLegacyRPC) {
      try {
        return await engineRequest<RoomPage>("room.list_page", {
          offset,
          limit,
          query: search,
          source: source === "all" ? "" : source,
        });
      } catch (error) {
        if (!engineMethodIsUnavailable(error, "room.list_page")) throw error;
        roomListUsesLegacyRPC = true;
      }
    }

    const items = await engineRequest<RoomItem[]>("room.list");
    return legacyRoomPage(items, offset, limit, search, source);
  }

  function legacyRedPacketMonitorPage(items: RedPacketMonitor[], roomIDs: string[]): RedPacketMonitorPage {
    const wanted = new Set(roomIDs);
    const summary: RedPacketMonitorSummary = {
      total: items.length,
      enabled: 0,
      running: 0,
      first_checked: 0,
      pending_first: 0,
      live_running: 0,
      errors: 0,
    };
    for (const monitor of items) {
      if (monitor.enabled) summary.enabled += 1;
      if (monitor.status === "running") {
        summary.running += 1;
        if (monitor.connection_status === "connecting") summary.pending_first += 1;
        else summary.first_checked += 1;
        if (monitor.live_status === "live") summary.live_running += 1;
      }
      if (monitor.status === "error" || monitor.connection_status === "error") summary.errors += 1;
    }
    return {
      items: items.filter((monitor) => wanted.has(monitor.room_id)),
      summary,
    };
  }

  async function loadRedPacketMonitorPage(roomIDs: string[]): Promise<RedPacketMonitorPage> {
    if (!redPacketMonitorListUsesLegacyRPC) {
      try {
        return await engineRequest<RedPacketMonitorPage>("red_packet_monitor.list_page", { room_ids: roomIDs });
      } catch (error) {
        if (!engineMethodIsUnavailable(error, "red_packet_monitor.list_page")) throw error;
        redPacketMonitorListUsesLegacyRPC = true;
      }
    }

    const items = await engineRequest<RedPacketMonitor[]>("red_packet_monitor.list");
    return legacyRedPacketMonitorPage(items, roomIDs);
  }

  async function loadLicenseStatus() {
    if (!engineListenerReady) return;
    try {
      licenseStatus = await engineRequest<LicenseStatus>("license.status");
      reconcileRoomSourceLicense();
    } catch (error) {
      licenseError = error instanceof Error ? error.message : String(error);
    }
  }

  async function loadRemoteSyncStatus(reportError = true) {
    if (!engineListenerReady) return;
    try {
      remoteSyncStatus = await engineRequest<RemoteSyncStatus>("remote_sync.status");
      remoteSyncStatusLoaded = true;
    } catch (error) {
      if (reportError) remoteSyncError = error instanceof Error ? error.message : String(error);
    }
  }

  async function openLicenseModal() {
    await hideEmbeddedBrowsers();
    licenseError = "";
    remoteSyncError = "";
    remoteSyncToken = "";
    remoteSyncEditing = false;
    licenseModalOpen = true;
    void loadLicenseStatus();
    void loadRemoteSyncStatus();
  }

  function closeLicenseModal() {
    if (licenseBusy || remoteSyncBusy) return;
    licenseModalOpen = false;
    licenseKey = "";
    licenseError = "";
    licenseReplacing = false;
    remoteSyncToken = "";
    remoteSyncError = "";
    remoteSyncEditing = false;
    scheduleEmbeddedBrowserSync();
  }

  async function loadSidebarActivities() {
	if (!engineListenerReady) return;
	const [activities, overview] = await Promise.allSettled([
		engineRequest<SidebarActivity[]>("activity.list"),
		engineRequest<ParticipationOverview>("red_packet_participation.overview"),
	]);
	if (activities.status === "fulfilled") sidebarActivities = activities.value;
	if (overview.status === "fulfilled") participationOverview = overview.value;
  }

  function formatParticipationDiamondTotal(value: number) {
	if (!Number.isFinite(value) || value <= 0) return "0";
	return Number.isInteger(value) ? String(value) : value.toFixed(2).replace(/0+$/, "").replace(/\.$/, "");
  }

  function syncRecentActivityScrollbar() {
	if (!recentActivityScroller) return;
	recentActivityScrollTop = recentActivityScroller.scrollTop;
	recentActivityClientHeight = recentActivityScroller.clientHeight;
	recentActivityScrollHeight = recentActivityScroller.scrollHeight;
  }

  function observeRecentActivityScroller(node: HTMLDivElement) {
	recentActivityScroller = node;
	const resizeObserver = new ResizeObserver(syncRecentActivityScrollbar);
	const mutationObserver = new MutationObserver(syncRecentActivityScrollbar);
	resizeObserver.observe(node);
	mutationObserver.observe(node, { childList: true, subtree: true, characterData: true });
	window.requestAnimationFrame(syncRecentActivityScrollbar);
	return {
		destroy() {
			resizeObserver.disconnect();
			mutationObserver.disconnect();
			if (recentActivityScroller === node) recentActivityScroller = null;
		},
	};
  }

  function scrollRecentActivityFromTrack(event: MouseEvent) {
	if (!recentActivityScroller || !recentActivityScrollbarTrack || event.target !== event.currentTarget) return;
	const bounds = recentActivityScrollbarTrack.getBoundingClientRect();
	const maxScroll = recentActivityScrollHeight - recentActivityClientHeight;
	if (bounds.height <= 0 || maxScroll <= 0) return;
	const thumbHeight = Math.max(42, bounds.height * recentActivityClientHeight / recentActivityScrollHeight);
	const available = Math.max(1, bounds.height - thumbHeight);
	const target = Math.max(0, Math.min(available, event.clientY - bounds.top - thumbHeight / 2));
	recentActivityScroller.scrollTop = target / available * maxScroll;
  }

  function startRecentActivityScrollbarDrag(event: PointerEvent) {
	if (event.button !== 0 || !recentActivityScroller) return;
	event.preventDefault();
	event.stopPropagation();
	recentActivityScrollbarDragPointer = event.pointerId;
	recentActivityScrollbarDragStartY = event.clientY;
	recentActivityScrollbarDragStartTop = recentActivityScroller.scrollTop;
	window.addEventListener("pointermove", moveRecentActivityScrollbarDrag);
	window.addEventListener("pointerup", endRecentActivityScrollbarDrag);
	window.addEventListener("pointercancel", endRecentActivityScrollbarDrag);
  }

  function moveRecentActivityScrollbarDrag(event: PointerEvent) {
	if (event.pointerId !== recentActivityScrollbarDragPointer || !recentActivityScroller || !recentActivityScrollbarTrack) return;
	const trackHeight = recentActivityScrollbarTrack.clientHeight;
	const thumbHeight = Math.max(42, trackHeight * recentActivityClientHeight / recentActivityScrollHeight);
	const available = Math.max(1, trackHeight - thumbHeight);
	const maxScroll = Math.max(0, recentActivityScrollHeight - recentActivityClientHeight);
	recentActivityScroller.scrollTop = Math.max(0, Math.min(maxScroll,
		recentActivityScrollbarDragStartTop + (event.clientY - recentActivityScrollbarDragStartY) / available * maxScroll));
  }

  function endRecentActivityScrollbarDrag(event: PointerEvent) {
	if (event.pointerId !== recentActivityScrollbarDragPointer) return;
	recentActivityScrollbarDragPointer = -1;
	window.removeEventListener("pointermove", moveRecentActivityScrollbarDrag);
	window.removeEventListener("pointerup", endRecentActivityScrollbarDrag);
	window.removeEventListener("pointercancel", endRecentActivityScrollbarDrag);
  }

  function syncSettingsModalScrollbar() {
	if (!settingsModalScroller) return;
	settingsModalScrollTop = settingsModalScroller.scrollTop;
	settingsModalClientHeight = settingsModalScroller.clientHeight;
	settingsModalScrollHeight = settingsModalScroller.scrollHeight;
  }

  function observeSettingsModalScroller(node: HTMLDivElement) {
	settingsModalScroller = node;
	const resizeObserver = new ResizeObserver(syncSettingsModalScrollbar);
	const mutationObserver = new MutationObserver(syncSettingsModalScrollbar);
	resizeObserver.observe(node);
	mutationObserver.observe(node, { childList: true, subtree: true, characterData: true });
	window.requestAnimationFrame(syncSettingsModalScrollbar);
	return {
		destroy() {
			resizeObserver.disconnect();
			mutationObserver.disconnect();
			if (settingsModalScroller === node) settingsModalScroller = null;
		},
	};
  }

  function scrollSettingsModalFromTrack(event: MouseEvent) {
	if (!settingsModalScroller || !settingsModalScrollbarTrack || event.target !== event.currentTarget) return;
	const bounds = settingsModalScrollbarTrack.getBoundingClientRect();
	const maxScroll = settingsModalScrollHeight - settingsModalClientHeight;
	if (bounds.height <= 0 || maxScroll <= 0) return;
	const thumbHeight = Math.max(42, bounds.height * settingsModalClientHeight / settingsModalScrollHeight);
	const available = Math.max(1, bounds.height - thumbHeight);
	const target = Math.max(0, Math.min(available, event.clientY - bounds.top - thumbHeight / 2));
	settingsModalScroller.scrollTop = target / available * maxScroll;
  }

  function startSettingsModalScrollbarDrag(event: PointerEvent) {
	if (event.button !== 0 || !settingsModalScroller) return;
	event.preventDefault();
	event.stopPropagation();
	settingsModalScrollbarDragPointer = event.pointerId;
	settingsModalScrollbarDragStartY = event.clientY;
	settingsModalScrollbarDragStartTop = settingsModalScroller.scrollTop;
	window.addEventListener("pointermove", moveSettingsModalScrollbarDrag);
	window.addEventListener("pointerup", endSettingsModalScrollbarDrag);
	window.addEventListener("pointercancel", endSettingsModalScrollbarDrag);
  }

  function moveSettingsModalScrollbarDrag(event: PointerEvent) {
	if (event.pointerId !== settingsModalScrollbarDragPointer || !settingsModalScroller || !settingsModalScrollbarTrack) return;
	const trackHeight = settingsModalScrollbarTrack.clientHeight;
	const thumbHeight = Math.max(42, trackHeight * settingsModalClientHeight / settingsModalScrollHeight);
	const available = Math.max(1, trackHeight - thumbHeight);
	const maxScroll = Math.max(0, settingsModalScrollHeight - settingsModalClientHeight);
	settingsModalScroller.scrollTop = Math.max(0, Math.min(maxScroll,
		settingsModalScrollbarDragStartTop + (event.clientY - settingsModalScrollbarDragStartY) / available * maxScroll));
  }

  function endSettingsModalScrollbarDrag(event: PointerEvent) {
	if (event.pointerId !== settingsModalScrollbarDragPointer) return;
	settingsModalScrollbarDragPointer = -1;
	window.removeEventListener("pointermove", moveSettingsModalScrollbarDrag);
	window.removeEventListener("pointerup", endSettingsModalScrollbarDrag);
	window.removeEventListener("pointercancel", endSettingsModalScrollbarDrag);
  }

  function sidebarActivityAccountName(accountID: string) {
	const account = accounts.find((item) => item.id === accountID);
	return account?.nickname || account?.name || account?.user_id || `账号 ${accountID.slice(0, 8)}`;
  }

  function sidebarActivityAccountSummary(activity: { accountSummaries: ActivityAccountSummary[] } | undefined, accountID: string) {
	return activity?.accountSummaries.find((summary) => summary.account_id === accountID);
  }

  function sidebarActivityAccountState(accountID: string, activityActive: boolean, stoppedAt?: string) {
	if (stoppedAt) return "已停止";
	const instance = browserInstances.find((item) => item.account_id === accountID);
	const context = instance ? browserParticipationContexts[instance.id] : undefined;
	if (activityActive && instance && ((context?.active && context?.prepared) || context?.accepting || browserRedPacketContextIds.includes(instance.id))) {
		return "参与中";
	}
	return activityActive ? "等待红包" : "已结束";
  }

  async function openSidebarActivity(activityID: string) {
	browserLayoutRevision += 1;
	await queueBrowserNativeLayout(hideEmbeddedBrowsers);
	sidebarActivityDetailID = activityID;
  }

  async function closeSidebarActivity() {
	sidebarActivityDetailID = "";
	browserLayoutRevision += 1;
	const revision = browserLayoutRevision;
	await tick();
	await new Promise<void>((resolve) => window.requestAnimationFrame(() => resolve()));
	await queueBrowserNativeLayout(() => syncEmbeddedBrowsers(revision));
  }

  async function stopSidebarParticipationBatch(activityID: string, accountIDs: string[]) {
	if (!isTauriDesktop() || stoppingSidebarActivityID) return;
	stoppingSidebarActivityID = activityID;
	try {
		await engineRequest<{ account_ids: string[] }>("activity.stop_participation_batch", { activity_id: activityID });
		const instanceIDs = browserInstances
			.filter((instance) => accountIDs.includes(instance.account_id))
			.map((instance) => instance.id);
		await Promise.all(instanceIDs.map((instanceId) =>
			invoke<void>("stop_browser_red_packet_context", { instanceId }).catch(() => undefined),
		));
		browserRedPacketContextIds = browserRedPacketContextIds.filter((instanceID) => !instanceIDs.includes(instanceID));
		await Promise.all([loadAccounts(false), loadBrowserParticipationContexts(), loadSidebarActivities()]);
		showToast(`已停止本批次 ${accountIDs.length} 个账号的后续红包参与`);
	} catch (error) {
		showToast(error instanceof Error ? error.message : String(error));
	} finally {
		stoppingSidebarActivityID = "";
	}
  }

  async function openParticipationSettings() {
	participationSettingsError = "";
	participationSettingsModalOpen = true;
	// Publish the modal guard before issuing native hide commands. A WebView
	// mount that was already in flight can otherwise finish in the small gap
	// after hideEmbeddedBrowsers and paint above the newly opened dialog.
	await hideEmbeddedBrowsers();
	if (!isTauriDesktop()) return;
	participationSettingsBusy = true;
	try {
		const [loadedParticipationSettings, loadedRoomSettings, loadedMonitoringSettings] = await Promise.all([
			engineRequest<ParticipationSettings>("red_packet_participation.settings"),
			engineRequest<RoomSettings>("room.settings"),
			engineRequest<MonitoringSettings>("red_packet_monitor.settings"),
		]);
		participationSettings = {
			...loadedParticipationSettings,
			draw_result_delay_seconds: Number.isFinite(loadedParticipationSettings.draw_result_delay_seconds)
				? loadedParticipationSettings.draw_result_delay_seconds
				: 1,
			draw_result_max_attempts: Number.isFinite(loadedParticipationSettings.draw_result_max_attempts)
				? loadedParticipationSettings.draw_result_max_attempts
				: 3,
			participation_countdown_seconds: Number.isFinite(loadedParticipationSettings.participation_countdown_seconds)
				? loadedParticipationSettings.participation_countdown_seconds
				: 10,
		};
		roomSettings = loadedRoomSettings;
		monitoringSettings = loadedMonitoringSettings;
	} catch (error) {
		participationSettingsError = error instanceof Error ? error.message : String(error);
	} finally {
		participationSettingsBusy = false;
	}
  }

  function closeParticipationSettings() {
	if (participationSettingsBusy) return;
	participationSettingsModalOpen = false;
	participationSettingsError = "";
	scheduleEmbeddedBrowserSync();
  }

  function normalizedParticipationSetting(value: number, maximum: number) {
	const parsed = Number(value);
	if (!Number.isFinite(parsed)) return 0;
	return Math.max(0, Math.min(maximum, Math.trunc(parsed)));
  }

  async function saveParticipationSettings() {
	if (participationSettingsBusy) return;
	participationSettingsBusy = true;
	participationSettingsError = "";
	if (settingsTab === "monitoring") {
		const nextMonitoringSettings = {
			global_request_interval_ms: Math.max(40, Math.min(2000, normalizedParticipationSetting(monitoringSettings.global_request_interval_ms, 2000))),
			account_request_interval_ms: Math.max(250, Math.min(5000, normalizedParticipationSetting(monitoringSettings.account_request_interval_ms, 5000))),
			global_concurrency: Math.max(1, Math.min(128, normalizedParticipationSetting(monitoringSettings.global_concurrency, 128))),
			account_concurrency: Math.max(1, Math.min(8, normalizedParticipationSetting(monitoringSettings.account_concurrency, 8))),
			probe_concurrency: Math.max(8, Math.min(256, normalizedParticipationSetting(monitoringSettings.probe_concurrency, 256))),
		};
		if (!isTauriDesktop()) {
			monitoringSettings = nextMonitoringSettings;
			participationSettingsBusy = false;
			showToast("监测设置已保存并应用");
			closeParticipationSettings();
			return;
		}
		try {
			monitoringSettings = await engineRequest<MonitoringSettings>("red_packet_monitor.set_settings", nextMonitoringSettings);
			showToast("监测设置已保存并应用到运行中的任务");
			participationSettingsBusy = false;
			closeParticipationSettings();
		} catch (error) {
			participationSettingsError = error instanceof Error ? error.message : String(error);
		} finally {
			participationSettingsBusy = false;
		}
		return;
	}
	if (settingsTab === "rooms") {
		const nextRoomSettings = {
			auto_recycle_offline_days: normalizedParticipationSetting(roomSettings.auto_recycle_offline_days, 3650),
			participation_prewarm_minutes: normalizedParticipationSetting(roomSettings.participation_prewarm_minutes, 1440),
			auto_recycle_low_live_enabled: roomSettings.auto_recycle_low_live_enabled,
			auto_recycle_max_live_sessions: normalizedParticipationSetting(roomSettings.auto_recycle_max_live_sessions, 100000),
			auto_recycle_no_packet_enabled: roomSettings.auto_recycle_no_packet_enabled,
			auto_recycle_no_packet_days: normalizedParticipationSetting(roomSettings.auto_recycle_no_packet_days, 3650),
			auto_recycle_imported_no_packet_enabled: roomSettings.auto_recycle_imported_no_packet_enabled,
		};
		if (!isTauriDesktop()) {
			roomSettings = nextRoomSettings;
			participationSettingsBusy = false;
			showToast("直播间设置已保存");
			closeParticipationSettings();
			return;
		}
		try {
			roomSettings = await engineRequest<RoomSettings>("room.set_settings", nextRoomSettings);
			showToast(roomSettings.auto_recycle_offline_days === 0 ? "已关闭直播间自动回收" : "直播间设置已保存");
			participationSettingsBusy = false;
			closeParticipationSettings();
		} catch (error) {
			participationSettingsError = error instanceof Error ? error.message : String(error);
		} finally {
			participationSettingsBusy = false;
		}
		return;
	}
	const next = {
		stop_after_joins: normalizedParticipationSetting(participationSettings.stop_after_joins, 100000),
		cooldown_seconds: normalizedParticipationSetting(participationSettings.cooldown_seconds, 86400),
		stop_after_wins: normalizedParticipationSetting(participationSettings.stop_after_wins, 100000),
		draw_result_delay_seconds: normalizedParticipationSetting(participationSettings.draw_result_delay_seconds, 60),
		draw_result_max_attempts: Math.max(1, normalizedParticipationSetting(participationSettings.draw_result_max_attempts, 20)),
		participation_countdown_seconds: normalizedParticipationSetting(participationSettings.participation_countdown_seconds, 300),
		minimum_diamonds: Math.max(1, normalizedParticipationSetting(participationSettings.minimum_diamonds, 1000000)),
		packet_type: (["all", "gift", "diamond"] as ParticipationPacketType[]).includes(participationSettings.packet_type)
			? participationSettings.packet_type
			: "diamond" as ParticipationPacketType,
		follow_policy: (["all", "follow_priority", "follow_only"] as ParticipationFollowPolicy[]).includes(participationSettings.follow_policy)
			? participationSettings.follow_policy
			: "follow_priority" as ParticipationFollowPolicy,
	};
	if (!isTauriDesktop()) {
		participationSettings = next;
		participationSettingsBusy = false;
		showToast("红包参与设置已保存");
		closeParticipationSettings();
		return;
	}
	try {
		participationSettings = await engineRequest<ParticipationSettings>("red_packet_participation.set_settings", next);
		await loadBrowserParticipationContexts();
		showToast("红包参与设置已保存");
		participationSettingsBusy = false;
		closeParticipationSettings();
	} catch (error) {
		participationSettingsError = error instanceof Error ? error.message : String(error);
	} finally {
		participationSettingsBusy = false;
	}
  }

  async function loadRoomRecycleBin() {
	if (roomRecycleLoading || !isTauriDesktop()) return;
	roomRecycleLoading = true;
	roomRecycleError = "";
	try {
		const [recycled, excluded] = await Promise.all([
			engineRequest<RoomItem[]>("room.recycle_bin"),
			permanentCenterRoomAccess()
				? engineRequest<CenterExclusionItem[]>("room.center_exclusions")
				: Promise.resolve([] as CenterExclusionItem[]),
		]);
		recycledRooms = recycled;
		centerExcludedRooms = excluded;
	} catch (error) {
		roomRecycleError = error instanceof Error ? error.message : String(error);
	} finally {
		roomRecycleLoading = false;
	}
  }

  async function openRoomRecycleBin() {
	await hideEmbeddedBrowsers();
	roomRecycleModalOpen = true;
	roomRecycleView = "recycle";
	roomRecycleError = "";
	await loadRoomRecycleBin();
  }

  function closeRoomRecycleBin() {
	if (roomRecycleBusyId) return;
	roomRecycleModalOpen = false;
	roomRecycleError = "";
	scheduleEmbeddedBrowserSync();
  }

  async function restoreRecycledRoom(room: RoomItem) {
	if (!isTauriDesktop() || roomRecycleBusyId) return;
	roomRecycleBusyId = room.id;
	roomRecycleError = "";
	try {
		await engineRequest<RoomItem>("room.recycle.restore", { room_id: room.id });
		await Promise.all([loadRooms(false), loadRedPacketMonitors(true), loadRoomRecycleBin()]);
		showToast(`已将「${roomDisplayName(room)}」恢复到直播间列表`);
	} catch (error) {
		roomRecycleError = error instanceof Error ? error.message : String(error);
	} finally {
		roomRecycleBusyId = "";
	}
  }

  function requestPermanentDeleteRoom(room: RoomItem) {
	roomPendingPermanentDelete = room;
	}

  async function permanentlyDeleteRecycledRoom() {
	const room = roomPendingPermanentDelete;
	if (!room || !isTauriDesktop() || roomRecycleBusyId) return;
	roomRecycleBusyId = room.id;
	roomRecycleError = "";
	try {
		await engineRequest<{ deleted: boolean }>("room.recycle.delete", { room_id: room.id });
		roomPendingPermanentDelete = null;
		await Promise.all([loadRooms(false), loadRedPacketMonitors(true), loadRoomRecycleBin()]);
		showToast(`已永久删除直播间「${roomDisplayName(room)}」`);
	} catch (error) {
		roomRecycleError = error instanceof Error ? error.message : String(error);
	} finally {
		roomRecycleBusyId = "";
	}
  }

  async function restoreCenterExcludedRoom(room: CenterExclusionItem) {
	if (!isTauriDesktop() || roomRecycleBusyId) return;
	roomRecycleBusyId = room.id;
	roomRecycleError = "";
	try {
		await engineRequest<RoomItem>("room.center_exclusion.restore", { room_id: room.id });
		await Promise.all([loadRooms(false), loadRedPacketMonitors(true), loadRoomRecycleBin()]);
		showToast(`已解除「${room.streamer_name || room.name || room.web_rid}」的中心库排除并恢复直播间`);
	} catch (error) {
		roomRecycleError = error instanceof Error ? error.message : String(error);
	} finally {
		roomRecycleBusyId = "";
	}
  }

  function formatRecycleTime(value?: string) {
	const timestamp = Date.parse(value || "");
	if (!Number.isFinite(timestamp)) return "时间未知";
	return new Intl.DateTimeFormat("zh-CN", {
		month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit", hour12: false,
	}).format(timestamp);
	}

  function localDateTimeInput(value: Date) {
	const offset = value.getTimezoneOffset() * 60_000;
	return new Date(value.getTime() - offset).toISOString().slice(0, 16);
  }

  function closeParticipationTaskMenu() {
	participationTaskMenuOpen = false;
  }

  async function toggleParticipationTaskMenu() {
	if (participationTaskMenuOpen) {
		closeParticipationTaskMenu();
		return;
	}
	importMenuOpen = false;
	statusMenuOpen = false;
	participationTaskMenuOpen = true;
	void loadParticipationSchedules();
  }

  async function loadParticipationSchedules() {
	if (!engineListenerReady) return;
	try {
		participationSchedules = await engineRequest<ParticipationSchedule[]>("red_packet_participation.schedule.list");
	} catch {
		// The task menu remains usable for immediate execution during a transient refresh failure.
	}
  }

  async function openParticipationSchedule(mode: ParticipationScheduleMode) {
	closeParticipationTaskMenu();
	participationScheduleManaging = false;
	participationScheduleMode = mode;
	participationScheduleError = "";
	participationScheduleRunAt = localDateTimeInput(new Date(Date.now() + 60 * 60_000));
	participationScheduleDailyTime = "20:00";
	participationScheduleInterval = 10;
	participationScheduleIntervalUnit = "minutes";
	participationScheduleUnitMenuOpen = false;
	participationScheduleModalX = 0;
	participationScheduleModalY = 0;
	participationScheduleModalWidth = Math.min(600, window.innerWidth - 44);
	participationScheduleModalHeight = Math.min(440, window.innerHeight - 44);
	await hideEmbeddedBrowsers();
	participationScheduleModalOpen = true;
	void loadParticipationSchedules();
  }

  async function openParticipationScheduleManager() {
	closeParticipationTaskMenu();
	participationScheduleManaging = true;
	participationScheduleError = "";
	participationScheduleUnitMenuOpen = false;
	participationScheduleModalX = 0;
	participationScheduleModalY = 0;
	participationScheduleModalWidth = Math.min(600, window.innerWidth - 44);
	participationScheduleModalHeight = Math.min(440, window.innerHeight - 44);
	await hideEmbeddedBrowsers();
	participationScheduleModalOpen = true;
	void loadParticipationSchedules();
  }

  function closeParticipationSchedule() {
	if (participationScheduleBusy) return;
	participationScheduleUnitMenuOpen = false;
	participationScheduleManaging = false;
	participationScheduleModalOpen = false;
	participationScheduleError = "";
	scheduleEmbeddedBrowserSync();
  }

  function startParticipationScheduleDrag(event: PointerEvent) {
	if (event.button !== 0 || (event.target as HTMLElement).closest("button, input, select, textarea, [role='button']")) return;
	participationScheduleDragPointer = event.pointerId;
	participationScheduleDragStartX = event.clientX;
	participationScheduleDragStartY = event.clientY;
	participationScheduleDragOriginX = participationScheduleModalX;
	participationScheduleDragOriginY = participationScheduleModalY;
	(event.currentTarget as HTMLElement).setPointerCapture(event.pointerId);
  }

  function moveParticipationScheduleDrag(event: PointerEvent) {
	if (event.pointerId !== participationScheduleDragPointer || !participationScheduleModalElement) return;
	const rect = participationScheduleModalElement.getBoundingClientRect();
	const inset = 10;
	const maxX = Math.max(0, (window.innerWidth - rect.width) / 2 - inset);
	const maxY = Math.max(0, (window.innerHeight - rect.height) / 2 - inset);
	participationScheduleModalX = Math.max(-maxX, Math.min(maxX, participationScheduleDragOriginX + event.clientX - participationScheduleDragStartX));
	participationScheduleModalY = Math.max(-maxY, Math.min(maxY, participationScheduleDragOriginY + event.clientY - participationScheduleDragStartY));
  }

  function endParticipationScheduleDrag(event: PointerEvent) {
	if (event.pointerId !== participationScheduleDragPointer) return;
	participationScheduleDragPointer = -1;
	const target = event.currentTarget as HTMLElement;
	if (target.hasPointerCapture(event.pointerId)) target.releasePointerCapture(event.pointerId);
  }

  function startParticipationScheduleResize(event: PointerEvent) {
	if (event.button !== 0) return;
	event.preventDefault();
	event.stopPropagation();
	participationScheduleResizePointer = event.pointerId;
	participationScheduleResizeStartX = event.clientX;
	participationScheduleResizeStartY = event.clientY;
	participationScheduleResizeStartWidth = participationScheduleModalWidth;
	participationScheduleResizeStartHeight = participationScheduleModalHeight;
	participationScheduleResizeTarget = event.currentTarget as HTMLElement;
	window.addEventListener("pointermove", moveParticipationScheduleResize);
	window.addEventListener("pointerup", endParticipationScheduleResize);
	window.addEventListener("pointercancel", endParticipationScheduleResize);
  }

  function moveParticipationScheduleResize(event: PointerEvent) {
	if (event.pointerId !== participationScheduleResizePointer) return;
	const minWidth = Math.min(420, window.innerWidth - 44);
	const minHeight = Math.min(300, window.innerHeight - 44);
	const maxWidth = Math.max(minWidth, window.innerWidth - 44 - Math.abs(participationScheduleModalX) * 2);
	const maxHeight = Math.max(minHeight, window.innerHeight - 44 - Math.abs(participationScheduleModalY) * 2);
	participationScheduleModalWidth = Math.max(minWidth, Math.min(maxWidth, participationScheduleResizeStartWidth + event.clientX - participationScheduleResizeStartX));
	participationScheduleModalHeight = Math.max(minHeight, Math.min(maxHeight, participationScheduleResizeStartHeight + event.clientY - participationScheduleResizeStartY));
  }

  function endParticipationScheduleResize(event: PointerEvent) {
	if (event.pointerId !== participationScheduleResizePointer) return;
	participationScheduleResizePointer = -1;
	participationScheduleResizeTarget = null;
	window.removeEventListener("pointermove", moveParticipationScheduleResize);
	window.removeEventListener("pointerup", endParticipationScheduleResize);
	window.removeEventListener("pointercancel", endParticipationScheduleResize);
  }

  function participationScheduleModeLabel(mode: ParticipationScheduleMode) {
	if (mode === "once") return "指定日期";
	if (mode === "daily") return "每天固定时间";
	return "间隔执行";
  }

  function participationScheduleDescription(schedule: ParticipationSchedule) {
	const next = formatLicenseDate(schedule.next_run_at, "待计算");
	if (schedule.mode === "daily") return `每天 ${schedule.daily_time} · 下次 ${next}`;
	if (schedule.mode === "interval") {
		const minutes = Math.round((schedule.interval_seconds || 0) / 60);
		return `每 ${minutes >= 60 && minutes % 60 === 0 ? `${minutes / 60} 小时` : `${minutes} 分钟`} · 下次 ${next}`;
	}
	return `执行时间 ${next}`;
  }

  async function saveParticipationSchedule() {
	if (participationScheduleBusy || !isTauriDesktop()) return;
	participationScheduleBusy = true;
	participationScheduleError = "";
	const intervalBase = Math.max(1, Math.trunc(Number(participationScheduleInterval) || 0));
	const params: Record<string, unknown> = { mode: participationScheduleMode };
	if (participationScheduleMode === "once") {
		const runAt = new Date(participationScheduleRunAt);
		if (!participationScheduleRunAt || !Number.isFinite(runAt.getTime()) || runAt.getTime() <= Date.now()) {
			participationScheduleError = "请选择晚于当前时间的执行日期";
			participationScheduleBusy = false;
			return;
		}
		params.run_at = runAt.toISOString();
	} else if (participationScheduleMode === "daily") {
		params.daily_time = participationScheduleDailyTime;
	} else {
		params.interval_seconds = intervalBase * (participationScheduleIntervalUnit === "hours" ? 3600 : 60);
	}
	try {
		await engineRequest<ParticipationSchedule>("red_packet_participation.schedule.create", params);
		await Promise.all([loadParticipationSchedules(), loadSidebarActivities()]);
		showToast(`${participationScheduleModeLabel(participationScheduleMode)}计划已创建`);
		participationScheduleBusy = false;
		closeParticipationSchedule();
		void claimDueParticipationSchedules();
	} catch (error) {
		participationScheduleError = error instanceof Error ? error.message : String(error);
	} finally {
		participationScheduleBusy = false;
	}
  }

  async function deleteParticipationSchedule(schedule: ParticipationSchedule) {
	if (participationScheduleBusy) return;
	participationScheduleBusy = true;
	try {
		await engineRequest("red_packet_participation.schedule.delete", { schedule_id: schedule.id });
		await Promise.all([loadParticipationSchedules(), loadSidebarActivities()]);
		showToast("执行计划已删除");
	} catch (error) {
		participationScheduleError = error instanceof Error ? error.message : String(error);
	} finally {
		participationScheduleBusy = false;
	}
  }

  async function claimDueParticipationSchedules() {
	if (isDetachedPageWindow || !engineListenerReady || !isTauriDesktop() || participationScheduleClaiming || participationBatchRunning) return;
	participationScheduleClaiming = true;
	try {
		// The browser page may never have been opened in this frontend session.
		// Populate the native instance inventory before atomically claiming a
		// due occurrence so an empty UI array cannot consume a scheduled run.
		if (!browserInventoryLoaded) await loadBrowserInstances();
		if (browserInstances.length === 0) return;
		const executions = await engineRequest<ParticipationScheduleExecution[]>("red_packet_participation.schedule.claim_due");
		for (const execution of executions) {
			await executeParticipationBatch(execution);
		}
		if (executions.length > 0) {
			await Promise.all([loadParticipationSchedules(), loadSidebarActivities()]);
		}
	} catch {
		// A later poll safely retries because Go claims each occurrence atomically.
	} finally {
		participationScheduleClaiming = false;
	}
  }

  function beginLicenseReplacement() {
    licenseKey = "";
    licenseError = "";
    licenseReplacing = true;
  }

  function cancelLicenseReplacement() {
    if (licenseBusy) return;
    licenseKey = "";
    licenseError = "";
    licenseReplacing = false;
  }

  function beginRemoteSyncTokenChange() {
    if (remoteSyncBusy) return;
    remoteSyncToken = "";
    remoteSyncError = "";
    remoteSyncEditing = true;
  }

  function cancelRemoteSyncTokenChange() {
    if (remoteSyncBusy) return;
    remoteSyncToken = "";
    remoteSyncError = "";
    remoteSyncEditing = false;
  }

  function remoteSyncStateLabel(status: RemoteSyncStatus) {
    if (remoteSyncBusy) return "连接中";
    if (status.upload_only) return status.active_endpoint ? "仅上传" : "上传连接中";
    if (!status.enabled) return status.configured ? "接收已停用" : "未配置";
    if (status.active_endpoint) return "已连接";
    if (status.last_error) return "连接异常";
    return "等待连接";
  }

  async function configureRemoteSync(enabled: boolean, enrollmentToken = "") {
    if (remoteSyncBusy) return;
    if (enabled && !remoteSyncStatus.configured && !enrollmentToken.trim()) {
      remoteSyncError = "请输入服务器安装时生成的注册令牌";
      return;
    }
    remoteSyncBusy = true;
    remoteSyncError = "";
    try {
      remoteSyncStatus = await engineRequest<RemoteSyncStatus>("remote_sync.configure", {
        enabled,
        enrollment_token: enrollmentToken.trim(),
      });
      remoteSyncToken = "";
      remoteSyncEditing = false;
      showToast(enabled ? "中心数据接收已启用" : "中心数据接收已停用，本机数据仍会上传");
    } catch (error) {
      remoteSyncError = error instanceof Error ? error.message : String(error);
      await loadRemoteSyncStatus(false);
    } finally {
      remoteSyncBusy = false;
    }
  }

  function saveRemoteSyncToken() {
    void configureRemoteSync(true, remoteSyncToken);
  }

  function formatLicenseDate(value?: string, emptyLabel = "永久有效") {
    const text = value?.trim();
    if (!text) return emptyLabel;
    const timestamp = Date.parse(text);
    if (!Number.isFinite(timestamp)) return text;
    return new Date(timestamp).toLocaleString("zh-CN", { hour12: false });
  }

  function getLicenseDaysRemaining(value?: string) {
    const timestamp = value ? Date.parse(value) : Number.NaN;
    if (!Number.isFinite(timestamp)) return null;
    const remaining = timestamp - Date.now();
    if (remaining <= 0) return 0;
    return Math.ceil(remaining / 86_400_000);
  }

  async function activateLicense() {
    if (!licenseKey.trim() || licenseBusy) return;
    licenseBusy = true;
    licenseError = "";
    try {
      const result = await engineRequest<LicenseOperation>("license.activate", { license_key: licenseKey.trim() });
      licenseStatus = result.status;
      reconcileRoomSourceLicense();
      if (!result.success) {
        licenseError = result.message;
        return;
      }
      licenseKey = "";
      licenseReplacing = false;
      showToast(result.message);
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
      reconcileRoomSourceLicense();
      if (!result.success) licenseError = result.message;
      else {
        showToast(result.message);
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

  function browserCPUUsagePercent(capacity: BrowserCapacity) {
    return Math.round(Math.max(0, Math.min(100, capacity.resources.cpu_usage_percent || 0)));
  }

  function browserMemoryUsagePercent(capacity: BrowserCapacity) {
    const { memory_total_bytes: total, memory_available_bytes: available } = capacity.resources;
    if (!total) return 0;
    return Math.round(Math.max(0, Math.min(100, ((total - Math.min(total, available)) / total) * 100)));
  }

  function browserResourceUsageTooltip(capacity: BrowserCapacity) {
    const { memory_total_bytes: total, memory_available_bytes: available } = capacity.resources;
    const used = total ? total - Math.min(total, available) : 0;
    return `CPU ${browserCPUUsagePercent(capacity)}% · 内存已用 ${formatFileSize(used)} / ${formatFileSize(total)}`;
  }

  function browserRecommendedLimitTooltip(capacity: BrowserCapacity) {
    const cpuCount = Math.max(1, capacity.resources.cpu_count || 1);
    const cpuLimit = Math.max(1, capacity.cpu_recommended_limit || Math.floor(cpuCount * 1.5));
    const memoryLimit = Math.max(1, capacity.memory_recommended_limit || 1);
    const autoLimit = Math.max(1, capacity.maximum_auto_instances || 24);
    const reserve = capacity.memory_reserve_bytes || 0;
    return `CPU：${cpuCount} 核 × 1.5 = ${cpuLimit} · 内存：预留 ${formatFileSize(reserve)}，按 ${formatFileSize(capacity.estimated_per_instance_bytes)}/实例 = ${memoryLimit}\n取 CPU ${cpuLimit}、内存 ${memoryLimit}、自动上限 ${autoLimit} 的最小值，最终建议 ${capacity.recommended_limit}`;
  }

  async function checkForAppUpdate(silent = false, openWhenAvailable = false) {
    if (!isTauriDesktop() || updateChecking) return;
    updateChecking = true;
    if (!silent) updateError = "";
    try {
      if (pendingAppUpdate && !updateDownloaded) {
        try { await pendingAppUpdate.close(); } catch { /* ignore stale updater resources */ }
        pendingAppUpdate = null;
      }
      const update = await checkUpdater();
      if (update) {
        pendingAppUpdate = update;
        const declaredSize = Number(update.rawJson?.size || 0);
        updateStatus = {
          current_version: update.currentVersion || clientVersion,
          latest_version: update.version,
          available: true,
          notes: update.body || "",
          force: false,
          filename: "",
          size: Number.isFinite(declaredSize) && declaredSize > 0 ? declaredSize : 0,
        };
        if (openWhenAvailable) await openUpdateModal();
      } else {
        pendingAppUpdate = null;
        updateStatus = {
          current_version: clientVersion,
          latest_version: clientVersion,
          available: false,
          notes: "",
          force: false,
          filename: "",
          size: 0,
        };
        if (!silent) {
          showToast(`当前 v${clientVersion} 已是最新版本`);
        }
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
    if (updateDownloading || updateDownloaded || !pendingAppUpdate) return;
    updateDownloading = true;
    updateError = "";
    updateProgress = { downloaded: 0, total: updateStatus?.size || 0, percent: 0 };
    try {
      let downloaded = 0;
      let total = updateStatus?.size || 0;
      await pendingAppUpdate.download((event: DownloadEvent) => {
        if (event.event === "Started") {
          downloaded = 0;
          total = event.data.contentLength || total;
        } else if (event.event === "Progress") {
          downloaded += event.data.chunkLength;
        } else if (event.event === "Finished" && total > 0) {
          downloaded = total;
        }
        updateProgress = {
          downloaded,
          total,
          percent: total > 0 ? Math.min(100, Math.round((downloaded / total) * 100)) : event.event === "Finished" ? 100 : 0,
        };
      });
      updateDownloaded = true;
      updateProgress = {
        downloaded: updateProgress.total || updateProgress.downloaded,
        total: updateProgress.total,
        percent: 100,
      };
    } catch (error) {
      updateError = error instanceof Error ? error.message : String(error);
    } finally {
      updateDownloading = false;
    }
  }

  async function installAppUpdate() {
    if (!updateDownloaded || updateInstalling || !pendingAppUpdate) return;
    updateInstalling = true;
    updateError = "";
    try {
      const installedVersion = pendingAppUpdate.version;
      try {
        localStorage.setItem("fubao.desktop.updateReceipt", JSON.stringify({
          target: installedVersion,
          installedAt: Date.now(),
        }));
      } catch { /* update installation must not depend on local storage */ }
      // The Windows installer must replace the bundled Go sidecar. Stop it
      // explicitly and wait for the executable lock to be released before
      // launching NSIS; closing the Tauri window alone only hides it to tray.
      await invoke("prepare_app_update");
      await pendingAppUpdate.install();
      await relaunch();
    } catch (error) {
      // If the installer could not be launched, restore the current engine so
      // the still-running client does not remain in a half-stopped state.
      try { await invoke("engine_restart"); } catch { /* preserve the update error */ }
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

  function redPacketEventIsActive(event: RedPacketEvent, clock = Date.now()) {
    const drawAt = eventTimestamp(event.expires_at || event.draw_at);
    return drawAt > clock;
  }

  // Keep a just-expired packet visible in the current red-packet library for
  // thirty seconds. This is display/history behavior only: native Go
  // participation still rejects packets after their actual deadline.
  function redPacketEventInLibraryWindow(event: RedPacketEvent, clock = Date.now()) {
    const drawAt = eventTimestamp(event.expires_at || event.draw_at);
    return drawAt <= 0 || drawAt > clock - 30_000;
  }

  function redPacketEventIsDiamond(event: RedPacketEvent) {
    return (event.title || "").includes("钻石");
  }

  function redPacketEventExpiryParts(event: RedPacketEvent, clock = Date.now()) {
    const value = event.expires_at || event.draw_at;
    const timestamp = eventTimestamp(value);
    if (!timestamp) return { countdown: "", absolute: "", text: "过期时间待解析", expired: false };
    const absolute = new Date(timestamp).toLocaleString("zh-CN", {
      month: "numeric",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
      hour12: false,
    });
    if (timestamp <= clock) {
      return { countdown: "已过期", absolute, text: `已过期 · ${absolute}`, expired: true };
    }
    const seconds = Math.max(0, Math.ceil((timestamp - clock) / 1000));
    const hours = Math.floor(seconds / 3600);
    const minutes = Math.floor((seconds % 3600) / 60);
    const remainingSeconds = seconds % 60;
    const pad = (value: number) => String(value).padStart(2, "0");
    const countdown = hours > 0
      ? `${pad(hours)}:${pad(minutes)}:${pad(remainingSeconds)}`
      : `${pad(minutes)}:${pad(remainingSeconds)}`;
    return { countdown, absolute, text: `${countdown} · ${absolute}`, expired: false };
  }

  function redPacketEventExpiry(event: RedPacketEvent) {
    return redPacketEventExpiryParts(event).text;
  }

  async function openLiveRoomByWebRID(value: string) {
    const webRID = value.trim();
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

  async function openRedPacketLiveRoom(event: RedPacketEvent) {
    await openLiveRoomByWebRID(event.web_rid || event.room_id || "");
  }

  function roomOpenWebRID(room: RoomItem, monitor?: RedPacketMonitor) {
    return (monitor?.web_rid || room.web_rid || room.id || "").trim();
  }

  function roomFollowSources(room: RoomItem) {
    const unique = new Map<string, RoomFollowSource>();
    for (const source of room.follow_sources ?? []) {
      const accountId = source.account_id?.trim();
      const accountName = source.account_name?.trim();
      if (!accountId || !accountName) continue;
      unique.set(accountId, { ...source, account_id: accountId, account_name: accountName });
    }
    return [...unique.values()];
  }

  function roomSourceLabel(room: RoomItem) {
    const sources = roomFollowSources(room);
    if (sources.length === 1) return `${sources[0].account_name}的关注`;
    if (sources.length > 1) return `${sources[0].account_name}等 ${sources.length} 个账号的关注`;
    if (room.source === "dy-kiro") return "旧福宝导入";
    if (room.source === "manual") return "手动导入";
	if (room.source === "center") return "来源于中心库";
    return "本机创建";
  }

  function roomSourceTooltip(room: RoomItem) {
    const sources = roomFollowSources(room);
    if (sources.length === 0) return roomSourceLabel(room);
    return `关注来源：${sources.map((source) => source.account_name).join("、")}`;
  }

  async function openRoomLiveRoom(room: RoomItem, monitor?: RedPacketMonitor) {
    await openLiveRoomByWebRID(roomOpenWebRID(room, monitor));
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

  function followingLiveSnapshot(instance: BrowserInstance) {
    return browserFollowingLive[instance.id];
  }

  function followingLiveTooltip(instance: BrowserInstance) {
    const snapshot = followingLiveSnapshot(instance);
    const error = browserFollowingLiveErrors[instance.id];
    if (error) return error;
    if (!snapshot) return "读取直播";
    return `${snapshot.total} 个正在直播`;
  }

  async function loadBrowserFollowingLive(instance: BrowserInstance, force = false) {
    const useNativePage = isTauriDesktop() && (
      browserWebviewMountedIds.includes(instance.id) || browserWebviewReadyIds.includes(instance.id)
    );
    if (browserFollowingLiveLoadingIds.includes(instance.id)) {
      if (useNativePage && !browserFollowingLivePendingNativeIds.includes(instance.id)) {
        browserFollowingLivePendingNativeIds = [...browserFollowingLivePendingNativeIds, instance.id];
      }
      return;
    }
    browserFollowingLiveLoadingIds = [...browserFollowingLiveLoadingIds, instance.id];
    const nextErrors = { ...browserFollowingLiveErrors };
    delete nextErrors[instance.id];
    browserFollowingLiveErrors = nextErrors;
    try {
      const result = useNativePage
        ? await invoke<FollowingLiveResult>("sync_browser_following_live", { instanceId: instance.id })
        : await engineRequest<FollowingLiveResult>("browser.following_live", {
            instance_id: instance.id,
            force,
          });
      browserFollowingLive = { ...browserFollowingLive, [instance.id]: result };
      if ((force || useNativePage) && !result.stale) {
        await loadRooms(false);
        void loadRedPacketMonitors(true);
      }
    } catch (error) {
      browserFollowingLiveErrors = {
        ...browserFollowingLiveErrors,
        [instance.id]: error instanceof Error ? error.message : String(error),
      };
    } finally {
      browserFollowingLiveLoadingIds = browserFollowingLiveLoadingIds.filter((id) => id !== instance.id);
      if (browserFollowingLivePendingNativeIds.includes(instance.id)) {
        browserFollowingLivePendingNativeIds = browserFollowingLivePendingNativeIds.filter((id) => id !== instance.id);
        window.setTimeout(() => void loadBrowserFollowingLive(instance, true), 0);
      }
    }
  }

  async function loadBrowserFollowingLives(instances: BrowserInstance[]) {
    const seenAccounts = new Set<string>();
    const queue = instances.filter((instance) => {
      if (!instance.account_id || seenAccounts.has(instance.account_id)) return false;
      seenAccounts.add(instance.account_id);
      return true;
    });
    await Promise.all(
      Array.from({ length: Math.min(2, queue.length) }, async () => {
        while (queue.length > 0) {
          const instance = queue.shift();
          if (instance) await loadBrowserFollowingLive(instance);
        }
      }),
    );
    if (queue.length === 0 && instances.length > 0) {
      await loadRooms(false);
      void loadRedPacketMonitors(true);
    }
  }

  async function openFollowingLive(instance: BrowserInstance) {
    followingLiveModalInstance = instance;
    // Native child WebViews always sit above HTML, regardless of CSS z-index.
    // Invalidate any pending bounds/show work, then serialize one final hide
    // before the live-room dialog is allowed to own the visual top layer.
    browserLayoutRevision += 1;
    await queueBrowserNativeLayout(hideEmbeddedBrowsers);
    void loadBrowserFollowingLive(instance);
  }

  async function closeFollowingLive() {
    followingLiveModalInstance = null;
    browserLayoutRevision += 1;
    const revision = browserLayoutRevision;
    await tick();
    await new Promise<void>((resolve) => window.requestAnimationFrame(() => resolve()));
    await queueBrowserNativeLayout(() => syncEmbeddedBrowsers(revision));
  }

  async function openFollowingLiveRoom(item: FollowingLiveItem) {
    const webRID = (item.web_rid || item.room_id || "").trim();
    if (!/^\d{6,24}$/.test(webRID)) {
      showToast("这个直播间暂时没有可打开的房间号");
      return;
    }
    if (!isTauriDesktop()) {
      window.open(`https://live.douyin.com/${webRID}`, "_blank", "noopener,noreferrer");
      return;
    }
    try {
      await invoke("open_live_room", { webRid: webRID });
    } catch (error) {
      showToast(error instanceof Error ? error.message : String(error));
    }
  }

  function truncatedTooltip(node: HTMLElement, initialText: string) {
    let text = initialText;
    let tooltip: HTMLDivElement | null = null;
    let frame = 0;
    const originalTabIndex = node.getAttribute("tabindex");

    function hideTooltip() {
      tooltip?.remove();
      tooltip = null;
    }

    function refresh() {
      const normalized = text.trim();
      const truncated = Boolean(normalized) && node.scrollWidth > node.clientWidth + 1;
      if (truncated) {
        node.dataset.tooltip = normalized;
        node.dataset.tooltipPlacement = "edge-safe";
        node.tabIndex = 0;
      } else {
        delete node.dataset.tooltip;
        delete node.dataset.tooltipPlacement;
        if (originalTabIndex === null) node.removeAttribute("tabindex");
        else node.setAttribute("tabindex", originalTabIndex);
        hideTooltip();
      }
    }

    function positionTooltip() {
      if (!tooltip) return;
      const rect = node.getBoundingClientRect();
      const tooltipRect = tooltip.getBoundingClientRect();
      const viewportInset = 8;
      let left = rect.left + (rect.width - tooltipRect.width) / 2;
      left = Math.min(Math.max(left, viewportInset), window.innerWidth - tooltipRect.width - viewportInset);
      let top = rect.bottom + 6;
      if (top + tooltipRect.height > window.innerHeight - viewportInset) {
        top = rect.top - tooltipRect.height - 6;
      }
      tooltip.style.left = `${Math.round(left)}px`;
      tooltip.style.top = `${Math.round(Math.max(viewportInset, top))}px`;
    }

    function showTooltip() {
      refresh();
      if (!node.dataset.tooltip || tooltip) return;
      tooltip = document.createElement("div");
      tooltip.className = "shared-overflow-tooltip";
      tooltip.textContent = node.dataset.tooltip;
      tooltip.setAttribute("role", "tooltip");
      document.body.appendChild(tooltip);
      positionTooltip();
    }

    function scheduleRefresh() {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(refresh);
    }

    node.classList.add("portal-tooltip-trigger");
    node.addEventListener("pointerenter", showTooltip);
    node.addEventListener("pointerleave", hideTooltip);
    node.addEventListener("focus", showTooltip);
    node.addEventListener("blur", hideTooltip);
    window.addEventListener("resize", positionTooltip);
    window.addEventListener("scroll", positionTooltip, true);
    const resizeObserver = new ResizeObserver(scheduleRefresh);
    resizeObserver.observe(node);
    scheduleRefresh();

    return {
      update(nextText: string) {
        text = nextText;
        scheduleRefresh();
      },
      destroy() {
        cancelAnimationFrame(frame);
        resizeObserver.disconnect();
        hideTooltip();
        node.classList.remove("portal-tooltip-trigger");
        window.removeEventListener("resize", positionTooltip);
        window.removeEventListener("scroll", positionTooltip, true);
        delete node.dataset.tooltip;
        delete node.dataset.tooltipPlacement;
        if (originalTabIndex === null) node.removeAttribute("tabindex");
        else node.setAttribute("tabindex", originalTabIndex);
      },
    };
  }

  function portalTooltip(node: HTMLElement, initialText: string) {
    let text = initialText;
    let tooltip: HTMLDivElement | null = null;

    function hideTooltip() {
      tooltip?.remove();
      tooltip = null;
    }

    function positionTooltip() {
      if (!tooltip) return;
      const rect = node.getBoundingClientRect();
      const tooltipRect = tooltip.getBoundingClientRect();
      const inset = 8;
      const gap = 6;
      const placement = node.dataset.tooltipPlacement || "top";
      let left = rect.left + (rect.width - tooltipRect.width) / 2;
      let top = rect.top - tooltipRect.height - gap;

      if (placement === "bottom") top = rect.bottom + gap;
      if (placement === "left") {
        left = rect.left - tooltipRect.width - gap;
        top = rect.top + (rect.height - tooltipRect.height) / 2;
      }
      if (placement === "right") {
        left = rect.right + gap;
        top = rect.top + (rect.height - tooltipRect.height) / 2;
      }
      if (top < inset) top = rect.bottom + gap;
      if (top + tooltipRect.height > window.innerHeight - inset) {
        top = rect.top - tooltipRect.height - gap;
      }
      left = Math.min(Math.max(left, inset), window.innerWidth - tooltipRect.width - inset);
      tooltip.style.left = `${Math.round(left)}px`;
      tooltip.style.top = `${Math.round(Math.max(inset, top))}px`;
    }

    function showTooltip() {
      const normalized = text.trim();
      if (!normalized || tooltip) return;
      tooltip = document.createElement("div");
      tooltip.className = "shared-overflow-tooltip";
      tooltip.textContent = normalized;
      tooltip.setAttribute("role", "tooltip");
      document.body.appendChild(tooltip);
      positionTooltip();
    }

    node.classList.add("portal-tooltip-trigger");
    node.addEventListener("pointerenter", showTooltip);
    node.addEventListener("pointerleave", hideTooltip);
    node.addEventListener("focus", showTooltip);
    node.addEventListener("blur", hideTooltip);
    window.addEventListener("resize", positionTooltip);
    window.addEventListener("scroll", positionTooltip, true);

    return {
      update(nextText: string) {
        text = nextText;
        if (tooltip) {
          tooltip.textContent = text.trim();
          positionTooltip();
        }
      },
      destroy() {
        hideTooltip();
        node.classList.remove("portal-tooltip-trigger");
        node.removeEventListener("pointerenter", showTooltip);
        node.removeEventListener("pointerleave", hideTooltip);
        node.removeEventListener("focus", showTooltip);
        node.removeEventListener("blur", hideTooltip);
        window.removeEventListener("resize", positionTooltip);
        window.removeEventListener("scroll", positionTooltip, true);
      },
    };
  }

  function switchView(key: NavKey) {
    const leavingBrowsers = activeView === "browsers" && key !== "browsers";
    const enteringBrowsers = activeView !== "browsers" && key === "browsers";
    if (leavingBrowsers || enteringBrowsers) {
      // A browser-page transition must invalidate pending geometry/show work.
      // Mounted child WebViews stay alive so returning preserves the exact
      // in-memory page, scroll, playback, dialog, and login state.
      browserLayoutRevision += 1;
    }
    if (leavingBrowsers) {
      void queueBrowserNativeLayout(hideEmbeddedBrowsers);
    }
    browserViewSettled = false;
    activeView = key;
    query = "";
    searchOpen = false;
    statusMenuOpen = false;
    importMenuOpen = false;
    roomSortMenuOpen = false;
    roomSourceMenuOpen = false;
    accountSortMenuOpen = false;
    if (engineListenerReady && (key === "accounts" || key === "browsers")) {
      void loadAccounts();
    }
    if (key === "accounts" && engineListenerReady) {
      void loadRooms();
      void loadRedPacketMonitors();
      void loadRedPacketEvents();
      void loadParticipationRecords();
    }
    if (key === "browsers" && engineListenerReady) {
      void loadBrowserInstances();
    }
  }

  async function openViewInNewWindow(key: NavKey) {
    navContextMenu = null;
    if (isTauriDesktop()) {
      try {
        await invoke("open_page_window", { view: key });
      } catch (error) {
        showToast(error instanceof Error ? error.message : String(error));
      }
      return;
    }
    const url = new URL(window.location.href);
    url.search = "";
    url.searchParams.set("window", "page");
    url.searchParams.set("view", key);
    window.open(url.toString(), `fubao-${key}`, "noopener");
  }

  function handleNavClick(event: MouseEvent, key: NavKey) {
    if (event.metaKey || event.ctrlKey) {
      event.preventDefault();
      void openViewInNewWindow(key);
      return;
    }
    switchView(key);
  }

  function handleNavAuxClick(event: MouseEvent, key: NavKey) {
    if (event.button !== 1) return;
    event.preventDefault();
    void openViewInNewWindow(key);
  }

  function showNavContextMenu(event: MouseEvent, key: NavKey) {
    event.preventDefault();
    const menuWidth = 148;
    const menuHeight = 38;
    navContextMenu = {
      key,
      x: Math.max(6, Math.min(event.clientX, window.innerWidth - menuWidth - 6)),
      y: Math.max(6, Math.min(event.clientY, window.innerHeight - menuHeight - 6)),
    };
  }

  function suppressNativeContextMenu(event: MouseEvent) {
    // The desktop shell owns its right-click interactions. Prevent WebKit /
    // WebView2 from exposing Reload, Inspect Element and AutoFill while still
    // allowing target-level handlers (such as the sidebar detached-window
    // menu) to receive the event and render the app's own menu.
    event.preventDefault();
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

  function queueBrowserNativeLayout(operation: () => Promise<void>) {
    const queued = browserNativeLayoutChain
      .catch(() => undefined)
      .then(operation);
    // Keep later layout work serialized even if one native command fails.
    // The returned promise still preserves the original failure for callers
    // that need to report it.
    browserNativeLayoutChain = queued.catch(() => undefined);
    return queued;
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
    const participationContext = browserParticipationContexts[instance.id];
    const retainedForParticipation = browserRedPacketContextIds.includes(instance.id) || Boolean(
      participationContext?.active && participationContext?.prepared,
    );
    // A participation task owns a real live-room page context even when its
    // card is outside the viewport. Hide a visible surface, but retain both
    // the native WebView and its Go runtime lease until the task/context ends.
    if (retainedForParticipation) {
      if (isTauriDesktop() && mounted) await hideEmbeddedBrowser(instance.id);
      return;
    }
    if (!mounted && !reserved) return;
    browserWebviewReleasingIds = [...browserWebviewReleasingIds, instance.id];
    try {
      if (isTauriDesktop() && mounted) {
        await invoke("close_browser_webview", { instanceId: instance.id }).catch(() => undefined);
      }
      browserRedPacketContextIds = browserRedPacketContextIds.filter((id) => id !== instance.id);
      browserWebviewMountedIds = browserWebviewMountedIds.filter((id) => id !== instance.id);
      browserWebviewMountingIds = browserWebviewMountingIds.filter((id) => id !== instance.id);
      browserWebviewLoadingIds = browserWebviewLoadingIds.filter((id) => id !== instance.id);
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
      browserIndependentWindowIds.includes(instance.id) ||
      browserWebviewMountingIds.includes(instance.id) ||
      browserWebviewMountingIds.length >= browserWebviewMountConcurrency ||
      activeView !== "browsers" ||
      instanceModalOpen ||
      licenseModalOpen ||
	  participationSettingsModalOpen ||
	  participationScheduleModalOpen ||
	  Boolean(sidebarActivityDetailID) ||
      !browserMountIsVisible(element)
    ) {
      return;
    }
    browserWebviewMountingIds = [...browserWebviewMountingIds, instance.id];
    if (!browserWebviewLoadingIds.includes(instance.id)) {
      browserWebviewLoadingIds = [...browserWebviewLoadingIds, instance.id];
    }
    const nextErrors = { ...browserWebviewErrors };
    delete nextErrors[instance.id];
    browserWebviewErrors = nextErrors;
    try {
      const admission = await engineRequest<BrowserAdmission>("browser.runtime.acquire", {
        instance_id: instance.id,
      });
      browserCapacity = admission.capacity;
      updateBrowserRuntimeState(instance.id, admission.state, admission.queue_position ?? 0);
      if (!admission.granted) {
        browserWebviewLoadingIds = browserWebviewLoadingIds.filter((id) => id !== instance.id);
        return;
      }
      await invoke("mount_browser_webview", {
        instanceId: instance.id,
        bounds: embeddedBrowserBounds(element),
      });
      // A mount can finish after the user switches pages or opens a modal.
      // Keep that native instance alive but hidden so returning still resumes
      // the exact in-memory page. Filters and viewport exits continue to
      // release it below because they are resource-management boundaries.
      if (activeView !== "browsers" || instanceModalOpen || licenseModalOpen || participationSettingsModalOpen || participationScheduleModalOpen || sidebarActivityDetailID) {
        if (!browserWebviewMountedIds.includes(instance.id)) {
          browserWebviewMountedIds = [...browserWebviewMountedIds, instance.id];
        }
        await hideEmbeddedBrowser(instance.id);
        return;
      }
      if (
        !element.isConnected ||
        !browserMountIsVisible(element)
      ) {
        await invoke("close_browser_webview", { instanceId: instance.id }).catch(() => undefined);
        browserCapacity = await engineRequest<BrowserCapacity>("browser.runtime.release", {
          instance_id: instance.id,
        }).catch(() => browserCapacity);
        updateBrowserRuntimeState(instance.id, "stopped");
        browserWebviewLoadingIds = browserWebviewLoadingIds.filter((id) => id !== instance.id);
        return;
      }
      if (!browserWebviewMountedIds.includes(instance.id)) {
        browserWebviewMountedIds = [...browserWebviewMountedIds, instance.id];
      }
      // A persistent account profile can already contain a fresh login from
      // the prior session. Synchronize immediately on mount instead of
      // leaving the stale CK badge visible until the polling interval fires.
      browserCookieCheckedAt.set(instance.id, Date.now());
      const loginStateUpdated = await invoke<boolean>("sync_browser_account_cookie", {
        instanceId: instance.id,
      }).catch(() => false);
      if (loginStateUpdated) {
        accounts = await engineRequest<AccountItem[]>("account.list");
      }
    } catch (error) {
      browserWebviewLoadingIds = browserWebviewLoadingIds.filter((id) => id !== instance.id);
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
      // Large batch creation can expose many cards at once. Admit native
      // WKWebViews in a small rolling window instead of flooding the macOS
      // main thread with every eligible mount in the same render frame.
      window.setTimeout(scheduleEmbeddedBrowserSync, 80);
    }
  }

  async function syncEmbeddedBrowsers(expectedRevision = browserLayoutRevision) {
    if (
      !isTauriDesktop() ||
      browserLayoutChanging ||
      followingLiveModalInstance ||
      sidebarActivityDetailID ||
      browserPendingClose ||
      expectedRevision !== browserLayoutRevision
    ) return;
    await tick();
    if (
      browserLayoutChanging ||
      followingLiveModalInstance ||
      sidebarActivityDetailID ||
      browserPendingClose ||
      expectedRevision !== browserLayoutRevision
    ) return;
    const visibleIds = new Set(visibleBrowserInstances.map((instance) => instance.id));
    await Promise.all(
      browserInstances.map(async (instance) => {
        if (browserLayoutChanging || expectedRevision !== browserLayoutRevision) return;
        if (browserIndependentWindowIds.includes(instance.id)) {
          if (browserWebviewMountedIds.includes(instance.id)) await hideEmbeddedBrowser(instance.id);
          return;
        }
        if (
          activeView !== "browsers" ||
          !browserViewSettled ||
          instanceModalOpen ||
          licenseModalOpen
		  || participationSettingsModalOpen
		  || participationScheduleModalOpen
		  || sidebarActivityDetailID
        ) {
          if (
            browserWebviewMountedIds.includes(instance.id) ||
            browserWebviewMountingIds.includes(instance.id)
          ) {
            await hideEmbeddedBrowser(instance.id);
          }
          return;
        }
        const element = document.querySelector<HTMLElement>(
          `[data-browser-instance="${instance.id}"]`,
        );
        if (
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
          if (browserLayoutChanging || expectedRevision !== browserLayoutRevision) return;
          browserCapacity = admission.capacity;
          updateBrowserRuntimeState(instance.id, admission.state, admission.queue_position ?? 0);
          if (!admission.granted) {
            await releaseEmbeddedBrowser({ ...instance, runtime_state: "running" });
            return;
          }
          if (
            browserLayoutChanging ||
            expectedRevision !== browserLayoutRevision ||
            !element.isConnected ||
            !browserMountIsVisible(element)
          ) return;
          await invoke("sync_browser_webview", {
            instanceId: instance.id,
            bounds: embeddedBrowserBounds(element),
            // Column dragging hides every native surface first. A WebView
            // whose first page-load event already arrived must be shown
            // again after its final geometry is applied; WebViews that are
            // still loading remain hidden until Rust emits `ready`.
            reveal:
              !browserWebviewLoadingIds.includes(instance.id) &&
              !browserWebviewErrors[instance.id],
          });
        } catch {
          if (browserLayoutChanging || expectedRevision !== browserLayoutRevision) return;
          browserWebviewMountedIds = browserWebviewMountedIds.filter((id) => id !== instance.id);
          await mountEmbeddedBrowser(instance, element);
        }
      }),
    );
  }

  function scheduleEmbeddedBrowserSync() {
    if (!isTauriDesktop() || browserLayoutChanging) return;
    const revision = browserLayoutRevision;
    window.cancelAnimationFrame(browserWebviewSyncFrame);
    browserWebviewSyncFrame = window.requestAnimationFrame(() => {
      void queueBrowserNativeLayout(() => syncEmbeddedBrowsers(revision));
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
    if (selectedParticipationAccountIds.includes(accountId)) {
      selectedParticipationAccountIds = selectedParticipationAccountIds.filter((id) => id !== accountId);
      return;
    }
    selectedParticipationAccountIds = licenseStatus.state === "active"
      ? [...selectedParticipationAccountIds, accountId]
      : [accountId];
  }

  function allSelectableParticipationAccountsSelected() {
    return selectableParticipationAccounts.length > 0 && selectableParticipationAccounts.every((account) =>
      selectedParticipationAccountIds.includes(account.id),
    );
  }

  function toggleAllParticipationAccounts() {
    if (licenseStatus.state !== "active" || browserCreating) return;
    const visibleIds = new Set(selectableParticipationAccounts.map((account) => account.id));
    selectedParticipationAccountIds = allSelectableParticipationAccountsSelected()
      ? selectedParticipationAccountIds.filter((id) => !visibleIds.has(id))
      : [...new Set([...selectedParticipationAccountIds, ...visibleIds])];
  }

  function selectManagementTab(tab: ManagementTab) {
    managementTab = tab;
    monitoringManagementExpanded = tab === "rooms" || tab === "monitoring";
    accountSortMenuOpen = false;
    roomSortMenuOpen = false;
    roomSourceMenuOpen = false;
    statusMenuOpen = false;
    participationGroupMenuOpen = false;
    accountGroupMenuId = "";
    query = "";
    roomRenderLimit = 300;
    redPacketRenderLimit = 300;
    participationRecordRenderLimit = 300;
    if (tab === "redpackets") redPacketHistoryVisible = false;
    accountStatusFilter = "all";
    if (tab === "participation" || tab === "monitoring") accountRole = tab;
    if (tab === "redpackets" && engineListenerReady) void loadRedPacketEvents();
    if (tab === "participation-records" && engineListenerReady) void loadParticipationRecords();
    if (tab === "rooms" && engineListenerReady) {
      void loadRooms();
      void loadRedPacketMonitors();
    }
  }

  function openManagementTab(tab: ManagementTab) {
    switchView("accounts");
    selectManagementTab(tab);
  }

  function toggleMonitoringManagement() {
    if (managementTab === "rooms" || managementTab === "monitoring") {
      monitoringManagementExpanded = true;
      return;
    }
    monitoringManagementExpanded = !monitoringManagementExpanded;
  }

  $: if (managementTab === "rooms" || managementTab === "monitoring") {
    monitoringManagementExpanded = true;
  }

  function toggleRedPacketHistory() {
    redPacketHistoryVisible = !redPacketHistoryVisible;
    redPacketRenderLimit = 300;
  }

  function searchPlaceholder() {
    if (activeView === "accounts") {
      if (managementTab === "redpackets") return "搜索红包、直播间或房间号";
      if (managementTab === "rooms") return "搜索直播间、主播或房间号";
	  if (managementTab === "participation-records") return "搜索参与账号、直播间或结果";
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
    scheduleRoomSearch();
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

  function accountStatus(account: AccountItem, clock = Date.now()) {
    const profile = accountRole === "monitoring" ? account.monitoring : account.participation;
    if (accountCookieStatus(account, accountRole) === "expired") return "CK 失效";
	if (accountRole === "participation" && participationChallengeBlocked(account)) return "拦截";
	if (accountRole === "participation" && profile?.red_packet_cooldown_until) {
		const cooldownUntil = new Date(profile.red_packet_cooldown_until).getTime();
		if (!Number.isNaN(cooldownUntil) && cooldownUntil > clock) return "冷却中";
	}
    if (!profile?.enabled || profile.last_error || /冷却|等待|cooldown/i.test(profile.last_use_status || "")) return "冷却中";
    return "可用";
  }

  function accountCookieStatus(account: AccountItem, role: AccountRole) {
    return role === "monitoring" ? account.monitoring?.cookie_status || "unknown" : account.cookie_status || "unknown";
  }

  function accountCookieMessage(account: AccountItem, role: AccountRole) {
    return role === "monitoring" ? account.monitoring?.cookie_message : account.cookie_message;
  }

  function participationChallengeBlocked(account?: AccountItem) {
	return account?.participation?.last_red_packet_status === "challenge_blocked";
  }

  function accountHealthMessage(account: AccountItem, role: AccountRole) {
	if (role === "participation" && participationChallengeBlocked(account)) {
		return account.participation?.last_red_packet_message || "验证码/安全验证拦截，处理完成后请重新启动参与";
	}
	return accountCookieMessage(account, role);
  }

  function browserCookieExpired(instance: BrowserInstance) {
    return browserCookieStatus(instance) === "expired";
  }

  function browserCookieStatus(instance: BrowserInstance) {
    return accounts.find((account) => account.id === instance.account_id)?.cookie_status || "unknown";
  }

  function browserCookieMessage(instance: BrowserInstance) {
    const message = accounts.find((account) => account.id === instance.account_id)?.cookie_message;
    return (message || "CK 已失效，请重新登录或导入").replaceAll("CK 已过期", "CK 已失效");
  }

  function browserParticipationBlocked(instance: BrowserInstance) {
	return participationChallengeBlocked(accounts.find((account) => account.id === instance.account_id));
  }

  function browserParticipationBlockedMessage(instance: BrowserInstance) {
	const account = accounts.find((item) => item.id === instance.account_id);
	return account?.participation?.last_red_packet_message || "验证码/安全验证拦截，处理完成后请重新启动参与";
  }

  function accountStatusFilterLabel(filter: AccountStatusFilter) {
    if (filter === "available") return "可用";
    if (filter === "expired") return "CK 失效";
    if (filter === "cooldown") return "冷却中";
    return "全部状态";
  }

  function participationGroupName(groupId?: string) {
    if (!groupId) return "未分组";
    return participationGroups.find((group) => group.id === groupId)?.name || "未分组";
  }

  function participationGroupFilterLabel(filter: ParticipationGroupFilter) {
    if (filter === "all") return "全部分组";
    if (filter === "ungrouped") return "未分组";
    return participationGroupName(filter);
  }

  function participationGroupMatches(account: AccountItem, filter: ParticipationGroupFilter) {
    if (filter === "all") return true;
    const groupId = account.participation?.group_id || "";
    return filter === "ungrouped" ? !groupId : groupId === filter;
  }

  async function createParticipationGroup(target: "filter" | "import" | "instance" = "filter") {
    const name = participationGroupDraft.trim();
    if (!name || participationGroupCreating) return;
    participationGroupCreating = true;
    try {
      const group = await engineRequest<ParticipationGroup>("account.participation_group.create", { name });
      participationGroups = await engineRequest<ParticipationGroup[]>("account.participation_group.list");
      participationGroupDraft = "";
      if (target === "filter") participationGroupFilter = group.id;
      if (target === "import") {
        importParticipationGroupId = group.id;
        importGroupMenuOpen = false;
      }
      if (target === "instance") instanceParticipationGroupFilter = group.id;
      showToast(`已新建分组「${group.name}」`);
    } catch (error) {
      showToast(error instanceof Error ? error.message : String(error));
    } finally {
      participationGroupCreating = false;
    }
  }

  async function setAccountParticipationGroup(account: AccountItem, groupId: string) {
    try {
      const updated = await engineRequest<AccountItem>("account.participation_group.set", {
        account_id: account.id,
        group_id: groupId,
      });
      accounts = accounts.map((item) => item.id === updated.id ? updated : item);
      accountGroupMenuId = "";
    } catch (error) {
      showToast(error instanceof Error ? error.message : String(error));
    }
  }

  function selectAccountStatus(filter: AccountStatusFilter) {
    accountStatusFilter = filter;
    statusMenuOpen = false;
  }

  function toggleImportMenu() {
    importMenuOpen = !importMenuOpen;
    importGroupMenuOpen = false;
    statusMenuOpen = false;
    accountSortMenuOpen = false;
	participationGroupMenuOpen = false;
	void closeParticipationTaskMenu();
  }

  function toggleStatusMenu() {
    statusMenuOpen = !statusMenuOpen;
    importMenuOpen = false;
    accountSortMenuOpen = false;
	roomSourceMenuOpen = false;
	participationGroupMenuOpen = false;
	void closeParticipationTaskMenu();
  }

  function closeFloatingMenus(event: PointerEvent) {
    const target = event.target as HTMLElement;
    if (!target.closest(".nav-context-menu")) {
      navContextMenu = null;
    }
    if (!target.closest(".menu-anchor")) {
      statusMenuOpen = false;
      importMenuOpen = false;
      importGroupMenuOpen = false;
      roomSortMenuOpen = false;
      roomSourceMenuOpen = false;
      roomCleanupMenuOpen = false;
      accountSortMenuOpen = false;
	  participationGroupMenuOpen = false;
	  accountGroupMenuId = "";
	  participationScheduleUnitMenuOpen = false;
	  void closeParticipationTaskMenu();
    }
  }

  function toggleRoomCleanupMenu() {
    roomCleanupMenuOpen = !roomCleanupMenuOpen;
    roomCleanupSettingsError = "";
    if (!roomCleanupSettingsBusy) {
      roomCleanupProgress = null;
      roomCleanupProcessed = 0;
      roomCleanupTotal = 0;
    }
    roomSortMenuOpen = false;
    roomSourceMenuOpen = false;
    statusMenuOpen = false;
    importMenuOpen = false;
    accountSortMenuOpen = false;
  }

  async function executeRoomCleanup() {
    if (roomCleanupSettingsBusy) return;
    const nextRoomSettings: RoomSettings = {
      ...roomSettings,
      auto_recycle_max_live_sessions: normalizedParticipationSetting(roomSettings.auto_recycle_max_live_sessions, 100000),
      auto_recycle_no_packet_days: normalizedParticipationSetting(roomSettings.auto_recycle_no_packet_days, 3650),
    };
    roomCleanupSettingsBusy = true;
    roomCleanupSettingsError = "";
    roomCleanupProgress = null;
    roomCleanupProcessed = 0;
    roomCleanupTotal = 0;
    try {
      roomSettings = isTauriDesktop()
        ? await engineRequest<RoomSettings>("room.set_settings", nextRoomSettings)
        : nextRoomSettings;
      let cursor = "";
      let aggregate: RoomCleanupProgress = {
        total: rooms.length,
        scanned: 0,
        cleaned: 0,
        recycled: 0,
        excluded: 0,
        skipped: 0,
        has_more: false,
      };
      if (isTauriDesktop()) {
        do {
          const step = await engineRequest<RoomCleanupProgress>("room.execute_cleanup", { cursor, limit: 500 });
          if (roomCleanupTotal === 0) roomCleanupTotal = step.total;
          roomCleanupProcessed += step.scanned;
          aggregate = {
            total: roomCleanupTotal,
            scanned: roomCleanupProcessed,
            cleaned: aggregate.cleaned + step.cleaned,
            recycled: aggregate.recycled + step.recycled,
            excluded: aggregate.excluded + step.excluded,
            skipped: aggregate.skipped + step.skipped,
            next_cursor: step.next_cursor,
            has_more: step.has_more,
          };
          roomCleanupProgress = aggregate;
          cursor = step.next_cursor || "";
          await tick();
        } while (aggregate.has_more && cursor);
      } else {
        roomCleanupTotal = rooms.length;
        roomCleanupProcessed = rooms.length;
        aggregate.scanned = rooms.length;
        aggregate.skipped = rooms.length;
        roomCleanupProgress = aggregate;
      }
      await Promise.all([loadRooms(false), loadRoomRecycleBin(), loadSidebarActivities()]);
      showToast(aggregate.cleaned > 0
        ? `清理完成：处理 ${aggregate.cleaned} 个直播间`
        : "清理完成，没有符合当前规则的直播间");
    } catch (error) {
      roomCleanupSettingsError = error instanceof Error ? error.message : String(error);
    } finally {
      roomCleanupSettingsBusy = false;
    }
  }

  function currentAccountSortMode(): AccountSortMode {
    return accountRole === "monitoring" ? monitoringAccountSortMode : participationAccountSortMode;
  }

  function accountSortModeLabel(mode: AccountSortMode) {
    if (mode === "total-requests") return "请求总数优先";
    if (mode === "today-requests") return "今日次数优先";
    if (mode === "last-request") return "最后请求时间优先";
    if (mode === "available-first") return "可用优先";
    if (mode === "join-count") return "参与次数优先";
    if (mode === "win-count") return "中奖次数优先";
    if (mode === "created-at") return "添加时间优先";
    return "默认顺序";
  }

  function accountSortOptions(): [AccountSortMode, string][] {
    if (accountRole === "monitoring") {
      return [
        ["default", "默认顺序"],
        ["total-requests", "请求总数优先"],
        ["today-requests", "今日次数优先"],
        ["last-request", "最后请求时间优先"],
        ["created-at", "添加时间优先"],
      ];
    }
    return [
      ["default", "默认顺序"],
      ["available-first", "可用优先"],
      ["join-count", "参与次数优先"],
      ["win-count", "中奖次数优先"],
      ["created-at", "添加时间优先"],
    ];
  }

  function accountSortTimestamp(value?: string) {
    if (!value) return 0;
    const timestamp = new Date(value).getTime();
    return Number.isNaN(timestamp) ? 0 : timestamp;
  }

  function accountSortDifference(left: AccountItem, right: AccountItem) {
    const mode = currentAccountSortMode();
    if (mode === "total-requests") {
      return (right.monitoring?.total_request_count ?? 0) - (left.monitoring?.total_request_count ?? 0);
    }
    if (mode === "today-requests") {
      return (right.monitoring?.today_request_count ?? 0) - (left.monitoring?.today_request_count ?? 0);
    }
    if (mode === "last-request") {
      return accountSortTimestamp(right.monitoring?.last_used_at) - accountSortTimestamp(left.monitoring?.last_used_at);
    }
    if (mode === "available-first") {
      return Number(accountStatus(right, redPacketClock) === "可用") - Number(accountStatus(left, redPacketClock) === "可用");
    }
    if (mode === "join-count") {
      return (right.participation?.join_count ?? 0) - (left.participation?.join_count ?? 0);
    }
    if (mode === "win-count") {
      return (right.participation?.win_count ?? 0) - (left.participation?.win_count ?? 0);
    }
    if (mode === "created-at") {
      return accountSortTimestamp(right.created_at) - accountSortTimestamp(left.created_at);
    }
    return 0;
  }

  function selectAccountSortMode(mode: AccountSortMode) {
    if (accountRole === "monitoring") {
      monitoringAccountSortMode = mode as MonitoringAccountSortMode;
    } else {
      participationAccountSortMode = mode as ParticipationAccountSortMode;
    }
    accountSortMenuOpen = false;
  }

  function toggleAccountSortMenu() {
    accountSortMenuOpen = !accountSortMenuOpen;
    roomSortMenuOpen = false;
    roomSourceMenuOpen = false;
    statusMenuOpen = false;
    importMenuOpen = false;
  }

  function roomSortModeLabel(mode: RoomSortMode) {
    if (mode === "instance-first") return "实例优先";
    if (mode === "live-first") return "开播优先";
    if (mode === "recent-live") return "最近开播";
    if (mode === "recent-redpacket") return "红包优先";
    return "默认顺序";
  }

  function selectRoomSortMode(mode: RoomSortMode) {
    roomSortMode = mode;
    roomSortMenuOpen = false;
  }

  function toggleRoomSortMenu() {
    roomSortMenuOpen = !roomSortMenuOpen;
    roomSourceMenuOpen = false;
    statusMenuOpen = false;
    importMenuOpen = false;
    accountSortMenuOpen = false;
  }

  function permanentCenterRoomAccess() {
    return licenseStatus.state === "active"
      && licenseStatus.edition === "专业版"
      && !licenseStatus.expires_at?.trim();
  }

  function reconcileRoomSourceLicense() {
	if (!permanentCenterRoomAccess()) {
		centerExcludedRooms = [];
		if (roomRecycleView === "center-exclusions") roomRecycleView = "recycle";
	}
    if (roomSourceFilter !== "center" || permanentCenterRoomAccess()) return;
    roomSourceFilter = "all";
    roomSourceMenuOpen = false;
    if (engineListenerReady && activeView === "accounts" && managementTab === "rooms") {
      void loadRooms(false);
    }
  }

  function roomSourceFilterLabel(filter: RoomSourceFilter) {
    if (filter === "following") return "关注列表";
    if (filter === "imported") return "导入";
    if (filter === "center") return "中心库";
    return "全部来源";
  }

  function roomSourceFilterOptions(): [RoomSourceFilter, string][] {
    const options: [RoomSourceFilter, string][] = [
      ["all", "全部来源"],
      ["following", "关注列表"],
      ["imported", "导入"],
    ];
    if (permanentCenterRoomAccess()) options.push(["center", "中心库"]);
    return options;
  }

  function selectRoomSourceFilter(filter: RoomSourceFilter) {
    if (filter === "center" && !permanentCenterRoomAccess()) return;
    roomSourceFilter = filter;
    roomSourceMenuOpen = false;
    roomRenderLimit = 300;
    void loadRooms(false);
  }

  function toggleRoomSourceMenu() {
    roomSourceMenuOpen = !roomSourceMenuOpen;
    roomSortMenuOpen = false;
    statusMenuOpen = false;
    importMenuOpen = false;
    accountSortMenuOpen = false;
  }

  async function pasteAccountCookie() {
    importMenuOpen = false;
    if (accountImportBusy) return;
    try {
      const cookie = await navigator.clipboard.readText();
      if (!cookie.trim()) {
        showToast("剪贴板里没有 Cookie");
        return;
      }
      await importAccountSources([{ name: "粘贴 Cookie", content: cookie }], currentAccountImportRole());
    } catch {
      showToast("无法读取剪贴板，请检查系统权限");
    }
  }

  function currentAccountImportRole(): AccountRole {
    return managementTab === "monitoring" || managementTab === "participation" ? managementTab : accountRole;
  }

  async function importAccountSources(sources: Array<{ name: string; content: string }>, role: AccountRole) {
    if (accountImportBusy || sources.length === 0) return;
    accountImportBusy = true;
    try {
      const result = await engineRequest<{
        imported: number;
        merged: number;
        failed: number;
        invalid_sources: number;
        total: number;
      }>("account.import_cookies", {
        role,
        sources,
        group_id: role === "participation" ? importParticipationGroupId : "",
      });
      await loadAccounts(false);
      managementTab = role;
      accountRole = role;
      const details = [
        result.imported ? `新增 ${result.imported} 个` : "",
        result.merged ? `更新或归类 ${result.merged} 个` : "",
        result.failed ? `${result.failed} 个导入失败` : "",
        result.invalid_sources ? `${result.invalid_sources} 个文件未识别` : "",
      ].filter(Boolean).join("，");
      showToast(`已导入到${roleLabel(role)}${details ? `：${details}` : ""}`);
    } catch (error) {
      showToast(error instanceof Error ? error.message : String(error));
    } finally {
      accountImportBusy = false;
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
    accountCreateGroupId = accountCreateRole === "participation" ? importParticipationGroupId : "";
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
    roomImportCompleted = 0;
    roomImportTotal = 0;
    roomImportModalOpen = true;
  }

  function normalizeRoomImportValues(raw: string) {
    const seen = new Set<string>();
    const valid: string[] = [];
    let invalid = 0;
    for (const part of raw.split(/[,，;；\s]+/)) {
      let value = part.trim();
      if (!value) continue;
      const slash = value.lastIndexOf("/");
      if (slash >= 0) value = value.slice(slash + 1);
      value = value.split(/[?#]/, 1)[0]?.trim() || "";
      if (!value || seen.has(value)) continue;
      seen.add(value);
      if (/^\d{6,20}$/.test(value)) valid.push(value);
      else invalid += 1;
    }
    return { valid, invalid };
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
    const normalized = normalizeRoomImportValues(roomImportText);
    if (normalized.valid.length === 0) {
      showToast("没有识别到有效的直播间 ID");
      return;
    }
    roomImportBusy = true;
    roomImportCompleted = 0;
    roomImportTotal = normalized.valid.length;
    roomError = "";
    try {
      const result = { imported: 0, merged: 0, invalid: normalized.invalid, total: 0 };
      const batchSize = normalized.valid.length > 10000 ? 2000 : normalized.valid.length > 3000 ? 500 : 100;
      for (let offset = 0; offset < normalized.valid.length; offset += batchSize) {
        const batch = normalized.valid.slice(offset, offset + batchSize);
        const persist = offset + batch.length >= normalized.valid.length;
        const current = await engineRequest<{ imported: number; merged: number; invalid?: number; total: number }>("room.import_ids", {
          ids: batch.join("\n"),
          persist,
        });
        result.imported += current.imported;
        result.merged += current.merged;
        result.invalid += current.invalid ?? 0;
        result.total = current.total;
        roomImportCompleted = Math.min(normalized.valid.length, offset + batch.length);
        await tick();
        await new Promise<void>((resolve) => window.requestAnimationFrame(() => resolve()));
      }
      roomRenderLimit = 300;
      await loadRooms(false);
      roomImportModalOpen = false;
      managementTab = "rooms";
      query = "";
      const invalidText = result.invalid ? `，${result.invalid} 条无效` : "";
      showToast(`已导入 ${result.imported} 个直播间${result.merged ? `，${result.merged} 个已存在` : ""}${invalidText}`);
    } catch (error) {
      roomError = error instanceof Error ? error.message : String(error);
      showToast(roomError);
    } finally {
      roomImportBusy = false;
      roomImportCompleted = 0;
      roomImportTotal = 0;
    }
  }

  async function handleAccountFiles(event: Event, folder = false) {
    const input = event.currentTarget as HTMLInputElement;
    const files = Array.from(input.files ?? []).filter((file) => /\.(json|txt|cookie)$/i.test(file.name));
    const role = currentAccountImportRole();
    try {
      const sources = await Promise.all(files.map(async (file) => ({
        name: file.webkitRelativePath || file.name,
        content: await file.text(),
      })));
      if (sources.length === 0) {
        showToast(folder ? "文件夹中没有可导入的 Cookie 文件" : "没有选择可导入的 Cookie 文件");
        return;
      }
      await importAccountSources(sources, role);
    } catch (error) {
      showToast(`读取账号文件失败：${error instanceof Error ? error.message : String(error)}`);
    } finally {
      input.value = "";
    }
  }

  function setDirectoryInput(node: HTMLInputElement) {
    node.setAttribute("webkitdirectory", "");
    node.setAttribute("directory", "");
  }

  function accountMeta(account: AccountItem) {
    if (accountRole === "participation") {
      const profile = account.participation;
      const binding = profile?.fingerprint_profile_id ? `指纹 ${profile.fingerprint_profile_id}` : "未绑定指纹";
      const diamond = profile?.diamond_status === "valid" && profile.diamond_checked_at
        ? `${profile.diamond_balance ?? 0} 钻`
        : "暂无";
      return `参与 ${profile?.join_count ?? 0} 次 · 中奖 ${profile?.win_count ?? 0} 次 · 钻石 ${diamond} · ${binding}`;
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

  // Followed-live feeds refresh once per minute. Keep a small grace window for
  // engine scheduling and IPC delivery, but never let a snapshot from a
  // previous desktop session make a broadcaster look permanently live.
  const FOLLOWING_LIVE_FRESH_MS = 150_000;

  function roomWasDiscoveredByInstance(room: RoomItem) {
    return room.source === "following-live" || roomFollowSources(room).length > 0;
  }

  function roomFollowingLiveTimestamp(room: RoomItem) {
    let latest = eventTimestamp(room.last_seen_live_at);
    for (const source of roomFollowSources(room)) {
      if (source.is_live === false) continue;
      latest = Math.max(latest, eventTimestamp(source.last_seen_live_at));
    }
    return latest;
  }

  function roomIsFollowingLive(room: RoomItem, clock = Date.now()) {
    if (!room.following_live) return false;
    const lastSeen = roomFollowingLiveTimestamp(room);
    return lastSeen > 0 && clock >= lastSeen - 60_000 && clock - lastSeen <= FOLLOWING_LIVE_FRESH_MS;
  }

  function roomIsCurrentlyLive(
    room: RoomItem,
    monitors: RedPacketMonitor[],
    overrides: Record<string, { status: string; connectionStatus: string }>,
    clock = Date.now(),
  ) {
    if (roomIsFollowingLive(room, clock)) return true;
    const monitor = roomMonitorFor(room, monitors);
    if (!monitor) return false;
    return roomLiveStatus(monitor, redPacketMonitorUiStatus(monitor, overrides)) === "已开播";
  }

  function roomLastLiveStartedAt(room: RoomItem, monitors: RedPacketMonitor[]) {
    const value = roomMonitorFor(room, monitors)?.live_started_at;
    const monitorTimestamp = value ? new Date(value).getTime() : 0;
    return Math.max(Number.isFinite(monitorTimestamp) ? monitorTimestamp : 0, roomFollowingLiveTimestamp(room));
  }

  function roomLastRedPacketAt(room: RoomItem, monitors: RedPacketMonitor[], events: RedPacketEvent[]) {
    const monitor = roomMonitorFor(room, monitors);
    let latest = eventTimestamp(monitor?.last_event_at);
    for (const event of events) {
      const belongsToRoom = (monitor && event.monitor_id === monitor.id)
        || event.room_id === room.id
        || Boolean(room.web_rid && event.web_rid === room.web_rid);
      if (belongsToRoom) latest = Math.max(latest, eventTimestamp(event.detected_at));
    }
    return latest;
  }

  function roomActiveRedPacketSummary(room: RoomItem, clock: number) {
    const monitor = roomMonitorFor(room, redPacketMonitors);
    const events = redPacketEvents.filter((event) => {
      const belongsToRoom = (monitor && event.monitor_id === monitor.id)
        || event.room_id === room.id
        || Boolean(room.web_rid && event.web_rid === room.web_rid)
        || Boolean(room.actual_room_id && event.room_id === room.actual_room_id);
      return belongsToRoom && redPacketEventIsActive(event, clock);
    });
    if (events.length === 0) return { count: 0, tip: "" };

    const nearest = events
      .map((event) => ({ event, expiresAt: eventTimestamp(event.expires_at || event.draw_at) }))
      .filter((item) => item.expiresAt > clock)
      .sort((left, right) => left.expiresAt - right.expiresAt)[0];
    const remaining = nearest ? redPacketEventExpiryParts(nearest.event, clock).countdown : "";
    return {
      count: events.length,
      tip: `检测到 ${events.length} 个未过期红包${remaining ? ` · 最近结束 ${remaining}` : ""}`,
    };
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

  function roomCombinedLiveStatus(
    room: RoomItem,
    monitor?: RedPacketMonitor,
    status = monitor ? redPacketMonitorUiStatus(monitor) : "",
    clock = Date.now(),
  ) {
    if (!room.enabled) return "已停用";
    if (roomIsFollowingLive(room, clock)) return "已开播";
	if (monitor && status === "running") return roomLiveStatus(monitor, status);
	if (room.center_live_status === "live" && centerRoomEvidenceFresh(room, clock)) return "已开播";
	if (room.center_live_status === "offline") return "未开播";
	if (monitor) return roomLiveStatus(monitor, status);
    return "未监测";
  }

  function centerRoomEvidenceFresh(room: RoomItem, clock = Date.now()) {
	const timestamp = Date.parse(room.center_live_at || "");
	return Number.isFinite(timestamp) && clock - timestamp <= 90_000;
  }

  function roomCombinedMonitorPhase(
    room: RoomItem,
    monitor?: RedPacketMonitor,
    status = monitor ? redPacketMonitorUiStatus(monitor) : "",
    clock = Date.now(),
  ) {
    if (roomIsFollowingLive(room, clock)) {
      const discovered = `实例关注发现 · ${formatMonitorTime(room.last_seen_live_at)}`;
      if (!monitor || status !== "running") return discovered;
      if (monitor.live_status === "error" || monitor.connection_status === "error") {
        return "实例已确认开播 · 红包探测异常";
      }
      if (monitor.live_status !== "live") return "实例已确认开播 · 等待红包检测";
    }
	if (room.center_live_status === "live" && centerRoomEvidenceFresh(room, clock)) {
		return `中心库确认开播 · ${formatMonitorTime(room.center_live_at, clock)}`;
	}
	if (room.source === "center" && !monitor) {
		return `中心库同步 · ${formatMonitorTime(room.center_live_at || room.updated_at, clock)}`;
	}
    return monitor ? roomMonitorPhase(monitor, status) : "红包监测未准备";
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

  function formatMonitorTime(value?: string, clock = Date.now()) {
    if (!value) return "暂无";
    const timestamp = Date.parse(value);
    if (!Number.isFinite(timestamp)) return value.replace("T", " ").slice(0, 16);
    const seconds = Math.max(0, Math.floor((clock - timestamp) / 1000));
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
      stopped: Math.max(0, redPacketMonitorSummary.enabled - runningRedPacketMonitorCount),
      error: redPacketMonitorSummary.errors,
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
    appendMonitorLog(`运行概况：${runningRedPacketMonitorCount}/${redPacketMonitorSummary.enabled} 个直播间正在监测`);
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

  function participationLogStatePayload() {
	return {
		logs: participationRuntimeLogs,
		join: participationRuntimeLogs.filter((item) => item.action === "join").length,
		receive: participationRuntimeLogs.filter((item) => item.action.startsWith("receive")).length,
		error: participationRuntimeLogs.filter((item) => item.error || (item.http_status && (item.http_status < 200 || item.http_status >= 300))).length,
	};
  }

  async function emitParticipationLogState() {
	if (!("__TAURI_INTERNALS__" in window)) return;
	try {
		await emit("participation-log://state", participationLogStatePayload());
	} catch {
		// The native participation-log window may already be closed.
	}
  }

  async function loadParticipationRuntimeLogs() {
	if (!engineListenerReady) return;
	try {
		participationRuntimeLogs = await engineRequest<ParticipationTrace[]>("red_packet_participation.logs");
		void emitParticipationLogState();
	} catch {
		// Keep the last safe trace snapshot during a transient engine refresh.
	}
  }

  async function openParticipationRuntimeLog() {
	if (!("__TAURI_INTERNALS__" in window)) {
		showToast("参与日志窗口仅支持桌面端");
		return;
	}
	try {
		await loadParticipationRuntimeLogs();
		await invoke("open_participation_log");
		window.setTimeout(() => void emitParticipationLogState(), 120);
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
      const page = await loadRedPacketMonitorPage(rooms.map((room) => room.id));
      // A polling request may have started before a start/stop action. Ignore
      // that older snapshot so it cannot put the row back into its old state.
      if (requestSeq !== redPacketMonitorListRequestSeq) return;
      const previous = redPacketMonitors;
      const next = applyRedPacketMonitorOverrides(page.items);
      recordMonitorStateChanges(previous, next);
      redPacketMonitors = next;
      redPacketMonitorSummary = page.summary;
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

  async function loadParticipationRecords() {
    if (participationRecordsLoading) return;
    participationRecordsLoading = true;
    participationRecordError = "";
    try {
      participationRecords = await engineRequest<ParticipationRecord[]>("red_packet_participation.list");
    } catch (error) {
      participationRecordError = error instanceof Error ? error.message : String(error);
    } finally {
      participationRecordsLoading = false;
    }
  }

  function participationRecordStatus(record: ParticipationRecord) {
    if (record.status === "pending") return { label: "等待中", tone: "pending" };
    if (record.status === "won") return { label: record.award ? `已中${record.award}` : "已中奖", tone: "success" };
    if (record.status === "not_won") return { label: "未中奖", tone: "muted" };
    if (record.status === "draw_error") return { label: "开奖异常", tone: "warning" };
    if (record.status === "joined") return { label: "参与成功", tone: "success" };
    if (record.status === "already_joined") return { label: "已参与", tone: "success" };
    if (record.status === "challenge_blocked") return { label: "验证拦截", tone: "warning" };
    if (record.status === "risk_control") return { label: "风控冷却", tone: "warning" };
    if (record.status === "login_expired") return { label: "CK 失效", tone: "error" };
    if (record.status === "network_error") return { label: "网络异常", tone: "warning" };
    if (record.status === "expired") return { label: "已过期", tone: "muted" };
    return { label: "参与失败", tone: "error" };
  }

  function participationRecordEndpoint(record: ParticipationRecord) {
    if (record.endpoint === "receive") return "开奖结果";
    if (record.endpoint === "rush") return "rush 回退";
    if (record.endpoint === "join") return "join 接口";
    return "等待请求";
  }

  function participationRecordExactTime(record: ParticipationRecord) {
    const timestamp = Date.parse(record.updated_at);
    return Number.isFinite(timestamp)
      ? new Date(timestamp).toLocaleString("zh-CN", { hour12: false })
      : record.updated_at;
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
	  const result = await engineRequest<{ started?: number; stopped?: number; account_count?: number }>(
        action === "start" ? "red_packet_monitor.start_all" : "red_packet_monitor.stop_all",
      );
      showToast(action === "start" ? `已启动 ${result.started ?? 0} 个直播间红包监测` : `已停止 ${result.stopped ?? 0} 个直播间红包监测`);
      appendMonitorLog(
		action === "start"
		  ? `${result.account_count ?? 0} 个监测账号已分摊监测 ${result.started ?? 0} 个直播间`
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
      redPacketMonitorSummary = {
        ...redPacketMonitorSummary,
        running: action === "start" ? redPacketMonitorSummary.enabled : 0,
        first_checked: 0,
        pending_first: action === "start" ? redPacketMonitorSummary.enabled : 0,
        live_running: action === "start" ? redPacketMonitorSummary.live_running : 0,
      };
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
      const [page, recycled, excluded] = await Promise.all([
        loadRoomPage(0, roomRenderLimit, query.trim(), roomSourceFilter),
        engineRequest<RoomItem[]>("room.recycle_bin").catch(() => recycledRooms),
        permanentCenterRoomAccess()
			? engineRequest<CenterExclusionItem[]>("room.center_exclusions").catch(() => centerExcludedRooms)
			: Promise.resolve([] as CenterExclusionItem[]),
      ]);
      rooms = page.items;
      roomTotalCount = page.total;
      recycledRooms = recycled;
      centerExcludedRooms = excluded;
      if (page.total === 0 && autoMigrate && !query.trim()) {
        await migrateLegacyRooms(true);
      } else {
        void loadRedPacketMonitors(true);
      }
    } catch (error) {
      roomError = error instanceof Error ? error.message : String(error);
    } finally {
      roomsLoading = false;
    }
  }

  function scheduleRoomSearch() {
    if (managementTab !== "rooms") return;
    window.clearTimeout(roomSearchTimer);
    roomSearchTimer = window.setTimeout(() => {
      roomRenderLimit = 300;
      void loadRooms(false);
    }, 220);
  }

  function showMoreRooms() {
    roomRenderLimit += 300;
    void loadRooms(false);
  }

  async function migrateLegacyRooms(silent = false) {
    if (roomsMigrating) return;
    importMenuOpen = false;
    roomsMigrating = true;
    roomError = "";
    try {
      const result = await engineRequest<{ imported: number; merged: number; total: number }>("room.migrate_legacy");
      const page = await loadRoomPage(0, roomRenderLimit, "", roomSourceFilter);
      rooms = page.items;
      roomTotalCount = page.total;
      if (!silent || result.imported > 0) {
        showToast(`已导入 ${result.imported} 个直播间，当前共 ${result.total} 个`);
      }
      managementTab = "rooms";
      query = "";
      roomRenderLimit = 300;
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      // A fresh installation normally has no legacy rooms_config.json. The
      // empty state already explains that there are no rooms, so the automatic
      // best-effort migration must not surface the same information as a toast.
      if (silent && message.includes("未找到旧福宝直播间数据")) {
        roomError = "";
      } else {
        roomError = message;
        showToast(roomError);
      }
    } finally {
      roomsMigrating = false;
    }
  }

  async function loadAccounts(autoMigrate = true) {
    if (accountsLoading) return;
    accountsLoading = true;
    accountError = "";
    try {
      const [items, groups] = await Promise.all([
        engineRequest<AccountItem[]>("account.list"),
        engineRequest<ParticipationGroup[]>("account.participation_group.list"),
      ]);
      accounts = items;
      participationGroups = groups;
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
      accountCreateGroupId = "";
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
        const updatedAccount = await invoke<AccountItem>("complete_account_rebind", { accountId: account.id });
        accounts = accounts.map((item) => item.id === updatedAccount.id ? updatedAccount : item);
        accountRebinding = null;
        const accountInstances = browserInstances.filter((instance) => instance.account_id === account.id);
        await Promise.all(
          accountInstances.map((instance) =>
            invoke("refresh_browser_account_cookie", { instanceId: instance.id }),
          ),
        );
        await loadBrowserInstances();
        showToast(`已重新绑定「${account.nickname || account.name}」并更新 CK`);
      } else {
        const result = await invoke<{ created: boolean; account: AccountItem }>("complete_account_create", {
          sessionId: createSessionId,
          role: accountCreateRole,
          groupId: accountCreateGroupId,
        });
        accountCreateSessionId = "";
        accountCreateGroupId = "";
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

  async function removeAccountRole(account: AccountItem, roleToRemove: AccountRole) {
    if (account.roles.length <= 1) {
      showToast("账号至少需要保留一个分类");
      return;
    }
    try {
      const updatedAccount = await engineRequest<AccountItem>("account.remove_role", {
        account_id: account.id,
        role: roleToRemove,
      });
      const roleToKeep = oppositeRole(roleToRemove);
      if (hasAccountRole(updatedAccount, roleToRemove) || !hasAccountRole(updatedAccount, roleToKeep)) {
        throw new Error(`未能移除${roleLabel(roleToRemove)}，请刷新后重试`);
      }
      accounts = accounts.map((item) => item.id === updatedAccount.id ? updatedAccount : item);
      await loadAccounts(false);
      showToast(`已移除${roleLabel(roleToRemove)}`);
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
	  browserInventoryLoaded = true;
	  void loadBrowserParticipationContexts();
      void loadBrowserFollowingLives(instances);
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

  async function loadBrowserParticipationContexts() {
	if (!engineListenerReady) return;
	try {
		const items = await engineRequest<BrowserParticipationContext[]>("red_packet_participation.contexts");
		browserParticipationContexts = Object.fromEntries(items.map((item) => [item.instance_id, item]));
		// The card Gift control represents an active native participation
		// context, not whether the scheduler can accept a new packet at this
		// exact moment. During cooldown, while waiting for a draw, or after a
		// temporary eligibility change, `accepting` can be false even though the
		// task and its live-room context are still running. Keep this marker in
		// lockstep with the top-bar runtime, which uses active + prepared.
		browserRedPacketContextIds = items
			.filter((item) => item.active && item.prepared)
			.map((item) => item.instance_id);
		scheduleEmbeddedBrowserSync();
	} catch {
		// Keep the last native context state during a transient sidecar refresh.
	}
  }

  function browserParticipationContextEnabled(instanceID: string) {
	const context = browserParticipationContexts[instanceID];
	return Boolean((context?.active && context?.prepared) || browserRedPacketContextIds.includes(instanceID));
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
    // Every native visibility mutation shares one queue. Without this, a
    // slow hide request from an earlier slider value can complete after the
    // final reveal and randomly strand a card on its HTML placeholder.
    void queueBrowserNativeLayout(hideEmbeddedBrowsers);
    browserColumnSyncTimer = window.setTimeout(async () => {
      if (revision !== browserLayoutRevision) return;
      // Flush any native bounds request that may have completed after the
      // first hide, then measure only the final settled grid.
      await queueBrowserNativeLayout(hideEmbeddedBrowsers);
      await tick();
      await new Promise<void>((resolve) => window.requestAnimationFrame(() => resolve()));
      await new Promise<void>((resolve) => window.requestAnimationFrame(() => resolve()));
      if (revision !== browserLayoutRevision) return;
      browserLayoutChanging = false;
      await queueBrowserNativeLayout(() => syncEmbeddedBrowsers(revision));
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
      const now = Date.now();
      const queue = visibleBrowserInstances.filter((instance) => {
        if (
          !browserWebviewMountedIds.includes(instance.id) ||
          browserWebviewReleasingIds.includes(instance.id)
        ) return false;
        const interval = browserCookieStatus(instance) === "valid" ? 60_000 : 5_000;
        return now - (browserCookieCheckedAt.get(instance.id) || 0) >= interval;
      });
      let accountUpdated = false;
      await Promise.all(
        Array.from({ length: Math.min(3, queue.length) }, async () => {
          while (queue.length > 0) {
            const instance = queue.shift();
            if (!instance) return;
            const showIndicator = browserCookieStatus(instance) !== "valid";
            browserCookieCheckedAt.set(instance.id, Date.now());
            if (showIndicator && !browserCookieCheckingIds.includes(instance.id)) {
              browserCookieCheckingIds = [...browserCookieCheckingIds, instance.id];
            }
            const startedAt = performance.now();
            try {
              const updated = await invoke<boolean>("sync_browser_account_cookie", {
                instanceId: instance.id,
              }).catch(() => false);
              accountUpdated ||= updated;
            } finally {
              const remaining = showIndicator ? 320 - (performance.now() - startedAt) : 0;
              if (showIndicator && remaining > 0) {
                await new Promise<void>((resolve) => window.setTimeout(resolve, remaining));
              }
              if (showIndicator) {
                browserCookieCheckingIds = browserCookieCheckingIds.filter((id) => id !== instance.id);
              }
            }
          }
        }),
      );
      if (accountUpdated) {
        accounts = await engineRequest<AccountItem[]>("account.list");
      }
    } finally {
      browserCookieCheckingIds = [];
      browserCookieSyncing = false;
    }
  }

  async function openInstanceModal() {
    selectedParticipationAccountIds = [];
    instanceParticipationGroupFilter = "all";
    if (!engineListenerReady) {
      browserError = "Go 引擎尚未连接";
      return;
    }
    await Promise.all([loadLicenseStatus(), loadAccounts(false), loadBrowserInstances()]);
    if (licenseStatus.state !== "active" && browserInstances.length >= 1) {
      showToast("免费版最多只能创建 1 个浏览器实例，请激活专业版后继续创建");
      return;
    }
    await hideEmbeddedBrowsers();
    instanceModalOpen = true;
  }

  async function refreshInstanceAccounts() {
    if (instanceAccountsRefreshing || accountsLoading || browserCreating) return;
    instanceAccountsRefreshing = true;
    browserError = "";
    try {
      await loadAccounts(false);
      const ownedAccountIds = new Set(browserInstances.map((instance) => instance.account_id));
      const eligibleAccountIds = new Set(
        accounts
          .filter((account) =>
            account.roles.includes("participation") &&
            account.participation?.enabled &&
            !ownedAccountIds.has(account.id),
          )
          .map((account) => account.id),
      );
      selectedParticipationAccountIds = selectedParticipationAccountIds.filter((id) => eligibleAccountIds.has(id));
    } finally {
      instanceAccountsRefreshing = false;
    }
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
    if (created.length > 0) {
      const createdByID = new Map(created.map((instance) => [instance.id, instance]));
      const merged = browserInstances.map((item) => createdByID.get(item.id) ?? item);
      const knownIDs = new Set(merged.map((item) => item.id));
      browserInstances = [
        ...merged,
        ...created.filter((instance) => !knownIDs.has(instance.id)),
      ];
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
      browserWebviewLoadingIds = browserWebviewLoadingIds.filter((id) => id !== instance.id);
      browserWebviewReadyIds = browserWebviewReadyIds.filter((id) => id !== instance.id);
      browserCookieCheckingIds = browserCookieCheckingIds.filter((id) => id !== instance.id);
      browserCookieCheckedAt.delete(instance.id);
      browserFollowingLivePendingNativeIds = browserFollowingLivePendingNativeIds.filter((id) => id !== instance.id);
      browserRedPacketContextIds = browserRedPacketContextIds.filter((id) => id !== instance.id);
      browserPendingClose = null;
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

  async function openBrowserCloseConfirm(instance: BrowserInstance) {
    if (browserClosingId || browserPendingClose) return;
    browserLayoutRevision += 1;
    await queueBrowserNativeLayout(hideEmbeddedBrowsers);
    browserPendingClose = instance;
  }

  async function cancelBrowserCloseConfirm() {
    if (browserClosingId) return;
    browserPendingClose = null;
    browserLayoutRevision += 1;
    const revision = browserLayoutRevision;
    await tick();
    await new Promise<void>((resolve) => window.requestAnimationFrame(() => resolve()));
    await queueBrowserNativeLayout(() => syncEmbeddedBrowsers(revision));
  }

  async function openBrowserInstance(instance: BrowserInstance) {
    if (browserOpeningId) return;
    browserOpeningId = instance.id;
    browserError = "";
    try {
      // The card and the framed independent window use the same account-keyed
      // native data store. Flush the latest login before moving the visible
      // page surface into its own utility window.
      if (isTauriDesktop()) {
        browserCookieCheckedAt.set(instance.id, Date.now());
        const loginStateUpdated = await invoke<boolean>("sync_browser_account_cookie", {
          instanceId: instance.id,
          requireLoggedIn: true,
        });
        if (loginStateUpdated) accounts = await engineRequest<AccountItem[]>("account.list");
      }
      await releaseEmbeddedBrowser(instance);
      await invoke("open_browser_instance_window", { instanceId: instance.id });
      if (!browserIndependentWindowIds.includes(instance.id)) {
        browserIndependentWindowIds = [...browserIndependentWindowIds, instance.id];
      }
      showToast(`已打开「${instance.account_name}」的独立实例窗口`);
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

  function browserRedPacketLiveTarget(instance: BrowserInstance) {
    const followed = followingLiveSnapshot(instance)?.items.find((item) => /^\d{6,20}$/.test(item.web_rid));
    if (followed) return { webRID: followed.web_rid, label: followed.nickname || followed.title || followed.web_rid };
    const activeEvent = redPacketEvents.find(
      (event) => redPacketEventIsActive(event, redPacketClock) && /^\d{6,20}$/.test(event.web_rid || ""),
    );
    if (activeEvent) {
      return {
        webRID: activeEvent.web_rid!,
        label: activeEvent.streamer_name || activeEvent.room_name || activeEvent.web_rid!,
      };
    }
    const liveMonitor = redPacketMonitors.find(
      (monitor) => monitor.live_status === "live" && /^\d{6,20}$/.test(monitor.web_rid || monitor.room_id),
    );
    if (liveMonitor) {
      return {
        webRID: liveMonitor.web_rid || liveMonitor.room_id,
        label: liveMonitor.streamer_name || liveMonitor.name || liveMonitor.web_rid || liveMonitor.room_id,
      };
    }
    return null;
  }

  async function toggleBrowserRedPacketContext(instance: BrowserInstance) {
    if (browserRedPacketPreparingIds.includes(instance.id)) return;
	if (browserCookieExpired(instance)) return;
	const participationContext = browserParticipationContexts[instance.id];
	const challengeBlocked = browserParticipationBlocked(instance);
	const canResumePendingResult = Boolean(
		participationContext?.resumable && !participationContext.prepared && participationContext.pending_draw_count &&
		/^\d{6,20}$/.test(participationContext.pending_result_web_rid || ""),
	);
	// A challenge deliberately finishes the current Go task, but handling it
	// in the native page is an explicit recovery action. Allow the Gift control
	// to start a fresh task and clear that terminal challenge state; configured
	// stop limits remain a hard block until the user changes their settings.
	if (participationContext?.stopped && !canResumePendingResult && !challengeBlocked) {
		showToast(participationContext.stop_reason || "已达到红包参与停止条件，请先调整参与设置");
		return;
	}
    if (!isTauriDesktop()) {
      showToast("红包页面参与仅支持桌面客户端");
      return;
    }
	const enabled = browserParticipationContextEnabled(instance.id);
    if (enabled) {
      browserRedPacketPreparingIds = [...browserRedPacketPreparingIds, instance.id];
      browserError = "";
      try {
        await invoke<void>("stop_browser_red_packet_context", { instanceId: instance.id });
        browserRedPacketContextIds = browserRedPacketContextIds.filter((id) => id !== instance.id);
		void loadBrowserParticipationContexts();
        showToast(`已停止「${instance.account_name}」的红包页面参与，未发出的任务已取消`);
      } catch (error) {
        const message = error instanceof Error ? error.message : String(error);
        browserError = message;
        showToast(message);
      } finally {
        browserRedPacketPreparingIds = browserRedPacketPreparingIds.filter((id) => id !== instance.id);
      }
      return;
    }
    const target = canResumePendingResult
		? { webRID: participationContext!.pending_result_web_rid!, label: "待开奖记录直播间" }
		: browserRedPacketLiveTarget(instance);
    if (!target) {
      showToast("暂未找到正在直播的房间，请先刷新该账号的关注直播或启动直播间监测");
      return;
    }
    browserRedPacketPreparingIds = [...browserRedPacketPreparingIds, instance.id];
    browserError = "";
    try {
      await invoke<string>("prepare_browser_red_packet_context", {
        instanceId: instance.id,
        webRid: target.webRID,
		resultOnly: canResumePendingResult,
		allowChallengeRecovery: challengeBlocked,
      });
      if (!participationContext?.stopped && !browserRedPacketContextIds.includes(instance.id)) {
        browserRedPacketContextIds = [...browserRedPacketContextIds, instance.id];
      }
	  void Promise.all([loadBrowserParticipationContexts(), loadSidebarActivities(), loadAccounts(false)]);
      showToast(canResumePendingResult
		? `「${instance.account_name}」已恢复待开奖记录查询，不会参与新的红包`
		: `「${instance.account_name}」已进入「${target.label}」，红包接口将使用真实页面签名`);
    } catch (error) {
      browserRedPacketContextIds = browserRedPacketContextIds.filter((id) => id !== instance.id);
      const message = error instanceof Error ? error.message : String(error);
      browserError = message;
      showToast(message);
    } finally {
      browserRedPacketPreparingIds = browserRedPacketPreparingIds.filter((id) => id !== instance.id);
    }
  }

  async function startBrowserRedPacketFromBatch(instance: BrowserInstance) {
	const context = browserParticipationContexts[instance.id];
	if (context?.accepting || context?.active || context?.task_active || browserRedPacketContextIds.includes(instance.id)) return false;
	const account = accounts.find((item) => item.id === instance.account_id);
	if (!account || account.cookie_status === "expired" || !account.roles.includes("participation") || account.participation?.last_red_packet_status === "challenge_blocked") return false;
	const target = browserRedPacketLiveTarget(instance);
	if (!target || browserRedPacketPreparingIds.includes(instance.id)) return false;
	const apiWasEnabled = Boolean(account.participation?.red_packet_api_enabled);
	let runtimeAcquired = false;
	browserRedPacketPreparingIds = [...browserRedPacketPreparingIds, instance.id];
	try {
		const admission = await engineRequest<BrowserAdmission>("browser.runtime.acquire", {
			instance_id: instance.id,
		});
		browserCapacity = admission.capacity;
		updateBrowserRuntimeState(instance.id, admission.state, admission.queue_position ?? 0);
		if (!admission.granted) return false;
		runtimeAcquired = true;
		if (!apiWasEnabled) {
			const updated = await engineRequest<AccountItem>("account.set_red_packet_api_enabled", {
				account_id: account.id,
				enabled: true,
			});
			accounts = accounts.map((item) => item.id === account.id ? updated : item);
		}
		await invoke<string>("prepare_browser_red_packet_context", {
			instanceId: instance.id,
			webRid: target.webRID,
			resultOnly: false,
			allowChallengeRecovery: false,
		});
		if (!browserRedPacketContextIds.includes(instance.id)) {
			browserRedPacketContextIds = [...browserRedPacketContextIds, instance.id];
		}
		return true;
	} catch {
		if (runtimeAcquired) {
			browserCapacity = await engineRequest<BrowserCapacity>("browser.runtime.release", {
				instance_id: instance.id,
			}).catch(() => browserCapacity);
			updateBrowserRuntimeState(instance.id, "stopped");
		}
		if (!apiWasEnabled) {
			const restored = await engineRequest<AccountItem>("account.set_red_packet_api_enabled", {
				account_id: account.id,
				enabled: false,
			}).catch(() => null);
			if (restored) accounts = accounts.map((item) => item.id === account.id ? restored : item);
		}
		return false;
	} finally {
		browserRedPacketPreparingIds = browserRedPacketPreparingIds.filter((id) => id !== instance.id);
	}
  }

  async function executeParticipationBatch(execution?: ParticipationScheduleExecution) {
	closeParticipationTaskMenu();
	if (participationBatchRunning) return;
	if (!isTauriDesktop()) {
		showToast("红包参与任务仅支持桌面客户端");
		scheduleEmbeddedBrowserSync();
		return;
	}
	participationBatchRunning = true;
	browserError = "";
	let started = 0;
	let skipped = 0;
	let monitorPreparationWarning = "";
	const startedAccountIDs: string[] = [];
	try {
		// Scheduled runs normally reach this point after the Go-side prewarm.
		// Immediate runs, zero-minute settings, or a missed prewarm still get a
		// final native check without allowing a monitoring-account issue to
		// consume the separate participation batch.
		try {
			await engineRequest("red_packet_monitor.start_all");
			void loadRedPacketMonitors();
		} catch (error) {
			monitorPreparationWarning = error instanceof Error ? error.message : String(error);
		}
		await Promise.all([loadAccounts(false), loadBrowserParticipationContexts()]);
		for (const instance of browserInstances) {
			if (await startBrowserRedPacketFromBatch(instance)) {
				started += 1;
				startedAccountIDs.push(instance.account_id);
			} else skipped += 1;
		}
		await engineRequest("red_packet_participation.batch_result", {
			schedule_id: execution?.schedule_id || "",
			mode: execution?.mode || "immediate",
			started,
			skipped,
			account_ids: startedAccountIDs,
		});
		await Promise.all([loadBrowserParticipationContexts(), loadSidebarActivities(), loadAccounts(false)]);
		const prefix = execution ? `${participationScheduleModeLabel(execution.mode)}计划已执行` : "红包参与任务已启动";
		showToast(`${prefix}：成功 ${started} 个${skipped > 0 ? `，跳过 ${skipped} 个` : ""}${monitorPreparationWarning ? "；直播间监测未能全部补启" : ""}`);
	} catch (error) {
		const message = error instanceof Error ? error.message : String(error);
		browserError = message;
		showToast(message);
	} finally {
		participationBatchRunning = false;
		scheduleEmbeddedBrowserSync();
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

	async function toggleAccountRedPacketAPI(account: AccountItem) {
		if (redPacketAPITogglingAccountIds.includes(account.id)) return;
		if (accountCookieStatus(account, "participation") === "expired") return;
		const previous = Boolean(account.participation?.red_packet_api_enabled);
		const enabled = !previous;
		redPacketAPITogglingAccountIds = [...redPacketAPITogglingAccountIds, account.id];
		accounts = accounts.map((item) => item.id === account.id
			? { ...item, participation: { ...item.participation, enabled: item.participation?.enabled ?? true, red_packet_api_enabled: enabled } }
			: item);
		try {
			const updated = await engineRequest<AccountItem>("account.set_red_packet_api_enabled", {
				account_id: account.id,
				enabled,
			});
			accounts = accounts.map((item) => item.id === account.id ? updated : item);
			showToast(enabled ? `已将「${account.nickname || account.name}」加入红包接口参与池` : `已将「${account.nickname || account.name}」移出红包接口参与池`);
		} catch (error) {
			accounts = accounts.map((item) => item.id === account.id
				? { ...item, participation: { ...item.participation, enabled: item.participation?.enabled ?? true, red_packet_api_enabled: previous } }
				: item);
			showToast(error instanceof Error ? error.message : String(error));
		} finally {
			redPacketAPITogglingAccountIds = redPacketAPITogglingAccountIds.filter((id) => id !== account.id);
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

  async function refresh() {
    if (refreshing) return;
    refreshing = true;
    const startedAt = performance.now();
    try {
      if (activeView === "browsers") {
        await Promise.all([loadBrowserInstances(), loadAccounts(false)]);
      } else if (managementTab === "redpackets") {
        await loadRedPacketEvents();
      } else if (managementTab === "rooms") {
        await loadRooms(false);
      } else if (managementTab === "participation-records") {
        await Promise.all([loadParticipationRecords(), loadParticipationRuntimeLogs()]);
      } else {
        await loadAccounts(false);
      }
      await loadSidebarActivities();
      const remaining = 260 - (performance.now() - startedAt);
      if (remaining > 0) await new Promise<void>((resolve) => window.setTimeout(resolve, remaining));
      showToast("最新状态已刷新");
    } catch (error) {
      showToast(error instanceof Error ? error.message : String(error));
    } finally {
      refreshing = false;
    }
  }

  function showToast(message: string) {
    if (toastTimer !== undefined) window.clearTimeout(toastTimer);
    toast = message;
    toastTimer = window.setTimeout(() => {
      toast = "";
      toastTimer = undefined;
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
    let unlistenParticipationLogReady: (() => void) | undefined;
    let unlistenParticipationLogClear: (() => void) | undefined;
    let unlistenBrowserWebviewReady: (() => void) | undefined;
    let unlistenBrowserWebviewLoadError: (() => void) | undefined;
    let unlistenBrowserInstanceWindowClosed: (() => void) | undefined;
    if ("__TAURI_INTERNALS__" in window) {
      void listen<string>("engine://message", (event) => handleEngineMessage(event.payload))
        .then((unlisten) => {
          unlistenEngine = unlisten;
          engineListenerReady = true;
          void loadLicenseStatus();
          void loadRemoteSyncStatus(false);
          void loadAccounts(false);
          void loadRedPacketMonitors();
		  void loadRedPacketEvents();
		  void loadSidebarActivities();
		  void loadParticipationSchedules();
          if (activeView === "accounts") {
            void loadRooms();
          }
          if (activeView === "browsers") {
			void Promise.all([loadAccounts(), loadBrowserInstances()]).then(() => claimDueParticipationSchedules());
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
      void listen("participation-log://ready", () => void emitParticipationLogState()).then((unlisten) => {
		unlistenParticipationLogReady = unlisten;
	  });
      void listen("participation-log://clear", async () => {
		try {
			await engineRequest("red_packet_participation.clear_logs");
			participationRuntimeLogs = [];
			void emitParticipationLogState();
		} catch (error) {
			showToast(error instanceof Error ? error.message : String(error));
		}
	  }).then((unlisten) => {
		unlistenParticipationLogClear = unlisten;
	  });
      void listen<BrowserWebviewEvent>("browser-webview://ready", (event) => {
        const instanceId = event.payload.instance_id?.trim();
        if (!instanceId) return;
        browserWebviewLoadingIds = browserWebviewLoadingIds.filter((id) => id !== instanceId);
        if (!browserWebviewReadyIds.includes(instanceId)) {
          browserWebviewReadyIds = [...browserWebviewReadyIds, instanceId];
        }
        const instance = browserInstances.find((item) => item.id === instanceId);
        if (instance) {
          browserCookieCheckedAt.delete(instanceId);
          void syncEmbeddedBrowserCookies();
          void loadBrowserFollowingLive(instance, true);
        }
        // The page can finish while a grid reflow is in progress. Reconcile
        // against the latest card bounds instead of trusting the geometry
        // captured when the child WebView was first created.
        scheduleEmbeddedBrowserSync();
      }).then((unlisten) => {
        unlistenBrowserWebviewReady = unlisten;
      });
      void listen<BrowserWebviewEvent>("browser-webview://load-error", (event) => {
        const instanceId = event.payload.instance_id?.trim();
        if (!instanceId) return;
        browserWebviewLoadingIds = browserWebviewLoadingIds.filter((id) => id !== instanceId);
        browserWebviewErrors = {
          ...browserWebviewErrors,
          [instanceId]: event.payload.message || "真实浏览器加载失败",
        };
      }).then((unlisten) => {
        unlistenBrowserWebviewLoadError = unlisten;
      });
      void listen<BrowserInstanceWindowEvent>("browser-instance-window://closed", (event) => {
        const instanceId = event.payload.instance_id?.trim();
        if (!instanceId) return;
        browserIndependentWindowIds = browserIndependentWindowIds.filter((id) => id !== instanceId);
        browserWebviewReadyIds = browserWebviewReadyIds.filter((id) => id !== instanceId);
        void loadBrowserInstances().finally(() => scheduleEmbeddedBrowserSync());
      }).then((unlisten) => {
        unlistenBrowserInstanceWindowClosed = unlisten;
      });
    }

    window.addEventListener("pointermove", resizeSidebar);
    window.addEventListener("pointerdown", closeFloatingMenus);
    document.addEventListener("contextmenu", suppressNativeContextMenu);
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
	  void loadSidebarActivities();
      if (managementTab === "participation-records") {
		void loadParticipationRecords();
		void loadParticipationRuntimeLogs();
	  }
    }, 5000);
    const redPacketClockTimer = window.setInterval(() => {
      redPacketClock = Date.now();
    }, 1000);
	const participationScheduleTimer = window.setInterval(() => {
	  void claimDueParticipationSchedules();
	}, 2000);
    const browserStatusTimer = window.setInterval(() => {
      void pollBrowserInstanceStatuses();
	  void loadBrowserParticipationContexts();
    }, 2000);
    const followingRoomSyncTimer = window.setInterval(() => {
      if (!engineListenerReady) return;
      if (browserInstances.length > 0) {
        // A room-list reload only reads the last persisted snapshot. Refresh
        // the followed-live feeds themselves so instance attribution and the
        // current live/offline flags keep advancing while the app stays open.
        void loadBrowserFollowingLives(browserInstances);
        return;
      }
      if (activeView === "accounts") void loadRooms(false);
    }, 60000);
    const browserCookieSyncTimer = window.setInterval(() => {
      void syncEmbeddedBrowserCookies();
    }, 5000);

    return () => {
      window.removeEventListener("pointermove", resizeSidebar);
      window.removeEventListener("pointerdown", closeFloatingMenus);
      document.removeEventListener("contextmenu", suppressNativeContextMenu);
      window.removeEventListener("pointerup", stopSidebarResize);
      window.removeEventListener("pointercancel", stopSidebarResize);
      window.removeEventListener("blur", stopSidebarResize);
      window.removeEventListener("resize", scheduleEmbeddedBrowserSync);
      window.removeEventListener("resize", scheduleAccountRebindSync);
      document.removeEventListener("scroll", scheduleEmbeddedBrowserSync, true);
      shellResizeObserver.disconnect();
      window.clearInterval(redPacketRefreshTimer);
      window.clearInterval(redPacketClockTimer);
	  window.clearInterval(participationScheduleTimer);
      window.clearInterval(browserStatusTimer);
      window.clearInterval(followingRoomSyncTimer);
      window.clearInterval(browserCookieSyncTimer);
      window.clearTimeout(browserColumnSyncTimer);
      window.cancelAnimationFrame(browserWebviewSyncFrame);
      window.cancelAnimationFrame(accountRebindSyncFrame);
      void releaseEmbeddedBrowsers();
      unlistenEngine?.();
      unlistenMonitorLogReady?.();
      unlistenMonitorLogClear?.();
      unlistenParticipationLogReady?.();
      unlistenParticipationLogClear?.();
      unlistenBrowserWebviewReady?.();
      unlistenBrowserWebviewLoadError?.();
      unlistenBrowserInstanceWindowClosed?.();
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
  class:windows-platform={isWindowsPlatform}
  class:detached-page-window={isDetachedPageWindow}
  class="app-shell"
  style={`--sidebar-width:${isDetachedPageWindow || sidebarCollapsed ? 0 : sidebarWidth}px`}
>
  {#if !isDetachedPageWindow}
  <aside class="sidebar">
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div class="window-strip" data-tauri-drag-region onpointerdown={startWindowDrag}>
      {#if !isWindowsPlatform}
        <button
          class="icon-button sidebar-toggle"
          aria-label={sidebarCollapsed ? "展开侧栏" : "收起侧栏"}
          data-tooltip={sidebarCollapsed ? "展开侧栏" : "收起侧栏"}
          data-tooltip-placement="right"
          onclick={toggleSidebar}
        >
          <SidebarSimple size={15} weight="regular" />
        </button>
      {/if}
    </div>

    <nav class="main-nav" aria-label="主要导航">
      {#each navItems as item}
        <button
          class:active={activeView === item.key}
          class="nav-item"
          aria-current={activeView === item.key ? "page" : undefined}
          onclick={(event) => handleNavClick(event, item.key)}
          onauxclick={(event) => handleNavAuxClick(event, item.key)}
          oncontextmenu={(event) => showNavContextMenu(event, item.key)}
        >
          <svelte:component this={item.icon} size={19} weight="regular" />
          <span>{item.label}</span>
          {#if item.key === "browsers" && browserParticipationTaskRunning}
			<span class="nav-task-running-dot" aria-hidden="true"></span>
		  {/if}
          {#if item.key === "browsers"}<span class="nav-count">{browserInstances.length}</span>{/if}
        </button>
      {/each}
    </nav>

    <div class="quick-list sidebar-data-overview">
      <p class="section-label">数据概览</p>
      {#if runningRedPacketMonitorCount > 0}
        <button class="quick-row" onclick={() => openManagementTab("rooms")}>
          <span>直播间正在监测</span>
          <strong class="quick-value">{runningRedPacketMonitorCount}</strong>
        </button>
      {/if}
      <button class="quick-row" onclick={() => openManagementTab("redpackets")}>
        <span>红包发放中</span>
        <strong class="quick-value">{activeRedPacketCount}</strong>
      </button>
      <button class="quick-row" onclick={() => openManagementTab("participation-records")}>
        <span>参与/中奖总次数</span>
        <strong class="quick-value">{participationOverview.join_count}/{participationOverview.win_count}</strong>
      </button>
      <button class="quick-row" onclick={() => openManagementTab("participation-records")}>
        <span>中奖总钻数</span>
        <strong class="quick-value">{formatParticipationDiamondTotal(participationOverview.win_diamonds)}</strong>
      </button>
      {#if expiredParticipationAccountCount > 0}
        <button class="quick-row warning" onclick={() => showExpiredAccounts("participation")}>
          <span>参与账号 CK 失效</span>
          <strong class="quick-value">{expiredParticipationAccountCount}</strong>
        </button>
      {/if}
      {#if expiredMonitoringAccountCount > 0}
        <button class="quick-row warning" onclick={() => showExpiredAccounts("monitoring")}>
          <span>监测账号 CK 失效</span>
          <strong class="quick-value">{expiredMonitoringAccountCount}</strong>
        </button>
      {/if}
    </div>

    <div class="sidebar-recent-activity-shell">
      <p class="section-label sidebar-recent-activity-title">最近活动</p>
      <div class="sidebar-recent-activity-body">
      <div class="quick-list sidebar-recent-activity" role="region" aria-label="最近活动记录" use:observeRecentActivityScroller onscroll={syncRecentActivityScrollbar}>
        {#if recentActivityItems.length === 0}
          <p class="recent-activity-empty">暂无活动</p>
        {:else}
          {#each recentActivityItems as activity (activity.id)}
            <div class="recent-activity-item">
              <div class="recent-activity-head">
                <div class="quick-row recent-activity-row static">
                  <span class:live={activity.tone === "live"} class="quick-status">
                    <svelte:component this={activity.icon} size={14} weight={activity.tone === "live" ? "fill" : "regular"} />
                  </span>
                  <span class="recent-activity-copy">
                    <span>{activity.label}</span>
                    <span class="recent-activity-meta">
                      <small>{activity.time}</small>
                      {#if activity.kind === "participation_batch_executed" && activity.active}
                        <button
                          class="recent-activity-inline-action"
                          aria-label="停止本批次"
                          data-tooltip="停止本批次"
                          data-tooltip-placement="bottom"
                          disabled={Boolean(stoppingSidebarActivityID)}
                          onclick={() => void stopSidebarParticipationBatch(activity.id, activity.accountIDs)}
                        >
                          {#if stoppingSidebarActivityID === activity.id}<ArrowClockwise class="spinning" size={11} />{:else}<Pause size={11} weight="fill" />{/if}
                        </button>
                      {/if}
                      {#if activity.accountIDs.length > 0}
                        <button
                          class="recent-activity-inline-action"
                          aria-label="查看参与任务详情"
                          data-tooltip="查看详情"
                          data-tooltip-placement="bottom"
                          onclick={() => void openSidebarActivity(activity.id)}
                        ><ClipboardText size={11} /></button>
                      {/if}
                    </span>
                  </span>
                </div>
              </div>
            </div>
          {/each}
        {/if}
      </div>
      {#if recentActivityScrollHeight > recentActivityClientHeight + 1}
        {@const thumbPercent = Math.max(0, Math.min(1, recentActivityClientHeight / recentActivityScrollHeight))}
        {@const scrollPercent = Math.max(0, Math.min(1, recentActivityScrollTop / Math.max(1, recentActivityScrollHeight - recentActivityClientHeight)))}
        <div
          bind:this={recentActivityScrollbarTrack}
          class="recent-activity-scrollbar"
          aria-hidden="true"
          onclick={scrollRecentActivityFromTrack}
        >
          <span
            role="presentation"
            class:dragging={recentActivityScrollbarDragPointer >= 0}
            style={`height:max(42px, ${thumbPercent * 100}%);top:${scrollPercent * 100}%;transform:translateY(-${scrollPercent * 100}%)`}
            onpointerdown={startRecentActivityScrollbarDrag}
          ></span>
        </div>
      {/if}
      </div>
    </div>

    <div class="sidebar-footer">
      <span class="brand-mark"><img src={appIconUrl} alt="" /></span>
      <span class="brand-copy">
        <span class="brand-title-row">
          <strong>福宝控制台</strong>
          {#if licenseStatus.edition === "专业版" && remoteSyncStatusLoaded}
            <button
              class:configured={remoteSyncStatus.configured}
              class="center-library-status-button"
              aria-label={remoteSyncStatus.configured ? "中心库已绑定，打开授权管理" : "中心库未绑定，打开授权管理"}
              data-tooltip={remoteSyncStatus.configured ? "中心库已绑定" : "中心库未绑定"}
              data-tooltip-placement="top"
              onclick={openLicenseModal}
            ><CloudArrowDown size={10} weight="bold" /></button>
          {/if}
        </span>
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
          {#if licenseStatus.state === "active" && licenseDaysRemaining !== null}
            <span
              class="license-expiry-brief"
              data-tooltip={`到期时间 ${formatLicenseDate(licenseStatus.expires_at, "")}`}
              data-tooltip-placement="top"
            >剩余{licenseDaysRemaining}天</span>
          {/if}
        </span>
      </span>
      <button
        class="icon-button"
        aria-label="打开设置"
        data-tooltip="设置"
        data-tooltip-placement="top"
        onclick={openParticipationSettings}
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
  {/if}

  <main class="main-panel">
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <header class="topbar" data-tauri-drag-region onpointerdown={startWindowDrag}>
      {#if !isWindowsPlatform && !isDetachedPageWindow && sidebarCollapsed}
        <button
          class="icon-button collapsed-sidebar-toggle"
          aria-label={sidebarCollapsed ? "展开侧栏" : "收起侧栏"}
          data-tooltip={sidebarCollapsed ? "展开侧栏" : "收起侧栏"}
          data-tooltip-placement="bottom"
          onclick={toggleSidebar}
        >
          <SidebarSimple size={15} weight="regular" />
        </button>
      {/if}
      <div class="title-group" data-tauri-drag-region>
        <div class="title-line" data-tauri-drag-region>
          <span class="title-icon" data-tauri-drag-region>
            {#if activeView === "browsers"}<Browser size={18} />
            {:else}<UserFocus size={18} />{/if}
          </span>
          <h1 data-tauri-drag-region>{viewMeta[activeView].title}</h1>
          {#if activeView === "browsers"}
            <span class="menu-anchor participation-task-anchor">
              <button
                class="participation-task-trigger"
                aria-haspopup="menu"
                aria-expanded={participationTaskMenuOpen}
                disabled={participationBatchRunning}
                onclick={toggleParticipationTaskMenu}
              >
                {#if participationBatchRunning}<ArrowClockwise class="spinning" size={11} />{:else}<Gift size={11} weight="fill" />{/if}
                <span>{participationBatchRunning ? "抢包中…" : "抢包"}</span>
                {#if participationTaskMenuOpen}<CaretLeft size={9} />{:else}<CaretRight size={9} />{/if}
              </button>
              <span
                class:open={participationTaskMenuOpen}
                class="participation-task-expand"
                aria-hidden={!participationTaskMenuOpen}
                inert={!participationTaskMenuOpen}
              >
                <span class="participation-task-menu" role="menu">
                  <button role="menuitem" onclick={() => void executeParticipationBatch()}>
                    <Play size={15} weight="fill" /><span>立即执行</span>
                  </button>
                  <button role="menuitem" onclick={() => openParticipationSchedule("once")}>
                    <ClockCountdown size={15} /><span>指定日期</span>
                  </button>
                  <button role="menuitem" onclick={() => openParticipationSchedule("daily")}>
                    <Radio size={15} /><span>每天固定时间</span>
                  </button>
                  <button role="menuitem" onclick={() => openParticipationSchedule("interval")}>
                    <ArrowClockwise size={15} /><span>间隔执行</span>
                  </button>
                </span>
              </span>
            </span>
            {#if !participationTaskMenuOpen && (participationBatchRunning || browserParticipationRuntime.accounts > 0)}
              <button
                class="browser-participation-runtime topbar-participation-runtime"
                aria-label="查看实际红包参与情况"
                data-tooltip="查看参与记录"
                data-tooltip-placement="bottom"
                onclick={() => openManagementTab("participation-records")}
              >
                <span class="browser-participation-runtime-state"><i></i>{participationBatchRunning && browserParticipationRuntime.accounts === 0 ? "正在准备参与上下文" : "参与任务进行中"}</span>
                {#if browserParticipationRuntime.accounts > 0}
                  <span>{browserParticipationRuntime.accounts} 个账号</span>
                  <span>就绪 {browserParticipationRuntime.prepared}/{browserParticipationRuntime.accounts}</span>
                  <span>可参与 {browserParticipationRuntime.accepting}</span>
                  <span>已参与 {browserParticipationRuntime.joined}</span>
                  <span>待开奖 {browserParticipationRuntime.pending}</span>
                  <span>中奖 {browserParticipationRuntime.won}</span>
                {/if}
              </button>
            {/if}
          {/if}
        </div>
        <p data-tauri-drag-region>
          {#if activeView === "accounts"}
            {accountSubtitle}
          {:else}
            {#if browserCapacity}
              <span class="browser-subtitle-copy" data-tauri-drag-region>{browserInstances.length} 个实例 · {browserCapacity.running} 个运行 ·</span>
              <span
                class="browser-recommended-limit"
                data-tooltip={browserRecommendedLimitTooltip(browserCapacity)}
                data-tooltip-placement="top"
              >建议上限 {browserCapacity.recommended_limit}</span>
              {#if browserCapacity.waiting > 0}<span class="browser-subtitle-copy" data-tauri-drag-region>· {browserCapacity.waiting} 个等待</span>{/if}
            {:else}
              {browserSubtitle}
            {/if}
            <span data-tauri-drag-region>·</span>
            <span
              class="browser-resource-usage"
              data-tauri-drag-region
              data-tooltip={browserCapacity ? browserResourceUsageTooltip(browserCapacity) : "正在读取本机资源占用"}
              data-tooltip-placement="bottom"
            >CPU {browserCapacity ? browserCPUUsagePercent(browserCapacity) : "--"}% · 内存 {browserCapacity ? browserMemoryUsagePercent(browserCapacity) : "--"}%</span>
            {#if participationSchedules.length > 0}
              <button class="participation-schedule-manage-trigger" onclick={openParticipationScheduleManager}>
                <ClockCountdown size={11} />
                <span>管理计划</span>
                <small>{participationSchedules.length}</small>
              </button>
            {/if}
          {/if}
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
                oninput={scheduleRoomSearch}
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
                <span class="import-target-label">账号添加到 <strong>{roleLabel(currentAccountImportRole())}</strong></span>
                {#if currentAccountImportRole() === "participation"}
                  <div class="import-group-picker">
                    <button type="button" class="import-group-trigger" aria-haspopup="menu" aria-expanded={importGroupMenuOpen} onclick={() => (importGroupMenuOpen = !importGroupMenuOpen)}>
                      <FolderOpen size={15} />
                      <span>{participationGroupName(importParticipationGroupId)}</span>
                      <CaretDown size={11} />
                    </button>
                    {#if importGroupMenuOpen}
                      <div class="import-group-options" role="menu" aria-label="选择参与账号导入分组">
                        <button class:active={!importParticipationGroupId} role="menuitemradio" aria-checked={!importParticipationGroupId} onclick={() => {
                          importParticipationGroupId = "";
                          importGroupMenuOpen = false;
                        }}><span>未分组</span>{#if !importParticipationGroupId}<CheckCircle size={13} weight="fill" />{/if}</button>
                        {#each participationGroups as group}
                          <button class:active={importParticipationGroupId === group.id} role="menuitemradio" aria-checked={importParticipationGroupId === group.id} onclick={() => {
                            importParticipationGroupId = group.id;
                            importGroupMenuOpen = false;
                          }}><span>{group.name}</span>{#if importParticipationGroupId === group.id}<CheckCircle size={13} weight="fill" />{/if}</button>
                        {/each}
                        <span class="menu-divider"></span>
                        <div class="group-create-row">
                          <input bind:value={participationGroupDraft} maxlength="24" placeholder="新建分组" aria-label="新分组名称" onkeydown={(event) => event.key === "Enter" && createParticipationGroup("import")} />
                          <button type="button" aria-label="新增分组" disabled={!participationGroupDraft.trim() || participationGroupCreating} onclick={() => createParticipationGroup("import")}><Plus size={14} /></button>
                        </div>
                      </div>
                    {/if}
                  </div>
                {/if}
                <button role="menuitem" disabled={accountImportBusy} onclick={pasteAccountCookie}><ClipboardText size={17} /><span>粘贴 Cookie</span></button>
                <button role="menuitem" disabled={accountImportBusy} onclick={startQrLogin}><QrCode size={17} /><span>扫码登录并添加</span></button>
                <button role="menuitem" disabled={accountImportBusy} onclick={() => chooseAccountFiles(false)}><FileArrowUp size={17} /><span>批量导入文件</span></button>
                <button role="menuitem" disabled={accountImportBusy} onclick={() => chooseAccountFiles(true)}><FolderOpen size={17} /><span>批量导入文件夹</span></button>
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
          <button
            class:limit-reached={browserInstanceCreateLimitReached}
            class="primary-action"
            aria-disabled={browserInstanceCreateLimitReached}
            aria-label={browserInstanceCreateLimitReached ? "免费版浏览器实例数量已达上限" : "新建实例"}
            data-tooltip={browserInstanceCreateLimitReached ? "免费版最多创建 1 个实例" : undefined}
            data-tooltip-placement="bottom"
            onclick={openInstanceModal}
          >
            <Plus size={14} weight="bold" />
            <span>新建实例</span>
          </button>
        {/if}
      </div>
    </header>

    <section
      class:account-content={activeView === "accounts"}
      class:import-popover-open={activeView === "accounts" && importMenuOpen}
      class="content"
    >
      {#if activeView === "browsers"}
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
            <div class="browser-runtime-strip">
              <div class:critical={browserCapacity.resources.pressure === "critical"} class="browser-capacity-note">
                <ClockCountdown size={14} />
                <span>{browserCapacity.message}</span>
                <small>运行 {browserCapacity.running}/{browserCapacity.effective_limit} · 建议 {browserCapacity.recommended_limit}</small>
              </div>
            </div>
          {/if}
          <div
            class="card-grid simple-grid browser-instance-grid"
            style={`--browser-columns:${browserColumns}`}
          >
            {#each visibleBrowserInstances as item, index}
			  {@const participationContext = browserParticipationContexts[item.id]}
			  {@const participationStopped = Boolean(participationContext?.stopped)}
			  {@const participationEnabled = browserParticipationContextEnabled(item.id)}
			  {@const cookieExpired = browserCookieExpired(item)}
			  {@const participationBlocked = browserParticipationBlocked(item)}
			  {@const followedLive = followingLiveSnapshot(item)}
			  {@const followedLiveLoading = browserFollowingLiveLoadingIds.includes(item.id)}
			  {@const pendingResultCanResume = Boolean(participationContext?.resumable && !participationContext?.prepared && participationContext?.pending_draw_count && /^\d{6,20}$/.test(participationContext?.pending_result_web_rid || ""))}
			  {@const participationTip = participationBlocked
				? "验证码/安全验证拦截；处理完成后可点击重新启动参与"
				: participationStopped
				? `${participationContext?.stop_reason || "已达到红包参与停止条件"}${pendingResultCanResume ? "；点击仅恢复待开奖记录查询" : ""}`
				: pendingResultCanResume ? "恢复待开奖记录查询" : participationEnabled ? "停止红包页面参与" : "进入直播间并启用红包页面参与"}
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
                    {:else if browserWebviewMountingIds.includes(item.id) || browserWebviewLoadingIds.includes(item.id)}
                      <span class="browser-preview-loading loading">
                        <span class="browser-loading-icon"><ArrowClockwise class="spinning" size={15} /></span>
                        <strong>{browserWebviewReadyIds.includes(item.id) ? "正在恢复实例" : "正在加载实例"}</strong>
                        <small>
                          {browserWebviewReadyIds.includes(item.id)
                            ? "正在恢复上次页面与账号登录状态"
                            : `正在准备「${item.account_name}」的独立浏览器`}
                        </small>
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
                  <span class={`simple-icon browser-tone-${index % 3}`}><Browser size={12} /></span>
                  <div class="simple-title browser-account-title">
                    <h2>
                      <span use:truncatedTooltip={item.account_name}>{item.account_name}</span>
                      <small use:truncatedTooltip={browserAccountDouyinId(item)}>{browserAccountDouyinId(item)}</small>
                      {#if cookieExpired}
                        <em
                          class="browser-cookie-expired"
                          use:portalTooltip={"CK 已失效"}
                          data-tooltip="CK 已失效"
                          data-tooltip-placement="bottom"
                        >CK</em>
					  {:else if participationBlocked}
						<em
						  class="browser-participation-blocked"
						  use:portalTooltip={browserParticipationBlockedMessage(item)}
						  data-tooltip={browserParticipationBlockedMessage(item)}
						  data-tooltip-placement="bottom"
						>拦截</em>
                      {/if}
                    </h2>
                  </div>
                  <div class="browser-card-actions">
                    {#if browserCookieCheckingIds.includes(item.id)}
                      <span class="browser-cookie-checking" role="status" aria-label="正在检测账号登录状态">
                        <ArrowClockwise class="spinning" size={11} />
                      </span>
                    {/if}
                    {#if followedLive || followedLiveLoading}
                      <button
                        class="browser-following-live-button"
                        class:has-live={Boolean(followedLive?.total)}
                        use:portalTooltip={followingLiveTooltip(item)}
                        aria-label={followedLive
                          ? `查看 ${followedLive.total} 个正在直播的关注账号`
                          : "正在读取直播中的关注账号"}
                        data-tooltip={followingLiveTooltip(item)}
                        data-tooltip-placement="bottom"
                        disabled={!followedLive}
                        onclick={() => openFollowingLive(item)}
                      >
                        {#if followedLiveLoading}
                          <ArrowClockwise class="spinning" size={10} />
                        {:else}
                          <Radio size={10} weight="fill" />
                        {/if}
                        {#if followedLive}<span>{followedLive.total}</span>{/if}
                      </button>
                    {/if}
                    {#if !cookieExpired}
                      <button
                        class="secondary-button browser-red-packet-button"
                        class:enabled={participationEnabled}
                        class:limit-reached={participationStopped}
                        aria-label={participationTip}
                        data-tooltip={participationTip}
                        data-tooltip-placement="left"
                        disabled={browserRedPacketPreparingIds.includes(item.id) || browserClosingId === item.id}
                        onclick={() => toggleBrowserRedPacketContext(item)}
                      >
                        {#if browserRedPacketPreparingIds.includes(item.id)}
                          <ArrowClockwise class="spinning" size={13} />
                        {:else}
                          <Gift size={13} weight={participationEnabled ? "fill" : "regular"} />
                        {/if}
                      </button>
                    {/if}
                    <button
                      class="secondary-button browser-close-button"
                      aria-label="关闭实例"
                      data-tooltip="关闭实例"
                      data-tooltip-placement="left"
                      disabled={browserClosingId === item.id}
                      onclick={() => openBrowserCloseConfirm(item)}
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
            红包 <span>{activeRedPacketCount}</span>
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
            class:active={managementTab === "participation-records"}
            role="tab"
            aria-selected={managementTab === "participation-records"}
            onclick={() => selectManagementTab("participation-records")}
          >
            参与记录 <span>{participationRecords.length}</span>
          </button>
          <button
            type="button"
            class:expanded={monitoringManagementExpanded}
            class:active={managementTab === "rooms" || managementTab === "monitoring"}
            class="monitor-management-toggle"
            aria-label={monitoringManagementExpanded ? "收起监测管理" : "展开监测管理"}
            aria-expanded={monitoringManagementExpanded}
            data-tooltip={managementTab === "rooms" || managementTab === "monitoring" ? "监测管理正在使用" : monitoringManagementExpanded ? "收起监测管理" : "展开监测管理"}
            data-tooltip-placement="top"
            onclick={toggleMonitoringManagement}
          >
            <Radio size={15} weight={monitoringManagementExpanded ? "fill" : "regular"} />
            <CaretRight class="monitor-management-caret" size={11} />
          </button>
          {#if monitoringManagementExpanded}
            <button
              class:active={managementTab === "rooms"}
              class="monitor-management-tab"
              role="tab"
              aria-selected={managementTab === "rooms"}
              onclick={() => selectManagementTab("rooms")}
            >
              直播间 <span>{roomTotalCount}</span>
            </button>
            <button
              class:active={managementTab === "monitoring"}
              class="monitor-management-tab"
              role="tab"
              aria-selected={managementTab === "monitoring"}
              onclick={() => selectManagementTab("monitoring")}
            >
              监测账号 <span>{monitoringAccounts.length}</span>
            </button>
          {/if}
          {#if managementTab === "redpackets"}
            <div class="red-packet-history-entry">
              <button
                type="button"
                aria-pressed={redPacketHistoryVisible}
                data-tooltip={redPacketHistoryVisible ? "返回未过期红包" : "查看已过期红包"}
                data-tooltip-placement="top"
                onclick={toggleRedPacketHistory}
              >
                {#if redPacketHistoryVisible}<Gift size={13} />{:else}<ClockCountdown size={13} />{/if}
                <span>{redPacketHistoryVisible ? "当前红包" : "历史红包"}</span>
                <small>{redPacketHistoryVisible ? activeRedPacketCount : historicalRedPacketCount}</small>
              </button>
            </div>
          {:else if managementTab === "rooms"}
            <div class="room-monitor-bulk-actions" aria-label="直播间红包监测批量操作">
              <div class="menu-anchor room-cleanup-settings-anchor">
                <button
                  class:active={roomCleanupMenuOpen || roomSettings.auto_recycle_low_live_enabled || roomSettings.auto_recycle_no_packet_enabled || roomSettings.auto_recycle_imported_no_packet_enabled}
                  class="monitor-log-button"
                  aria-label="直播间自动清理设置"
                  aria-haspopup="menu"
                  aria-expanded={roomCleanupMenuOpen}
                  data-tooltip={roomCleanupMenuOpen ? undefined : "自动清理设置"}
                  data-tooltip-placement="bottom"
                  onclick={toggleRoomCleanupMenu}
                ><GearSix size={13} /></button>
                {#if roomCleanupMenuOpen}
                  <div class="floating-menu room-cleanup-menu" role="menu" aria-label="直播间自动清理设置">
                    <strong>自动清理</strong>
                    <label class="room-cleanup-rule">
                      <input type="checkbox" bind:checked={roomSettings.auto_recycle_low_live_enabled} />
                      <span>累计开播不超过</span>
                      <span class="room-cleanup-number"><input type="number" min="0" max="100000" step="1" bind:value={roomSettings.auto_recycle_max_live_sessions} /><em>次</em></span>
                    </label>
                    <small>从未获得有效检测结果的直播间会跳过</small>
                    <label class="room-cleanup-rule">
                      <input type="checkbox" bind:checked={roomSettings.auto_recycle_no_packet_enabled} />
                      <span>近</span>
                      <span class="room-cleanup-number"><input type="number" min="1" max="3650" step="1" bind:value={roomSettings.auto_recycle_no_packet_days} /><em>天</em></span>
                      <span>未发红包</span>
                    </label>
                    <small>从首次有效检测或最后发现红包开始计算</small>
                    <label class="room-cleanup-rule room-cleanup-rule-wide">
                      <input type="checkbox" bind:checked={roomSettings.auto_recycle_imported_no_packet_enabled} />
                      <span>清理本地导入且从未发现红包的直播间</span>
                    </label>
                    <small>包括尚未有效检测过的本地导入记录；不影响关注列表和中心库数据</small>
                    {#if roomCleanupSettingsBusy || roomCleanupProgress}
                      <div class="room-cleanup-progress" aria-live="polite">
                        <div><span>执行进度</span><strong>{roomCleanupTotal > 0 ? Math.min(100, Math.round(roomCleanupProcessed / roomCleanupTotal * 100)) : 100}%</strong></div>
                        <progress max={Math.max(1, roomCleanupTotal)} value={roomCleanupTotal > 0 ? Math.min(roomCleanupProcessed, roomCleanupTotal) : 1}></progress>
                        {#if roomCleanupProgress}
                          <small>已扫描 {roomCleanupProgress.scanned} 个，已处理 {roomCleanupProgress.cleaned} 个（回收站 {roomCleanupProgress.recycled}{#if permanentCenterRoomAccess()}，中心库清除 {roomCleanupProgress.excluded}{/if}）</small>
                        {:else}
                          <small>正在保存规则并扫描直播间…</small>
                        {/if}
                      </div>
                    {/if}
                    {#if roomCleanupSettingsError}<p>{roomCleanupSettingsError}</p>{/if}
                    <div class="room-cleanup-menu-footer">
                      <button type="button" disabled={roomCleanupSettingsBusy} onclick={() => (roomCleanupMenuOpen = false)}>取消</button>
                      <button type="button" class="primary" disabled={roomCleanupSettingsBusy} onclick={() => void executeRoomCleanup()}>{roomCleanupSettingsBusy ? "执行中…" : roomCleanupProgress ? "再次执行" : "执行清理"}</button>
                    </div>
                  </div>
                {/if}
              </div>
              <button
                class="monitor-log-button room-recycle-entry"
                aria-label={permanentCenterRoomAccess()
                  ? `打开直播间回收站与中心库排除${recycledRooms.length + centerExcludedRooms.length ? `，共 ${recycledRooms.length + centerExcludedRooms.length} 条记录` : ""}`
                  : `打开直播间回收站${recycledRooms.length ? `，共 ${recycledRooms.length} 条记录` : ""}`}
                data-tooltip={permanentCenterRoomAccess() ? "回收站与中心库排除" : "直播间回收站"}
                data-tooltip-placement="bottom"
                onclick={openRoomRecycleBin}
              >
                <Archive size={13} />
                {#if recycledRooms.length + (permanentCenterRoomAccess() ? centerExcludedRooms.length : 0) > 0}<span>{recycledRooms.length + (permanentCenterRoomAccess() ? centerExcludedRooms.length : 0)}</span>{/if}
              </button>
              <button class="monitor-log-button" aria-label="查看红包监测运行日志" data-tooltip="查看红包监测运行日志" data-tooltip-placement="top" onclick={openMonitorRuntimeLog}>
                <TerminalWindow size={13} />
              </button>
              {#if canStartAnyRedPacketMonitor || redPacketBatchAction === "start"}
                <button
                  class="secondary-button compact-action monitor-bulk-start"
                  disabled={redPacketBatchAction !== "" || redPacketMonitorSummary.enabled === 0}
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
          {:else if managementTab === "participation-records"}
            <div class="room-monitor-bulk-actions" aria-label="红包参与日志操作">
              <button class="monitor-log-button" aria-label="查看红包参与详细日志" data-tooltip="查看红包参与详细日志" data-tooltip-placement="top" onclick={openParticipationRuntimeLog}>
                <TerminalWindow size={13} />
              </button>
            </div>
          {:else if managementTab === "participation" || managementTab === "monitoring"}
            <div class="account-tab-filters">
            {#if managementTab === "participation"}
              <div class="menu-anchor filter-anchor group-filter-anchor">
                <button class="filter-button" aria-haspopup="menu" aria-expanded={participationGroupMenuOpen} onclick={() => {
                  participationGroupMenuOpen = !participationGroupMenuOpen;
                  statusMenuOpen = false;
                }}>
                  <FolderOpen size={15} />
                  <span>{participationGroupFilterLabel(participationGroupFilter)}</span>
                  <CaretDown size={12} />
                </button>
                {#if participationGroupMenuOpen}
                  <div class="floating-menu group-filter-menu" role="menu">
                    {#each [["all", "全部分组"], ["ungrouped", "未分组"], ...participationGroups.map((group) => [group.id, group.name])] as option}
                      <button class:active={participationGroupFilter === option[0]} role="menuitemradio" aria-checked={participationGroupFilter === option[0]} onclick={() => {
                        participationGroupFilter = option[0];
                        participationGroupMenuOpen = false;
                      }}>
                        <span>{option[1]}</span>
                        {#if participationGroupFilter === option[0]}<CheckCircle size={14} weight="fill" />{/if}
                      </button>
                    {/each}
                    <span class="menu-divider"></span>
                    <div class="group-create-row">
                      <input bind:value={participationGroupDraft} maxlength="24" placeholder="新建分组" aria-label="新分组名称" onkeydown={(event) => event.key === "Enter" && createParticipationGroup("filter")} />
                      <button type="button" aria-label="新增分组" disabled={!participationGroupDraft.trim() || participationGroupCreating} onclick={() => createParticipationGroup("filter")}><Plus size={14} /></button>
                    </div>
                  </div>
                {/if}
              </div>
            {/if}
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
            </div>
          {/if}
        </div>
        <section
          class:fill-panel={managementTab === "redpackets" || managementTab === "rooms" || managementTab === "participation-records" || visibleAccounts.length > 8}
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
                {#if redPacketHistoryVisible}<ClockCountdown size={28} />{:else}<Gift size={28} />{/if}
                <strong>{query ? "没有匹配的红包" : redPacketHistoryVisible ? "暂无历史红包" : "暂无未过期红包"}</strong>
                <span>{query ? "换个关键词试试" : redPacketHistoryVisible ? "过期红包会保留在这里" : "新发现且尚未过期的红包会显示在这里"}</span>
              </div>
            {:else}
              <div class="red-packet-event-list">
                <div class="red-packet-event-head">
				  <span>红包</span><span>直播间 / 监测账号</span><span>奖品与时效</span><span>发现时间</span><span>参与人数</span>
                </div>
                {#each visibleRedPacketEvents as event}
                  {@const expiry = redPacketEventExpiryParts(event, redPacketClock)}
                  <article class="red-packet-event-row">
                    <div class="red-packet-event-identity">
                      <span class="room-avatar red-packet-avatar">
                        {#if redPacketEventIsDiamond(event)}
                          <DiamondGem size={18} weight="fill" />
                        {:else}
                          <Gift size={17} weight="fill" />
                        {/if}
                      </span>
                      <div>
                        <strong>{event.title || "直播间红包"}</strong>
						<small>{event.packet_id || "红包事件"} · {event.data_source === "center" ? "来源于中心库" : event.source === "luckybox_api" ? "红包接口" : "实时检测"}</small>
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
					  <small>{event.streamer_name || "尚未读取主播"} · {event.data_source === "center" ? "中心库同步" : `账号 ${event.account_name || event.account_id || "待解析"}`}</small>
                    </div>
					<div class="red-packet-event-prize">
					  <strong>{event.prize || "奖品待解析"}</strong>
					  <small class="red-packet-event-expiry">
						{#if expiry.countdown}
						  <span class:expired={expiry.expired} class="red-packet-event-countdown">{expiry.countdown}</span>
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
                <strong>{query || roomSourceFilter !== "all" ? "没有匹配的直播间" : "还没有直播间数据"}</strong>
                <span>{query || roomSourceFilter !== "all" ? "调整搜索或来源筛选条件" : "点击右上角“导入数据”，从旧福宝复制直播间列表"}</span>
              </div>
            {:else}
              <div class="room-list">
                <div class="room-list-head">
                  <span>直播间</span>
                  <span class="room-source-head menu-anchor">
                    <span>主播 / 房间标识</span>
                    <button
                      type="button"
                      class:active={roomSourceFilter !== "all"}
                      class="room-live-sort-button"
                      aria-label={`直播间来源筛选：${roomSourceFilterLabel(roomSourceFilter)}`}
                      aria-haspopup="menu"
                      aria-expanded={roomSourceMenuOpen}
                      data-tooltip={roomSourceMenuOpen ? undefined : roomSourceFilterLabel(roomSourceFilter)}
                      data-tooltip-placement="bottom"
                      onclick={toggleRoomSourceMenu}
                    ><FunnelSimple size={11} weight="bold" /></button>
                    {#if roomSourceMenuOpen}
                      <div class="floating-menu room-sort-menu room-source-menu" role="menu" aria-label="直播间来源筛选">
                        {#each roomSourceFilterOptions() as option}
                          <button
                            type="button"
                            class:active={roomSourceFilter === option[0]}
                            role="menuitemradio"
                            aria-checked={roomSourceFilter === option[0]}
                            onclick={() => selectRoomSourceFilter(option[0])}
                          >
                            <span>{option[1]}</span>
                            {#if roomSourceFilter === option[0]}<CheckCircle size={13} weight="fill" />{/if}
                          </button>
                        {/each}
                      </div>
                    {/if}
                  </span>
                  <span class="room-list-status-head menu-anchor">
                    <span>直播与红包状态</span>
                    <button
                      type="button"
                      class:active={roomSortMode !== "default"}
                      class="room-live-sort-button"
                      aria-label={`直播间排序：${roomSortModeLabel(roomSortMode)}`}
                      aria-haspopup="menu"
                      aria-expanded={roomSortMenuOpen}
                      data-tooltip={roomSortMenuOpen ? undefined : roomSortModeLabel(roomSortMode)}
                      data-tooltip-placement="bottom"
                      onclick={toggleRoomSortMenu}
                    ><ArrowsDownUp size={11} weight="bold" /></button>
                    {#if roomSortMenuOpen}
                      <div class="floating-menu room-sort-menu" role="menu" aria-label="直播间排序">
                        {#each [
                          ["default", "默认顺序"],
                          ["instance-first", "实例优先"],
                          ["live-first", "开播优先"],
                          ["recent-live", "最近开播"],
                          ["recent-redpacket", "红包优先"],
                        ] as option}
                          <button
                            type="button"
                            class:active={roomSortMode === option[0]}
                            role="menuitemradio"
                            aria-checked={roomSortMode === option[0]}
                            onclick={() => selectRoomSortMode(option[0] as RoomSortMode)}
                          >
                            <span>{option[1]}</span>
                            {#if roomSortMode === option[0]}<CheckCircle size={13} weight="fill" />{/if}
                          </button>
                        {/each}
                      </div>
                    {/if}
                  </span>
                  <span>操作</span>
                </div>
                {#each visibleRooms as room}
                    {@const roomMonitor = roomMonitorFor(room, redPacketMonitors)}
                    {@const roomMonitorStatus = roomMonitor ? redPacketMonitorUiStatus(roomMonitor, redPacketMonitorOverrides) : ""}
                    {@const roomStatusValue = roomCombinedLiveStatus(room, roomMonitor, roomMonitorStatus, redPacketClock)}
                    {@const activeRoomRedPackets = roomActiveRedPacketSummary(room, redPacketClock)}
                    {@const roomLiveWebRID = roomOpenWebRID(room, roomMonitor)}
                    <article class="room-row">
                    <div class="room-identity">
                      <span class="room-avatar"><Radio size={18} weight="fill" /></span>
                      <div>
                        <div class="room-identity-title">
                          <strong>{roomDisplayName(room)}</strong>
                          {#if /^\d{6,24}$/.test(roomLiveWebRID)}
                            <button
                              type="button"
                              class="icon-button room-open-live-action"
                              aria-label="在浏览器中打开直播间"
                              data-tooltip="在浏览器中打开直播间"
                              data-tooltip-placement="top"
                              onclick={() => openRoomLiveRoom(room, roomMonitor)}
                            ><ArrowSquareOut size={11} /></button>
                          {/if}
                        </div>
                        <small>{room.web_rid ? `房间号 ${room.web_rid}` : `记录号 ${room.id}`}</small>
                      </div>
                    </div>
                    <div class="room-streamer-info">
                      <div class="room-streamer-title">
                        <strong>{roomMonitor?.streamer_name || room.streamer_name || "尚未读取主播"}</strong>
                        {#if activeRoomRedPackets.count > 0}
                          <span
                            class="room-red-packet-indicator"
                            aria-label={activeRoomRedPackets.tip}
                            data-tooltip={activeRoomRedPackets.tip}
                            data-tooltip-placement="top"
                          ><Gift size={10} weight="fill" /></span>
                        {/if}
                      </div>
                      <div
                        class="room-streamer-meta"
                        data-tooltip={roomSourceTooltip(room)}
                        data-tooltip-placement="top"
                      >
                        <small>
                          房间标识 {roomMonitor?.actual_room_id || room.actual_room_id || room.id} ·
                          <span class="room-follow-source">{roomSourceLabel(room)}</span>
                        </small>
                      </div>
                    </div>
                    <div class="room-live-state">
                      <span
                        class:running={roomStatusValue === "已开播"}
                        class:warning={roomStatusValue === "检测中" || roomStatusValue === "探测异常"}
                        class:muted={roomStatusValue === "已停用" || roomStatusValue === "未监测" || roomStatusValue === "未开播"}
                        class="status-pill"
                      ><span></span>{roomStatusValue}</span>
                      <small>{roomCombinedMonitorPhase(room, roomMonitor, roomMonitorStatus, redPacketClock)}</small>
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
                {#if rooms.length < roomTotalCount}
                  <div class="room-list-more">
                    <span>已显示 {rooms.length} / {roomTotalCount} 条</span>
                    <button onclick={showMoreRooms}>继续显示</button>
                  </div>
                {/if}
              </div>
            {/if}
          {:else if managementTab === "participation-records"}
            {#if participationRecordError}
              <div class="account-notice error">
                <WarningCircle size={17} />
                <span>{participationRecordError}</span>
                <button onclick={loadParticipationRecords}>重试</button>
              </div>
            {:else if participationRecordsLoading && participationRecords.length === 0}
              <div class="account-empty"><ArrowClockwise class="spinning" size={22} /><span>正在读取参与记录…</span></div>
            {:else if visibleParticipationRecords.length === 0}
              <div class="account-empty">
                <Gift size={28} />
                <strong>{query ? "没有匹配的参与记录" : "还没有参与记录"}</strong>
                <span>{query ? "换个关键词试试" : "开启参与账号的红包接口开关后，真实参与结果会保留在这里"}</span>
              </div>
            {:else}
              <div class="participation-record-list">
                <div class="participation-record-head">
                  <span>参与账号</span><span>红包 / 直播间</span><span>参与结果</span><span>接口</span><span>时间</span>
                </div>
                {#each visibleParticipationRecords as record}
                  {@const result = participationRecordStatus(record)}
                  <article class="participation-record-row">
                    <div class="participation-record-account">
                      <span class="participation-record-icon"><UserCircle size={16} weight="fill" /></span>
                      <div><strong>{record.account_name || "已删除账号"}</strong><small>账号记录 {record.account_id.slice(0, 8)}</small></div>
                    </div>
                    <div class="participation-record-event">
                      <div class="participation-record-event-title">
                        <strong>{record.title || record.prize || "直播红包"}</strong>
                        {#if record.web_rid && /^\d{6,24}$/.test(record.web_rid)}
                          <button
                            class="icon-button room-open-live-action"
                            aria-label="打开参与记录对应直播间"
                            data-tooltip="打开直播间"
                            data-tooltip-placement="top"
                            onclick={() => openLiveRoomByWebRID(record.web_rid!)}
                          ><ArrowSquareOut size={11} /></button>
                        {/if}
                      </div>
                      <small>{record.room_name || record.streamer_name || `直播间 ${record.web_rid || record.room_id || "未知"}`} · {record.prize || "奖品待解析"}</small>
                    </div>
                    <div class="participation-record-result">
                      <span class={`participation-result-pill ${result.tone}`} data-tooltip={record.message || result.label} data-tooltip-placement="top">{result.label}</span>
                      <small>{record.status === "not_won" ? "已开奖" : record.message || "等待结果"}</small>
                    </div>
                    <div class="participation-record-endpoint">
                      <strong>{participationRecordEndpoint(record)}</strong>
                      <small>{record.attempt_count ? `${record.attempt_count} 次请求` : "尚未请求"}</small>
                    </div>
                    <span class="participation-record-time" data-tooltip={participationRecordExactTime(record)} data-tooltip-placement="left">{formatMonitorTime(record.updated_at)}</span>
                  </article>
                {/each}
                {#if visibleParticipationRecords.length < filteredParticipationRecords.length}
                  <div class="room-list-more">
                    <span>已显示 {visibleParticipationRecords.length} / {filteredParticipationRecords.length} 条</span>
                    <button onclick={() => (participationRecordRenderLimit += 300)}>继续显示</button>
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
              <div class:participation-grid={accountRole === "participation"} class="account-list-head">
                <span>账号</span>
                <span>分类</span>
                {#if accountRole === "participation"}<span>分组</span>{/if}
                <span class="account-list-status-head menu-anchor">
                  <span>状态与数据</span>
                  <button
                    type="button"
                    class:active={currentAccountSortMode() !== "default"}
                    class="room-live-sort-button"
                    aria-label={`${roleLabel(accountRole)}排序：${accountSortModeLabel(currentAccountSortMode())}`}
                    aria-haspopup="menu"
                    aria-expanded={accountSortMenuOpen}
                    data-tooltip={accountSortMenuOpen ? undefined : accountSortModeLabel(currentAccountSortMode())}
                    data-tooltip-placement="bottom"
                    onclick={toggleAccountSortMenu}
                  ><ArrowsDownUp size={11} weight="bold" /></button>
                  {#if accountSortMenuOpen}
                    <div class="floating-menu account-sort-menu" role="menu" aria-label={`${roleLabel(accountRole)}排序`}>
                      {#each accountSortOptions() as option}
                        <button
                          type="button"
                          class:active={currentAccountSortMode() === option[0]}
                          role="menuitemradio"
                          aria-checked={currentAccountSortMode() === option[0]}
                          onclick={() => selectAccountSortMode(option[0])}
                        >
                          <span>{option[1]}</span>
                          {#if currentAccountSortMode() === option[0]}<CheckCircle size={13} weight="fill" />{/if}
                        </button>
                      {/each}
                    </div>
                  {/if}
                </span>
                <span>操作</span>
              </div>
              {#each visibleAccounts as account}
                <article class:participation-grid={accountRole === "participation"} class="account-row">
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
                            type="button"
                            aria-label="移除参与分类"
                            data-tooltip="移除参与分类"
                            data-tooltip-placement="top"
                            onclick={(event) => {
                              event.stopPropagation();
                              void removeAccountRole(account, "participation");
                            }}
                          ><X size={8} weight="bold" /></button>
                        {/if}
                      </span>
                    {/if}
                    {#if hasAccountRole(account, "monitoring")}
                      <span class="role-badge monitoring">
                        <span class="role-badge-label">监测</span>
                        {#if account.roles.length > 1}
                          <button
                            type="button"
                            aria-label="移除监测分类"
                            data-tooltip="移除监测分类"
                            data-tooltip-placement="top"
                            onclick={(event) => {
                              event.stopPropagation();
                              void removeAccountRole(account, "monitoring");
                            }}
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
                  {#if accountRole === "participation"}
                    <div class="account-group-cell menu-anchor">
                      <button type="button" class="account-group-button" aria-haspopup="menu" aria-expanded={accountGroupMenuId === account.id} onclick={() => (accountGroupMenuId = accountGroupMenuId === account.id ? "" : account.id)}>
                        <FolderOpen size={13} />
                        <span>{participationGroupName(account.participation?.group_id)}</span>
                        <CaretDown size={10} />
                      </button>
                      {#if accountGroupMenuId === account.id}
                        <div class="floating-menu account-group-menu" role="menu">
                          <button class:active={!account.participation?.group_id} role="menuitemradio" aria-checked={!account.participation?.group_id} onclick={() => setAccountParticipationGroup(account, "")}><span>未分组</span>{#if !account.participation?.group_id}<CheckCircle size={13} weight="fill" />{/if}</button>
                          {#each participationGroups as group}
                            <button class:active={account.participation?.group_id === group.id} role="menuitemradio" aria-checked={account.participation?.group_id === group.id} onclick={() => setAccountParticipationGroup(account, group.id)}><span>{group.name}</span>{#if account.participation?.group_id === group.id}<CheckCircle size={13} weight="fill" />{/if}</button>
                          {/each}
                        </div>
                      {/if}
                    </div>
                  {/if}
                  <div class="account-health">
                    <div class="account-health-status">
					  <span class:warning={accountStatus(account, redPacketClock) === "冷却中"} class:blocked={accountStatus(account, redPacketClock) === "拦截"} class:expired={accountStatus(account, redPacketClock) === "CK 失效"} class="status-pill" data-tooltip={accountHealthMessage(account, accountRole) || undefined} data-tooltip-placement="top">
						<span></span>{accountStatus(account, redPacketClock)}
					  </span>
					  {#if accountStatus(account, redPacketClock) === "CK 失效"}
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
                      use:portalTooltip={accountRole === "monitoring" ? accountMonitoringUsageTip(account) : ""}
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
					{#if accountRole === "participation" && accountCookieStatus(account, "participation") !== "expired"}
						<button
							class:enabled={Boolean(account.participation?.red_packet_api_enabled)}
							class="account-red-packet-api-button"
							aria-label={account.participation?.red_packet_api_enabled ? "关闭红包接口参与" : "加入红包接口参与池"}
							aria-pressed={Boolean(account.participation?.red_packet_api_enabled)}
							data-tooltip={account.participation?.red_packet_api_enabled ? "关闭红包接口参与" : "加入红包接口参与池"}
							data-tooltip-placement="left"
							disabled={redPacketAPITogglingAccountIds.includes(account.id)}
							onclick={() => toggleAccountRedPacketAPI(account)}
						><Gift size={14} weight={account.participation?.red_packet_api_enabled ? "fill" : "regular"} /></button>
					{/if}
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

{#if navContextMenu}
  <div
    class="nav-context-menu"
    role="menu"
    aria-label="页面菜单"
    style={`left:${navContextMenu.x}px;top:${navContextMenu.y}px`}
  >
    <button role="menuitem" onclick={() => openViewInNewWindow(navContextMenu!.key)}>
      <ArrowSquareOut size={14} />
      <span>在新窗口打开</span>
    </button>
  </div>
{/if}

{#if sidebarActivityDetail}
  <div
    class="modal-backdrop"
    role="presentation"
    onclick={(event) => event.currentTarget === event.target && void closeSidebarActivity()}
  >
    <dialog class="modal sidebar-activity-modal" open aria-labelledby="sidebar-activity-modal-title">
      <div class="modal-head">
        <div>
          <span class="modal-icon sidebar-activity-modal-icon"><ClipboardText size={17} /></span>
          <h2 id="sidebar-activity-modal-title">活动详情</h2>
        </div>
        <button class="icon-button" aria-label="关闭" onclick={() => void closeSidebarActivity()}><X size={17} /></button>
      </div>

      <div class="sidebar-activity-summary">
        <div>
          <small>活动内容</small>
          <strong>{sidebarActivityDetail.label}</strong>
        </div>
        <span class:active={sidebarActivityDetail.active} class="sidebar-activity-status">
          {sidebarActivityDetail.active ? "进行中" : sidebarActivityDetail.stoppedAt ? "已停止" : "已结束"}
        </span>
      </div>

      <dl class="sidebar-activity-timestamps">
        <div><dt>启动时间</dt><dd>{formatLicenseDate(sidebarActivityDetail.createdAt, "未知")}</dd></div>
        {#if sidebarActivityDetail.finishedAt}
          <div><dt>完成时间</dt><dd>{formatLicenseDate(sidebarActivityDetail.finishedAt, "未知")}</dd></div>
        {/if}
        {#if sidebarActivityDetail.stoppedAt}
          <div><dt>停止时间</dt><dd>{formatLicenseDate(sidebarActivityDetail.stoppedAt, "未知")}</dd></div>
        {/if}
        {#if sidebarActivityDetail.finishedAt}
          <div><dt>参与结果</dt><dd>参与 {sidebarActivityDetail.joinCount} 次 · 中奖 {sidebarActivityDetail.winCount} 次 / {formatParticipationDiamondTotal(sidebarActivityDetail.winDiamonds)} 钻</dd></div>
        {/if}
      </dl>

      <div class="sidebar-activity-account-list">
        <p>参与账号 <span>{sidebarActivityDetail.accountIDs.length}</span></p>
        {#each sidebarActivityDetail.accountIDs as accountID}
          {@const accountSummary = sidebarActivityAccountSummary(sidebarActivityDetail, accountID)}
          <div class="sidebar-activity-account-row">
            <span><UserCircle size={15} />{accountSummary?.account_name || sidebarActivityAccountName(accountID)}</span>
            <small class:active={sidebarActivityAccountState(accountID, sidebarActivityDetail.active, sidebarActivityDetail.stoppedAt) === "参与中"}>
              {#if accountSummary}
                参与 {accountSummary.join_count} · 中奖 {accountSummary.win_count} / {formatParticipationDiamondTotal(accountSummary.win_diamonds)} 钻
              {:else}
                {sidebarActivityAccountState(accountID, sidebarActivityDetail.active, sidebarActivityDetail.stoppedAt)}
              {/if}
            </small>
          </div>
        {/each}
      </div>

      <div class="modal-actions">
        <button class="secondary-button" onclick={() => void closeSidebarActivity()}>关闭</button>
      </div>
    </dialog>
  </div>
{/if}

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
            <span>{updateDownloaded ? "新版本已下载，等待安装" : "正在下载并验签…"}</span>
            <strong>{updateProgress.percent}%</strong>
          </div>
          <progress max="100" value={updateProgress.percent}></progress>
          <small>
            {formatFileSize(updateProgress.downloaded)}
            {#if updateProgress.total > 0} / {formatFileSize(updateProgress.total)}{/if}
          </small>
        </div>
      {:else if updateStatus.size > 0}
        <p class="update-package-meta">安装包 {formatFileSize(updateStatus.size)} · 下载后自动校验完整性</p>
      {:else}
        <p class="update-package-meta">签名更新包 · 自动验签并安装</p>
      {/if}

      {#if updateError}<div class="license-error"><WarningCircle size={14} />{updateError}</div>{/if}
      <div class="modal-actions">
        <button class="secondary-button" disabled={updateDownloading || updateInstalling} onclick={closeUpdateModal}>稍后更新</button>
        {#if updateDownloaded}
          <button class="primary-action" disabled={updateInstalling} onclick={installAppUpdate}>
            <ArrowClockwise class={updateInstalling ? "spinning" : undefined} size={14} />
            {updateInstalling ? "正在安装…" : "安装并启动"}
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

{#if participationSettingsModalOpen}
  <div
    class="modal-backdrop"
    role="presentation"
    onclick={(event) => event.currentTarget === event.target && closeParticipationSettings()}
  >
    <dialog class="modal participation-settings-modal" open aria-labelledby="participation-settings-title">
      <div class="modal-head">
        <div>
          <span class="modal-icon participation-settings-icon"><GearSix size={18} weight="fill" /></span>
          <h2 id="participation-settings-title">设置</h2>
        </div>
        <button class="icon-button" aria-label="关闭" disabled={participationSettingsBusy} onclick={closeParticipationSettings}><X size={17} /></button>
      </div>

      <div class="settings-modal-scroll-shell">
        <div
          id="settings-modal-scroll"
          class="settings-modal-scroll"
          use:observeSettingsModalScroller
          onscroll={syncSettingsModalScrollbar}
        >
          <div class="general-settings-tabs" role="tablist" aria-label="设置分类">
            <button type="button" role="tab" aria-selected={settingsTab === "participation"} class:active={settingsTab === "participation"} onclick={() => settingsTab = "participation"}><Gift size={13} />红包参与</button>
            <button type="button" role="tab" aria-selected={settingsTab === "rooms"} class:active={settingsTab === "rooms"} onclick={() => settingsTab = "rooms"}><Radio size={13} />直播间</button>
            <button type="button" role="tab" aria-selected={settingsTab === "monitoring"} class:active={settingsTab === "monitoring"} onclick={() => settingsTab = "monitoring"}><SlidersHorizontal size={13} />监测设置</button>
          </div>

          {#if settingsTab === "participation"}
        <p class="participation-settings-intro">以下限制按每个参与账号独立计算，填 0 表示不限制。</p>
        <div class="participation-settings-list">
          <div class="participation-setting-row participation-packet-type-row">
            <span><strong>参与红包类型</strong><small>只分配符合所选类型的红包，默认仅参与钻石红包</small></span>
            <div class="participation-packet-type-options" role="radiogroup" aria-label="可以参与的红包类型">
              {#each [["all", "不限"], ["gift", "礼物红包"], ["diamond", "钻石红包"]] as option}
                <button
                  type="button"
                  role="radio"
                  aria-checked={participationSettings.packet_type === option[0]}
                  class:active={participationSettings.packet_type === option[0]}
                  disabled={participationSettingsBusy}
                  onclick={() => participationSettings.packet_type = option[0] as ParticipationPacketType}
                >{option[1]}</button>
              {/each}
            </div>
          </div>
          <div class="participation-setting-row participation-packet-type-row">
            <span><strong>参与哪些红包</strong><small>按每个参与账号自己的关注直播判断；优先模式会先等候关注主播红包，无可用关注快照时按不限处理</small></span>
            <div class="participation-packet-type-options" role="radiogroup" aria-label="参与红包的关注范围">
              {#each [["all", "不限"], ["follow_priority", "关注列表优先"], ["follow_only", "只参加关注主播"]] as option}
                <button
                  type="button"
                  role="radio"
                  aria-checked={participationSettings.follow_policy === option[0]}
                  class:active={participationSettings.follow_policy === option[0]}
                  disabled={participationSettingsBusy}
                  onclick={() => participationSettings.follow_policy = option[0] as ParticipationFollowPolicy}
                >{option[1]}</button>
              {/each}
            </div>
          </div>
          <label class="participation-setting-row">
            <span><strong>最低钻石</strong><small>能明确计算每份红包钻石数时，低于该门槛不参与</small></span>
            <span class="number-field"><input type="number" min="1" max="1000000" step="1" bind:value={participationSettings.minimum_diamonds} /><em>钻</em></span>
          </label>
          <label class="participation-setting-row">
            <span><strong>参与倒计时</strong><small>红包有效期剩余少于该秒数时不参与；填 0 表示不限制，但已过期红包仍不会参与</small></span>
            <span class="number-field"><input type="number" min="0" max="300" step="1" bind:value={participationSettings.participation_countdown_seconds} /><em>秒</em></span>
          </label>
          <label class="participation-setting-row">
            <span><strong>参与停止</strong><small>参与达到指定次数后，不再分配后续红包任务</small></span>
            <span class="number-field"><input type="number" min="0" max="100000" step="1" bind:value={participationSettings.stop_after_joins} /><em>次</em></span>
          </label>
          <label class="participation-setting-row">
            <span><strong>参与冷却</strong><small>参与成功后，等待指定秒数才能参与下一次</small></span>
            <span class="number-field"><input type="number" min="0" max="86400" step="1" bind:value={participationSettings.cooldown_seconds} /><em>秒</em></span>
          </label>
          <label class="participation-setting-row">
            <span><strong>中奖停止</strong><small>累计中奖达到指定次数后，不再分配后续红包任务</small></span>
            <span class="number-field"><input type="number" min="0" max="100000" step="1" bind:value={participationSettings.stop_after_wins} /><em>次</em></span>
          </label>
          <label class="participation-setting-row">
            <span><strong>开奖后查询</strong><small>开奖后等待指定秒数再查询开奖结果；填 0 表示立即查询</small></span>
            <span class="number-field"><input type="number" min="0" max="60" step="1" bind:value={participationSettings.draw_result_delay_seconds} /><em>秒</em></span>
          </label>
          <label class="participation-setting-row">
            <span><strong>最多查询次数</strong><small>开奖结果最多查询指定次数，全部无结果后立即使用钻石增量兜底</small></span>
            <span class="number-field"><input type="number" min="1" max="20" step="1" bind:value={participationSettings.draw_result_max_attempts} /><em>次</em></span>
          </label>
        </div>
          {:else if settingsTab === "rooms"}
        <p class="participation-settings-intro">直播间自动整理与参与任务预热均由 Go 引擎持久化执行，不依赖当前页面或实例卡片是否渲染。</p>
        <div class="participation-settings-list">
          <label class="participation-setting-row room-auto-recycle-setting">
            <span><strong>参与任务提前检查</strong><small>指定日期、每天固定时间和间隔执行会在每轮任务前检查直播间监测状态，未全部启动时自动执行“全部启动”；填 0 表示仅在执行时检查</small></span>
            <span class="number-field"><input type="number" min="0" max="1440" step="1" bind:value={roomSettings.participation_prewarm_minutes} /><em>分钟</em></span>
          </label>
          <label class="participation-setting-row room-auto-recycle-setting">
            <span><strong>自动回收未开播直播间</strong><small>连续指定天数的监测均明确确认未开播后，自动停止监测并移入回收站；填 0 关闭自动回收</small></span>
            <span class="number-field"><input type="number" min="0" max="3650" step="1" bind:value={roomSettings.auto_recycle_offline_days} /><em>天</em></span>
          </label>
        </div>
          {:else}
        <p class="participation-settings-intro">以下参数由 Go 引擎持久化并热更新正在运行的监测池；降低间隔或提高并发会增加接口压力，遇到限流时账号仍会自动冷却。</p>
        <div class="participation-settings-list monitoring-settings-list">
          <label class="participation-setting-row">
            <span><strong>全局请求间隔</strong><small>本机所有监测账号发起两次请求之间的最小间隔</small></span>
            <span class="number-field"><input type="number" min="40" max="2000" step="10" bind:value={monitoringSettings.global_request_interval_ms} /><em>毫秒</em></span>
          </label>
          <label class="participation-setting-row">
            <span><strong>单账号请求间隔</strong><small>同一监测账号发起两次请求之间的最小间隔</small></span>
            <span class="number-field"><input type="number" min="250" max="5000" step="50" bind:value={monitoringSettings.account_request_interval_ms} /><em>毫秒</em></span>
          </label>
          <label class="participation-setting-row">
            <span><strong>全局慢请求并发</strong><small>已按间隔发出的请求中，可同时等待响应的最大数量</small></span>
            <span class="number-field"><input type="number" min="1" max="128" step="1" bind:value={monitoringSettings.global_concurrency} /><em>条</em></span>
          </label>
          <label class="participation-setting-row">
            <span><strong>单账号慢请求并发</strong><small>每个监测账号可同时等待响应的最大请求数量</small></span>
            <span class="number-field"><input type="number" min="1" max="8" step="1" bind:value={monitoringSettings.account_concurrency} /><em>条</em></span>
          </label>
          <label class="participation-setting-row">
            <span><strong>原生探测窗口</strong><small>同时进入直播状态探测与红包查询流水线的任务上限</small></span>
            <span class="number-field"><input type="number" min="8" max="256" step="8" bind:value={monitoringSettings.probe_concurrency} /><em>个</em></span>
          </label>
        </div>
          {/if}

          {#if participationSettingsError}<div class="license-error"><WarningCircle size={14} />{participationSettingsError}</div>{/if}
        </div>
        {#if settingsModalScrollHeight > settingsModalClientHeight + 1}
          {@const thumbPercent = Math.min(1, settingsModalClientHeight / Math.max(settingsModalScrollHeight, 1))}
          {@const scrollPercent = Math.min(1, settingsModalScrollTop / Math.max(settingsModalScrollHeight - settingsModalClientHeight, 1))}
          <div bind:this={settingsModalScrollbarTrack} class="modal-scrollbar" aria-hidden="true" onclick={scrollSettingsModalFromTrack}>
            <span
              role="scrollbar"
              aria-label="设置内容滚动条"
              aria-controls="settings-modal-scroll"
              aria-valuemin="0"
              aria-valuemax={Math.max(0, settingsModalScrollHeight - settingsModalClientHeight)}
              aria-valuenow={Math.round(settingsModalScrollTop)}
              tabindex="0"
              class:dragging={settingsModalScrollbarDragPointer !== -1}
              style={`height:max(42px, ${thumbPercent * 100}%); top:${scrollPercent * 100}%; transform:translateY(-${scrollPercent * 100}%);`}
              onpointerdown={startSettingsModalScrollbarDrag}
            ></span>
          </div>
        {/if}
      </div>
      <div class="modal-actions">
        <button class="secondary-button" disabled={participationSettingsBusy} onclick={closeParticipationSettings}>取消</button>
        <button class="primary-action" disabled={participationSettingsBusy} onclick={saveParticipationSettings}>
          {#if participationSettingsBusy}<ArrowClockwise class="spinning" size={14} />{/if}
          {participationSettingsBusy ? "保存中…" : "保存设置"}
        </button>
      </div>
    </dialog>
  </div>
{/if}

{#if roomRecycleModalOpen}
  <div class="modal-backdrop" role="presentation" onclick={(event) => event.currentTarget === event.target && closeRoomRecycleBin()}>
    <dialog class="modal room-recycle-modal" open aria-labelledby="room-recycle-title">
      <div class="modal-head">
        <div>
          <span class="modal-icon room-recycle-icon"><Archive size={18} /></span>
          <h2 id="room-recycle-title">直播间清理记录</h2>
        </div>
        <button class="icon-button" aria-label="关闭" disabled={Boolean(roomRecycleBusyId)} onclick={closeRoomRecycleBin}><X size={17} /></button>
      </div>
      <div class="room-recycle-tabs" role="tablist" aria-label="直播间清理记录分类">
        <button class:active={roomRecycleView === "recycle"} role="tab" aria-selected={roomRecycleView === "recycle"} onclick={() => (roomRecycleView = "recycle")}>回收站 <span>{recycledRooms.length}</span></button>
        {#if permanentCenterRoomAccess()}
          <button class:active={roomRecycleView === "center-exclusions"} role="tab" aria-selected={roomRecycleView === "center-exclusions"} onclick={() => (roomRecycleView = "center-exclusions")}>中心库全局排除 <span>{centerExcludedRooms.length}</span></button>
        {/if}
      </div>
      <p class="participation-settings-intro">
        {roomRecycleView === "recycle"
          ? permanentCenterRoomAccess()
            ? "自动清理会先将直播间移入回收站；永久删除中心库关联房间时会同步进入全局排除。"
            : "自动清理会先将本地直播间移入回收站；恢复后保持未启动状态，永久删除只清除本机数据。"
          : "全局排除会从中心库清除直播间及红包数据，并拦截其他客户端再次上传；解除后恢复为未启动直播间。"}
      </p>
      <div class="room-recycle-list">
        {#if roomRecycleLoading && recycledRooms.length === 0 && centerExcludedRooms.length === 0}
          <div class="room-recycle-empty"><ArrowClockwise class="spinning" size={18} />正在读取回收站…</div>
        {:else if roomRecycleView === "recycle" && recycledRooms.length === 0}
          <div class="room-recycle-empty"><Archive size={20} /><span>回收站为空</span></div>
        {:else if roomRecycleView === "recycle"}
          {#each recycledRooms as room}
            <article class="room-recycle-row">
              <span class="room-avatar"><Radio size={16} weight="fill" /></span>
              <div class="room-recycle-copy">
                <strong>{roomDisplayName(room)}</strong>
                <small>{room.web_rid ? `房间号 ${room.web_rid}` : `记录号 ${room.id}`} · {formatRecycleTime(room.recycled_at)}</small>
                <p>{room.recycle_reason || "已进入直播间回收站"}</p>
              </div>
              <div class="room-recycle-actions">
                <button class="icon-button restore" aria-label="恢复到直播间列表" data-tooltip="恢复到直播间列表" data-tooltip-placement="top" disabled={Boolean(roomRecycleBusyId)} onclick={() => restoreRecycledRoom(room)}>
                  <ArrowClockwise class={roomRecycleBusyId === room.id && !roomPendingPermanentDelete ? "spinning" : undefined} size={14} />
                </button>
                <button class="icon-button permanent-delete" aria-label="永久删除直播间" data-tooltip="永久删除" data-tooltip-placement="top" disabled={Boolean(roomRecycleBusyId)} onclick={() => requestPermanentDeleteRoom(room)}>
                  <Trash size={14} />
                </button>
              </div>
            </article>
          {/each}
        {:else if centerExcludedRooms.length === 0}
          <div class="room-recycle-empty"><ShieldCheck size={20} /><span>中心库全局排除为空</span></div>
        {:else}
          {#each centerExcludedRooms as room}
            <article class="room-recycle-row">
              <span class="room-avatar"><Radio size={16} weight="fill" /></span>
              <div class="room-recycle-copy">
                <strong>{room.streamer_name || room.name || `直播间 ${room.web_rid.slice(-4)}`}</strong>
                <small>房间号 {room.web_rid} · {formatRecycleTime(room.excluded_at)}</small>
                <p>{room.reason || "已从中心库全局排除，后续上传将被拦截"}</p>
              </div>
              <div class="room-recycle-actions">
                <button class="icon-button restore" aria-label="解除排除并恢复直播间" data-tooltip="解除排除并恢复" data-tooltip-placement="top" disabled={Boolean(roomRecycleBusyId)} onclick={() => restoreCenterExcludedRoom(room)}>
                  <ArrowClockwise class={roomRecycleBusyId === room.id ? "spinning" : undefined} size={14} />
                </button>
              </div>
            </article>
          {/each}
        {/if}
      </div>
      {#if roomRecycleError}<div class="license-error"><WarningCircle size={14} />{roomRecycleError}</div>{/if}
      <div class="modal-actions"><button class="secondary-button" disabled={Boolean(roomRecycleBusyId)} onclick={closeRoomRecycleBin}>完成</button></div>
    </dialog>
  </div>
{/if}

{#if participationScheduleModalOpen}
  <div
    class="modal-backdrop"
    role="presentation"
    onclick={(event) => event.currentTarget === event.target && closeParticipationSchedule()}
  >
    <dialog
      bind:this={participationScheduleModalElement}
      class="modal participation-schedule-modal"
      style={`width:${participationScheduleModalWidth}px;height:${participationScheduleModalHeight}px;transform:translate(${participationScheduleModalX}px, ${participationScheduleModalY}px)`}
      open
      aria-labelledby="participation-schedule-title"
    >
      <div
        class="modal-head participation-schedule-modal-head"
        role="group"
        aria-label="执行计划窗口标题，可拖动"
        onpointerdown={startParticipationScheduleDrag}
        onpointermove={moveParticipationScheduleDrag}
        onpointerup={endParticipationScheduleDrag}
        onpointercancel={endParticipationScheduleDrag}
      >
        <div>
          <span class="modal-icon participation-schedule-icon"><ClockCountdown size={18} /></span>
          <h2 id="participation-schedule-title">{participationScheduleManaging ? "管理执行计划" : "新增执行计划"}</h2>
        </div>
        <button class="icon-button" aria-label="关闭" disabled={participationScheduleBusy} onclick={closeParticipationSchedule}><X size={17} /></button>
      </div>

      <div class="participation-schedule-modal-body">
        <p class="participation-settings-intro">{participationScheduleManaging ? "查看和删除已持久化的执行计划。拖动标题可移动面板，拖动右下角可调整大小。" : "计划由 Go 引擎持久化；触发时批量启动已登录实例的真实红包页面参与。"}</p>
        {#if !participationScheduleManaging}
          <div class="participation-schedule-modes" role="tablist" aria-label="调度方式">
            {#each ["once", "daily", "interval"] as mode}
              <button
                class:active={participationScheduleMode === mode}
                role="tab"
                aria-selected={participationScheduleMode === mode}
                onclick={() => {
                  participationScheduleMode = mode as ParticipationScheduleMode;
                  participationScheduleUnitMenuOpen = false;
                }}
              >{participationScheduleModeLabel(mode as ParticipationScheduleMode)}</button>
            {/each}
          </div>

          <div class="participation-schedule-fields">
        {#if participationScheduleMode === "once"}
          <label>
            <span><strong>执行日期</strong><small>到达指定日期和时间后执行一次</small></span>
            <input type="datetime-local" min={localDateTimeInput(new Date())} bind:value={participationScheduleRunAt} />
          </label>
        {:else if participationScheduleMode === "daily"}
          <label>
            <span><strong>每天固定时间</strong><small>每天在本机时间到达后执行一次</small></span>
            <input type="time" bind:value={participationScheduleDailyTime} />
          </label>
        {:else}
          <label>
            <span><strong>执行间隔</strong><small>保存后立即执行一轮，之后按此间隔再次调度</small></span>
            <span class="schedule-interval-field">
              <input type="number" min="1" max={participationScheduleIntervalUnit === "hours" ? 720 : 43200} step="1" bind:value={participationScheduleInterval} />
              <span class="menu-anchor schedule-unit-select">
                <button
                  class="schedule-unit-trigger"
                  type="button"
                  aria-label="间隔单位"
                  aria-haspopup="listbox"
                  aria-expanded={participationScheduleUnitMenuOpen}
                  onclick={() => (participationScheduleUnitMenuOpen = !participationScheduleUnitMenuOpen)}
                >
                  <span>{participationScheduleIntervalUnit === "hours" ? "小时" : "分钟"}</span>
                  <CaretDown size={12} />
                </button>
                {#if participationScheduleUnitMenuOpen}
                  <span class="floating-menu schedule-unit-menu" role="listbox" aria-label="选择间隔单位">
                    {#each [["minutes", "分钟"], ["hours", "小时"]] as option}
                      <button
                        class:active={participationScheduleIntervalUnit === option[0]}
                        type="button"
                        role="option"
                        aria-selected={participationScheduleIntervalUnit === option[0]}
                        onclick={() => {
                          participationScheduleIntervalUnit = option[0] as "minutes" | "hours";
                          participationScheduleUnitMenuOpen = false;
                        }}
                      >
                        <span>{option[1]}</span>
                        {#if participationScheduleIntervalUnit === option[0]}<CheckCircle size={13} weight="fill" />{/if}
                      </button>
                    {/each}
                  </span>
                {/if}
              </span>
            </span>
          </label>
        {/if}
          </div>
        {/if}

        {#if participationScheduleManaging}
          {#if participationSchedules.length > 0}
            <div class="participation-schedule-existing">
              <span class="schedule-section-title">已启用计划</span>
              {#each participationSchedules as schedule (schedule.id)}
                <div class="participation-schedule-row">
                  <span><strong>{participationScheduleModeLabel(schedule.mode)}</strong><small>{participationScheduleDescription(schedule)}</small></span>
                  <button
                    class="icon-button"
                    aria-label="删除执行计划"
                    data-tooltip="删除执行计划"
                    data-tooltip-placement="left"
                    disabled={participationScheduleBusy}
                    onclick={() => deleteParticipationSchedule(schedule)}
                  ><Trash size={14} /></button>
                </div>
              {/each}
            </div>
          {:else}
            <div class="participation-schedule-empty">暂无已启用的执行计划</div>
          {/if}
        {/if}

        {#if participationScheduleError}<div class="license-error"><WarningCircle size={14} />{participationScheduleError}</div>{/if}
      </div>
      <div class="modal-actions">
        {#if participationScheduleManaging}
          <button class="secondary-button" disabled={participationScheduleBusy} onclick={closeParticipationSchedule}>关闭</button>
        {:else}
          <button class="secondary-button" disabled={participationScheduleBusy} onclick={closeParticipationSchedule}>取消</button>
          <button class="primary-action" disabled={participationScheduleBusy} onclick={saveParticipationSchedule}>
            {#if participationScheduleBusy}<ArrowClockwise class="spinning" size={14} />{/if}
            {participationScheduleBusy ? "保存中…" : "保存计划"}
          </button>
        {/if}
      </div>
      <span
        class="participation-schedule-resize-handle"
        role="separator"
        aria-orientation="horizontal"
        aria-label="调整计划窗口大小"
        onpointerdown={startParticipationScheduleResize}
      ></span>
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
        <button class="icon-button" aria-label="关闭" disabled={licenseBusy || remoteSyncBusy} onclick={closeLicenseModal}><X size={17} /></button>
      </div>

      <div class:professional={licenseStatus.state === "active"} class="license-summary">
        <div>
          <span class="license-edition">{licenseStatus.edition}</span>
          <strong>{licenseStatus.state === "active" ? "当前设备已激活" : "当前设备未激活"}</strong>
        </div>
        <p>{licenseStatus.detail}</p>
        {#if licenseStatus.machine_code}<small>设备码 {licenseStatus.machine_code}</small>{/if}
      </div>

      {#if licenseStatus.state === "active" && !licenseReplacing}
        <div class="license-details">
          <span class="license-key-detail">
            <small>激活码</small>
            <strong>{licenseStatus.license_key_masked || "已保存"}</strong>
            <button
              aria-label="更换授权码"
              data-tooltip="更换授权码"
              data-tooltip-placement="top"
              onclick={beginLicenseReplacement}
            ><PencilSimple size={12} weight="bold" /></button>
          </span>
          <span><small>最近验证</small><strong>{formatLicenseDate(licenseStatus.last_validated_at, "刚刚")}</strong></span>
          <span><small>到期时间</small><strong>{formatLicenseDate(licenseStatus.expires_at)}</strong></span>
        </div>
      {:else}
        <label class="field license-key-field">
          <span>{licenseReplacing ? "新激活码" : "激活码"}</span>
          <input bind:value={licenseKey} placeholder={licenseReplacing ? "输入新的福宝激活码" : "输入福宝激活码"} autocomplete="off" spellcheck="false" onkeydown={(event) => event.key === "Enter" && activateLicense()} />
        </label>
        {#if licenseReplacing}<p class="license-replace-hint">新授权验证成功后才会替换当前授权。</p>{/if}
      {/if}

      <section
        class:connected={Boolean(remoteSyncStatus.active_endpoint)}
        class:error={Boolean(remoteSyncStatus.last_error) && !remoteSyncStatus.active_endpoint}
        class="remote-sync-panel"
        aria-labelledby="remote-sync-title"
      >
        <div class="remote-sync-head">
          <span class="remote-sync-icon"><CloudArrowDown size={15} weight="bold" /></span>
          <div>
            <strong id="remote-sync-title">中心库数据获取</strong>
            <span class="remote-sync-token-summary">
              <small>{remoteSyncStatus.configured ? (remoteSyncStatus.token_masked || "令牌已绑定") : "尚未绑定令牌"}</small>
              {#if remoteSyncStatus.configured && !remoteSyncEditing}
                <button
                  type="button"
                  aria-label="更换令牌"
                  data-tooltip="更换令牌"
                  data-tooltip-placement="top"
                  disabled={remoteSyncBusy}
                  onclick={beginRemoteSyncTokenChange}
                ><PencilSimple size={11} /></button>
              {/if}
            </span>
          </div>
          <span class="remote-sync-state">{remoteSyncStateLabel(remoteSyncStatus)}</span>
        </div>

        {#if !remoteSyncStatus.configured || remoteSyncEditing}
          <div class="field remote-sync-token-field">
            <span>服务器注册令牌</span>
            <div class="remote-sync-token-row">
              <input
                aria-label="服务器注册令牌"
                type="password"
                bind:value={remoteSyncToken}
                placeholder="输入服务器安装完成后生成的注册令牌"
                autocomplete="off"
                spellcheck="false"
                onkeydown={(event) => event.key === "Enter" && saveRemoteSyncToken()}
              />
              <div class="remote-sync-actions">
                {#if remoteSyncEditing}
                  <button class="secondary-button" disabled={remoteSyncBusy} onclick={cancelRemoteSyncTokenChange}>取消</button>
                {/if}
                <button class="primary-action" disabled={remoteSyncBusy || !remoteSyncToken.trim()} onclick={saveRemoteSyncToken}>
                  {#if remoteSyncBusy}<ArrowClockwise class="spinning" size={13} />{:else}<CloudArrowDown size={13} />{/if}
                  {remoteSyncBusy ? "连接中…" : "保存并连接"}
                </button>
              </div>
            </div>
          </div>
        {/if}

        {#if remoteSyncError}
          <div class="remote-sync-error"><WarningCircle size={13} />{remoteSyncError}</div>
        {:else if remoteSyncStatus.last_error && !remoteSyncStatus.active_endpoint}
          <div class="remote-sync-error"><WarningCircle size={13} />{remoteSyncStatus.last_error}</div>
        {/if}
      </section>

      {#if licenseError}<div class="license-error"><WarningCircle size={14} />{licenseError}</div>{/if}
      <div class="modal-actions">
        <button class="secondary-button" disabled={licenseBusy || remoteSyncBusy} onclick={licenseReplacing ? cancelLicenseReplacement : closeLicenseModal}>{licenseReplacing ? "取消更换" : "关闭"}</button>
        {#if licenseStatus.state === "active" && !licenseReplacing}
          <button class="primary-action" disabled={licenseBusy || remoteSyncBusy} onclick={refreshLicense}>
            <ArrowClockwise class={licenseBusy ? "spinning" : undefined} size={14} />{licenseBusy ? "刷新中…" : "刷新授权"}
          </button>
        {:else if licenseReplacing}
          <button class="primary-action" disabled={licenseBusy || remoteSyncBusy || !licenseKey.trim()} onclick={activateLicense}>
            {#if licenseBusy}<ArrowClockwise class="spinning" size={14} />{:else}<ShieldCheck size={14} />{/if}
            {licenseBusy ? "验证中…" : "确认更换"}
          </button>
        {:else}
          <button class="primary-action" disabled={licenseBusy || remoteSyncBusy || !licenseKey.trim()} onclick={activateLicense}>
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
        {licenseStatus.state === "active" ? "选择一个或多个参与账号。" : "免费版最多创建 1 个浏览器实例，请选择一个参与账号。"}创建后先显示在实例卡片中，点击卡片时才打开独立浏览器窗口。
      </p>

      <div class="instance-account-heading">
        <span class="instance-account-heading-title">
          <strong>选择参与账号</strong>
          <label class="instance-group-filter">
            <FolderOpen size={13} />
            <select bind:value={instanceParticipationGroupFilter} aria-label="按参与账号分组筛选">
              <option value="all">全部分组</option>
              <option value="ungrouped">未分组</option>
              {#each participationGroups as group}<option value={group.id}>{group.name}</option>{/each}
            </select>
            <CaretDown size={10} />
          </label>
        </span>
        <span class="instance-account-heading-actions">
          <span>{selectedParticipationAccountIds.length > 0 ? `${selectedParticipationAccountIds.length} 个已选 · ` : ""}{selectableParticipationAccounts.length} 个可创建</span>
          {#if licenseStatus.state === "active" && selectableParticipationAccounts.length > 0}
            <button
              type="button"
              class="instance-select-all"
              disabled={browserCreating}
              aria-pressed={allSelectableParticipationAccountsSelected()}
              onclick={toggleAllParticipationAccounts}
            >{allSelectableParticipationAccountsSelected() ? "反选" : "全选"}</button>
          {/if}
        </span>
      </div>

      {#if accountsLoading && accounts.length === 0}
        <div class="instance-account-empty"><ArrowClockwise class="spinning" size={20} /><span>正在读取参与账号…</span></div>
      {:else if selectableParticipationAccounts.length === 0}
        <div class="instance-account-empty">
          <UserFocus size={24} />
          <strong>{eligibleParticipationAccounts.length > 0 ? "当前分组没有可用账号" : "没有可用的参与账号"}</strong>
          <span>{eligibleParticipationAccounts.length > 0 ? "切换其他分组后再试" : "已有实例的账号不会重复显示"}</span>
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
        <div class="instance-account-list" role="group" aria-label={licenseStatus.state === "active" ? "选择参与账号，可多选" : "选择一个参与账号"}>
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
                <small>{account.user_id ? `抖音号 ${account.user_id}` : "尚未读取抖音号"} · {participationGroupName(account.participation?.group_id)}</small>
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
      <div class="modal-actions instance-modal-actions">
        <button
          type="button"
          class="icon-button instance-account-refresh"
          disabled={browserCreating || accountsLoading || instanceAccountsRefreshing}
          aria-label="刷新参与账号"
          data-tooltip={instanceAccountsRefreshing || accountsLoading ? "正在刷新参与账号" : "刷新参与账号"}
          data-tooltip-placement="top"
          onclick={refreshInstanceAccounts}
        ><ArrowClockwise class={instanceAccountsRefreshing || accountsLoading ? "spinning" : undefined} size={14} /></button>
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
          {roomImportBusy ? `正在导入 ${roomImportCompleted}/${roomImportTotal}…` : "导入"}
        </button>
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

{#if followingLiveModalInstance}
  {@const followingLiveResult = followingLiveSnapshot(followingLiveModalInstance)}
  <div
    class="modal-backdrop"
    role="presentation"
    onclick={(event) => event.currentTarget === event.target && void closeFollowingLive()}
  >
    <dialog class="modal following-live-modal" open aria-labelledby="following-live-modal-title">
      <div class="modal-head following-live-modal-head">
        <div>
          <span class="modal-icon following-live-modal-icon"><Radio size={16} weight="fill" /></span>
          <div>
            <h2 id="following-live-modal-title">正在直播</h2>
            <p>{followingLiveModalInstance.account_name} · {followingLiveResult ? `${followingLiveResult.total} 个关注账号正在直播` : "正在读取关注直播"}</p>
          </div>
        </div>
        <div class="following-live-head-actions">
          <button
            class="icon-button"
            aria-label="刷新正在直播列表"
            data-tooltip="刷新正在直播列表"
            data-tooltip-placement="left"
            disabled={browserFollowingLiveLoadingIds.includes(followingLiveModalInstance.id)}
            onclick={() => loadBrowserFollowingLive(followingLiveModalInstance!, true)}
          ><ArrowClockwise
            class={browserFollowingLiveLoadingIds.includes(followingLiveModalInstance.id) ? "spinning" : undefined}
            size={14}
          /></button>
          <button class="icon-button" aria-label="关闭" onclick={() => void closeFollowingLive()}><X size={16} /></button>
        </div>
      </div>

      {#if browserFollowingLiveLoadingIds.includes(followingLiveModalInstance.id) && !followingLiveResult}
        <div class="following-live-state"><ArrowClockwise class="spinning" size={18} /><span>正在读取这个账号的关注直播…</span></div>
      {:else if browserFollowingLiveErrors[followingLiveModalInstance.id] && !followingLiveResult}
        <div class="following-live-state error">
          <WarningCircle size={19} />
          <strong>暂时无法读取关注直播</strong>
          <span>{browserFollowingLiveErrors[followingLiveModalInstance.id]}</span>
          <button class="secondary-button" onclick={() => loadBrowserFollowingLive(followingLiveModalInstance!, true)}>重新读取</button>
        </div>
      {:else if !followingLiveResult || followingLiveResult.items.length === 0}
        <div class="following-live-state"><Radio size={20} /><strong>当前没有关注账号正在直播</strong><span>点击右上角刷新可重新读取。</span></div>
      {:else}
        <div class="following-live-list">
          {#each followingLiveResult.items as live}
            <article class="following-live-row">
              {#if live.avatar_url}
                <img src={live.avatar_url} alt="" loading="lazy" referrerpolicy="no-referrer" />
              {:else}
                <span class="following-live-avatar"><UserCircle size={19} weight="fill" /></span>
              {/if}
              <div class="following-live-identity">
                <strong use:truncatedTooltip={live.nickname || "未命名主播"}>{live.nickname || "未命名主播"}</strong>
                <small use:truncatedTooltip={live.title || "直播标题尚未读取"}>{live.title || "直播标题尚未读取"}</small>
              </div>
              <div class="following-live-room-meta">
                <span>房间号 <strong>{live.web_rid || "—"}</strong></span>
                <span>房间标识 <strong>{live.room_id || "—"}</strong></span>
              </div>
              <span class="following-live-viewers">{live.viewer_count ? `${live.viewer_count} 在线` : "正在直播"}</span>
              <button
                class="icon-button"
                aria-label={`打开 ${live.nickname || "主播"} 的直播间`}
                data-tooltip="打开直播间"
                data-tooltip-placement="left"
                onclick={() => openFollowingLiveRoom(live)}
              ><ArrowSquareOut size={14} /></button>
            </article>
          {/each}
        </div>
      {/if}
      {#if followingLiveResult?.stale}<p class="following-live-stale">网络暂时不可用，当前展示最近一次成功读取的结果。</p>{/if}
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

{#if roomPendingPermanentDelete}
  <ConfirmDialog
    title="永久删除直播间"
    message={`确定永久删除「${roomDisplayName(roomPendingPermanentDelete)}」吗？\n直播间记录、对应监测快照和本地红包历史将被移除，此操作无法撤销。`}
    confirmText="永久删除"
    busy={roomRecycleBusyId === roomPendingPermanentDelete.id}
    onCancel={() => !roomRecycleBusyId && (roomPendingPermanentDelete = null)}
    onConfirm={permanentlyDeleteRecycledRoom}
  />
{/if}

{#if browserPendingClose}
  <ConfirmDialog
    title="关闭浏览器实例"
    message={`确定关闭「${browserPendingClose.account_name}」的浏览器实例吗？\n当前嵌入页面会关闭并从列表移除；账号登录环境与本地配置会保留，下次创建时可继续使用。`}
    confirmText="确认关闭"
    busyText="正在关闭…"
    icon="close"
    busy={browserClosingId === browserPendingClose.id}
    onCancel={cancelBrowserCloseConfirm}
    onConfirm={() => closeBrowserInstance(browserPendingClose!)}
  />
{/if}

{#if toast}<div class:browser-toast={activeView === "browsers"} class="toast" role="status" aria-live="polite"><CheckCircle size={16} weight="fill" />{toast}</div>{/if}
