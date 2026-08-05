<script lang="ts">
  import { invoke } from "@tauri-apps/api/core";
  import { listen } from "@tauri-apps/api/event";
  import { onMount } from "svelte";
  import { BrowserIcon as Browser, WarningCircleIcon as WarningCircle } from "phosphor-svelte";

  type InstanceMetadata = {
    instance_id: string;
    account_name: string;
    surface?: string;
  };

  type BrowserWebviewEvent = {
    instance_id: string;
    message?: string;
  };

  const instanceId = new URL(window.location.href).searchParams.get("instance")?.trim() || "";
  const isWindowsDesktop = /Windows/i.test(navigator.userAgent);
  let viewport: HTMLElement;
  let metadata: InstanceMetadata | null = null;
  let loading = true;
  let error = "";
  let mounted = false;
  let mounting = false;
  let readyReceived = false;
  let pendingSync = false;
  let resizeFrame = 0;
  let revealTimer = 0;

  function bounds() {
    const rect = viewport.getBoundingClientRect();
    return { x: rect.left, y: rect.top, width: rect.width, height: rect.height };
  }

  function isAlreadyExistsError(reason: unknown) {
    const text = reason instanceof Error ? reason.message : String(reason);
    return text.includes("already exists");
  }

  async function syncBrowserSurface() {
    if (!instanceId || !viewport || !viewport.isConnected) return;
    if (metadata?.surface === "external_chrome") return;
    if (mounting) {
      pendingSync = true;
      return;
    }
    window.cancelAnimationFrame(resizeFrame);
    resizeFrame = window.requestAnimationFrame(async () => {
      if (mounting) {
        pendingSync = true;
        return;
      }
      mounting = true;
      try {
        const box = bounds();
        if (box.width < 80 || box.height < 80) {
          // Layout not ready yet; try again next frame without clearing the
          // loading placeholder (avoids a white flash).
          pendingSync = true;
          return;
        }
        if (!mounted) {
          try {
            await invoke("mount_browser_webview", { instanceId, bounds: box });
          } catch (reason) {
            if (!isAlreadyExistsError(reason)) throw reason;
          }
          mounted = true;
          error = "";
        }
        // Only reveal the native surface after the page reported ready (or a
        // fallback timeout). Until then keep geometry updates hidden so the
        // HTML “正在打开实例…” state stays visible.
        const shouldReveal = readyReceived && !error;
        await invoke("sync_browser_webview", {
          instanceId,
          bounds: box,
          reveal: shouldReveal,
        });
        if (shouldReveal) {
          loading = false;
        }
      } catch (reason) {
        if (isAlreadyExistsError(reason)) {
          mounted = true;
          error = "";
          return;
        }
        error = reason instanceof Error ? reason.message : String(reason);
        loading = false;
      } finally {
        mounting = false;
        if (pendingSync) {
          pendingSync = false;
          void syncBrowserSurface();
        }
      }
    });
  }

  onMount(() => {
    if (!instanceId) {
      error = "浏览器实例标识缺失";
      loading = false;
      return;
    }
    void invoke<InstanceMetadata>("browser_instance_window_metadata", { instanceId })
      .then((result) => {
        metadata = result;
        // Import accounts must use the external Chrome shell, not this embedded
        // host. Refuse to mount a child WebView that would race and error.
        if (result.surface === "external_chrome") {
          error = "该账号为导入 Cookie，请关闭本窗后从卡片重新「打开实例」使用 Chrome 外壳";
          loading = false;
          return;
        }
        void syncBrowserSurface();
      })
      .catch((reason) => {
        error = reason instanceof Error ? reason.message : String(reason);
        loading = false;
      });

    const observer = new ResizeObserver(() => {
      if (metadata?.surface === "external_chrome") return;
      void syncBrowserSurface();
    });
    observer.observe(viewport);
    let unlistenReady: (() => void) | undefined;
    let unlistenError: (() => void) | undefined;
    void listen<BrowserWebviewEvent>("browser-webview://ready", (event) => {
      if (event.payload.instance_id !== instanceId) return;
      readyReceived = true;
      // Do not clear `loading` here — wait until sync has applied bounds + show
      // so the placeholder is not removed over an off-screen/hidden native view.
      void syncBrowserSurface();
    }).then((unlisten) => (unlistenReady = unlisten));
    void listen<BrowserWebviewEvent>("browser-webview://load-error", (event) => {
      if (event.payload.instance_id !== instanceId) return;
      error = event.payload.message || "实例页面加载失败";
      loading = false;
    }).then((unlisten) => (unlistenError = unlisten));
    // Fallback if the native ready event is missed. Keep this close to the
    // post-bootstrap reveal delay so the spinner is not replaced by a long
    // blank surface.
    revealTimer = window.setTimeout(() => {
      if (loading && !error) {
        readyReceived = true;
        void syncBrowserSurface();
      }
    }, 4_000);
    const cookieTimer = window.setInterval(() => {
      void invoke("sync_browser_account_cookie", { instanceId }).catch(() => undefined);
    }, 60_000);

    return () => {
      observer.disconnect();
      window.cancelAnimationFrame(resizeFrame);
      window.clearTimeout(revealTimer);
      window.clearInterval(cookieTimer);
      unlistenReady?.();
      unlistenError?.();
    };
  });
</script>

<main class="instance-window-shell">
  {#if !isWindowsDesktop}
    <header data-tauri-drag-region>
      <div data-tauri-drag-region>
        <span><Browser size={12} /></span>
        <strong>{metadata?.account_name || "浏览器实例"}</strong>
      </div>
    </header>
  {/if}
  <section bind:this={viewport} class="instance-window-viewport">
    {#if loading && !error}
      <div class="instance-window-state">
        <i></i>
        <span>正在打开实例…</span>
      </div>
    {/if}
    {#if error}
      <div class="instance-window-state error">
        <WarningCircle size={20} />
        <span>{error}</span>
      </div>
    {/if}
  </section>
</main>

<style>
  :global(html),
  :global(body),
  :global(#app) {
    width: 100%;
    height: 100%;
    margin: 0;
    overflow: hidden;
    background: #fff;
  }
  .instance-window-shell {
    display: flex;
    width: 100%;
    height: 100%;
    flex-direction: column;
    background: #fff;
  }
  header {
    display: flex;
    height: 28px;
    min-height: 28px;
    max-height: 28px;
    align-items: center;
    justify-content: center;
    border-bottom: 1px solid #ece8e1;
    user-select: none;
  }
  header > div {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  header span {
    display: grid;
    width: 18px;
    height: 18px;
    place-items: center;
    border-radius: 5px;
    background: #edf2fd;
    color: #5879d8;
  }
  header strong {
    color: #403c36;
    font-size: 11px;
    font-weight: 650;
  }
  .instance-window-viewport {
    position: relative;
    min-height: 0;
    flex: 1 1 auto;
    overflow: hidden;
    background: #f7f6f3;
  }
  .instance-window-state {
    position: absolute;
    inset: 0;
    z-index: 2;
    display: grid;
    place-content: center;
    justify-items: center;
    gap: 8px;
    color: #8b867e;
    font-size: 11px;
    background: #f7f6f3;
  }
  .instance-window-state i {
    width: 17px;
    height: 17px;
    border: 2px solid #d8d4cc;
    border-top-color: #5879d8;
    border-radius: 50%;
    animation: spin 0.8s linear infinite;
  }
  .instance-window-state.error {
    color: #b45444;
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
</style>
