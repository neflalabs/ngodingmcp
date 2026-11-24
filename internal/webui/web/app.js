"use strict";

const $ = (id) => document.getElementById(id);
const editor = $("editor");
const lineNumbers = $("lineNumbers");
const treeEl = $("tree");
const searchResults = $("searchResults");
const syntaxBanner = $("syntaxBanner");
const externalBanner = $("externalChangeBanner");

function debounce(fn, delay) {
  let timer = null;
  const debounced = (...args) => {
    clearTimeout(timer);
    timer = setTimeout(() => fn(...args), delay);
  };
  debounced.cancel = () => clearTimeout(timer);
  debounced.flush = (...args) => {
    clearTimeout(timer);
    fn(...args);
  };
  return debounced;
}

// Reactive state store: changes to tracked keys trigger targeted UI updates.
const stateListeners = new Map();
function onState(keys, fn) {
  for (const k of keys) {
    if (!stateListeners.has(k)) stateListeners.set(k, new Set());
    stateListeners.get(k).add(fn);
  }
}

const rawState = {
  currentPath: null,
  dirty: false,
  expandedDirs: new Set(),
  activeNode: null,
  lastLineCount: 0,
  externalConflict: false,
  sseState: "connecting", // "connecting" | "connected" | "reconnecting"
  lastSeq: 0,
  lastSelfWritePath: null,
  lastSelfWriteAt: 0,
};

const state = new Proxy(rawState, {
  set(target, prop, value) {
    const prev = target[prop];
    target[prop] = value;
    if (prev !== value && stateListeners.has(prop)) {
      for (const fn of stateListeners.get(prop)) {
        fn(value, prev);
      }
    }
    return true;
  },
});

// escapeHtml makes a value safe to interpolate into innerHTML.
function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;",
  })[c]);
}

// --- API ------------------------------------------------------------------

async function api(path, options = {}) {
  const opts = { credentials: "same-origin", ...options };
  if (opts.body !== undefined && typeof opts.body !== "string") {
    opts.body = JSON.stringify(opts.body);
  }
  if (opts.body !== undefined) {
    opts.headers = { "Content-Type": "application/json", ...(opts.headers || {}) };
  }
  const res = await fetch("/api" + path, opts);
  if (res.status === 401) {
    location.href = "/login";
    throw new Error("unauthorized");
  }
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    const err = new Error(data.error || res.statusText);
    err.status = res.status;
    err.data = data;
    throw err;
  }
  return data;
}

function setStatus(msg, isError = false) {
  const el = $("status");
  el.textContent = msg;
  el.classList.toggle("error", isError);
}

// Cached line counter: avoids rebuilding multi-thousand-line strings on every keystroke.
function countLinesFast(text) {
  if (!text) return 1;
  let count = 1;
  let idx = -1;
  while ((idx = text.indexOf("\n", idx + 1)) !== -1) {
    count++;
  }
  return count;
}

function updateLineNumbers(force = false) {
  const lines = countLinesFast(editor.value);
  if (!force && lines === state.lastLineCount) return;
  state.lastLineCount = lines;
  const arr = new Array(lines);
  for (let i = 0; i < lines; i++) {
    arr[i] = i + 1;
  }
  lineNumbers.textContent = arr.join("\n") + "\n";
}

const textEncoder = new TextEncoder();
function updateTokenEstimateNow() {
  const bytes = textEncoder.encode(editor.value).length;
  const tokens = Math.floor(bytes / 4);
  $("tokenEstimate").textContent = `~${tokens.toLocaleString()} tokens (${bytes.toLocaleString()} B)`;
}
const updateTokenEstimate = debounce(updateTokenEstimateNow, 150);

function updateUI() {
  const hasFile = !!state.currentPath;
  $("btnSave").disabled = !hasFile || !state.dirty;
  $("btnRename").disabled = !hasFile;
  $("btnDelete").disabled = !hasFile;
  $("btnCheckSyntax").disabled = !hasFile;
  editor.disabled = !hasFile;
  $("currentPath").textContent = state.currentPath || "no file selected";
  const badge = $("saveState");
  badge.textContent = state.dirty ? "unsaved" : "saved";
  badge.classList.toggle("dirty", state.dirty);
  document.title = (state.dirty ? "● " : "") + (state.currentPath || "NgodingMCP");
  updateLineNumbers();
  updateTokenEstimateNow();
}

onState(["currentPath", "dirty"], () => {
  updateUI();
});

onState(["externalConflict"], (conflict) => {
  if (!externalBanner) return;
  externalBanner.classList.toggle("hidden", !conflict);
});

onState(["sseState"], (status) => {
  const dot = $("liveDot");
  if (!dot) return;
  dot.className = "live-dot" + (status === "connected" ? " connected" : status === "reconnecting" ? " reconnecting" : "");
  dot.title =
    status === "connected"
      ? "Live workspace sync active"
      : status === "reconnecting"
      ? "Reconnecting live sync…"
      : "Connecting live sync…";
});

function showExternalConflict(msg) {
  const el = $("externalChangeMsg");
  if (el) el.textContent = msg;
  state.externalConflict = true;
}

function clearExternalConflict() {
  state.externalConflict = false;
}

function markDirty() {
  if (!state.dirty) {
    state.dirty = true;
  } else {
    updateLineNumbers();
    updateTokenEstimate();
  }
}

// --- Tree -----------------------------------------------------------------

let treeLoadToken = 0;

async function loadTree() {
  const token = ++treeLoadToken;
  const prevScroll = treeEl.scrollTop;
  const frag = document.createDocumentFragment();
  try {
    await renderDir(frag, "", token);
    if (token !== treeLoadToken) return;
    treeEl.innerHTML = "";
    treeEl.appendChild(frag);
    treeEl.scrollTop = prevScroll;
    setStatus("workspace loaded");
  } catch (err) {
    if (token === treeLoadToken) setStatus(err.message, true);
  }
}

const scheduleTreeRefresh = debounce(() => {
  loadTree();
}, 180);

async function renderDir(parent, dir, token = treeLoadToken) {
  const data = await api("/tree?path=" + encodeURIComponent(dir));
  if (token !== treeLoadToken) return;
  for (const entry of data.entries) {
    const li = document.createElement("li");
    const row = document.createElement("div");
    row.className = "node " + (entry.isDir ? "dir" : "file");
    row.title = entry.path;

    const label = document.createElement("span");
    label.className = "node-label";
    label.textContent = entry.name;
    row.appendChild(label);

    // Hover action buttons
    const actions = document.createElement("span");
    actions.className = "node-actions";

    const btnRename = document.createElement("button");
    btnRename.title = entry.isDir ? "Rename folder" : "Rename file";
    btnRename.textContent = "✏";
    btnRename.addEventListener("click", async (ev) => {
      ev.stopPropagation();
      await renamePath(entry.path, entry.isDir);
    });

    const btnDel = document.createElement("button");
    btnDel.className = "act-del";
    btnDel.title = entry.isDir ? "Delete folder" : "Delete file";
    btnDel.textContent = "✕";
    btnDel.addEventListener("click", async (ev) => {
      ev.stopPropagation();
      await deletePath(entry.path, entry.isDir);
    });

    actions.append(btnRename, btnDel);

    if (entry.isDir) {
      const btnAddFile = document.createElement("button");
      btnAddFile.title = "New file in this folder";
      btnAddFile.textContent = "+f";
      btnAddFile.addEventListener("click", async (ev) => {
        ev.stopPropagation();
        await newFileIn(entry.path);
      });
      actions.insertBefore(btnAddFile, btnRename);
    }

    row.appendChild(actions);

    if (entry.isDir) {
      const children = document.createElement("ul");
      const wasOpen = state.expandedDirs.has(entry.path);
      children.hidden = !wasOpen;
      if (wasOpen) {
        row.classList.add("open");
      }
      row.addEventListener("click", async (ev) => {
        ev.stopPropagation();
        if (children.hidden && !children.dataset.loaded) {
          try {
            await renderDir(children, entry.path, treeLoadToken);
            children.dataset.loaded = "1";
          } catch (err) {
            setStatus(err.message, true);
            return;
          }
        }
        children.hidden = !children.hidden;
        row.classList.toggle("open", !children.hidden);
        if (children.hidden) {
          state.expandedDirs.delete(entry.path);
        } else {
          state.expandedDirs.add(entry.path);
        }
      });
      li.append(row, children);
      if (wasOpen) {
        try {
          await renderDir(children, entry.path, token);
          children.dataset.loaded = "1";
        } catch (_) {
          // Directory may have been removed or become inaccessible; collapse it.
          state.expandedDirs.delete(entry.path);
          children.hidden = true;
          row.classList.remove("open");
        }
      }
    } else {
      if (entry.path === state.currentPath) {
        row.classList.add("active");
        state.activeNode = row;
      }
      row.addEventListener("click", (ev) => {
        ev.stopPropagation();
        openFile(entry.path);
      });
      li.append(row);
    }
    parent.appendChild(li);
  }
}

