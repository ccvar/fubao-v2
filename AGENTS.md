# Prototype Instructions

Run the local server yourself and open the preview in the browser available to this environment. Do not give the user server-start instructions when you can run it.

Before making substantial visual changes, use the Product Design plugin's `get-context` skill when the visual source is unclear or no longer matches the current goal. When the user gives durable prototype-specific design feedback, preferences, or decisions, record them in `AGENTS.md`.

For Douyin browser instances, use the legacy 福宝/DY-KIRO Chrome 124 user agent and inject account cookies on the parent domain `.douyin.com` with `Secure`, `SameSite=None`, and `/` before navigating or reloading the embedded page. Persist CK health in the Go account store and present the same expired state in both browser-instance cards and the account-management table; temporary network failures must remain unknown rather than being reported as expired.

Never enqueue full-account CK revalidation during account-list or browser-list rendering. The Go sidecar's interactive credential and browser-window requests must remain immediately responsive; use persisted CK health for list rendering and perform a fresh online check only for the account being explicitly rebound or otherwise acted on.

Embedded browser Cookie polling is adaptive. Keep the lightweight scheduler at five seconds, but only unknown, expired, or actively logging-in mounted instances receive five-second native checks and the compact checking indicator. Once an instance is confirmed logged in, check it at most once per 60 seconds and do so silently. A completed first page load, explicit CK rebind, or “打开实例” action still forces an immediate check. Temporary inspection or network failures remain unknown and must never become CK expiry by timeout alone.

In participation and monitoring account rows, keep CK health on the first line and role-specific statistics on the second line. When CK is expired, place a compact “重新绑定 CK” icon immediately after the expired badge. Rebinding opens an isolated native Douyin child WebView inside the rebind panel for that canonical account; do not create a second Tauri `WebviewWindow`, because that path blocks the macOS main run loop in this app. After the user confirms login completion, Rust reads the new Douyin cookies, sends them directly over the authenticated native channel to the Go store, forces CK validation, and refreshes account/browser health without exposing raw cookies to the frontend.

The account status filter stays compact and exposes only “可用”, “CK 失效”, and “冷却中” in addition to “全部状态”. Account-row delete icons remain low-contrast by default and become prominent only on hover or keyboard focus.

Account sorting lives beside the “状态与数据” table heading and follows the same compact menu interaction as room sorting. Monitoring accounts support request-total, today-request, last-request-time, and added-time priority; participation accounts support available, participation-count, win-count, and added-time priority. Numeric priorities sort high-to-low, time priorities sort newest-first, and equal values preserve the current list order.

账号导入入口必须明确显示当前目标分类，避免让用户猜测账号会进入“参与账号”还是“监测账号”。“粘贴 Cookie / 扫码登录 / 批量导入文件 / 批量导入文件夹”都添加到当前明确显示的目标分类；同一账号再次导入到另一分类时只补充分配，不移除原分类。批量导入兼容旧福宝账号 JSON、浏览器 Cookie JSON、原始 Cookie 或 Cookie Header、cURL、Netscape cookie.txt，以及逐行 Cookie 文本。

只有参与账号拥有一个由 Go 账号存储持久化的分组，监测账号不显示也不保存分组。参与账号页在状态筛选旁提供分组筛选与新建分组，列表提供独立分组列并允许修改归属；“粘贴 Cookie / 扫码登录 / 批量导入文件 / 批量导入文件夹”四种参与账号导入方式都必须明确选择目标分组，浏览器实例创建弹窗也必须支持按参与账号分组筛选。

All hover help in the desktop UI uses the shared dark in-app Tooltip (`data-tooltip` plus an edge-safe placement), never the browser-native `title` tooltip. Keep `aria-label` on icon-only controls.

Top-bar dropdowns and other floating menus must always render above the scrollbars of the business tables beneath them. Native table scrollbar thumbs must never bleed through or visually cover an open menu; hiding only the underlying thumb while a menu is open is acceptable and must not change table scroll position or behavior.

When implementing from a selected generated mock, treat that image as the source of truth for layout, component anatomy, density, spacing, color, typography, visible content, and hierarchy.

Build app UI in `src/`. Keep `.openai/hosting.json`, `worker/index.js`, `scripts/prepare-sites-build.mjs`, and `tests/sites-worker.test.mjs` intact so the same local prototype can be handed to Sites. Before a Sites handoff, run `npm run build` and `npm run test:sites`; the build must leave `dist/client/index.html`, `dist/server/index.js`, and `dist/.openai/hosting.json`.

The Pilot screenshot is a layout reference only. Preserve the two-column desktop structure and visual rhythm, but never reuse Pilot/gcms navigation, onboarding copy, site-management concepts, or business behavior. This product is 福宝控制台, centered on 红包监测、参与任务、浏览器实例、账号与代理, with a Go business engine.

The primary sidebar exposes only “浏览器实例” and “账号与直播间”. “监测总览” and “红包任务” are not standalone pages; their live data remains available through the sidebar data overview and the 红包/直播间 tabs on “账号与直播间”. Show the current total browser-instance count as the compact badge on “浏览器实例”.

Closing the main window hides it to the native system tray instead of terminating the client, so the Go engine and active monitoring/participation work continue running. A left click on the tray icon or the tray “打开福宝控制台” action restores and focuses the main window; macOS Dock reopen does the same. Only the explicit tray “彻底退出” action terminates the Go engine and exits the application.

Keep the desktop development URL pinned to this project's dedicated port and use Vite `strictPort` so Tauri cannot silently attach to another local project's dev server.

On the 账号与直播间 page, keep the 直播间/参与账号/监测账号 tab strip outside the table card so the table is the only framed surface. The account status filter belongs in that external tab strip; do not repeat the old explanatory account-copy in the content area.

Keep the external account tab strip vertically balanced around the table, use the same responsive inline inset for tabs and table rows, and keep the selected tab/count surfaces light and quiet rather than using a heavy fill.

