# Design QA

## Evidence

- Source visual truth:
  - `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-40c55c9c-7822-4e1b-8760-fe8cfc86c7ed.png`
  - `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-7ce8d455-6d4b-47d0-92c1-692a7d5ccc8e.png`
  - `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-f0d0419e-2d58-4ffd-b622-632b8f3c74d3.png`
  - `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-1e9aace2-a4c8-40da-8717-3d66652c0d62.png`
- Rendered implementation:
  - `.design-qa/implementation-rooms-adaptive.png`
  - `.design-qa/implementation-accounts-adaptive.png`
- Same-input comparisons:
  - `.design-qa/compare-room-source-implementation.png`
  - `.design-qa/compare-accounts-source-implementation.png`
  - `.design-qa/compare-search-source-collapsed.png`
  - `.design-qa/compare-search-source-expanded.png`
- Native implementation viewport: 1180 × 760 px at 1× CSS scale.
- Source captures: 1926 × 1516 px and 1932 × 1496 px at 1× image density.
- Comparison normalization: each source and implementation capture was proportionally resized to 900 px wide and placed side by side without cropping.
- State:
  - “账号与直播间 / 直播间” with 2636 migrated rooms.
  - “账号与直播间 / 参与账号” with 6 rows.
  - Top-toolbar search in collapsed and expanded states.

## Full-view comparison

- The room panel now fills the available content height while retaining a compact bottom inset.
- The six-row participation panel now ends immediately after its final row instead of stretching to the bottom of the window.
- The top bar, search row, three-tab hierarchy, table columns, semantic badges, and sidebar remain visually consistent between both states.
- The former full-width content search row is removed; its space is returned to the room list.

## Focused region comparison

- Bottom edge of the room panel: the excessive lower gap highlighted in the source is reduced to the compact content inset.
- Bottom edge of the participation panel: the large blank area highlighted in the source is removed.
- The room status badge no longer stretches across its grid track; it keeps intrinsic pill width.
- The toolbar shows only a 24 px search icon by default. Clicking it expands a compact 250 px × 28 px search field without increasing the 60 px top-bar height.

## Required fidelity surfaces

- Typography: existing system font family, compact title hierarchy, weights, truncation, and small table copy are preserved.
- Spacing and layout: adaptive panel height is state-aware; dense room rows keep internal scrolling, while short account lists use intrinsic height.
- Colors and tokens: existing paper, line, green, amber, coral, and blue semantic tokens remain unchanged.
- Image and icon quality: existing Phosphor icon components are retained; no raster placeholders or improvised icon artwork were introduced.
- Copy and content: “账号与直播间”, “直播间 / 参与账号 / 监测账号”, and “导入数据 / 导入直播间” remain intact.
- Toolbar semantics: the non-functional shield icon is removed, while search, refresh, and import remain clearly separated.

## Interaction verification

- Three management tabs switch correctly.
- “导入数据” opens a menu whose first action is “导入直播间”.
- The room search state and account search state use their respective placeholder copy.
- Search opens with keyboard focus, filters live, and closes with its close button or Escape while clearing the hidden filter.
- The native client displayed the migrated total of 2636 rooms and the account counts of 6 participation / 99 monitoring.
- The development terminal showed no frontend error or unhandled rejection during these checks.
- The management tab strip now sits outside the framed table, while the table header/list retain the only visible card border.
- The account status filter is rendered in the account tab strip and opens as a native menu without inheriting tab-count pill backgrounds.

## Comparison history

1. Earlier P2: the room panel had too much space below it, reducing the usable list height.
   - Fix: reduced the account-management content bottom inset from 42 px to 18 px and retained the room panel as a viewport-filling internal-scroll region.
   - Post-fix evidence: `.design-qa/implementation-rooms-adaptive.png`.
2. Earlier P2: short participation lists inherited `flex: 1`, leaving a large blank area inside the card.
   - Fix: panels with eight or fewer account rows now size to content; room and longer account lists retain the fill-and-scroll behavior.
   - Post-fix evidence: `.design-qa/implementation-accounts-adaptive.png`.