function remapExpandedDirs(oldPath, newPath) {
  const updated = new Set();
  for (const dir of state.expandedDirs) {
    if (dir === oldPath) {
      updated.add(newPath);
    } else if (dir.startsWith(oldPath + "/")) {
      updated.add(newPath + dir.slice(oldPath.length));
    } else {
      updated.add(dir);
    }
  }
  state.expandedDirs = updated;
}

function pruneExpandedDirs(targetPath, isDir) {
  if (!isDir) return;
  for (const dir of Array.from(state.expandedDirs)) {
    if (dir === targetPath || dir.startsWith(targetPath + "/")) {
      state.expandedDirs.delete(dir);
    }
  }
}

async function renamePath(oldPath, isDir) {
  const to = prompt(`Rename ${isDir ? "folder" : "file"}:`, oldPath);
  if (!to || to === oldPath) return;
  try {
    const data = await api("/rename", {
      method: "POST",
      body: { from: oldPath, to },
    });
    if (isDir) {
      remapExpandedDirs(oldPath, data.path);
      if (state.currentPath && state.currentPath.startsWith(oldPath + "/")) {
        state.currentPath = data.path + state.currentPath.slice(oldPath.length);
      }
    } else if (state.currentPath === oldPath) {
      state.currentPath = data.path;
    }
    await loadTree();
    setStatus(`Renamed to ${data.path}`);
  } catch (err) {
    setStatus(err.message, true);
  }
}

async function deletePath(targetPath, isDir) {
  const promptMsg = isDir
    ? `Delete folder "${targetPath}" and all its contents?`
    : `Delete file "${targetPath}"?`;
  if (!confirm(promptMsg)) return;

  try {
    await api(`/file?path=${encodeURIComponent(targetPath)}&recursive=${isDir ? "1" : "0"}`, {
      method: "DELETE",
    });
    pruneExpandedDirs(targetPath, isDir);
    if (state.currentPath === targetPath || (isDir && state.currentPath && state.currentPath.startsWith(targetPath + "/"))) {
      clearExternalConflict();
      editor.value = "";
      syntaxBanner.classList.add("hidden");
      state.currentPath = null;
      state.dirty = false;
    }
    await loadTree();
    setStatus(`Deleted ${targetPath}`);
  } catch (err) {
    setStatus(err.message, true);
  }
}

async function newFileIn(dirPath) {
  const name = prompt(`New file inside "${dirPath}":`);
  if (!name) return;
  const p = dirPath.replace(/\/+$/, "") + "/" + name.replace(/^\/+/, "");
  try {
    const data = await api("/file", { method: "PUT", body: { path: p, content: "" } });
    state.expandedDirs.add(dirPath);
    await loadTree();
    await openFile(data.path);
  } catch (err) {
    setStatus(err.message, true);
  }
}

function setActiveNode(path) {
  if (state.activeNode) state.activeNode.classList.remove("active");
  state.activeNode = null;
  for (const el of treeEl.querySelectorAll(".node.file")) {
    if (el.title === path) {
      el.classList.add("active");
      state.activeNode = el;
      break;
    }
  }
}

// --- Editor ---------------------------------------------------------------

function jumpToLine(lineNum) {
  const target = Math.max(1, Number(lineNum) || 1);
  const lines = editor.value.split("\n");
  const clamped = Math.min(target, lines.length);
  let start = 0;
  for (let i = 0; i < clamped - 1; i++) {
    start += lines[i].length + 1;
  }
  const end = start + lines[clamped - 1].length;
  editor.focus();
  editor.setSelectionRange(start, end);
  updateCursor();

  const lineHeight = parseFloat(getComputedStyle(editor).lineHeight) || 20;
  const targetTop = Math.max(0, (clamped - 1) * lineHeight - editor.clientHeight / 3);
  editor.scrollTop = targetTop;
  lineNumbers.scrollTop = targetTop;
}

async function openFile(path, lineNum = null, options = {}) {
  const { force = false, preserveScroll = false } = options;
  if (!force && state.currentPath === path && lineNum) {
    jumpToLine(lineNum);
    return;
  }
  if (!force && state.dirty && !confirm("Discard unsaved changes?")) return;
  const prevScrollTop = editor.scrollTop;
  const prevSelStart = editor.selectionStart;
  const prevSelEnd = editor.selectionEnd;
  try {
    const data = await api("/file?path=" + encodeURIComponent(path));
    clearExternalConflict();
    editor.value = data.content;
    state.currentPath = data.path;
    state.dirty = false;
    updateUI();
    setActiveNode(data.path);
    syntaxBanner.classList.add("hidden");
    syntaxBanner.textContent = "";
    setStatus(`${data.totalLines} lines · ${data.size} bytes`);
    if (preserveScroll) {
      const maxPos = editor.value.length;
      editor.setSelectionRange(Math.min(prevSelStart, maxPos), Math.min(prevSelEnd, maxPos));
      editor.scrollTop = prevScrollTop;
      lineNumbers.scrollTop = prevScrollTop;
      updateCursor();
    } else if (lineNum) {
      jumpToLine(lineNum);
    } else {
      updateCursor();
      editor.focus();
    }
    if (isMobileViewport() && !preserveScroll) {
      setSidebarCollapsed(true, false);
    }
  } catch (err) {
    setStatus(err.message, true);
  }
}

async function saveFile() {
  if (!state.currentPath) return;
  const savingPath = state.currentPath;
  state.lastSelfWritePath = savingPath;
  state.lastSelfWriteAt = Date.now();
  try {
    const data = await api("/file", {
      method: "PUT",
      body: { path: savingPath, content: editor.value },
    });
    clearExternalConflict();
    state.dirty = false;
    setStatus(`saved ${data.bytes} bytes at ${new Date().toLocaleTimeString()}`);
    // Auto-check syntax on save
    checkSyntax();
  } catch (err) {
    setStatus(err.message, true);
  }
}

async function checkSyntax() {
  if (!state.currentPath) return;
  try {
    const res = await api("/validate?path=" + encodeURIComponent(state.currentPath));
    syntaxBanner.classList.remove("hidden");
    if (res.valid) {
      syntaxBanner.className = "syntax-banner success";
      syntaxBanner.textContent = `✓ Syntax OK (${res.language})`;
      setTimeout(() => {
        if (syntaxBanner.classList.contains("success")) {
          syntaxBanner.classList.add("hidden");
        }
      }, 4000);
    } else {
      syntaxBanner.className = "syntax-banner";
      let msg = `Syntax Errors (${res.language}):\n`;
      for (const e of res.errors || []) {
        msg += `• Line ${e.line}, Col ${e.col}: ${e.message}\n`;
      }
      syntaxBanner.textContent = msg.trim();
    }
  } catch (err) {
    setStatus(err.message, true);
  }
}