Desktop chrome rules: use the native macOS traffic-light controls only; never draw duplicate traffic lights inside the web UI. Keep the sidebar title strip and main top bar as native Tauri drag regions backed by the explicit `start_window_drag` command, while leaving buttons interactive. Set the native macOS traffic-light position to `{ x: 15, y: 20 }` so their center aligns with the 44px web title row. Align the sidebar toggle horizontally with the macOS traffic lights and keep it visible at the same position when the sidebar is fully collapsed. The sidebar must remain mouse-resizable between 220px and 360px, keyboard-adjustable, collapsible, and remember its last expanded width. Its resize hit area may keep the `col-resize` cursor, but the visible divider must never thicken or change color on hover or while dragging.

Single-press dragging and double-press maximize/restore must both work from non-interactive areas of the sidebar title strip and main top bar. Buttons, inputs, labels, and the sidebar resize handle must never trigger either window action.

Keep persistent desktop chrome very compact: the main top bar should stay around 60px high, the primary sidebar navigation should use dense 34px rows with no unnecessary inter-row gaps, and the bottom workspace/status footer should stay around 48px high. Keep page titles near 16px, title-bar and footer icon hover boxes near 24px with minimal internal padding, the sidebar toggle hover box near 22px, and the primary title-bar action near 24px high. In both expanded and collapsed states, the native macOS traffic lights, sidebar toggle, page-title row, and right-side toolbar must share the same horizontal center axis; the subtitle sits on the second line.

Do not append the decorative “本机运行” phrase to any main top-bar subtitle. Browser-instance resource metrics such as CPU and memory remain visible because they carry real runtime data.

Primary sidebar pages support desktop-style detached windows: normal left-click switches the current window, while the row context menu, Command/Ctrl-click, and middle-click open that page in its own native window. Reopening the same page focuses its existing detached window. Detached windows show only the selected business page and share the same Go engine/account store; native child WebViews for browser cards and account login/rebind flows must attach to the invoking page window rather than being hard-coded to `main`.

Use one compact modal-footer button standard across every dialog: right-align actions, use a 30px control height, 8px radius, 6px gap, and content-sized widths with a small minimum; the destructive confirmation dialog follows the same dimensions.

Render the top-bar “新建监测” action as plain icon-and-text chrome with no filled button background, border, shadow, rounded container, or press translation. Keep it 24px high and separate the top-bar action groups by about 9px while preserving the compact 24px icon hover boxes.

In the expanded desktop layout, use the same 34px left and right inset for the main top bar and scrollable content so the title group aligns with the search/cards and the right action group aligns with the filter/cards. At narrow widths, both regions use a 16px horizontal inset. The collapsed title bar may keep its larger left inset to clear native macOS controls.

Keep the compact sidebar footer permanently visible at the bottom of the window. The recent-activity list owns the flexible/scrollable space above it; it must never push the footer below the viewport. Do not hide the footer settings button at narrow breakpoints.

The recent-activity section must never render demo or sample events. A fresh local store with no persisted activity shows only the low-contrast text “暂无活动”; real persisted activity replaces that empty state.

The sidebar names its live counters section “数据概览”. Its four counter rows are text-only: do not render leading status icons, and place each count directly before the status label without the Chinese classifier “个”, while preserving the existing semantic colors and click targets. A separate “最近活动” section sits below it, reuses the same compact icon-and-copy row language, and owns the remaining scrollable space above the fixed footer.

In the sidebar “数据概览”, render each leading count in a compact, prominent condensed numeric face with tabular figures. Keep the counts visibly larger than the deliberately smaller supporting copy, while preserving warning colors.

The sidebar recent-activity list displays the complete persisted history returned by the Go store (currently up to 100 newest entries) in its own scrollable region; never impose a smaller frontend-only item cap such as four rows.

The recent-activity region uses a permanently reserved, thin Pilot-style internal scrollbar with no visible track background and only a rounded gray thumb. Keep the visible thumb close to the sidebar divider with only a minimal inset while retaining a wider invisible hit area for dragging. Scrolling remains contained within recent activity so the data overview and fixed workspace footer do not move.

Keep the “最近活动” section title fixed above its internal scroll viewport; only activity rows participate in scrolling, and the custom track begins below the title.

The DY-KIRO-derived application icon uses a flat pure-white interior tile while preserving transparent outer corners and the original colored artwork. Background replacement must not redraw, resize, move, or simplify the heart line, circular arcs, dots, envelope, coin, highlights, or shadows.

The Tauri bundle must explicitly list the PNG, macOS `.icns`, and Windows `.ico` application icons. Never rely on implicit icon discovery, because Universal macOS CI builds can otherwise emit an `.app` without `CFBundleIconFile` or `Contents/Resources`.

Desktop self-updates use Tauri's signed updater flow, matching Pilot: download and install the signed updater artifact, then relaunch through `tauri-plugin-process`. Never replace the installed macOS app by mounting a DMG and moving the `.app` from a detached shell script. Every desktop release must publish the macOS `.app.tar.gz`, Windows signed NSIS updater, their updater signatures, and a standard Tauri `latest.json`; the DMG remains only for first-time/manual installation.

The sidebar footer always shows the current application version followed by the current license edition. A fresh installation defaults to “免费版”; successful Keygen activation changes it to “专业版”. Reuse the legacy 福宝/DY-KIRO product, device fingerprint, machine activation, refresh, unbind, and offline-grace semantics in the Go engine. License keys remain only in the permission-restricted Go store and the frontend receives only safe status and masked metadata. Free and professional editions currently have no feature-level restrictions. The active-license panel shows its absolute expiry or “永久有效” when Keygen provides no expiry, and exposes an icon-only “更换授权码” action; replacing a key must validate the new key before overwriting the current working authorization. When a professional license has a finite expiry, the sidebar footer shows a compact “剩余 N 天” reminder immediately after the edition badge and uses the shared dark in-app Tooltip to reveal the exact expiry; permanent licenses omit this reminder.