3. Earlier P2: room status pills stretched to the full width of their grid column.
   - Fix: room-row status pills now use `justify-self: start`.
   - Post-fix evidence: `.design-qa/implementation-rooms-adaptive.png`.
4. Earlier P2: search occupied a full content row and the adjacent shield icon suggested functionality that did not exist.
   - Fix: moved search into a progressive-disclosure top-bar control, removed the empty content row, and deleted the non-functional shield action.
   - Post-fix evidence:
     - `.design-qa/implementation-search-collapsed.png`
     - `.design-qa/implementation-search-expanded.png`
5. Earlier P2: the account tab strip and table shared one card frame, and moving the status filter into the strip caused nested tab styles to leak into the filter menu.
   - Fix: moved the tab strip outside `.account-panel`, aligned its horizontal inset with the table content, and scoped tab selectors to direct tab buttons only.
   - Post-fix evidence:
     - `.design-qa/implementation-tabs-outside-rooms.png`
     - `.design-qa/implementation-tabs-outside-accounts.png`
     - `.design-qa/implementation-tabs-final.png`
6. Earlier P2: the external tab strip had asymmetric top/bottom whitespace and its inset diverged from the responsive table padding.
   - Fix: balanced the strip with a 17px top content inset and 18px lower separation, applied a shared responsive inline-padding token to tabs and all table rows, and lightened the selected tab/count surfaces.
   - Post-fix evidence: `.design-qa/implementation-tabs-spacing-final.png`.
7. Earlier P2: the external tab rail remained inset from the framed table on both sides, making the left and right edges read as misaligned.
   - Fix: removed the tab rail's row-content padding so its outer edges align directly with the table frame; table headers and rows retain their own responsive inset.
   - Post-fix evidence: `.design-qa/implementation-tabs-outer-aligned.png`.

## Findings

- No actionable P0, P1, or P2 visual differences remain for the requested account/table framing, alignment, and adaptive-height behavior.

## Follow-up polish

- P3: a future virtualized room list could replace incremental 300-row rendering if the room collection grows substantially beyond the current 2636 records.

## Embedded browser follow-up

- Source reference: `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-ff72c524-3919-489d-8ae7-f6495a287b7e.png`.
- Implementation capture: `.design-qa/implementation-embedded-browser-adaptive.png`.
- Full-view comparison: `.design-qa/compare-embedded-browser-adaptive.png`.
- Focused comparison: `.design-qa/compare-embedded-browser-focused.png`.
- Native implementation viewport: 1180 × 760 px at 1× CSS scale.
- Source image: 890 × 980 px at 1× image density.
- State: “浏览器实例”, one instance for account “川、”, with a real interactive Douyin child WebView showing the live login QR overlay returned by `douyin.com`.

### Embedded browser fidelity

- The card preserves the reference anatomy: account header, status pill, embedded browser region, runtime metadata, and separate “打开实例” action.
- The placeholder browser artwork has been replaced with a native Tauri child WebView. It is not a screenshot, canvas reproduction, iframe, or frontend mock.
- The child WebView uses independent storage per account and receives its Cookie through the authenticated native Go-to-Rust path without exposing the raw value to the frontend.
- Page zoom is calculated from the live child width against a desktop content target and constrained to 26%–50%, rounded to stable 1% steps so card-height reflow does not cause visible scale jumps.
- Window resize, sidebar drag/collapse, scrolling, filtering, and card relayout all resynchronize child position, size, visibility, and zoom.

### Embedded browser interaction verification

- The macOS accessibility tree exposed a separate native HTML content tree with title “抖音精选电脑版 - 抖音旗下优质视频平台” and URL `douyin.com/jingxuan`.
- Real Douyin links, search input, navigation tabs, video items, and the login overlay were present in the child accessibility tree.
- Navigating away from “浏览器实例” hides the child WebView so it cannot cover other client pages.
- “打开实例” remains a separate action that opens the account profile in its own Chrome window.
- The tested migrated Cookie did not restore the account session, so the live site correctly displayed its QR login overlay. A refreshed valid Cookie is required to show that account's authenticated feed.

