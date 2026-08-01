<script lang="ts">
  import { emit, listen } from "@tauri-apps/api/event";
  import { onMount, tick } from "svelte";
  import {
    CheckCircleIcon as CheckCircle,
    CircleIcon as Circle,
    TerminalWindowIcon as TerminalWindow,
    TrashIcon as Trash,
    WarningCircleIcon as WarningCircle,
  } from "phosphor-svelte";

  type MonitorRuntimeLog = {
    id: string;
    at: string;
    level: "info" | "success" | "warning" | "error";
    message: string;
  };

  type MonitorLogState = {
    logs: MonitorRuntimeLog[];
    running: number;
    stopped: number;
    error: number;
  };

  const emptyState: MonitorLogState = { logs: [], running: 0, stopped: 0, error: 0 };
  let state: MonitorLogState = emptyState;
  let logList: HTMLElement | undefined;

  function logIcon(level: MonitorRuntimeLog["level"]) {
    if (level === "success") return CheckCircle;
    if (level === "error") return WarningCircle;
    return Circle;
  }

  async function clearLogs() {
    state = { ...state, logs: [] };
    void scrollLogListToBottom();
    try {
      await emit("monitor-log://clear");
    } catch {
      // The parent window may already be closing.
    }
  }

  async function scrollLogListToBottom() {
    await tick();
    if (logList) logList.scrollTop = logList.scrollHeight;
  }

  onMount(() => {
    let unlistenState: (() => void) | undefined;
    void listen<MonitorLogState>("monitor-log://state", (event) => {
      state = event.payload;
      void scrollLogListToBottom();
    }).then((unlisten) => {
      unlistenState = unlisten;
      void emit("monitor-log://ready");
    });

    return () => unlistenState?.();
  });
</script>

<svelte:head>
  <title>红包监测运行日志</title>
</svelte:head>

<main class="native-log-window">
  <!-- Native traffic lights remain visible over this white overlay title row. -->
  <header class="native-log-header" data-tauri-drag-region>
    <div class="native-log-title-group">
      <span class="native-log-icon"><TerminalWindow size={14} /></span>
      <h1>红包监测运行日志</h1>
    </div>
  </header>

  <section class="native-log-summary" aria-label="运行统计">
    <div class="native-log-stats">
      <span><strong>{state.running}</strong> 运行中</span>
      <i aria-hidden="true">·</i>
      <span><strong>{state.stopped}</strong> 未启动</span>
      <i aria-hidden="true">·</i>
      <span><strong>{state.error}</strong> 异常</span>
    </div>
    <button class="native-log-clear" disabled={state.logs.length === 0} onclick={clearLogs}>
      <Trash size={12} />清空日志
    </button>
  </section>

  <section class="native-log-list" bind:this={logList} aria-label="运行日志">
    {#if state.logs.length === 0}
      <div class="native-log-empty">暂无运行记录</div>
    {:else}
      {#each state.logs as log (log.id)}
        {@const Icon = logIcon(log.level)}
        <article class:error={log.level === "error"} class:success={log.level === "success"} class:warning={log.level === "warning"} class="native-log-row">
          <time>{log.at}</time>
          <Icon size={11} weight={log.level === "info" ? "regular" : "fill"} />
          <p>{log.message}</p>
        </article>
      {/each}
    {/if}
  </section>

</main>

<style>
  :global(*) { box-sizing: border-box; }
  :global(html), :global(body), :global(#app) {
    width: 100%;
    min-width: 0;
    min-height: 100%;
    height: 100%;
    margin: 0;
    overflow: hidden;
    background: #ffffff;
  }

  :global(body) {
    color: #35322e;
    font: 13px/1.45 -apple-system, BlinkMacSystemFont, "SF Pro Text", "PingFang SC", sans-serif;
  }

  .native-log-window {
    display: flex;
    flex-direction: column;
    width: 100%;
    height: 100%;
    padding: 0 16px 12px;
    background: #ffffff;
  }

  .native-log-header {
    display: flex;
    align-items: center;
    justify-content: center;
    flex: 0 0 32px;
    min-height: 32px;
    margin: 0 -16px 4px;
    padding: 0 16px;
    background: #ffffff;
    user-select: none;
    -webkit-user-select: none;
  }

  .native-log-title-group { display: flex; align-items: center; gap: 7px; min-width: 0; }
  .native-log-icon {
    display: grid;
    place-items: center;
    width: 22px;
    height: 22px;
    border-radius: 6px;
    color: #2f9274;
    background: #eaf5f0;
  }
  h1 { margin: 0; font-size: 15px; line-height: 1.2; font-weight: 700; letter-spacing: -0.01em; }

  button { font: inherit; cursor: pointer; }
  button:disabled { cursor: default; opacity: 0.52; }

  .native-log-summary {
    display: flex;
    align-items: center;
    justify-content: space-between;
    min-height: 20px;
    margin-bottom: 2px;
  }
  .native-log-stats { display: flex; align-items: baseline; gap: 6px; color: #8c877f; font-size: 10.5px; }
  .native-log-stats strong { color: #38342f; font-size: 13px; line-height: 1; }
  .native-log-stats i { color: #c1bcb4; font-style: normal; }
  .native-log-clear {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 3px;
    height: 20px;
    padding: 0 5px;
    border: 0;
    border-radius: 5px;
    color: #777168;
    background: transparent;
    font-size: 10.5px;
    font-weight: 600;
  }
  .native-log-clear:hover:not(:disabled) { color: #514c45; background: #f3f1ed; }

  .native-log-list {
    display: flex;
    flex-direction: column;
    justify-content: flex-start;
    flex: 1 1 auto;
    min-height: 0;
    overflow: auto;
    border: 1px solid #e9e5de;
    border-radius: 10px;
    background: #ffffff;
    scrollbar-width: thin;
    scrollbar-color: #c9c5bd transparent;
  }
  .native-log-list::-webkit-scrollbar { width: 7px; }
  .native-log-list::-webkit-scrollbar-track { background: transparent; }
  .native-log-list::-webkit-scrollbar-thumb { border-radius: 999px; background: #c9c5bd; }
  .native-log-row {
    display: grid;
    grid-template-columns: 54px 11px minmax(0, 1fr);
    flex: 0 0 20px;
    height: 20px;
    align-items: center;
    gap: 5px;
    min-height: 20px;
    padding: 0 8px;
    color: #565149;
    font-size: 10px;
    line-height: 1;
  }
  .native-log-row time { color: #9a958d; font-size: 10px; font-variant-numeric: tabular-nums; text-decoration: none; }
  :global(.native-log-row > svg) { color: #8e8a82; }
  :global(.native-log-row.success > svg) { color: #319576; }
  :global(.native-log-row.warning > svg) { color: #c28a2e; }
  :global(.native-log-row.error > svg) { color: #c95143; }
  .native-log-row p {
    min-width: 0;
    margin: 0;
    overflow: hidden;
    text-decoration: none;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .native-log-empty { display: grid; flex: 1; min-height: 72px; place-items: center; color: #9a958d; }

</style>
