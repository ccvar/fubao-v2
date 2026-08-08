<script lang="ts">
  import { CaretDownIcon as CaretDown, CaretRightIcon as CaretRight } from "phosphor-svelte";

  type ParticipationTaskRun = {
    id: string;
    mode: string;
    title?: string;
    mode_label: string;
    status: "running" | "completed" | "stopped" | "abnormal" | string;
    account_count: number;
    skipped_count?: number;
    started_at: string;
    ended_at?: string;
    success_count: number;
    failure_count: number;
    win_count: number;
    win_diamonds: number;
    end_reason?: string;
    task_ids: string[];
    account_summaries?: Array<{ account_id: string }>;
  };

  type ParticipationTaskRunGroup = {
    key: string;
    started_at: string;
    runs: ParticipationTaskRun[];
    task_count: number;
    account_count: number;
    success_count: number;
    win_count: number;
    win_diamonds: number;
  };

  export let group: ParticipationTaskRunGroup;
  export let initialExpanded = false;
  export let selectedRunId = "";
  export let clock = Date.now();
  export let onSelect: (runID: string) => void;

  let expanded = initialExpanded;

  function runStatus(run: ParticipationTaskRun) {
    if (run.status === "running") return { label: "运行中", tone: "success" };
    if (run.status === "stopped") return { label: "已停止", tone: "muted" };
    if (run.status === "abnormal") return { label: "异常结束", tone: "warning" };
    return { label: "已完成", tone: "muted" };
  }

  function runName(run: ParticipationTaskRun) {
    if (run.mode === "manual" || (!run.mode && run.account_count === 1)) {
      return run.title || "单账号启动";
    }
    return run.mode_label || run.title || "红包参与任务";
  }

  function runDate(value?: string) {
    const timestamp = Date.parse(value || "");
    if (!Number.isFinite(timestamp)) return "—";
    return new Date(timestamp).toLocaleDateString("zh-CN", {
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
    });
  }

  function runWeekday(value?: string) {
    const timestamp = Date.parse(value || "");
    if (!Number.isFinite(timestamp)) return "";
    return new Date(timestamp).toLocaleDateString("zh-CN", { weekday: "short" });
  }

  function runDateContext(value: string, currentClock: number) {
    const timestamp = Date.parse(value);
    if (!Number.isFinite(timestamp)) return "日期未知";
    const date = new Date(timestamp);
    const today = new Date(currentClock);
    const dateDay = new Date(date.getFullYear(), date.getMonth(), date.getDate()).getTime();
    const todayDay = new Date(today.getFullYear(), today.getMonth(), today.getDate()).getTime();
    const difference = Math.round((todayDay - dateDay) / 86400000);
    if (difference === 0) return "今天";
    if (difference === 1) return "昨天";
    return runWeekday(value);
  }

  function runClockTime(value?: string) {
    const timestamp = Date.parse(value || "");
    if (!Number.isFinite(timestamp)) return "—";
    return new Date(timestamp).toLocaleTimeString("zh-CN", {
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
      hour12: false,
    });
  }

  function runExactTime(value?: string) {
    const timestamp = Date.parse(value || "");
    return Number.isFinite(timestamp)
      ? new Date(timestamp).toLocaleString("zh-CN", { hour12: false })
      : "暂无时间";
  }

  function runEndTime(run: ParticipationTaskRun) {
    if (!run.ended_at) return "结束 进行中";
    const started = Date.parse(run.started_at);
    const ended = Date.parse(run.ended_at);
    if (!Number.isFinite(started) || !Number.isFinite(ended)) return "结束 —";
    const startDate = new Date(started);
    const endDate = new Date(ended);
    const startDay = new Date(startDate.getFullYear(), startDate.getMonth(), startDate.getDate()).getTime();
    const endDay = new Date(endDate.getFullYear(), endDate.getMonth(), endDate.getDate()).getTime();
    const dayDifference = Math.max(0, Math.round((endDay - startDay) / 86400000));
    if (dayDifference === 1) return `次日 ${runClockTime(run.ended_at)}`;
    if (dayDifference > 1) return `+${dayDifference}天 ${runClockTime(run.ended_at)}`;
    return `结束 ${runClockTime(run.ended_at)}`;
  }

  function runDuration(run: ParticipationTaskRun, currentClock: number) {
    const started = Date.parse(run.started_at);
    const ended = run.ended_at ? Date.parse(run.ended_at) : currentClock;
    if (!Number.isFinite(started) || !Number.isFinite(ended) || ended < started) return "—";
    const totalSeconds = Math.max(0, Math.floor((ended - started) / 1000));
    const days = Math.floor(totalSeconds / 86400);
    const hours = Math.floor((totalSeconds % 86400) / 3600);
    const minutes = Math.floor((totalSeconds % 3600) / 60);
    const seconds = totalSeconds % 60;
    const parts: string[] = [];
    if (days > 0) parts.push(`${days}天`);
    if (hours > 0) parts.push(`${hours}小时`);
    if (minutes > 0) parts.push(`${minutes}分`);
    if (seconds > 0 || parts.length === 0) parts.push(`${seconds}秒`);
    return parts.join("");
  }

  function formatDiamonds(value: number) {
    if (!Number.isFinite(value) || value <= 0) return "0";
    return Number.isInteger(value) ? String(value) : value.toFixed(2).replace(/\.?0+$/, "");
  }
