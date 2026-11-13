"use strict";

// Light/dark theme. Loaded in <head> so the theme applies before first paint.
// Without a saved choice the page follows the system preference
// (prefers-color-scheme) and keeps following it live; clicking a
// [data-theme-toggle] button saves an explicit choice in localStorage.
(function () {
  const KEY = "ngodingmcp-theme";
  const media = window.matchMedia ? window.matchMedia("(prefers-color-scheme: light)") : null;

  function saved() {
    try {
      const v = localStorage.getItem(KEY);
      return v === "light" || v === "dark" ? v : null;
    } catch (_) {
      return null; // storage blocked: just follow the system
    }
  }

  function current() {
    return saved() || (media && media.matches ? "light" : "dark");
  }

  function apply() {
    const theme = current();
    document.documentElement.dataset.theme = theme;
    document.querySelectorAll("[data-theme-toggle]").forEach((btn) => {
      const next = theme === "dark" ? "light" : "dark";
      btn.textContent = theme === "dark" ? "☀" : "☾";
      btn.title = "Switch to " + next + " mode";
      btn.setAttribute("aria-label", btn.title);
    });
  }

  function toggle() {
    const next = current() === "dark" ? "light" : "dark";
    try {
      localStorage.setItem(KEY, next);
    } catch (_) {
      // Not persisted, but still switch for this page view.
      document.documentElement.dataset.theme = next;
      return;
    }
    apply();
  }

  apply();
  if (media) {
    const onChange = () => { if (!saved()) apply(); };
    if (media.addEventListener) media.addEventListener("change", onChange);
    else if (media.addListener) media.addListener(onChange);
  }
  // Another tab changed the choice.
  window.addEventListener("storage", (ev) => { if (ev.key === KEY) apply(); });

  document.addEventListener("DOMContentLoaded", () => {
    document.querySelectorAll("[data-theme-toggle]").forEach((btn) => {
      btn.addEventListener("click", toggle);
    });
    apply();
  });
})();