For the professional edition, show a very small cloud-download icon immediately after “福宝控制台” in the sidebar footer once the center-library state has loaded. The downward arrow represents receiving center-library data. Use green when a center-library Key is bound and gray when it is unbound; the free edition shows no icon. The icon uses the shared dark in-app Tooltip and opens the existing authorization-management dialog. Center-library icons inside that dialog use the same downward-arrow direction.

At narrow widths (760px and below), the sidebar becomes a true icon rail. Never render a web logo or any decorative mark in the native traffic-light strip, because it overlaps macOS window controls. Keep the four primary navigation icons in one dense uninterrupted group, then separate the workspace and recent-activity icon groups with restrained spacing.

Use `/Users/apple/work/DY-KIRO/assets/icon.png` as the source artwork for the desktop application icon. Place it at about 83% scale (850px artwork on a 1024px transparent canvas) before regenerating the Tauri icon set; direct Dock comparison showed both 720px and 770px variants were visibly smaller than neighboring macOS icons.

Account migration uses one canonical local account entity with independent `monitoring` and `participation` role assignments. The same login may belong to both roles simultaneously. “添加到另一分类” must preserve the current role; removing a role must never silently delete the canonical account or its other role. Monitoring counters/status stay in the monitoring profile, while participation statistics, proxy binding, fingerprint binding, and tags stay in the participation profile. DY-KIRO migration is copy-only and must never modify the legacy `douyin_accounts.json` or `lottery_accounts.json` files. Cookie data stays inside the Go engine’s permission-restricted local store and must never be returned by list APIs or rendered in the frontend.

Commands that dynamically create Tauri child WebViews must be asynchronous so Windows WebView2 environment creation never blocks the main-thread IPC/message pump. On Windows, every account-keyed WebView2 profile lives under the application LocalAppData directory rather than roaming AppData; macOS continues to use account-keyed WKWebView data-store identifiers. Raw login data remains native-only on every platform.

Completing a CK rebind must confirm the authenticated state in the native Douyin page, retry the native Cookie-manager read briefly for Windows WebView2 propagation, persist the fresh browser-login signal in Go, and return the safe updated account view immediately. Never leave the frontend's previous expired badge in place merely because a subsequent online validation is temporarily unavailable, and never allow a transient validator result to override the just-confirmed native login state.

The main top-bar refresh action must reload the current page's real Go-backed data, including account health on participation and monitoring tabs. It must never be a decorative timer or update only browser-side demo state.

Each browser instance belongs to exactly one participation account. Creating an instance must select a participation account first, then the Go engine resolves that account's Cookie internally and prepares a dedicated browser profile directory. Browser instances must never share profile directories, cache, Cookie state, or login sessions. Instance cards host real interactive Tauri child WebViews rather than screenshots or visual placeholders; the real page is the card's primary surface and sits flush against the card's top, left, and right edges without its own inner border. The outer instance-card border remains. Compact account metadata sits below it with an icon-only “打开实例” action beside the account identity. Keep this identity row single-line: use a small 28 px instance icon, show the account name followed directly by the smaller muted Douyin ID value without an `抖音号` prefix, and do not repeat an `账号：…` subtitle. The icon action uses the shared dark hover-tip treatment. Embedded-state badges, preview hints, decorative domain chrome, and repeated browser/runtime metadata are omitted. “打开实例” opens a separate full-size window. Embedded page zoom follows child WebView width only, is rounded to stable percentage steps, and must be recomputed after window, sidebar, scroll, or layout changes. The frontend may display only safe account and instance metadata. Raw Cookie values may travel only over the authenticated native Go-to-Rust channel for WebView cookie injection and must never be emitted to, stored by, or rendered in the frontend.

“打开实例”使用与运行日志一致的原生 Tauri 工具窗口框架：保留系统窗口边框和 macOS 红绿灯，白色紧凑标题行内显示实例账号，内容区只承载真实抖音子 WebView，不显示 Chrome 标签页、地址栏或工具栏。该窗口复用卡片 WebView 的账号专属数据目录、Cookie 注入链路和运行资源租约；关闭后释放独立窗口租约并恢复卡片挂载，原始 Cookie 仍不得进入前端 JavaScript。

One participation account owns at most one browser instance. Repeated create/open operations for the same account must reuse that stable instance and its account-keyed browser profile. The account rebind WebView and browser instance share the same native account data store so login state is continuous; completing a CK rebind must immediately refresh any mounted instance for that account. Different accounts must still use separate profiles and data stores.

Successful login inside an independently opened browser instance must return changed Douyin cookies through an authenticated loopback-only channel, update and revalidate the canonical Go account, and refresh its mounted embedded preview. Raw CK must never enter frontend JavaScript.

The compact sidebar footer gear opens 红包参与设置 rather than license management. Its global values are persisted by the Go red-packet store and enforced independently for each participation account: 参与停止 blocks future assignments after the configured joined count, 参与冷却 waits the configured seconds after an accepted join, and 中奖停止 blocks future assignments after the configured confirmed-win count; zero means unlimited. Each explicit browser-account start of red-packet participation appends a safe persistent “参与账号…启动了红包参与” entry to the sidebar recent-activity feed. License management remains available from the edition badge.

红包参与设置包含持久化的参与红包类型规则：“不限 / 礼物红包 / 钻石红包”，默认“钻石红包”。该规则必须由 Go 引擎在创建参与任务前按红包事件的真实类型过滤；类型不明确的红包只能在“不限”时参与，不能只在前端做视觉筛选，且被过滤的红包不得产生请求记录或计入任务次数。