### Embedded browser findings

- No actionable P0, P1, or P2 implementation or fidelity issues remain.
- P3: the account currently used for validation should refresh or re-import its Cookie to verify the authenticated Douyin state inside the card.

final result: passed

## Windows sidebar-toggle edge alignment

- Source reference: `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-7660d8fb-721f-41ce-a95d-783010c90261.png`.
- Baseline captures:
  - `.design-qa/windows-toggle-expanded-mac-baseline.png`.
  - `.design-qa/windows-toggle-collapsed-mac-baseline.png`.

### Requested refinement

- Windows keeps the native system title bar and window controls.
- In the expanded layout, the sidebar toggle is anchored 10 px from the sidebar's right edge instead of floating near the title-strip center.
- In the fully collapsed layout, the toggle is anchored 16 px from the content window's left edge.
- macOS retains its native-traffic-light-aligned placement.

### Verification

- The local app was checked in both expanded and collapsed states at `http://127.0.0.1:1437/`.
- The baseline confirms the macOS placement and page-title alignment remain unchanged.
- The Windows-only CSS resolves the expanded toggle to the sidebar edge and the collapsed toggle to the content edge without replacing the native Windows title bar.
- `npm run check`, `npm run build`, and `npm run test:sites` pass.

### Findings

- No actionable P0, P1, or P2 issues remain for the requested Windows toggle placement.

final result: passed

## Resizable followed-live dialog

- Source reference: `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-0a327867-6e54-4f0d-8dae-232bd6df553a.png`.
- Implementation capture: `.design-qa/following-live-resizable.png`.
- The dialog now resizes within the desktop viewport, with minimum and maximum dimensions that keep its controls usable.
- The live-room list owns vertical scrolling through a quiet 3 px scrollbar and explicitly suppresses horizontal overflow.
- Flexible grid columns and a narrow-container reflow prevent room metadata from widening the dialog or creating a horizontal scrollbar.
- The dialog remains fixed on small screens, where free resizing would make the touch layout unstable.

final result: passed

## Browser followed-live count and detail modal

- Legacy reference inspected: `/Users/apple/work/DY-KIRO/src/network/live_discovery.py` (`discover_similar_rooms` and `similar_room_by_anchor`).
- Current implementation capture: `.design-qa/implementation-following-live-modal.png`.
- State: current Tauri development app, browser instance account “川、”, 14 followed accounts currently live.

### Behavior

- The Go engine signs and requests the current instance account's real followed-live feed; raw Cookie data never enters frontend JavaScript.
- Instance cards expose a compact `N 正在直播` action and a shared in-app tooltip.
- The detail modal lists safe metadata only: avatar, nickname, live title, room number, room identifier, viewer count, and an external-room action.
- Results are cached per canonical account for 60 seconds, with a stale-success fallback for temporary network failures instead of a false zero state.
- The legacy seed-room path remains a similar-room discovery mechanism and is not used as the followed-live data source.

### Verification

- A real engine RPC for “川、” returned 14 live items with room numbers and room identifiers.
- The current `target/debug/fubao-desktop` process rendered the count and opened the populated modal; no other 福宝/Wails window was used for acceptance.
- `npm run check`, `npm run build:engine`, and `go test ./...` pass for this implementation.

### Findings

- No actionable P0, P1, or P2 issues remain for the requested followed-live count or detail interaction.

final result: passed

## Browser-card identity and CK-badge hover follow-up

- Source references:
  - `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-40ffd67f-5d7d-4da7-87f7-54c2733cb06a.png`.
  - `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-bb1d8385-4158-4107-8d15-ad20e20c6d42.png`.

### Requested refinements