function updateCursor() {
  const pos = editor.selectionStart;
  const before = editor.value.slice(0, pos);
  const line = before.split("\n").length;
  const col = pos - before.lastIndexOf("\n");
  $("cursor").textContent = `Ln ${line}, Col ${col}`;
}

// Sync line number scroll with editor
editor.addEventListener("scroll", () => {
  lineNumbers.scrollTop = editor.scrollTop;
});

// Insert text while preserving browser native Undo/Redo history (Ctrl+Z).
function insertEditorText(text, selStart, selEnd) {
  editor.focus();
  editor.setSelectionRange(selStart, selEnd);
  let inserted = false;
  try {
    inserted = document.execCommand("insertText", false, text);
  } catch (_) {
    inserted = false;
  }
  if (!inserted) {
    editor.setRangeText(text, selStart, selEnd, "end");
    markDirty();
  }
}

// --- Actions --------------------------------------------------------------

async function newFile() {
  const p = prompt("New file path (relative to workspace root):");
  if (!p) return;
  try {
    const data = await api("/file", { method: "PUT", body: { path: p, content: "" } });
    await loadTree();
    await openFile(data.path);
  } catch (err) {
    setStatus(err.message, true);
  }
}

async function newFolder() {
  const p = prompt("New folder path (relative to workspace root):");
  if (!p) return;
  try {
    await api("/mkdir", { method: "POST", body: { path: p } });
    await loadTree();
    setStatus("folder created");
  } catch (err) {
    setStatus(err.message, true);
  }
}

async function renameCurrent() {
  if (!state.currentPath) return;
  const oldPath = state.currentPath;
  const to = prompt("Rename to:", oldPath);
  if (!to || to === oldPath) return;
  try {
    const data = await api("/rename", {
      method: "POST",
      body: { from: oldPath, to },
    });
    // Preserve dirty state if the editor has unsaved buffer edits.
    state.currentPath = data.path;
    await loadTree();
    setStatus("renamed");
  } catch (err) {
    setStatus(err.message, true);
  }
}

async function deleteCurrent() {
  if (!state.currentPath) return;
  if (!confirm(`Delete ${state.currentPath}?`)) return;
  try {
    await api("/file?path=" + encodeURIComponent(state.currentPath), { method: "DELETE" });
    clearExternalConflict();
    editor.value = "";
    syntaxBanner.classList.add("hidden");
    state.currentPath = null;
    state.dirty = false;
    await loadTree();
    setStatus("deleted");
  } catch (err) {
    setStatus(err.message, true);
  }
}

// --- Search ---------------------------------------------------------------

let searchSeq = 0;

async function runSearch() {
  const q = $("searchInput").value.trim();
  if (!q) {
    searchSeq++;
    searchResults.classList.add("hidden");
    searchResults.innerHTML = "";
    return;
  }
  const seq = ++searchSeq;
  const glob = $("globInput").value.trim();
  try {
    const data = await api(
      `/search?q=${encodeURIComponent(q)}&glob=${encodeURIComponent(glob)}`
    );
    if (seq !== searchSeq) return;
    searchResults.innerHTML = "";
    if (!data.matches.length) {
      searchResults.textContent = "no matches";
    } else {
      const frag = document.createDocumentFragment();
      for (const m of data.matches) {
        const row = document.createElement("div");
        row.className = "hit";
        row.title = `${m.path}:${m.line}`;
        const b = document.createElement("b");
        b.textContent = `${m.path}:${m.line}`;
        row.append(b, document.createTextNode("  " + m.text.trim()));
        row.addEventListener("click", () => openFile(m.path, m.line));
        frag.appendChild(row);
      }
      searchResults.appendChild(frag);
    }
    searchResults.classList.remove("hidden");
    setStatus(`${data.matches.length} match(es)`);
  } catch (err) {
    if (seq === searchSeq) setStatus(err.message, true);
  }
}

const scheduleLiveSearch = debounce(runSearch, 250);

// --- Stats Modal ----------------------------------------------------------

let statsTimer = null;
let statsLoadSeq = 0;

function openStatsModal() {
  $("statsModal").showModal();
  $("statsBody").innerHTML = "Fetching workspace stats…";
  loadStats();
}

// scheduleStatsRefresh reloads the stats while the modal is open, so it follows
// workspace changes live. A burst of changes (many edits in a row) is debounced
// into one request.
function scheduleStatsRefresh() {
  if (!$("statsModal").open) return;
  clearTimeout(statsTimer);
  statsTimer = setTimeout(loadStats, 500);
}

// loadStats fetches /stats and renders it in place, without the "Fetching…"
// text, so a live refresh only changes the numbers. A response that arrives
// after a newer request was started is dropped.
async function loadStats() {
  const body = $("statsBody");
  const seq = ++statsLoadSeq;
  try {
    const st = await api("/stats");
    if (seq !== statsLoadSeq || !$("statsModal").open) return;
    const num = (v) => Number(v) || 0;
    let extRows = "";
    for (const [ext, e] of Object.entries(st.byExtension || {})) {
      extRows += `<tr><td>${escapeHtml(ext)}</td><td>${num(e.files)}</td><td>${num(e.lines)}</td><td>${num(e.bytes).toLocaleString()} B</td></tr>`;
    }
    let largestRows = "";
    for (const f of st.largestFiles || []) {
      largestRows += `<tr><td>${escapeHtml(f.path)}</td><td>${num(f.lines)} lines</td></tr>`;
    }

    body.innerHTML = `
      <div class="stats-grid">
        <div class="stat-box"><div class="stat-val">${num(st.totalFiles)}</div><div class="stat-lbl">Files</div></div>
        <div class="stat-box"><div class="stat-val">${num(st.totalLines).toLocaleString()}</div><div class="stat-lbl">Lines</div></div>
        <div class="stat-box"><div class="stat-val">${Math.floor(num(st.totalBytes) / 1024).toLocaleString()} KB</div><div class="stat-lbl">Size</div></div>
      </div>
      <div class="stats-section-title">Files By Extension</div>
      <div class="table-scroll">
      <table class="stats-table">
        <thead><tr><th>Extension</th><th>Files</th><th>Lines</th><th>Bytes</th></tr></thead>
        <tbody>${extRows}</tbody>
      </table>
      </div>
      <div class="stats-section-title">Largest Files</div>
      <div class="table-scroll">
      <table class="stats-table">
        <thead><tr><th>Path</th><th>Lines</th></tr></thead>
        <tbody>${largestRows}</tbody>
      </table>
      </div>
    `;
  } catch (err) {
    if (seq !== statsLoadSeq) return;
    body.innerHTML = `<span class="err-text">Failed to load stats: ${escapeHtml(err.message)}</span>`;
  }
}

// --- Global Search & Replace Modal -----------------------------------------

function openSearchReplaceModal() {
  $("srResults").classList.add("hidden");
  $("srResults").textContent = "";
  $("searchReplaceModal").showModal();
}