The data-management screen is named “账号与直播间” and orders its tabs as “红包 / 直播间 / 参与账号 / 监测账号”. The red-packet monitor reuses 福宝’s signed `luckybox/box/list` request path (with `lottery_info` as a safe fallback) but must surface only explicit 红包 payloads; 福袋/lottery payloads are filtered out. Legacy room migration copies `rooms_config.json` into the Go engine’s permission-restricted local store and must never modify the old 福宝 data. The top-bar action is named “导入数据”, with “导入直播间” kept as a distinct first menu action.

The room-management workflow owns red-packet monitoring controls: the “直播间” tab provides per-room start/stop actions plus “全部启动/全部停止” in its upper-right corner, while the adjacent “红包” tab is an event-only feed of red packets detected by running room monitors and must not show monitor controls.

The shared settings dialog uses top-level tabs for “红包参与” and “直播间”. Room settings persist an automatic recycle threshold that defaults to 7 confirmed offline monitoring days; zero disables automatic recycling. Count each local calendar day at most once, reset the streak after any confirmed live result, and never count unknown, network-error, or CK-error outcomes as offline evidence. Recycled rooms stop monitoring but remain recoverable with the recorded reason and recycle time. Restoring clears the offline streak and returns the room in a stopped state. Permanent deletion is available only inside the recycle bin and always requires the compact destructive confirmation dialog.

Participation task schedules prewarm room monitoring through the Go engine before every “指定日期 / 每天固定时间 / 间隔执行” occurrence. The lead time is persisted in the shared “直播间” settings, defaults to 10 minutes, and zero means check only when the participation batch actually starts. When the prewarm window opens, check all eligible non-recycled rooms and invoke the same native “全部启动” path only as needed; invalid monitoring CKs are skipped by the Go account store. Immediate execution always performs the execution-time check. This behavior must remain independent of the current page, browser-card rendering, and window visibility, and deleting or stopping a participation schedule must not automatically stop room monitoring.

Room red-packet monitoring is explicitly two-stage. First probe Douyin's room-entry endpoint to determine whether the room is live; only a room positively confirmed live may query red-packet endpoints. Unknown rooms use a short initial probe cadence, offline rooms use a slower live-status cadence, live rooms use a faster red-packet cadence, and an active red packet uses the fastest cadence. Monitor-task state, live/offline state, and red-packet state are separate UI concepts. In the room table, combine the streamer and room identifier into one column with the identifier and source shown below the streamer, and show the current live/offline result in the status area.

Runtime monitor logs must attribute every room-monitor state transition to both the live room and the monitoring account. A red-packet discovery log must include the monitoring account, live room, packet identity, prize information, and expiry/draw time. The red-packet event feed persists and displays prize details plus an absolute/relative expiry; legacy luckybox payloads without an absolute deadline derive it from `send_time + delay_time` or the reported countdown.

Monitoring CK health is independent from participation/browser login health. Any successful monitoring business-interface request proves the monitoring CK is valid; temporary request or network failures remain unknown/error and must never mark it expired. Only an absent Cookie may immediately be marked expired for monitoring. Participation accounts continue to use the stricter authenticated browser-login validation.

Per-room red-packet monitor actions must optimistically update the row status and icon immediately, then reconcile with the Go engine response without allowing an older polling response to restore the previous “未启动” state.

In the room list, show a compact gift indicator immediately after the streamer name only while that room has at least one unexpired red packet. Its standard tooltip reports the active packet count and the nearest remaining time; historical or expired packets must never keep the indicator visible.

In the room list, place a compact external-link icon immediately after the room title whenever a valid Douyin room ID is available. It opens the room in the system browser, uses the shared standard tooltip, and must not trigger monitoring actions.

“导入直播间” follows 福宝’s batch-import interaction: it opens a modal that accepts pasted room IDs or `.txt/.csv` uploads, normalizes links/separators, deduplicates existing rooms, and imports only valid numeric room IDs.

High-volume room imports and monitoring must remain responsive at roughly 100,000 rooms. Import in bounded chunks with visible completed/total progress but persist only at the final chunk; room and monitor tables consume Go-backed pages plus separate global summaries rather than transferring or reactively sorting the whole store. Bulk monitor startup rolls work into a bounded native request queue, and recurring probes coalesce persistence instead of serializing the full monitor store after every room result.

Large live-room imports must remain responsive on Windows. Normalize and deduplicate the input before submission, send valid room IDs to Go in bounded batches, yield the frontend between batches, and show real completed/total progress such as “正在导入 15/1200…”. The Go room store must use indexed lookup rather than scanning every existing room for every imported ID.

On the “账号与直播间” screen, the high-volume room list fills the available viewport with only a compact bottom inset and owns its internal scroll. Participation and monitoring account panels size to their actual row content instead of stretching to fill the window and leaving a large blank area inside the card.

Search lives in the top toolbar as a compact progressive-disclosure control: show only the search icon by default, then expand a small inline input when clicked. Do not reserve a full content row for search. Remove decorative top-bar health/security icons that have no working behavior.

On the browser-instance screen, keep only a compact 1–10 column slider in a floating lower-right control rather than in the title bar; do not add a layout icon or persistent label beside it, and expose its current value through the shared standard tooltip. Keep the slider borderless and about 20px high, collapsed to a small visible rail by default, and expand it on hover or keyboard focus. Its value directly controls the instance-card grid and persists locally. Browser grid rows and cards must size to content instead of stretching to fill the remaining viewport. While the slider is dragged, hide stale native child-WebView surfaces and debounce bounds/zoom recalculation so only the final layout can be shown. Native child-WebView bounds must avoid the slider and its visible tooltip so the floating control always remains unobscured at the top visual layer.

Embedded browser previews must mount as soon as a useful portion of their preview surface intersects the scrollable content viewport; never require the whole card or preview to be visible first. Clip native child-WebView bounds to the content viewport while scrolling, and resynchronize them through intersection/resize/scroll observation so a first-row card cannot remain on its HTML placeholder after entering view.