- Reduced the browser-instance identity tile to 20 px while keeping the account row on one compact line.
- Account name and Douyin ID now create the shared dark edge-safe Tooltip only when the rendered text is actually truncated; full text remains visually quiet and does not gain an unnecessary hover surface.
- Closing an instance now opens the compact confirmation dialog before native teardown and removal. The account-keyed profile and login environment remain available for later reuse.
- The `CK 已失效` badge is no longer a tooltip trigger and ignores pointer hover, so its coral status surface cannot acquire the gray hover box shown in the reference.

### Verification

- `npm run check` passes with 0 errors and 0 warnings.
- `npm run build` completes and preserves the required Sites artifacts.
- `npm run test:sites` passes all 4 tests.
- Static selector inspection confirms the expired badge has no `data-tooltip` attribute, no hover selector, and uses `pointer-events: none`.

### Findings

- No actionable P0, P1, or P2 issue remains for the requested icon size, overflow help, safe-close confirmation, or CK-badge hover behavior.

final result: passed

## Browser instance loading-state follow-up

- Source reference: `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-7c105ee4-6047-496a-8187-f9fd05eb959d.png`.
- Immediate loading capture: `.design-qa/implementation-browser-loading.png`.
- Settled native-WebView capture: `.design-qa/implementation-browser-loading-settled.png`.
- State: five account-owned browser instances mounting simultaneously in a three-column grid.

### Requested loading feedback

- Every card now displays a compact spinning indicator, “正在加载实例”, and the target account name while its native child WebView is being prepared.
- A newly created child WebView remains hidden and off-card until the first usable Douyin page load is ready, so an unpainted native white surface cannot cover the HTML loading state.
- Douyin SPA navigation has a bounded native reveal fallback so cards cannot remain indefinitely in the loading state when the page omits a reliable completed-load event.
- Native load failures clear the loading state and use the existing error presentation rather than leaving an ambiguous blank card.

### Verification

- The 0.2-second capture shows all five cards with the same compact, centered loading treatment and intact identity/action rows.
- The 5.7-second capture shows all five placeholders replaced in place by interactive native Douyin surfaces without card-height or grid-position shifts.
- The loading copy remains readable at the persisted three-column layout and does not overlap the close/open instance actions.
- `npm run check`, `cargo fmt --check`, and `cargo check` pass with the new event lifecycle.

### Findings

- No actionable P0, P1, or P2 issues remain for the requested browser-instance loading feedback.

final result: passed

## Single-line browser identity follow-up

- Source references:
  - `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-20a11777-b47e-437c-820a-d5e1ed3f8e3c.png`.
  - `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-50543a5f-3a4f-421f-8f6b-4f05545bf900.png`.
- Implementation capture: `.design-qa/implementation-browser-card-id-only.png`.
- Focused comparison: `.design-qa/compare-browser-card-id-only.png`.

### Requested refinements

- Removed the repeated `账号：…` subtitle so the identity area occupies one line.
- Reduced the instance icon to 28 px with a 17 px glyph.
- Placed the Douyin ID value directly after the account name using smaller, lighter text, without an `抖音号` prefix.
- Kept the external-window action aligned at the right and protected it from long ID overflow with truncation rules.

### Verification

- Accessibility output exposes `川、 59557349249` and `秋天🍃 2292903240813657` as single headings, with no `账号：` or `抖音号` prefix.
- Focused visual comparison confirms the row is shorter and the account name remains visually dominant over the muted numeric identifier.

### Findings

- No actionable P0, P1, or P2 issues remain for the requested identity-row simplification.

final result: passed

## Browser card content-first follow-up

- Source reference: `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-f3e3b188-e490-461e-b5af-5f0f063c9b6e.png` (928 × 1016 px).
- Implementation capture: `.design-qa/implementation-browser-card-content-first-loaded.png` (1180 × 760 px).
- Focused side-by-side comparison: `.design-qa/compare-browser-card-content-first.png`.
- State: “浏览器实例”, account “川、”, real Douyin WebView loaded with its login overlay.

### Requested hierarchy changes