async function executeSearchReplace(dryRun) {
  const pattern = $("srPattern").value.trim();
  const replacement = $("srReplacement").value;
  const glob = $("srGlob").value.trim();
  const resDiv = $("srResults");

  if (!pattern) {
    alert("Pattern is required");
    return;
  }

  resDiv.classList.remove("hidden");
  resDiv.innerHTML = "Processing…";

  try {
    const res = await api("/search-replace", {
      method: "POST",
      body: { pattern, replacement, glob, dryRun, maxFiles: 50 },
    });

    let msg = `<b>${dryRun ? "[DRY RUN] Would change" : "Changed"} ${Number(res.filesChanged) || 0} files (${Number(res.totalReplacements) || 0} replacements):</b><br>`;
    for (const f of res.files || []) {
      msg += `• ${escapeHtml(f.path)}: ${Number(f.replacements) || 0} replacements<br>`;
    }
    if (res.truncated) {
      msg += `<i>(Truncated at 50 files limit)</i><br>`;
    }
    resDiv.innerHTML = msg;

    if (!dryRun) {
      await loadTree();
      if (state.currentPath) {
        await openFile(state.currentPath, null, { force: true, preserveScroll: true });
      }
    }
  } catch (err) {
    resDiv.innerHTML = `<span class="err-text">Error: ${escapeHtml(err.message)}</span>`;
  }
}

// --- Background processes --------------------------------------------------

function formatProcDuration(ms) {
  if (!ms || ms < 1000) return (ms || 0) + "ms";
  const s = Math.round(ms / 1000);
  if (s < 60) return s + "s";
  const m = Math.floor(s / 60);
  if (m < 60) return m + "m " + (s % 60) + "s";
  return Math.floor(m / 60) + "h " + (m % 60) + "m";
}

function procStatus(p) {
  if (p.running && p.stopping) return "stopping";
  if (p.running && !p.ready) return "starting";
  if (p.running) return "running";
  return "exited";
}

function buildProcItem(p) {
  const li = document.createElement("li");
  li.className = "proc-item";
  li.dataset.procName = p.name;
  li.tabIndex = 0;
  li.addEventListener("click", () => openProcLogs(p.name));
  li.addEventListener("keydown", (ev) => {
    if (ev.key === "Enter" || ev.key === " ") {
      ev.preventDefault();
      openProcLogs(p.name);
    }
  });

  const dot = document.createElement("span");
  dot.className = "proc-dot";

  const meta = document.createElement("span");
  meta.className = "proc-meta";
  const name = document.createElement("div");
  name.className = "proc-name";
  name.textContent = p.name;
  const sub = document.createElement("div");
  sub.className = "proc-sub";
  meta.append(name, sub);

  const stop = document.createElement("button");
  stop.className = "proc-stop";
  stop.textContent = "stop";
  stop.addEventListener("click", async (ev) => {
    ev.stopPropagation();
    stop.disabled = true;
    try {
      await api("/processes/" + encodeURIComponent(p.name) + "/stop", {
        method: "POST",
        body: { wait: false },
      });
      setStatus("stopping " + p.name);
    } catch (err) {
      setStatus("stop " + p.name + ": " + err.message, true);
    }
    loadProcesses();
  });

  const restart = document.createElement("button");
  restart.className = "proc-restart";
  restart.textContent = "restart";
  restart.title = "Restart " + p.name;
  restart.addEventListener("click", (ev) => {
    ev.stopPropagation();
    restartProcess(p.name, restart);
  });

  li.append(dot, meta, restart, stop);
  updateProcItem(li, p);
  return li;
}

function updateProcItem(li, p) {
  const status = procStatus(p);
  li.classList.toggle("active", procLogs.name === p.name);
  li.title = (p.argv || []).join(" ") + (p.dir ? "\ncwd: " + p.dir : "") + "\nclick to view logs";
  const dot = li.querySelector(".proc-dot");
  if (dot) dot.className = "proc-dot " + status;
  const sub = li.querySelector(".proc-sub");
  if (sub) {
    const pid = p.running ? "pid " + p.pid : "exit " + p.exitCode;
    sub.textContent = status + " · " + pid + " · " + formatProcDuration(p.durationMs);
  }
  const stop = li.querySelector(".proc-stop");
  if (stop) stop.disabled = !p.running;
}

async function loadProcesses() {
  const list = $("procList");
  if (!list) return;
  try {
    const data = await api("/processes");
    const procs = data.processes || [];
    if (procs.length === 0) {
      list.innerHTML = "";
      const li = document.createElement("li");
      li.className = "proc-empty";
      li.textContent = "no background processes";
      list.appendChild(li);
      return;
    }
    // Remove empty/unavailable placeholder if present.
    for (const empty of list.querySelectorAll(".proc-empty")) {
      empty.remove();
    }
    const existing = new Map();
    for (const el of list.querySelectorAll(".proc-item")) {
      existing.set(el.dataset.procName, el);
    }
    const seen = new Set();
    procs.forEach((p, idx) => {
      seen.add(p.name);
      let li = existing.get(p.name);
      if (li) {
        updateProcItem(li, p);
      } else {
        li = buildProcItem(p);
      }
      if (list.children[idx] !== li) {
        list.insertBefore(li, list.children[idx] || null);
      }
    });
    for (const [name, el] of existing) {
      if (!seen.has(name)) el.remove();
    }
  } catch (err) {
    // The panel is optional; keep the editor usable if the endpoint fails.
    list.innerHTML = "";
    const li = document.createElement("li");
    li.className = "proc-empty";
    li.textContent = "unavailable: " + err.message;
    list.appendChild(li);
  }
}

async function restartProcess(name, btn) {
  if (btn) btn.disabled = true;
  setStatus("restarting " + name + "…");
  try {
    await api("/processes/" + encodeURIComponent(name) + "/restart", { method: "POST", body: {} });
    setStatus("restarted " + name);
  } catch (err) {
    setStatus("restart " + name + ": " + err.message, true);
  }
  if (btn) btn.disabled = false;
  loadProcesses();
  if (procLogs.name === name) pollProcLogs();
}

// --- Process logs viewer ------------------------------------------------------

// Cap the lines kept in the viewer so a chatty dev server cannot grow the page forever.
const PROC_LOG_MAX_LINES = 5000;

const procLogs = { name: null, after: 0, pid: null, timer: null, busy: false, lines: 0 };

function openProcLogs(name) {
  procLogs.name = name;
  procLogs.after = 0;
  procLogs.pid = null;
  procLogs.lines = 0;
  $("procLogsName").textContent = name;
  $("procLogsMeta").textContent = "loading…";
  $("procLogsOutput").textContent = "";
  const modal = $("procLogsModal");
  if (!modal.open) modal.showModal();
  clearInterval(procLogs.timer);
  pollProcLogs();
  procLogs.timer = setInterval(pollProcLogs, 1000);
  loadProcesses();
}

function closeProcLogs() {
  clearInterval(procLogs.timer);
  procLogs.timer = null;
  procLogs.name = null;
  const modal = $("procLogsModal");
  if (modal.open) modal.close();
  loadProcesses();
}

function appendProcLines(lines) {
  if (!lines.length) return;
  const out = $("procLogsOutput");
  const atBottom = out.scrollHeight - out.scrollTop - out.clientHeight < 24;
  const frag = document.createDocumentFragment();
  for (const l of lines) {
    const row = document.createElement("span");
    row.className = "log-line" + (l.stream === "stderr" ? " stderr" : "");
    row.textContent = l.text + "\n";
    frag.appendChild(row);
  }
  out.appendChild(frag);
  procLogs.lines += lines.length;
  while (procLogs.lines > PROC_LOG_MAX_LINES && out.firstChild) {
    out.removeChild(out.firstChild);
    procLogs.lines--;
  }
  if ($("procLogsFollow").checked || atBottom) out.scrollTop = out.scrollHeight;
}

function noteProcLog(text) {
  appendProcLines([{ text: "── " + text + " ──", stream: "note" }]);
  const last = $("procLogsOutput").lastChild;
  if (last) last.className = "log-line note";
}

