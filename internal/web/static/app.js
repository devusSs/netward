// Counts the "Checked 12 s ago" times up every second. The server renders the
// same text, so the page also works without this script; HTMX replaces the
// overview every 30 seconds with fresh data.
(function () {
  "use strict";

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

  function syncClock() {
    const overview = document.getElementById("overview");
    const serverTime = overview ? Date.parse(overview.dataset.serverTime) : NaN;
    offset = Number.isNaN(serverTime) ? 0 : serverTime - Date.now();
  }

  function update() {
    const now = Date.now() + offset;
    for (const el of document.querySelectorAll("time[data-relative]")) {
      const at = Date.parse(el.dateTime);
      if (!Number.isNaN(at)) {
        el.textContent = ago(Math.max(0, Math.floor((now - at) / 1000)));
      }
    }
  }

  document.addEventListener("DOMContentLoaded", function () {
    syncClock();
    update();
    setInterval(update, 1000);
  });

  // A swapped-in overview carries a new server time.
  document.addEventListener("htmx:afterSwap", function () {
    syncClock();
    update();
  });
})();