- The real webpage is now the first and largest card surface.
- Account identity moved below the webpage, with a compact “打开实例” action aligned on the same row at the right.
- The “已嵌入” badge and the redundant “真实浏览器 · 可直接操作” footer hint were removed.
- Browser runtime, profile isolation, and Cookie/cache isolation metadata remain in a compact final row.
- Embedded content zoom now uses a stable width-only 26%–50% range, allowing more of the live page to fit inside compact cards without scale changes caused by card-height reflow.

### Verification

- The native accessibility tree confirms the card no longer exposes the “已嵌入” label or preview hint.
- “打开实例” remains an enabled button adjacent to the account name.
- The separate child accessibility tree reports the live title “抖音精选电脑版 - 抖音旗下优质视频平台” and URL `douyin.com/jingxuan`, confirming the content remains a real WebView.
- The focused comparison confirms that the oversized login panel is visibly reduced and the account/action row is materially more compact.

### Findings

- No actionable P0, P1, or P2 differences remain for the requested card hierarchy, action placement, redundant-copy removal, or additional page scaling.

final result: passed

## Minimal browser card follow-up

- Source reference: `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-8a5f93ba-edd2-4d2f-91a3-a067c314bd9f.png` (902 × 990 px).
- Implementation capture: `.design-qa/implementation-browser-card-minimal-loaded.png` (1180 × 760 px).
- Focused comparison: `.design-qa/compare-browser-card-minimal.png`.
- State: two real Douyin child WebViews loaded for accounts “川、” and “秋天🍃”.

### Requested reductions

- Removed the decorative `douyin.com` domain bar from the card preview.
- Removed the repeated “Google Chrome · 独立配置 · Cookie 与缓存已隔离” metadata row.
- The embedded-page scale now uses a width-only 26%–50% range with 1% quantization, fitting the desktop page consistently into cards of the same width.
- The resulting card contains only the real webpage plus the compact account identity and “打开实例” row.

### Verification

- Neither removed module appears in the frontend accessibility tree.
- Both child accessibility trees report the live Douyin title and `douyin.com/jingxuan` URL.
- The focused side-by-side comparison confirms that the card is shorter, the login panel is smaller, and no redundant chrome remains.

### Findings

- No actionable P0, P1, or P2 differences remain for the requested reductions.

final result: passed

## Flush browser surface and unobstructed tooltip follow-up

- Source references:
  - `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-f9ca5c11-6d53-4303-b41f-2839aab5a322.png`.
  - `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-81ba41eb-3b22-48c6-b7d8-6475c9084184.png`.
  - `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-23948ce0-933a-468a-8ff2-8721f4712c75.png`.
- Implementation captures:
  - `.design-qa/implementation-browser-card-stable-scale-loaded.png`.
  - `.design-qa/implementation-browser-card-tip-left.png`.
- Focused comparisons:
  - `.design-qa/compare-browser-card-stable-scale.png`.
  - `.design-qa/compare-browser-card-flush.png`.
  - `.design-qa/compare-browser-card-tip-left.png`.

### Requested refinements

- The live page now meets the outer card at its top, left, and right edges; the inner preview border, radius, shadow, and inset padding were removed while the outer card boundary remains intact.
- Page scaling now depends only on the child WebView width, is constrained to 26%–50%, and is rounded to 1% steps to avoid unstable resizing.
- The account row uses a compact 32 px identity icon and a 28 px icon-only external-window action.
- The dark “打开实例” tooltip opens horizontally to the action's left, inside the account row, so the native child WebView cannot cover it.

### Verification

- Focus and hover states expose the action label without overlapping the live webpage.
- The macOS accessibility tree still reports each embedded surface as a separate real Douyin child WebView with title “抖音精选电脑版 - 抖音旗下优质视频平台” and URL `douyin.com/jingxuan`.
- Side-by-side inspection confirms that no inner browser frame or top/side gutter remains.

### Findings

- No actionable P0, P1, or P2 issues remain for the requested scaling, spacing, compact action, or tooltip visibility changes.

final result: passed