</script>

<section class="participation-run-group">
  <button
    type="button"
    class="participation-run-group-head"
    aria-expanded={expanded}
    aria-label={`${expanded ? "收起" : "展开"}${runDate(group.started_at)}的任务记录`}
    onclick={() => (expanded = !expanded)}
  >
    <span class="participation-run-group-date">
      {#if expanded}<CaretDown size={11} weight="bold" />{:else}<CaretRight size={11} weight="bold" />{/if}
      <strong>{runDateContext(group.started_at, clock)} · {runDate(group.started_at)}</strong>
    </span>
    <span class="participation-run-group-summary">
      {group.task_count} 个任务 · {group.account_count} 个账号 · {group.success_count} 次参与 · {group.win_count} 次中奖 / {formatDiamonds(group.win_diamonds)} 钻
    </span>
  </button>
  <div class="participation-run-group-body" hidden={!expanded}>
    <div class="participation-run-head">
      <span>任务</span><span>参与账号</span><span>开始 / 结束</span><span>任务耗时</span><span>参与结果</span><span>中奖</span>
    </div>
    {#each group.runs as run (run.id)}
      {@const taskStatus = runStatus(run)}
      <button
        type="button"
        class:selected={selectedRunId === run.id}
        class="participation-run-row"
        aria-pressed={selectedRunId === run.id}
        aria-label={`查看“${runName(run)}”的参与明细`}
        onclick={() => onSelect(run.id)}
      >
        <span class="participation-run-name">
          <strong>{runName(run)}</strong>
          <small><span class={`participation-run-status ${taskStatus.tone}`}>{taskStatus.label}</span></small>
        </span>
        <span class="participation-run-metric">
          <strong>{run.account_count} 个</strong>
          <small>{run.skipped_count ? `跳过 ${run.skipped_count} 个` : "全部已纳入"}</small>
        </span>
        <span
          class="participation-run-period"
          data-tooltip={`开始：${runExactTime(run.started_at)}；结束：${run.ended_at ? runExactTime(run.ended_at) : "任务仍在运行"}`}
          data-tooltip-placement="top"
        >
          <strong>开始 {runClockTime(run.started_at)}</strong>
          <small>{runEndTime(run)}</small>
        </span>
        <span class="participation-run-metric duration">
          <strong>{runDuration(run, run.ended_at ? 0 : clock)}</strong>
          <small>{run.status === "running" ? "实时统计" : "任务耗时"}</small>
        </span>
        <span class="participation-run-metric result">
          <strong>{run.success_count} 次成功</strong><small>{run.failure_count} 次失败</small>
        </span>
        <span class="participation-run-metric win">
          <strong>{run.win_count} 次</strong><small>{formatDiamonds(run.win_diamonds)} 钻</small>
        </span>
      </button>
    {/each}
  </div>
</section>