async function pollProcLogs() {
  const name = procLogs.name;
  if (!name || procLogs.busy || document.hidden) return;
  procLogs.busy = true;
  try {
    const q = procLogs.after > 0 ? "?after=" + procLogs.after : "?tail=500";
    const data = await api("/processes/" + encodeURIComponent(name) + "/logs" + q);
    if (procLogs.name !== name) return; // switched while waiting
    const p = data.process || {};
    const logs = data.logs || {};

    if (procLogs.pid !== null && p.pid !== procLogs.pid && procLogs.after > 0) {
      procLogs.pid = p.pid;
      procLogs.after = 0;
      $("procLogsOutput").textContent = "";
      procLogs.lines = 0;
      noteProcLog("restarted (pid " + p.pid + ")");
    } else {
      procLogs.pid = p.pid;
    }

    if (logs.truncated && procLogs.after === 0) noteProcLog("older lines dropped");
    appendProcLines(logs.lines || []);
    if (typeof logs.nextSeq === "number") procLogs.after = logs.nextSeq;

    const status = procStatus(p);
    $("procLogsDot").className = "proc-dot " + status;
    const pid = p.running ? "pid " + p.pid : "exit " + p.exitCode;
    $("procLogsMeta").textContent =
      status + " · " + pid + " · " + formatProcDuration(p.durationMs) + " · " + (p.argv || []).join(" ");
    $("btnProcLogsStop").disabled = !p.running || !!p.stopping;
  } catch (err) {
    if (procLogs.name === name) $("procLogsMeta").textContent = "error: " + err.message;
  } finally {
    procLogs.busy = false;
  }
}

async function stopProcLogsProcess() {
  const name = procLogs.name;
  if (!name) return;
  $("btnProcLogsStop").disabled = true;
  try {
    await api("/processes/" + encodeURIComponent(name) + "/stop", { method: "POST", body: { wait: false } });
    setStatus("stopping " + name);
  } catch (err) {
    setStatus("stop " + name + ": " + err.message, true);
  }
  pollProcLogs();
  loadProcesses();
}

async function stopAllProcesses() {
  if (!confirm("Stop every background process?")) return;
  try {
    await api("/processes/stop-all", {
      method: "POST",
      body: { graceSeconds: 3 },
    });
    setStatus("stopped all processes");
  } catch (err) {
    setStatus("stop all: " + err.message, true);
  }
  loadProcesses();
}

// --- Upload ---------------------------------------------------------------

const upload = { files: [], maxBytes: 20 * 1024 * 1024, busy: false };

function formatBytes(n) {
  if (n >= 1048576) return (n / 1048576).toFixed(1) + " MB";
  if (n >= 1024) return (n / 1024).toFixed(1) + " KB";
  return n + " B";
}

function openUploadModal() {
  // Default destination: the folder of the open file, else the root.
  if (!$("uploadDir").value && state.currentPath && state.currentPath.includes("/")) {
    $("uploadDir").value = state.currentPath.slice(0, state.currentPath.lastIndexOf("/"));
  }
  $("uploadHint").textContent = "max " + formatBytes(upload.maxBytes) + " per file";
  $("uploadModal").showModal();
}

function addUploadFiles(fileList) {
  for (const f of fileList) {
    if (!upload.files.some((x) => x.name === f.name && x.size === f.size)) upload.files.push(f);
  }
  renderUploadList();
}

function renderUploadList() {
  const list = $("uploadList");
  list.innerHTML = "";
  upload.files.forEach((f, i) => {
    const li = document.createElement("li");
    const name = document.createElement("span");
    name.textContent = f.name;
    const size = document.createElement("span");
    size.className = "upload-size" + (f.size > upload.maxBytes ? " upload-err" : "");
    size.textContent = formatBytes(f.size) + (f.size > upload.maxBytes ? " (too large)" : "");
    const rm = document.createElement("button");
    rm.className = "ghost";
    rm.textContent = "✕";
    rm.title = "Remove";
    rm.addEventListener("click", () => { upload.files.splice(i, 1); renderUploadList(); });
    li.append(name, size, rm);
    list.appendChild(li);
  });
  $("btnUploadSend").disabled = upload.busy || upload.files.length === 0;
}

async function sendUpload() {
  if (upload.busy || upload.files.length === 0) return;
  const dir = $("uploadDir").value.trim() || ".";
  const overwrite = $("uploadOverwrite").checked;
  const form = new FormData();
  for (const f of upload.files) form.append("file", f, f.name);
  const resDiv = $("uploadResults");
  upload.busy = true;
  renderUploadList();
  resDiv.classList.remove("hidden");
  resDiv.textContent = "Uploading…";
  try {
    const qs = new URLSearchParams({ dir, overwrite: overwrite ? "true" : "false" });
    const res = await fetch("/api/upload?" + qs, { method: "POST", body: form, credentials: "same-origin" });
    if (res.status === 401) { location.href = "/login"; return; }
    const data = await res.json().catch(() => ({}));
    resDiv.innerHTML = "";
    const files = data.files || [];
    if (files.length === 0) {
      resDiv.textContent = data.error || res.statusText;
      resDiv.classList.add("upload-err");
      return;
    }
    resDiv.classList.remove("upload-err");
    let okCount = 0;
    for (const f of files) {
      const line = document.createElement("div");
      if (f.error) {
        line.className = "upload-err";
        line.textContent = "✗ " + (f.name || "?") + ": " + f.error + (f.status === 409 ? " — tick 'overwrite' to replace it" : "");
      } else {
        okCount++;
        line.textContent = "✓ " + f.path + " (" + formatBytes(f.bytes) + ")";
      }
      resDiv.appendChild(line);
    }
    // Keep only the files that failed, so they can be retried.
    const failed = new Set(files.filter((f) => f.error).map((f) => f.name));
    upload.files = upload.files.filter((f) => failed.has(f.name));
    setStatus("uploaded " + okCount + " of " + files.length + " file(s)", okCount !== files.length);
    if (okCount > 0) await loadTree();
  } catch (err) {
    resDiv.textContent = err.message;
    resDiv.classList.add("upload-err");
  } finally {
    upload.busy = false;
    renderUploadList();
  }
}

// --- Sidebar --------------------------------------------------------------

const SIDEBAR_KEY = "ngodingmcp.sidebarCollapsed";
const mobileMedia = window.matchMedia ? window.matchMedia("(max-width: 720px)") : null;

function isMobileViewport() {
  return mobileMedia ? mobileMedia.matches : window.innerWidth <= 720;
}

function setSidebarCollapsed(collapsed, remember = true) {
  $("layout").classList.toggle("sidebar-collapsed", collapsed);
  const btn = $("btnSidebar");
  btn.setAttribute("aria-expanded", String(!collapsed));
  btn.title = (collapsed ? "Show sidebar" : "Collapse sidebar") + " (Ctrl+B)";
  if (remember && !isMobileViewport()) {
    try { localStorage.setItem(SIDEBAR_KEY, collapsed ? "1" : "0"); } catch (_) { /* storage blocked */ }
  }
}

function toggleSidebar() {
  setSidebarCollapsed(!$("layout").classList.contains("sidebar-collapsed"));
}

{
  let collapsed = false;
  if (isMobileViewport()) {
    collapsed = true;
  } else {
    try { collapsed = localStorage.getItem(SIDEBAR_KEY) === "1"; } catch (_) { /* storage blocked */ }
  }
  // Apply without the slide animation on page load.
  $("layout").style.transition = "none";
  setSidebarCollapsed(collapsed, false);
  requestAnimationFrame(() => { $("layout").style.transition = ""; });
}

// --- Workspaces -----------------------------------------------------------

const wsState = { data: null, busy: false };

function wsSensitiveLabel(allow) {
  return allow ? "sensitive files allowed" : "sensitive files blocked";
}

