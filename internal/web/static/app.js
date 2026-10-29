// Small enhancements on top of the server-rendered pages, which also work
// without this script:
//
//   - relative times like "Checked 12 s ago" count up every second,
//   - copy buttons copy addresses and domains to the clipboard,
//   - the live indicator shows when the page was last refreshed and whether
//     the last refresh failed,
//   - HTMX refreshes do not destroy a text selection or move keyboard focus.
(function () {
  "use strict";

  const toastDuration = 1600;
  const copiedDuration = 1200;

  // Same wording as ago in pages.go.
  function ago(seconds) {
    if (seconds < 60) {
      return seconds + " s ago";
    }
    if (seconds < 3600) {
      return Math.floor(seconds / 60) + " min ago";
    }
    if (seconds < 86400) {
      return Math.floor(seconds / 3600) + " h " + Math.floor((seconds % 3600) / 60) + " min ago";
    }
    return Math.floor(seconds / 86400) + " days ago";
  }

  // offset is how far the server's clock is ahead of the browser's.
  let offset = 0;

  function content() {
    return document.querySelector("[data-server-time]");
  }

  function syncClock() {
    const el = content();
    const serverTime = el ? Date.parse(el.dataset.serverTime) : NaN;
    offset = Number.isNaN(serverTime) ? 0 : serverTime - Date.now();

    // The live indicator shows when the content was rendered.
    const updated = document.querySelector("time[data-updated]");
    if (updated && el) {
      updated.dateTime = el.dataset.serverTime;
    }
  }

  function updateTimes() {
    const now = Date.now() + offset;
    for (const el of document.querySelectorAll("time[data-relative]")) {
      const at = Date.parse(el.dateTime);
      if (!Number.isNaN(at)) {
        el.textContent = ago(Math.max(0, Math.floor((now - at) / 1000)));
      }
    }
  }

  function setOnline(online) {
    const live = document.getElementById("live");
    if (!live) {
      return;
    }
    live.classList.toggle("offline", !online);
    const text = live.querySelector(".live-text");
    const offline = live.querySelector(".live-offline");
    if (online && offline) {
      offline.remove();
      text.hidden = false;
    } else if (!online && !offline) {
      const note = document.createElement("span");
      note.className = "live-offline";
      note.textContent = "Connection lost, retrying";
      text.hidden = true;
      live.append(note);
    }
  }

  // Copy buttons are hidden in the HTML because they need this script.
  function enableCopyButtons() {
    for (const button of document.querySelectorAll("button.copy[hidden]")) {
      button.hidden = false;
    }
  }

  let toastTimer = 0;

  function toast(message) {
    const el = document.getElementById("toast");
    if (!el) {
      return;
    }
    el.textContent = message;
    el.classList.add("show");
    clearTimeout(toastTimer);
    toastTimer = setTimeout(function () {
      el.classList.remove("show");
    }, toastDuration);
  }

  async function copyText(text) {
    if (navigator.clipboard && window.isSecureContext) {
      try {
        await navigator.clipboard.writeText(text);
        return;
      } catch {
        // Fall through, for example when the browser denies the permission.
      }
    }
    // The clipboard API needs HTTPS and a permission; without them, copy a
    // selected text area.
    const area = document.createElement("textarea");
    area.value = text;
    area.readOnly = true;
    area.className = "visually-hidden";
    document.body.append(area);
    area.select();
    const copied = document.execCommand("copy");
    area.remove();
    if (!copied) {
      throw new Error("copy failed");
    }
  }

  document.addEventListener("click", async function (event) {
    const button = event.target.closest("button.copy");
    if (!button) {
      return;
    }
    const text = button.dataset.copy;
    try {
      await copyText(text);
      button.classList.add("copied");
      setTimeout(function () {
        button.classList.remove("copied");
      }, copiedDuration);
      toast("Copied " + text);
    } catch {
      toast("Could not copy " + text);
    }
  });

  // focusKey identifies a copy button across refreshes by its value and its
  // position among the buttons with the same value.
  function focusKey(root) {
    const active = document.activeElement;
    if (!active || !root.contains(active) || !active.dataset.copy) {
      return null;
    }
    const same = Array.from(root.querySelectorAll("button.copy")).filter(function (b) {
      return b.dataset.copy === active.dataset.copy;
    });
    return { value: active.dataset.copy, index: same.indexOf(active) };
  }

  let pendingFocus = null;

  document.addEventListener("htmx:beforeSwap", function (event) {
    const target = event.detail.target;
    // Keep a text selection inside the content; the next refresh swaps.
    const selection = window.getSelection();
    if (selection && !selection.isCollapsed && target.contains(selection.anchorNode)) {
      event.detail.shouldSwap = false;
      return;
    }
    pendingFocus = focusKey(target);
  });

  document.addEventListener("htmx:afterSwap", function () {
    setOnline(true);
    syncClock();
    updateTimes();
    enableCopyButtons();

    if (pendingFocus) {
      const key = pendingFocus;
      pendingFocus = null;
      const same = Array.from(document.querySelectorAll("button.copy")).filter(function (b) {
        return b.dataset.copy === key.value;
      });
      const button = same[key.index];
      if (button) {
        button.focus({ preventScroll: true });
      }
    }
  });

  document.addEventListener("htmx:sendError", function () {
    setOnline(false);
  });
  document.addEventListener("htmx:responseError", function () {
    setOnline(false);
  });

  document.addEventListener("DOMContentLoaded", function () {
    enableCopyButtons();
    syncClock();
    updateTimes();
    setInterval(updateTimes, 1000);
  });
})();