Returning to the browser-instance screen must show each already-rendered instance exactly as it was left, including its in-memory page, scroll, playback, dialog, and login state. Switching to another application page hides the native child WebViews without destroying them or releasing their runtime leases; returning repositions and reveals those same WebViews immediately and must not show a restoring state. A newly mounted instance still uses the same account-keyed native profile and last safe Douyin page.

Browser instance records are not a fixed concurrency allowance. The Go engine computes a recommended runtime limit from the current machine's CPU and available memory, admits visible or explicitly opened instances into shared resource leases, and queues the rest in stable order. Existing work is never killed merely because pressure rises, but critical memory pressure closes new admission until recovery. Switching application pages only hides mounted child WebViews and retains their leases so their exact in-memory state survives; scrolling a card out of the usable viewport still destroys its embedded child WebView and releases its lease. Waiting cards retry automatically and show their queue state. Independently opened browser windows consume the same shared runtime capacity.

浏览器运行建议上限的 CPU 基准使用逻辑核心数的 1.5 倍，再与扣除 25%（最低 2GB）预留后的内存容量、24 个自动硬上限取最小值；内存受限和临界压力仍会下调或暂停新增。标题栏“建议上限”必须使用共享暗色 Tooltip 展示 Go 引擎返回的 CPU 上限、内存预留、单实例估算、内存上限、自动硬上限和最终结果，不能由前端另行猜测计算。

浏览器实例标题栏“建议上限”的计算 Tooltip 固定向上展开，并使用较宽的两行专用布局：第一行展示 CPU 与内存计算，第二行展示取最小值和最终建议；避免向下被原生实例 WebView 遮挡，也避免过窄多行向上侵入原生标题栏。

Native browser WebView teardown is idempotent and must never inspect the page URL, Cookie, or other WKWebView state after destruction begins. Persist the last safe Douyin location only from navigation/page-load callbacks, ignore overlapping close requests, and throttle large batch-created instance mounts through a small rolling native-mount window so creating many records cannot flood the macOS main thread.

An active red-packet participation task retains its real native live-room WebView and Go runtime lease even when the instance card is outside the viewport or another app page is selected; hide the surface without destroying the page context. Release both only after the participation context ends. The browser screen shows a compact live task summary from safe Go context state—active accounts, prepared/accepting contexts, task-local joined count, pending draws, and wins—and never invents frontend counters.

On the browser-instance screen, place the compact live participation-task summary immediately after the top-bar “抢包” trigger rather than in a separate content row. Hide that summary while the “抢包” action menu is expanded, restore it after the menu collapses, and keep its height aligned with the compact trigger so active-task status does not increase the page header or content height.

Room records without a valid 6–20 digit `web_rid` are invalid and must be removed rather than displayed as record-only rooms. Red-packet bulk controls reflect actual runtime state: hide “全部停止” when none are running, hide “全部启动” when all eligible rooms are running, visually distinguish start from stop, and provide a compact runtime-log entry point.

When dismissing the CK rebind flow, hide and destroy the native child WebView before unmounting the HTML modal. If native teardown fails, keep the modal visible and report the failure so an orphan WebView can never remain over the main interface.

“扫码登录” and “重新绑定 CK” share the same embedded native Douyin login modal and lifecycle. Rebind updates an existing canonical account; scan login creates or deduplicates a canonical account and assigns it to the currently selected account role. Keep the native-storage privacy hint inline to the left of the modal action buttons rather than giving it a separate bottom row.

The red-packet runtime log is a separate native Tauri utility window, not an in-page modal. Use the system title bar/traffic lights for window movement and closing; do not render redundant content close/finish buttons. Keep this utility window compact and freely resizable down to a small working size. Its white HTML title row overlays the native title area: center the compact green terminal icon immediately before the small title on the same traffic-light axis, with no gray title strip or duplicate in-body heading. Keep its three runtime counters as compact inline text with the small “清空日志” action aligned on their right, and synchronize log updates with the main window over Tauri events. Log lines use small plain text without underlines or row separators, in compact fixed-height single-line rows; keep chronology oldest-to-newest from top to bottom and automatically scroll to the newest appended row. The main-window entry is an icon-only, borderless toolbar button with a hover tip.

Monitoring-account request counters are local to this desktop client: never import legacy total/today values. Display them as `请求总数 / 今日次数 · 相对最后请求时间`, with a hover tip containing the exact local timestamp; batch persistence so room polling never writes once per request.

Participation-role labels use a compact teal palette, never the red/pink error palette used by CK expiry. Participation and monitoring role labels must remain equal, fixed-width compact pills and must not stretch when both roles are present.

Every explicit browser-card red-packet start creates one independent participation task. Participation-count and win-count stop limits apply only inside that task, never to the account's lifetime statistics. When the current task reaches either limit, its control returns to low-contrast gray, new join assignments stop, and the native context remains only long enough to finish result queries for already accepted packets; once those results are resolved, end the task automatically. A later click always creates a fresh task with zero task counters. After an accepted packet reaches its draw time, query Douyin's personal `luckybox/receive` result in the native account page context and persist a definitive “未中奖” or “已中奖” result with the real prize. Missing, mismatched, or temporarily unavailable result data stays pending and must never be fabricated.

One participation account may have only one unresolved accepted red packet at a time. Until that packet has a definitive personal draw result, do not send the account into a later red-packet round. Once the result is resolved, re-check the configured cooldown and stop limits, then retry only newer red packets that are still unexpired; result lookup for the accepted packet must remain allowed while join assignment is blocked.

The personal draw-result query has a configurable post-draw timeout in the participation settings, defaulting to 10 seconds. If no definitive matching result is available when that window expires, persist the record as “开奖异常”, release the unresolved-result gate, and continue the current participation task according to its cooldown and stop limits. The participation-record tab exposes a native utility-window log entry matching the room-monitor log affordance. Participation logs may show only safe business request parameters and recursively redacted response JSON; Cookies, tokens, signatures, signed URLs, headers, device fingerprints, and other native credentials must never be persisted in or sent to that log window.