function renderWorkspaceButton() {
  const d = wsState.data;
  if (!d) return;
  const name = $("wsName");
  name.textContent = d.activeName || "unsaved folder";
  name.classList.toggle("unsaved", !d.activeName);
  $("btnWorkspaces").title = "Switch workspace\n" + d.activeRoot;
  $("rootPath").textContent = d.activeRoot;
  $("rootPath").title = d.activeRoot;
}

async function loadWorkspaces() {
  wsState.data = await api("/workspaces");
  renderWorkspaceButton();
  return wsState.data;
}

function renderWorkspaces() {
  const d = wsState.data;
  if (!d) return;
  const active = $("wsActive");
  active.innerHTML = "";
  active.append("Active: ");
  const code = document.createElement("code");
  code.textContent = d.activeRoot;
  active.append(code);
  active.append(" · " + wsSensitiveLabel(d.effective.allowSensitive) + " · max " + formatBytes(d.effective.maxFileBytes) + " per file");

  const list = $("wsList");
  list.innerHTML = "";
  for (const w of d.workspaces) {
    const li = document.createElement("li");
    li.className = "ws-item" + (w.active ? " active" : "");

    const main = document.createElement("div");
    main.className = "ws-item-main";
    const nameRow = document.createElement("div");
    nameRow.className = "ws-item-name";
    nameRow.append(w.name);
    if (w.active) {
      const t = document.createElement("span");
      t.className = "ws-tag on";
      t.textContent = "active";
      nameRow.append(t);
    }
    if (w.missing) {
      const t = document.createElement("span");
      t.className = "ws-tag warn";
      t.textContent = "folder missing";
      nameRow.append(t);
    }
    const path = document.createElement("div");
    path.className = "ws-item-path";
    path.textContent = w.path;
    path.title = w.path;
    const meta = document.createElement("div");
    meta.className = "ws-item-meta";
    const sens = w.allowSensitive === null || w.allowSensitive === undefined
      ? "sensitive: default"
      : (w.allowSensitive ? "sensitive: allowed" : "sensitive: blocked");
    const max = w.maxFileBytes > 0 ? "max " + formatBytes(w.maxFileBytes) : "max: default";
    meta.textContent = sens + " · " + max;
    main.append(nameRow, path, meta);

    const actions = document.createElement("div");
    actions.className = "ws-item-actions";
    const open = document.createElement("button");
    open.className = "primary";
    open.textContent = w.active ? "active" : "open";
    open.disabled = w.active || w.missing || wsState.busy;
    open.addEventListener("click", () => switchWorkspace(w.name));
    const edit = document.createElement("button");
    edit.className = "ghost";
    edit.textContent = "edit";
    edit.addEventListener("click", () => editWorkspace(w));
    const del = document.createElement("button");
    del.className = "ghost danger";
    del.textContent = "✕";
    del.title = "Remove from the list (the folder is not touched)";
    del.addEventListener("click", () => deleteWorkspace(w.name));
    actions.append(open, edit, del);

    li.append(main, actions);
    list.appendChild(li);
  }

  const warn = $("wsProcWarn");
  if (d.runningProcesses && d.runningProcesses.length) {
    warn.textContent = "Running background processes (" + d.runningProcesses.join(", ") +
      ") belong to the current workspace and will be stopped when you switch.";
    warn.classList.remove("hidden");
  } else {
    warn.classList.add("hidden");
  }

  $("wsDefaults").textContent = "Defaults: " + wsSensitiveLabel(d.defaults.allowSensitive) +
    ", max " + formatBytes(d.defaults.maxFileBytes) + " per file. Empty settings inherit these.";
}

function resetWorkspaceForm() {
  $("wsOriginal").value = "";
  $("wsFormTitle").textContent = "Add workspace";
  $("wsFormName").value = "";
  $("wsFormPath").value = "";
  $("wsFormSensitive").value = "";
  $("wsFormMaxMB").value = "";
  $("btnWsCancelEdit").classList.add("hidden");
}

function editWorkspace(w) {
  $("wsOriginal").value = w.name;
  $("wsFormTitle").textContent = "Edit “" + w.name + "”";
  $("wsFormName").value = w.name;
  $("wsFormPath").value = w.path;
  $("wsFormSensitive").value = w.allowSensitive === null || w.allowSensitive === undefined
    ? "" : (w.allowSensitive ? "allow" : "block");
  $("wsFormMaxMB").value = w.maxFileBytes > 0 ? String(+(w.maxFileBytes / 1048576).toFixed(2)) : "";
  $("btnWsCancelEdit").classList.remove("hidden");
  $("wsFormName").focus();
}

async function openWorkspacesModal() {
  resetWorkspaceForm();
  $("workspacesModal").showModal();
  try {
    await loadWorkspaces();
    renderWorkspaces();
  } catch (err) {
    setStatus(err.message, true);
  }
}

async function saveWorkspace(ev) {
  ev.preventDefault();
  const original = $("wsOriginal").value;
  const sens = $("wsFormSensitive").value;
  const mb = parseFloat($("wsFormMaxMB").value);
  const body = {
    name: $("wsFormName").value.trim(),
    path: $("wsFormPath").value.trim(),
    allowSensitive: sens === "" ? null : sens === "allow",
    maxFileBytes: mb > 0 ? Math.round(mb * 1048576) : 0,
  };
  if (body.allowSensitive === true && !confirm("Allow .env files and private keys in this workspace? The AI will be able to read them.")) {
    return;
  }
  try {
    const path = original ? "/workspaces/" + encodeURIComponent(original) : "/workspaces";
    wsState.data = await api(path, { method: original ? "PUT" : "POST", body });
    renderWorkspaceButton();
    renderWorkspaces();
    resetWorkspaceForm();
    setStatus(original ? "workspace updated" : "workspace saved");
  } catch (err) {
    setStatus(err.message, true);
    alert(err.message);
  }
}

async function deleteWorkspace(name) {
  if (!confirm("Remove “" + name + "” from the list? The folder itself is not deleted.")) return;
  try {
    wsState.data = await api("/workspaces/" + encodeURIComponent(name), { method: "DELETE" });
    renderWorkspaceButton();
    renderWorkspaces();
    if ($("wsOriginal").value === name) resetWorkspaceForm();
    setStatus("workspace removed from the list");
  } catch (err) {
    setStatus(err.message, true);
  }
}

async function switchWorkspace(name) {
  if (wsState.busy) return;
  if (state.dirty && !confirm("You have unsaved changes in " + state.currentPath + ". Discard them and switch?")) return;
  wsState.busy = true;
  renderWorkspaces();
  setStatus("switching to " + name + "…");
  try {
    let res;
    try {
      res = await api("/workspaces/" + encodeURIComponent(name) + "/activate", { method: "POST", body: { stopProcesses: false } });
    } catch (err) {
      const running = err.status === 409 && err.data && err.data.runningProcesses;
      if (!running) throw err;
      if (!confirm("These background processes are still running: " + running.join(", ") +
        "\n\nStop them and switch to “" + name + "”?")) {
        setStatus("switch cancelled");
        return;
      }
      res = await api("/workspaces/" + encodeURIComponent(name) + "/activate", { method: "POST", body: { stopProcesses: true } });
    }
    // The old workspace's file is gone from view.
    clearExternalConflict();
    state.activeNode = null;
    state.expandedDirs.clear();
    editor.value = "";
    syntaxBanner.classList.add("hidden");
    searchResults.classList.add("hidden");
    searchResults.innerHTML = "";
    $("uploadDir").value = "";
    if (procLogs.name) closeProcLogs();
    state.currentPath = null;
    state.dirty = false;
    await loadWorkspaces();
    renderWorkspaces();
    await loadTree();
    if (!$("procPanel").classList.contains("hidden")) await loadProcesses();
    const stopped = res.stoppedProcesses && res.stoppedProcesses.length
      ? " (stopped: " + res.stoppedProcesses.join(", ") + ")" : "";
    setStatus("workspace: " + res.name + stopped);
    $("workspacesModal").close();
  } catch (err) {
    setStatus(err.message, true);
    alert(err.message);
  } finally {
    wsState.busy = false;
    renderWorkspaces();
  }
}

