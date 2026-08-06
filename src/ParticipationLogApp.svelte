<script lang="ts">
  import { emit, listen } from "@tauri-apps/api/event";
  import { onMount } from "svelte";
  import {
    CaretRightIcon as CaretRight,
    CheckCircleIcon as CheckCircle,
    TerminalWindowIcon as TerminalWindow,
    TrashIcon as Trash,
    WarningCircleIcon as WarningCircle,
  } from "phosphor-svelte";

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
    follow_policy?: "all" | "follow_priority" | "follow_only";
    followed?: boolean;
    follow_match_known?: boolean;
    created_at: string;
  };

  type ParticipationLogState = {
    logs: ParticipationTrace[];
    join: number;
    receive: number;
    error: number;
  };

  let state: ParticipationLogState = { logs: [], join: 0, receive: 0, error: 0 };

  function actionLabel(action: string) {
    if (action === "join") return "参与请求";
    if (action === "receive_timeout") return "开奖超时";
    if (action === "receive") return "开奖结果查询";
    return action || "原生请求";
  }

  function endpointLabel(endpoint?: string, httpStatus?: number) {
    // endpoint=page is the native page-script channel result when no Douyin
    // join/receive HTTP exchange completed (timeout / not in room / context).
    const name =
      endpoint === "join"
        ? "join"
        : endpoint === "receive"
          ? "receive"
          : endpoint === "rush"
            ? "rush"
            : endpoint === "page" || !endpoint
              ? "页面上下文"
              : endpoint;
    return httpStatus ? `${name} · HTTP ${httpStatus}` : name;
  }

  function exactTime(value: string) {
    const parsed = Date.parse(value);
    return Number.isFinite(parsed) ? new Date(parsed).toLocaleString("zh-CN", { hour12: false }) : value;
  }

  function prettyJSON(value: unknown) {
    if (typeof value !== "string") return JSON.stringify(value, null, 2);
    if (!value) return "{}";
    try {
      return JSON.stringify(JSON.parse(value), null, 2);
    } catch {
      return value;
    }
  }

  function followLabel(log: ParticipationTrace) {
    if (log.follow_policy === "follow_only") return log.followed ? "只参加关注主播 · 已匹配" : "只参加关注主播";
    if (log.follow_policy === "follow_priority") {
      if (!log.follow_match_known) return "关注列表优先 · 快照不可用，按不限处理";
      return log.followed ? "关注列表优先 · 已匹配关注主播" : "关注列表优先 · 非关注主播候选";
    }
    if (log.follow_policy === "all") return "不限关注范围";
    return "";
  }

  async function clearLogs() {
    await emit("participation-log://clear");
  }

  onMount(() => {
    let unlistenState: (() => void) | undefined;
    void listen<ParticipationLogState>("participation-log://state", (event) => {
      state = event.payload;
    }).then((unlisten) => {
      unlistenState = unlisten;
      void emit("participation-log://ready");
    });
    return () => unlistenState?.();
  });
</script>

<svelte:head><title>红包参与详细日志</title></svelte:head>