Browser-instance refinements: the identity-row instance tile is 20px. Account names and Douyin IDs expose the shared dark in-app Tooltip only when the rendered text is actually truncated. Cards expose compact icon-only “打开实例” and “关闭实例” actions with shared in-app tooltips; closing always requires the compact confirmation dialog, then destroys the mounted child WebView and removes the card while preserving the account-keyed profile for later reuse. The CK-expired badge keeps its coral surface unchanged on hover and does not add a gray hover fill. When participation CK is expired, hide the red-packet participation icon in both the instance card and participation-account row instead of leaving an unusable control visible. The “新建实例” dialog supports multi-select batch creation for eligible participation accounts, while accounts that already own an instance remain excluded.

有效专业版的“新建实例”多选对话框在可创建账号数量后提供紧凑的“全选 / 反选”切换；全选只作用于当前可创建账号，全部选中时“反选”清空当前选择。免费版继续保持单选且不显示全选操作。

Each browser-instance identity row may show the number of accounts currently live among that instance account's Douyin follows. Read it through the Go engine with the instance's canonical participation credential, never through frontend Cookie access. Show `0` only after a successful snapshot confirms zero live follows; while loading show only the compact spinner, and when no successful result exists render neither “未知” nor a failed-result placeholder. Clicking a successful count opens safe room metadata only: avatar, account name, live title, room number, room identifier, viewer count, and an external room action. Legacy 福宝 discovery/seed-room code and `similar_room_by_anchor` are references for signing and resilient parsing only; similar-room expansion must never be presented as the current account's followed-live list.

The followed-live detail dialog is resizable within the desktop viewport like a lightweight Pilot utility panel. Its list owns vertical scrolling with a thin quiet scrollbar, never exposes horizontal scrolling, and reflows each live-room row when the dialog is narrowed instead of forcing fixed-width columns beyond the panel edge.

Browser instance cards keep a compact loading indicator visible until the native child WebView reports its first completed Douyin page load. Initial child WebViews remain hidden and off-card until ready; never let an unpainted white native surface cover the HTML loading state.

On Windows, an independently opened browser instance uses only the native system title bar; do not render the redundant in-page account/title strip above the Douyin surface. Opening an instance must restore, bring forward, and focus its independent window. Reopening the same instance reuses and foregrounds the existing native window instead of creating a duplicate WebView or surfacing an `already exists` error.

Opening any settings surface from the browser-instance page must publish its modal-open guard before hiding native child WebViews. In-flight mounts must re-check that guard and remain hidden so a late native surface can never paint above the settings dialog.

In the “新建浏览器实例” dialog, keep the participation-account refresh action fixed at the far left of the modal footer. Refreshing rotates that fixed icon and preserves the existing account-list surface and modal dimensions; never replace a populated list with a loading frame that makes the dialog jump.

Red-packet prize parsing follows 福宝's luckybox grouping rule: group every box row belonging to the same activity, ignore zero placeholder amounts, sum positive diamond values, and display the activity total plus share count (for example `总99钻，24份红包`). The luckybox share count may be encoded in `biz_extra.tags[business_type]` and must be decoded before falling back to grouped-row count. If no authoritative positive prize is available, show a pending state instead of a fabricated `0钻`. Event expiry is a live countdown plus absolute time in the compact form `00:32 · 8/1 13:55:19`; visually emphasize the countdown over the absolute timestamp. Red-packet room titles open the corresponding `https://live.douyin.com/{web_rid}` page in the system browser, stay left-aligned on one line, and truncate with an ellipsis inside their grid column instead of overlapping prize data. Keep the external-link icon immediately beside the visible title text rather than pushing it to the far edge of the column.

The 红包 tab defaults to current, unexpired red packets only. Its tab badge and the main title subtitle count only unexpired red packets. Keep expired records behind a compact right-aligned “历史红包” entry; history mode shows non-current records and provides an equally compact return to current red packets.

Differentiate red-packet kinds in the event list: use a compact faceted gemstone icon for “钻石红包” (never a plain filled rhombus) and retain the gift icon for gift/other red packets. In the “账号与直播间” title subtitle, report the live runtime workload as `x 个房间正在监测` rather than repeating the total canonical room count; whenever that monitored count is positive, append `y 个正在直播` using only monitored rooms whose current engine-probed live status is live. The 直播间 tab badge remains the total room count.

Expired red-packet rows must say “已过期” instead of freezing the countdown at `00:00`; keep the absolute expiry timestamp beside that label.

Live red-packet countdowns must visibly tick once per second. Pass the reactive clock explicitly into countdown formatting, active-count calculation, and current/history filtering so Svelte recompiles those expressions every second and moves a packet into history immediately when it expires.

The live-room table exposes one compact sort menu in the “直播与红包状态” header with default order, currently-live-first, recently-started, and recently-detected-red-packet options. “最近开播” must use a persisted `live_started_at` captured only when the room transitions into the live state (or when a legacy live monitor has no start timestamp); never sort by the recurring `last_live_checked_at` probe time because polling must not reshuffle the list. “红包优先” sorts by the latest persisted red-packet event time, newest first, while rooms without any red-packet record preserve their original relative order.

Bulk red-packet monitoring distributes rooms across all enabled monitoring accounts with stable room assignment, shared and per-account request pacing, cooldown after rate-limit/transport failures, and automatic failover. Rate-limit or transport errors must never be presented as Cookie expiry. Monitoring logs and room rows retain account-to-room attribution while raw Cookie data remains inside the Go engine.

Followed-live discovery is account-centric rather than instance-centric. Each participation account's browser credential refreshes its Douyin followed-live feed at most once per refresh cycle, and successful fresh snapshots upsert into the canonical room list by public `web_rid` first and actual room ID second. Never delete canonical rooms or history merely because a later followed-live snapshot omits them. When several participation accounts report the same room, keep one room with all safe source attributions; render one source as `账号名的关注` and multiple sources as `首个账号等 N 个账号的关注` with the full account list in the shared tooltip. Participation accounts only discover rooms; the independent monitoring-account pool performs red-packet monitoring.