// --- Live Workspace Sync (SSE) --------------------------------------------

let eventSource = null;

function handleWorkspaceEvent(ev) {
  if (!ev || typeof ev !== "object") return;
  if (ev.op === "hello") {
    if (state.lastSeq > 0 && ev.seq > state.lastSeq) {
      scheduleTreeRefresh();
      scheduleStatsRefresh();
      if (state.currentPath) checkActiveFileAfterEvent("modified", state.currentPath);
    }
    state.lastSeq = Math.max(state.lastSeq, ev.seq || state.lastSeq);
    return;
  }
  if (typeof ev.seq === "number") {
    state.lastSeq = Math.max(state.lastSeq, ev.seq);
  }
  scheduleStatsRefresh();

  // Ignore echo of our own just-completed save.
  if (
    ev.op === "write" &&
    ev.path === state.lastSelfWritePath &&
    Date.now() - state.lastSelfWriteAt < 1500
  ) {
    state.lastSelfWritePath = null;
    scheduleTreeRefresh();
    return;
  }

  if (ev.op === "switch") {
    clearExternalConflict();
    state.expandedDirs.clear();
    state.activeNode = null;
    if (state.currentPath) {
      state.currentPath = null;
      state.dirty = false;
      editor.value = "";
    }
    loadWorkspaces().catch(() => {});
    scheduleTreeRefresh();
    if (!$("procPanel").classList.contains("hidden")) loadProcesses();
    setStatus("workspace switched externally");
    return;
  }

  if (ev.op === "rename" && ev.from) {
    remapExpandedDirs(ev.from, ev.path);
    if (state.currentPath === ev.from) {
      state.currentPath = ev.path;
      setStatus("renamed externally to " + ev.path);
    } else if (state.currentPath && state.currentPath.startsWith(ev.from + "/")) {
      state.currentPath = ev.path + state.currentPath.slice(ev.from.length);
    }
    scheduleTreeRefresh();
    return;
  }

  if (ev.op === "delete" && ev.path) {
    pruneExpandedDirs(ev.path, true);
    if (
      state.currentPath === ev.path ||
      (state.currentPath && state.currentPath.startsWith(ev.path + "/"))
    ) {
      if (state.dirty) {
        showExternalConflict(`"${state.currentPath}" was deleted on disk. Save to recreate or discard.`);
      } else {
        clearExternalConflict();
        editor.value = "";
        syntaxBanner.classList.add("hidden");
        const removed = state.currentPath;
        state.currentPath = null;
        state.dirty = false;
        setStatus(`"${removed}" was deleted externally`);
      }
    }
    scheduleTreeRefresh();
    return;
  }

  if (ev.op === "write" && ev.path && ev.path === state.currentPath) {
    checkActiveFileAfterEvent("modified", ev.path);
  }
  scheduleTreeRefresh();
}

async function checkActiveFileAfterEvent(kind, path) {
  if (!state.currentPath || state.currentPath !== path) return;
  if (state.dirty) {
    showExternalConflict(`"${path}" was ${kind} on disk while you have unsaved edits.`);
    return;
  }
  try {
    const data = await api("/file?path=" + encodeURIComponent(path));
    if (state.currentPath !== path || state.dirty) return;
    if (data.content === editor.value) return;
    const prevScroll = editor.scrollTop;
    const prevStart = editor.selectionStart;
    const prevEnd = editor.selectionEnd;
    editor.value = data.content;
    clearExternalConflict();
    updateUI();
    const maxPos = editor.value.length;
    editor.setSelectionRange(Math.min(prevStart, maxPos), Math.min(prevEnd, maxPos));
    editor.scrollTop = prevScroll;
    lineNumbers.scrollTop = prevScroll;
    updateCursor();
    setStatus(`synced external update (${data.totalLines} lines)`);
  } catch (err) {
    if (err.status === 404) {
      showExternalConflict(`"${path}" is no longer available on disk.`);
    }
  }
}

function connectWorkspaceEvents() {
  if (typeof EventSource === "undefined") return;
  if (eventSource) {
    eventSource.close();
    eventSource = null;
  }
  state.sseState = "connecting";
  const es = new EventSource("/api/events");
  eventSource = es;

  es.onopen = () => {
    state.sseState = "connected";
  };

  es.onmessage = (msg) => {
    state.sseState = "connected";
    if (!msg.data) return;
    try {
      handleWorkspaceEvent(JSON.parse(msg.data));
    } catch (_) {
      // Ignore malformed frames.
    }
  };

  es.onerror = () => {
    state.sseState = "reconnecting";
  };
}

// --- Wiring ---------------------------------------------------------------

$("btnSidebar").addEventListener("click", toggleSidebar);
$("sidebarBackdrop").addEventListener("click", () => setSidebarCollapsed(true, false));
document.addEventListener("keydown", (ev) => {
  if (ev.key === "Escape" && isMobileViewport() && !$("layout").classList.contains("sidebar-collapsed")) {
    setSidebarCollapsed(true, false);
    return;
  }
  if ((ev.ctrlKey || ev.metaKey) && !ev.shiftKey && !ev.altKey && ev.key.toLowerCase() === "b") {
    ev.preventDefault();
    toggleSidebar();
  }
});

if ($("btnExternalReload")) {
  $("btnExternalReload").addEventListener("click", () => {
    if (state.currentPath) {
      openFile(state.currentPath, null, { force: true, preserveScroll: true });
    }
  });
}
if ($("btnExternalDismiss")) {
  $("btnExternalDismiss").addEventListener("click", clearExternalConflict);
}

// --- Settings Modal ----------------------------------------------------------

async function openSettingsModal() {
  try {
    const s = await api("/settings");
    $("settingsAddr").value = s.addr || "";
    $("settingsToken").value = s.token || "";
    $("settingsRun").checked = !!s.runEnabled;
    $("settingsSensitive").checked = !!s.allowSensitive;
    $("settingsDownload").checked = !!s.allowDownload;
    $("settingsPublicURL").value = s.publicUrl || "";
    $("settingsTrustedProxies").value = (s.trustedProxies || []).join(", ");
    $("settingsOAuth").checked = !!s.oauth;
    $("settingsModal").showModal();
  } catch (err) {
    setStatus("settings: " + err.message, true);
  }
}

async function saveSettings() {
  const publicUrl = $("settingsPublicURL").value.trim();
  const trustedProxies = $("settingsTrustedProxies").value
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
  const oauth = $("settingsOAuth").checked;
  const addr = $("settingsAddr").value.trim();
  const token = $("settingsToken").value.trim();
  const runEnabled = $("settingsRun").checked;
  const allowSensitive = $("settingsSensitive").checked;
  const allowDownload = $("settingsDownload").checked;
  try {
    await api("/settings", {
      method: "PUT",
      body: { publicUrl, oauth, trustedProxies, addr, token, runEnabled, allowSensitive, allowDownload },
    });
    $("settingsModal").close();
    setStatus("settings saved — most changes apply after restart", false);
  } catch (err) {
    setStatus("settings: " + err.message, true);
    alert("settings: " + err.message);
  }
}