<main class="participation-log-window">
  <header class="participation-log-header" data-tauri-drag-region>
    <div><span><TerminalWindow size={14} /></span><h1>红包参与详细日志</h1></div>
  </header>
  <section class="participation-log-summary">
    <div><span><strong>{state.join}</strong> 参与</span><i>·</i><span><strong>{state.receive}</strong> 开奖查询</span><i>·</i><span><strong>{state.error}</strong> 异常</span></div>
    <button disabled={state.logs.length === 0} onclick={clearLogs}><Trash size={12} />清空日志</button>
  </section>
  <section class="participation-log-list" aria-label="参与请求日志">
    {#if state.logs.length === 0}
      <div class="participation-log-empty">暂无参与请求日志</div>
    {:else}
      {#each [...state.logs].reverse() as log (log.id)}
        {@const failed = Boolean(log.error || (log.http_status && (log.http_status < 200 || log.http_status >= 300)))}
        <details class:error={failed} class="participation-log-row">
          <summary>
            <CaretRight class="participation-log-caret" size={11} />
            {#if failed}<WarningCircle class="status-error" size={12} weight="fill" />{:else}<CheckCircle class="status-success" size={12} weight="fill" />{/if}
            <time>{exactTime(log.created_at)}</time>
            <strong>{log.account_name || log.account_id.slice(0, 8)}</strong>
            <span>{actionLabel(log.action)}</span>
            <em>{endpointLabel(log.endpoint, log.http_status)}</em>
          </summary>
          <div class="participation-log-detail">
            {#if followLabel(log)}<section><h2>关注策略</h2><pre>{followLabel(log)}</pre></section>{/if}
            <section><h2>请求参数</h2><pre>{prettyJSON(log.request_params)}</pre></section>
            <section><h2>响应参数</h2><pre>{prettyJSON(log.response_params)}</pre></section>
            {#if log.error}<section class="participation-log-error"><h2>异常</h2><pre>{log.error}</pre></section>{/if}
            <small>事件 {log.event_id} · 任务 {log.task_id || "未归属"}</small>
          </div>
        </details>
      {/each}
    {/if}
  </section>
</main>

<style>
  :global(*) { box-sizing: border-box; }
  :global(html), :global(body), :global(#app) { width: 100%; height: 100%; margin: 0; overflow: hidden; background: #fff; }
  :global(body) { color: #37332e; font: 12px/1.45 -apple-system, BlinkMacSystemFont, "SF Pro Text", "PingFang SC", sans-serif; }
  button { font: inherit; cursor: pointer; }
  button:disabled { cursor: default; opacity: .48; }
  .participation-log-window { display: flex; width: 100%; height: 100%; flex-direction: column; padding: 0 14px 12px; background: #fff; }
  .participation-log-header { display: flex; min-height: 32px; align-items: center; justify-content: center; margin: 0 -14px 4px; user-select: none; }
  .participation-log-header > div { display: flex; align-items: center; gap: 7px; }
  .participation-log-header span { display: grid; width: 22px; height: 22px; place-items: center; border-radius: 6px; background: #eaf5f0; color: #2f9274; }
  h1 { margin: 0; font-size: 14px; line-height: 1; }
  .participation-log-summary { display: flex; min-height: 24px; align-items: center; justify-content: space-between; }
  .participation-log-summary > div { display: flex; align-items: baseline; gap: 6px; color: #8d887f; font-size: 10px; }
  .participation-log-summary strong { color: #38342f; font-size: 12px; }
  .participation-log-summary i { color: #c2bdb5; font-style: normal; }
  .participation-log-summary button { display: flex; height: 20px; align-items: center; gap: 3px; padding: 0 5px; border: 0; border-radius: 5px; background: transparent; color: #777168; font-size: 10px; }
  .participation-log-summary button:hover:not(:disabled) { background: #f3f1ed; }
  .participation-log-list { flex: 1 1 auto; min-height: 0; overflow: auto; border: 1px solid #e8e4dd; border-radius: 10px; scrollbar-width: thin; scrollbar-color: #cbc6bd transparent; }
  .participation-log-empty { display: grid; height: 100%; place-items: center; color: #9a958c; }
  .participation-log-row { border-bottom: 1px solid #eeeae4; }
  .participation-log-row:last-child { border-bottom: 0; }
  .participation-log-row summary { display: grid; grid-template-columns: 11px 12px 132px minmax(80px, .65fr) minmax(90px, .65fr) minmax(90px, auto); min-height: 30px; align-items: center; gap: 7px; padding: 0 9px; cursor: pointer; list-style: none; }
  .participation-log-row summary::-webkit-details-marker { display: none; }
  .participation-log-row summary:hover { background: #faf9f7; }
  :global(.participation-log-caret) { color: #a39e96; transition: transform .15s ease; }
  details[open] :global(.participation-log-caret) { transform: rotate(90deg); }
  :global(.status-success) { color: #319576; }
  :global(.status-error) { color: #cb624e; }
  time, summary em { overflow: hidden; color: #969188; font-size: 10px; font-style: normal; text-overflow: ellipsis; white-space: nowrap; }
  summary strong, summary span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  summary strong { font-size: 10.5px; }
  summary span { color: #5d574f; font-size: 10px; }
  .participation-log-detail { display: grid; gap: 8px; padding: 8px 12px 11px 39px; background: #faf9f7; }
  .participation-log-detail section { min-width: 0; }
  .participation-log-detail h2 { margin: 0 0 4px; color: #777168; font-size: 10px; }
  pre { max-height: 190px; margin: 0; overflow: auto; padding: 8px; border: 1px solid #e6e1da; border-radius: 7px; background: #fff; color: #4f4a43; font: 10px/1.5 ui-monospace, SFMono-Regular, Menlo, monospace; white-space: pre-wrap; word-break: break-all; }
  .participation-log-error pre { color: #a44b3e; }
  .participation-log-detail small { overflow: hidden; color: #aaa59c; font-size: 9px; text-overflow: ellipsis; white-space: nowrap; }
  @media (max-width: 560px) {
    .participation-log-row summary { grid-template-columns: 11px 12px 100px minmax(70px, 1fr) minmax(80px, 1fr); }
    .participation-log-row summary em { display: none; }
  }
</style>