Every successful followed-live account snapshot is authoritative only for that account's current-live flags: mark omitted attributions offline without deleting their canonical rooms or source history. Treat a fresh followed-live flag as evidence that the room is currently live, expire stale evidence after a short refresh grace period, and surface it in the combined room status even before red-packet probing catches up. The live-room sort menu includes `实例优先`, which brings all rooms discovered from participation-account browser credentials to the top, with currently live and most recently seen rooms first.

Windows in-app upgrades follow Pilot/Tauri Updater behavior: launch the NSIS package in passive update mode with `/P /R /UPDATE`, never as a normal installer that shows the “Already Installed” uninstall-choice wizard. Keep the PowerShell update helper hidden, wait for the current client to exit, preserve local application data, and let the installer relaunch the upgraded client.

Keep the native Windows title bar and system minimize/maximize/close controls. On Windows, remove the macOS-only web title strip entirely so primary navigation starts directly below the native title bar, and disable sidebar collapsing rather than placing a toggle in the content topbar. Preserve the normal page icon before the content title and keep the Windows topbar slightly more vertically relaxed than the macOS chrome. Preserve the existing macOS title strip, collapsible behavior, traffic-light alignment, page-title icon, and right-opening toggle Tooltip.

On Windows, retain the native caption and system window controls. Keep the sidebar warm gray while the main topbar and content remain opaque pure white, and remove the sidebar's visible right divider because the background contrast already separates the regions. Preserve the transparent resize hit area without revealing or changing a divider on hover or drag; do not switch to a custom-drawn Windows title bar merely for this blend.

参与账号拥有持久化的红包接口参与开关，默认关闭；灰色表示关闭、红色表示开启。只有开关开启、CK 有效且不在冷却期的参与账号才能被 Go 引擎分配红包参与任务。参与请求按账号和红包事件幂等去重；关闭开关仅阻止未来分配，不中断已发出的请求。原始 CK 和签名请求始终留在 Go/Rust 原生层。

红包参与设置包含持久化的关注范围策略：“不限 / 关注列表优先 / 只参加关注主播”，默认“关注列表优先”。关注关系必须按每个参与账号自己的原生关注直播快照独立判断；优先模式先为关注主播红包保留账号的单一未开奖参与名额，关注快照暂不可用时按不限处理；“只参加关注主播”在无法确认匹配时不发送请求。该策略必须由 Go 调度器在原生请求前和积压事件重试时再次执行，并在安全参与日志中记录策略与匹配结果；关注关系、CK 与请求凭证不得返回前端 JavaScript。

红包参与设置包含持久化的“最低钻石”门槛，默认 1 钻。只有在红包数据能够可靠给出总钻石数与份数或明确的每份钻石数时，Go 引擎才在原生请求前按每份金额执行门槛；金额未知或信息不完整时不得猜测，也不得因此拦截参与。

浏览器实例页提供全局“启动任务”入口，支持立即执行、指定日期、每天固定时间和间隔执行。计划定义、下次执行时间和原子到期领取必须持久化在 Go redpacket store；间隔计划保存后立即执行第一轮。每次触发只批量准备真实已登录浏览器实例的红包页面上下文并开启对应参与账号的红包接口参与池，已激活任务不得重复并发启动；失败实例回滚本次自动开启的参与池开关。计划创建、触发、批量成功数和跳过数必须写入安全的最近活动，原始 CK、签名和接口响应仍不得进入前端。

顶部批量红包参与每次执行在最近活动中只保留一条批次摘要，不重复显示逐账号启动活动。摘要文案包含执行方式、参与实例数和跳过数；活动标题比数据概览小一号，第二行按“相对时间、停止、详情”的顺序显示紧凑控件，不显示向右展开箭头。详情图标打开紧凑弹窗展示本批次账号及安全状态，弹窗打开期间必须持续隐藏原生实例 WebView，停止图标则停止整个批次。停止批次应先在 Go 层阻止这些账号的未来分配并关闭参与池，再清理对应原生页面上下文；已经发出的单次请求允许完成。

最近活动中的普通历史消息只读展示，不得复用页面切换或刷新行为制造虚假的可点击反馈；只有具备账号明细的红包参与批次摘要提供详情弹窗入口，运行中的批次另提供明确的停止操作。

“启动任务”入口放在浏览器实例标题第一行并利用标题右侧空白做内联收缩展开；关闭时箭头向右提示展开，展开后箭头向左提示收回。操作项不得使用独立浮层、外框或阴影，第二行只保留实例、容量和资源信息。展开内容必须留在紧凑标题栏自身的几何范围内，不得撑高标题栏、下推实例卡片或隐藏真实浏览器子 WebView；操作区与下方原生 WebView 不得发生几何相交，CSS 层级不能作为覆盖原生 WebView 的解决方案。

“管理计划”不放在启动方式展开组中；当存在已启用计划时，在浏览器实例标题第二行的 CPU/内存后显示紧凑入口和数量。管理面板只展示计划管理内容，支持拖动标题移动、拖动右下角调整大小，所有尺寸下都禁止横向滚动；新增计划仍由各启动方式入口打开。

浏览器实例标题中的“启动任务”使用紧凑绿色语义；“管理计划”不使用任何按钮或数量徽标背景，只通过棕金色图标、文字和数字区分启动操作与计划管理，hover 也只加深文字颜色。

执行计划中的间隔单位使用与应用一致的自定义紧凑下拉菜单，不使用浏览器或系统原生 `select` 外观。