$("btnWorkspaces").addEventListener("click", openWorkspacesModal);
$("btnSettings").addEventListener("click", openSettingsModal);
$("btnCloseSettings").addEventListener("click", () => $("settingsModal").close());
$("btnCancelSettings").addEventListener("click", () => $("settingsModal").close());
$("btnSaveSettings").addEventListener("click", saveSettings);
$("btnCloseWorkspaces").addEventListener("click", () => $("workspacesModal").close());
$("wsForm").addEventListener("submit", saveWorkspace);
$("btnWsCancelEdit").addEventListener("click", resetWorkspaceForm);
$("btnWsUseCurrent").addEventListener("click", () => {
  if (!wsState.data) return;
  $("wsFormPath").value = wsState.data.activeRoot;
  if (!$("wsFormName").value) {
    const parts = wsState.data.activeRoot.split("/").filter(Boolean);
    $("wsFormName").value = parts[parts.length - 1] || "";
  }
});

editor.addEventListener("input", markDirty);
editor.addEventListener("click", updateCursor);
editor.addEventListener("keyup", updateCursor);
editor.addEventListener("keydown", (ev) => {
  if ((ev.ctrlKey || ev.metaKey) && ev.key.toLowerCase() === "s") {
    ev.preventDefault();
    saveFile();
    return;
  }
  if (ev.key === "Tab") {
    ev.preventDefault();
    const start = editor.selectionStart;
    const end = editor.selectionEnd;
    if (ev.shiftKey) {
      const before = editor.value.slice(0, start);
      const lineStart = before.lastIndexOf("\n") + 1;
      const linePrefix = editor.value.slice(lineStart, lineStart + 2);
      const removeLen = linePrefix === "  " ? 2 : (editor.value[lineStart] === " " || editor.value[lineStart] === "\t" ? 1 : 0);
      if (removeLen > 0) {
        insertEditorText("", lineStart, lineStart + removeLen);
        editor.setSelectionRange(Math.max(lineStart, start - removeLen), Math.max(lineStart, end - removeLen));
      }
      return;
    }
    insertEditorText("  ", start, end);
    return;
  }
  if (ev.key === "Enter") {
    const start = editor.selectionStart;
    const end = editor.selectionEnd;
    const before = editor.value.slice(0, start);
    const lineStart = before.lastIndexOf("\n") + 1;
    const currentLine = before.slice(lineStart);
    const indentMatch = currentLine.match(/^[ \t]*/);
    const indent = indentMatch ? indentMatch[0] : "";
    if (indent.length > 0) {
      ev.preventDefault();
      insertEditorText("\n" + indent, start, end);
    }
  }
});

$("btnSave").addEventListener("click", saveFile);
$("btnCheckSyntax").addEventListener("click", checkSyntax);
$("btnNewFile").addEventListener("click", newFile);
$("btnNewFolder").addEventListener("click", newFolder);
$("btnRefresh").addEventListener("click", loadTree);
$("btnRename").addEventListener("click", renameCurrent);
$("btnDelete").addEventListener("click", deleteCurrent);

$("btnStats").addEventListener("click", openStatsModal);
$("btnCloseStats").addEventListener("click", () => $("statsModal").close());

$("btnSearchReplaceModal").addEventListener("click", openSearchReplaceModal);
$("btnCloseSearchReplace").addEventListener("click", () => $("searchReplaceModal").close());
$("btnSrDryRun").addEventListener("click", () => executeSearchReplace(true));
$("btnSrExecute").addEventListener("click", () => {
  if (confirm("Are you sure you want to replace across the workspace?")) {
    executeSearchReplace(false);
  }
});

$("btnUpload").addEventListener("click", openUploadModal);
$("btnCloseUpload").addEventListener("click", () => $("uploadModal").close());
$("btnUploadPick").addEventListener("click", () => $("uploadInput").click());
$("uploadInput").addEventListener("change", () => { addUploadFiles($("uploadInput").files); $("uploadInput").value = ""; });
$("btnUploadSend").addEventListener("click", sendUpload);
$("btnUploadClear").addEventListener("click", () => {
  upload.files = [];
  $("uploadResults").classList.add("hidden");
  renderUploadList();
});
{
  const drop = $("uploadDrop");
  drop.addEventListener("keydown", (ev) => { if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); $("uploadInput").click(); } });
  ["dragenter", "dragover"].forEach((t) => drop.addEventListener(t, (ev) => { ev.preventDefault(); drop.classList.add("over"); }));
  ["dragleave", "dragend"].forEach((t) => drop.addEventListener(t, () => drop.classList.remove("over")));
  drop.addEventListener("drop", (ev) => {
    ev.preventDefault();
    drop.classList.remove("over");
    if (ev.dataTransfer && ev.dataTransfer.files) addUploadFiles(ev.dataTransfer.files);
  });
}

$("btnRefreshProc").addEventListener("click", loadProcesses);
$("btnStopAllProc").addEventListener("click", stopAllProcesses);
$("btnCloseProcLogs").addEventListener("click", closeProcLogs);
$("procLogsModal").addEventListener("close", () => { if (procLogs.name) closeProcLogs(); });
// Any modal closes when its backdrop (the <dialog> element itself) is clicked,
// mirroring Escape. The process-logs modal has extra cleanup, wired to its
// "close" event above.
document.querySelectorAll("dialog.modal").forEach((dialog) => {
  dialog.addEventListener("click", (ev) => {
    if (ev.target === dialog) dialog.close();
  });
});
$("btnProcLogsStop").addEventListener("click", stopProcLogsProcess);
$("btnProcLogsRestart").addEventListener("click", () => { if (procLogs.name) restartProcess(procLogs.name, $("btnProcLogsRestart")); });
$("btnProcLogsClear").addEventListener("click", () => { $("procLogsOutput").textContent = ""; procLogs.lines = 0; });

$("searchInput").addEventListener("keydown", (ev) => {
  if (ev.key === "Enter") {
    scheduleLiveSearch.cancel();
    runSearch();
  }
});
$("searchInput").addEventListener("input", () => {
  if (!$("searchInput").value.trim()) {
    scheduleLiveSearch.cancel();
    searchSeq++;
    searchResults.classList.add("hidden");
    searchResults.innerHTML = "";
    return;
  }
  scheduleLiveSearch();
});
$("globInput").addEventListener("input", () => {
  if ($("searchInput").value.trim()) {
    scheduleLiveSearch();
  }
});

window.addEventListener("beforeunload", (ev) => {
  if (eventSource) eventSource.close();
  if (state.dirty) {
    ev.preventDefault();
    ev.returnValue = "";
  }
});

// showVersion puts the server version in the status bar; the build time is in
// the tooltip (also on the brand), so a rebuilt binary is easy to recognise.
function showVersion(info) {
  if (!info.version) return;
  const built = info.built ? new Date(info.built).toLocaleString() : "";
  const label = "NgodingMCP " + info.version + (built ? "\nbuilt " + built : "");
  $("appVersion").textContent = info.version;
  $("appVersion").title = label;
  const brand = document.querySelector(".topbar .brand");
  if (brand) brand.title = label;
}

(async function init() {
  updateUI();
  connectWorkspaceEvents();
  try {
    const info = await api("/info");
    $("rootPath").textContent = info.root;
    $("rootPath").title = info.root;
    showVersion(info);
    if (info.uploadMaxBytes > 0) upload.maxBytes = info.uploadMaxBytes;
    if (info.workspacesEnabled) {
      $("btnWorkspaces").classList.remove("hidden");
      await loadWorkspaces().catch((err) => setStatus(err.message, true));
    }
    if (info.processesEnabled) {
      $("procPanel").classList.remove("hidden");
      await loadProcesses();
      setInterval(() => {
        if (!document.hidden) loadProcesses();
      }, 3000);
      document.addEventListener("visibilitychange", () => {
        if (!document.hidden) {
          loadProcesses();
          if (procLogs.name) pollProcLogs();
        }
      });
    }
  } catch (err) {
    setStatus(err.message, true);
  }
  await loadTree();
})();
