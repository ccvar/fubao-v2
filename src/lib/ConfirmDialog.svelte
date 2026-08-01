<script lang="ts">
  import { TrashIcon as Trash, XIcon as X } from "phosphor-svelte";

  export let title = "请确认操作";
  export let message: string;
  export let confirmText = "确认";
  export let cancelText = "取消";
  export let onConfirm: () => void;
  export let onCancel: () => void;
  export let busy = false;

  let confirmButton: HTMLButtonElement;

  $: if (confirmButton && !busy) {
    queueMicrotask(() => confirmButton?.focus());
  }

  function handleKeydown(event: KeyboardEvent) {
    if (event.key === "Escape" && !busy) {
      event.preventDefault();
      onCancel();
    }
  }
</script>

<svelte:window onkeydown={handleKeydown} />

<div class="confirm-layer" role="presentation">
  <button
    class="confirm-backdrop"
    type="button"
    aria-label="关闭确认框"
    disabled={busy}
    onclick={onCancel}
  ></button>
  <dialog
    open
    class="confirm-card"
    role="alertdialog"
    aria-modal="true"
    aria-labelledby="confirm-title"
    aria-describedby="confirm-message"
  >
    <button class="confirm-close" type="button" aria-label="关闭" disabled={busy} onclick={onCancel}>
      <X size={14} />
    </button>
    <div class="confirm-head">
      <div class="confirm-icon" aria-hidden="true"><Trash size={18} weight="fill" /></div>
      <div class="confirm-copy">
        <h2 id="confirm-title">{title}</h2>
        <p id="confirm-message">{message}</p>
      </div>
    </div>
    <div class="confirm-actions">
      <button class="confirm-cancel" type="button" disabled={busy} onclick={onCancel}>{cancelText}</button>
      <button
        bind:this={confirmButton}
        class="confirm-submit"
        type="button"
        disabled={busy}
        onclick={onConfirm}
      >
        {busy ? "正在删除…" : confirmText}
      </button>
    </div>
  </dialog>
</div>

<style>
  .confirm-layer {
    position: fixed;
    z-index: 1000;
    inset: 0;
    display: grid;
    place-items: center;
    padding: 16px;
    isolation: isolate;
  }

  .confirm-backdrop {
    position: absolute;
    inset: 0;
    padding: 0;
    border: 0;
    background: rgb(23 24 23 / 34%);
    backdrop-filter: blur(3px);
    cursor: default;
    animation: confirm-fade 160ms ease-out;
  }

  .confirm-card {
    position: relative;
    width: min(330px, calc(100vw - 32px));
    margin: 0;
    padding: 14px;
    border: 1px solid #dedad2;
    border-radius: 13px;
    background: #fff;
    box-shadow: 0 18px 48px rgb(32 28 20 / 19%), 0 2px 8px rgb(32 28 20 / 8%);
    animation: confirm-rise 180ms cubic-bezier(.2, .8, .2, 1);
  }

  .confirm-close {
    position: absolute;
    top: 8px;
    right: 8px;
    display: grid;
    width: 25px;
    height: 25px;
    padding: 0;
    place-items: center;
    border: 0;
    border-radius: 7px;
    background: transparent;
    color: #8d8981;
    cursor: pointer;
  }

  .confirm-close:hover {
    background: #f2f0ec;
    color: #37342f;
  }

  .confirm-head {
    display: flex;
    min-width: 0;
    align-items: flex-start;
    gap: 9px;
    padding-right: 22px;
  }

  .confirm-icon {
    display: grid;
    width: 30px;
    height: 30px;
    flex: 0 0 30px;
    place-items: center;
    border-radius: 8px;
    background: #fae7e3;
    color: #b84436;
  }

  .confirm-copy {
    min-width: 0;
  }

  .confirm-copy h2 {
    margin: 1px 0 0;
    color: #2f2c28;
    font-size: 14px;
    font-weight: 680;
    line-height: 1.35;
  }

  .confirm-copy p {
    margin: 4px 0 0;
    color: #77736c;
    font-size: 11.5px;
    line-height: 1.55;
    overflow-wrap: anywhere;
    white-space: pre-line;
  }

  .confirm-actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--modal-action-gap, 6px);
    margin-top: 12px;
  }

  .confirm-actions button {
    min-width: 64px;
    height: var(--modal-action-height, 30px);
    padding: 0 12px;
    border-radius: var(--modal-action-radius, 8px);
    font: inherit;
    font-size: 11.5px;
    font-weight: 650;
    cursor: pointer;
  }

  .confirm-actions button:disabled {
    cursor: default;
    opacity: .58;
  }

  .confirm-cancel {
    border: 1px solid #dcd8d0;
    background: #fff;
    color: #3c3934;
  }

  .confirm-cancel:hover:not(:disabled) {
    background: #f6f4f0;
  }

  .confirm-submit {
    border: 1px solid #bd493b;
    background: #bd493b;
    color: #fff;
  }

  .confirm-submit:hover:not(:disabled) {
    background: #a93e32;
    border-color: #a93e32;
  }

  .confirm-actions button:focus-visible,
  .confirm-close:focus-visible {
    outline: 2px solid rgb(189 73 59 / 24%);
    outline-offset: 2px;
  }

  @keyframes confirm-fade {
    from { opacity: 0; }
  }

  @keyframes confirm-rise {
    from { opacity: 0; transform: translateY(7px) scale(.98); }
  }
</style>