红包接口参与必须使用参与账号专属浏览器实例的真实直播页面上下文。实例卡片提供紧凑的红包图标；点击后先将该账号的原生子 WebView 切换到已确认开播的直播间并验证登录，再允许 Go 调度红包参与。`join/rush` 必须由直播页面中的 `bdms.js`/`window.fetch` 生成动态签名，禁止回退到脱离页面上下文的 Go HTTP 直连。页面上下文未准备好时不要创建误导性的参与失败记录；原始 Cookie、签名 URL 和原始接口响应不得进入前端 JavaScript。

计划调度不得依赖浏览器实例页面是否处于当前视图，也不得依赖实例卡片是否已经渲染。调度到点后，原生层必须复用现有账号 WebView，或使用同一账号隔离数据目录与原生 Cookie 注入链路按需创建不可见 WebView，等待直播页面和登录状态就绪后再启用红包参与；原始 Cookie 和页面请求结果仍不得进入前端 JavaScript。

浏览器实例卡片的红包图标必须是真实的双向开关：启用后再次点击应通过原生通道注销该账号的页面参与上下文并取消所有尚未发出的任务，不能只改变前端样式；已经发出的单次请求允许结束。自动页面参与对每个红包事件只允许发送一次 `join`，禁止在未捕获真实页面交互请求模板时猜测式追加 `rush` 回退，因为同一次事件的 `join → rush` 双请求会触发 `rush_spam` 风控。

红包监测 payload 中的 `activity_id` 只是活动分组键，可能是跨直播间重复的 `AC...` 业务标识；参与接口必须优先使用当前原始盒子行里 3 位以上的纯数字 `box_id_str/box_id`，缺少真实数字 box ID 时不得发送参与请求。单个参与账号的页面参与任务必须严格串行，并在每次发送前重新检查冷却状态，禁止同账号并发 `join/rush` 造成假性 `rush_spam` 风控。

在“账号与直播间”的“监测账号”后保留独立“参与记录”页签。每个账号与红包事件的参与尝试必须在 Go redpacket store 中先持久化占位再发送请求，以提供跨客户端重启的幂等去重；记录只向前端暴露账号、红包、直播间、接口类型、请求次数、结果、冷却和时间等安全元数据，绝不保存或返回 CK、签名参数、请求头或原始响应体。

浏览器实例页的标题副文案在“本机运行”后紧凑显示实时 CPU 与内存占用百分比；资源数据由 Go 原生层采样并随现有容量轮询刷新，详细内存用量使用共享暗色 Tooltip 展示。

远程同步服务与桌面客户端保留在同一个仓库，但使用独立的 `fubao-sync-server` Go 二进制、`server-v*` 版本和 GitHub 构建发布流程。客户端默认只与 `https://fbv2.ccvar.com/api/v1` 通信，通过权限受限的 Go 私有配置保存设备令牌，并使用可恢复的本地 Outbox 非阻塞地同步直播间状态与红包事件。远程协议只允许经过白名单定义的安全字段；Cookie、签名、请求头、原始接口响应、参与账号凭证以及不必要的本地账号标识不得离开 Go 原生层。服务端由 systemd 运行、Caddy 终止 HTTPS、SQLite 持久化，并以幂等键处理客户端至少一次投递。

远程同步客户端先以 `https://fbv2.ccvar.com/healthz` 验证标准入口；健康检查或后续服务请求不可用时，自动降级到 `https://fbv2.ccvar.com:8087/healthz` 与对应的 `/api/v1` 入口。备用入口稳定后短期保持使用，随后自动重试标准入口；鉴权或参数类 4xx 错误不得通过切换端口掩盖。服务端 Caddy 配置必须同时提供标准 HTTPS 和 HTTPS 8087，安装提示明确要求放行 TCP 8087。

远程直播间/红包同步由用户在“授权管理”中配置。界面只接收服务端注册令牌并直接交给 Go 引擎；启用同步前必须先兑换为设备凭证，状态接口不得返回注册令牌或设备凭证，兑换成功后不得再次显示注册令牌明文。更换令牌验证失败时必须保留上一套可用设备注册。

“授权管理”中的中心同步区命名为“中心库数据获取”。已绑定状态只显示 Go 引擎返回的脱敏令牌、紧邻其后的图标式“更换令牌”操作和连接状态，不显示线路、待同步数量、最近成功时间或启停接收操作；完整注册令牌和设备凭证始终不得进入前端 JavaScript。

中心库令牌编辑状态把令牌输入框与“取消 / 保存并连接”操作排在同一行，按钮紧邻输入框右侧，输入框与按钮保持相同高度；仅在窄窗口空间不足时换行。

所有绑定中心服务的客户端共享直播间与红包业务数据。任一客户端的本地发现按稳定直播间/红包标识上传中心库，其他客户端使用持久化增量游标自动拉取并合并；本机产生的记录保留本地来源，仅从远端取得的记录在直播间和红包列表明确标记“来源于中心库”。客户端必须忽略自身中心变更并禁止把纯中心来源数据回传，避免循环同步；Cookie、账号、签名、请求上下文和参与数据仍不得同步。

中心库上传不以同步 KEY 或授权状态为前提：未绑定同步 KEY 的客户端自动注册仅上传设备身份，持续上传安全白名单内的直播间与红包数据，但服务端必须拒绝该身份读取中心增量。绑定同步 KEY 后才取得完整设备身份；无有效授权仍只上传，有限期且当前有效的授权只拉取红包，永久有效授权才拉取直播间与红包。直播间和红包使用独立持久化游标，保证有限期授权升级为永久授权后仍能补拉直播间；已落地的中心数据不因授权变化主动删除。

免费版或无有效授权的客户端最多只能创建一个浏览器实例；有效专业版不受该授权数量限制，仍服从机器资源建议上限。实例数量限制必须由 Go 引擎在复用判断与持久化新实例的同一临界区内强制执行，前端只负责同步提示与免费版单选；授权失效不得自动删除或关闭既有实例，只阻止继续新增。
