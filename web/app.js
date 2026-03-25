(function () {
  "use strict";

  const THEME_KEY = "local-notes-theme";
  const SIDEBAR_KEY = "local-notes-sidebar-collapsed";
  /** 单条笔记正文参与检索的最大字符数，避免极大文件拖慢输入 */
  const SEARCH_BODY_MAX_CHARS = 24000;
  const SEARCH_LIST_DEBOUNCE_MS = 110;

  /** @typedef {{ id: string, title: string, body: string, updatedAt: number, dir: string }} Note */

  const els = {
    app: document.getElementById("app"),
    sidebar: document.getElementById("sidebar"),
    btnSidebarCollapse: document.getElementById("btn-sidebar-collapse"),
    btnSidebarExpand: document.getElementById("btn-sidebar-expand"),
    noteList: document.getElementById("note-list"),
    search: document.getElementById("search"),
    btnNew: document.getElementById("btn-new"),
    btnDelete: document.getElementById("btn-delete"),
    btnTheme: document.getElementById("btn-theme"),
    emptyState: document.getElementById("empty-state"),
    editorWrap: document.getElementById("editor-wrap"),
    editorMain: document.getElementById("editor-main"),
    title: document.getElementById("note-title"),
    body: document.getElementById("note-body"),
    preview: document.getElementById("note-preview"),
    tabEdit: document.getElementById("tab-edit"),
    tabPreview: document.getElementById("tab-preview"),
    savedHint: document.getElementById("saved-hint"),
    noteCount: document.getElementById("note-count"),
  };

  /** @type {Note[]} */
  let notes = [];
  /** @type {string | null} */
  let activeId = null;
  let saveTimer = null;
  let hintTimer = null;
  /** @type {"edit" | "preview"} */
  let viewMode = "preview";
  /** 当前笔记在仓库中的相对目录，如 2026/03/24/n_xxx，用于解析相对路径图片 */
  let activeNoteDir = "";
  let searchListTimer = null;

  async function refreshNotes() {
    const r = await fetch("/api/notes");
    if (!r.ok) throw new Error("load failed");
    const data = await r.json();
    notes = Array.isArray(data) ? data : [];
  }

  function loadTheme() {
    const stored = localStorage.getItem(THEME_KEY);
    const prefersDark = window.matchMedia("(prefers-color-scheme: dark)").matches;
    const theme = stored === "light" || stored === "dark" ? stored : prefersDark ? "dark" : "light";
    document.documentElement.setAttribute("data-theme", theme);
  }

  function applySidebarCollapsed(collapsed) {
    if (!els.app || !els.sidebar || !els.btnSidebarCollapse || !els.btnSidebarExpand) return;
    els.app.classList.toggle("sidebar-collapsed", collapsed);
    els.sidebar.setAttribute("aria-hidden", collapsed ? "true" : "false");
    els.btnSidebarCollapse.setAttribute("aria-expanded", collapsed ? "false" : "true");
    if (collapsed) els.btnSidebarExpand.removeAttribute("hidden");
    else els.btnSidebarExpand.setAttribute("hidden", "");
    localStorage.setItem(SIDEBAR_KEY, collapsed ? "1" : "0");
  }

  function loadSidebarState() {
    applySidebarCollapsed(localStorage.getItem(SIDEBAR_KEY) === "1");
  }

  function collapseSidebar() {
    if (!els.app?.classList.contains("sidebar-collapsed") && els.sidebar?.contains(document.activeElement)) {
      els.btnSidebarExpand?.focus();
    }
    applySidebarCollapsed(true);
  }

  function expandSidebar() {
    applySidebarCollapsed(false);
    els.btnSidebarCollapse?.focus();
  }

  function toggleTheme() {
    const next = document.documentElement.getAttribute("data-theme") === "dark" ? "light" : "dark";
    document.documentElement.setAttribute("data-theme", next);
    localStorage.setItem(THEME_KEY, next);
  }

  function getActiveNote() {
    return notes.find((n) => n.id === activeId) || null;
  }

  function formatTime(ts) {
    const d = new Date(ts);
    const now = new Date();
    const sameDay =
      d.getFullYear() === now.getFullYear() &&
      d.getMonth() === now.getMonth() &&
      d.getDate() === now.getDate();
    if (sameDay) {
      return d.toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit" });
    }
    return d.toLocaleDateString("zh-CN", { month: "short", day: "numeric" });
  }

  function stripMdLine(s) {
    return s
      .replace(/^#{1,6}\s+/, "")
      .replace(/!\[[^\]]*\]\([^)]*\)/g, "")
      .replace(/`+/g, "")
      .replace(/\*\*([^*]+)\*\*/g, "$1")
      .replace(/\*([^*]+)\*/g, "$1")
      .trim();
  }

  function listTitle(note) {
    const t = note.title.trim();
    if (t) return t;
    const lines = note.body.trim().split(/\n/);
    let pick = "";
    for (const line of lines) {
      const x = stripMdLine(line);
      if (x) {
        pick = x;
        break;
      }
    }
    return pick.slice(0, 44) || "无标题笔记";
  }

  function searchTokens(raw) {
    return raw
      .trim()
      .toLowerCase()
      .split(/\s+/)
      .filter((t) => t.length > 0);
  }

  /**
   * 无搜索词时顺序与 API 一致；有搜索时：空格分词须全部命中（标题或正文前段），标题命中数多的排前。
   */
  function filterNotes(query) {
    const tokens = searchTokens(query);
    if (tokens.length === 0) return [...notes];

    const scored = [];
    for (const n of notes) {
      const titleL = n.title.toLowerCase();
      const bodySlice =
        n.body.length > SEARCH_BODY_MAX_CHARS ? n.body.slice(0, SEARCH_BODY_MAX_CHARS) : n.body;
      const bodyL = bodySlice.toLowerCase();

      let titleHits = 0;
      let ok = true;
      for (const t of tokens) {
        const inT = titleL.includes(t);
        const inB = bodyL.includes(t);
        if (!inT && !inB) {
          ok = false;
          break;
        }
        if (inT) titleHits++;
      }
      if (!ok) continue;
      scored.push({ n, titleHits, updatedAt: n.updatedAt });
    }

    scored.sort((a, b) => {
      if (b.titleHits !== a.titleHits) return b.titleHits - a.titleHits;
      return b.updatedAt - a.updatedAt;
    });
    return scored.map((x) => x.n);
  }

  function applyListTabIndices() {
    const btns = [...els.noteList.querySelectorAll(".note-item-btn")];
    btns.forEach((b) => {
      b.tabIndex = -1;
    });
    const pick = btns.find((b) => b.dataset.id === activeId) || btns[0];
    if (pick) pick.tabIndex = 0;
  }

  function focusNoteListButton(btn) {
    const btns = [...els.noteList.querySelectorAll(".note-item-btn")];
    btns.forEach((b) => {
      b.tabIndex = -1;
    });
    if (!btn || !btns.includes(btn)) return;
    btn.tabIndex = 0;
    btn.focus();
    btn.scrollIntoView({ block: "nearest" });
  }

  function renderList() {
    if (searchListTimer) {
      clearTimeout(searchListTimer);
      searchListTimer = null;
    }

    const query = els.search.value;
    const filtered = filterNotes(query);
    const listEl = els.noteList;
    const prevScrollTop = listEl.scrollTop;
    const ae = document.activeElement;
    const wasListBtn = ae?.classList?.contains("note-item-btn") && listEl.contains(ae);
    const prevListId = wasListBtn ? ae.dataset.id : null;

    listEl.innerHTML = "";
    const frag = document.createDocumentFragment();
    for (const note of filtered) {
      const li = document.createElement("li");
      li.className = "note-item";
      li.setAttribute("role", "listitem");
      const btn = document.createElement("button");
      btn.type = "button";
      btn.className = "note-item-btn" + (note.id === activeId ? " active" : "");
      btn.dataset.id = note.id;
      const preview = document.createElement("div");
      preview.className = "note-item-preview";
      preview.textContent = listTitle(note);
      const meta = document.createElement("div");
      meta.className = "note-item-meta";
      meta.textContent = formatTime(note.updatedAt);
      btn.append(preview, meta);
      li.append(btn);
      frag.append(li);
    }
    listEl.append(frag);
    listEl.scrollTop = prevScrollTop;

    if (wasListBtn && prevListId) {
      const again = [...listEl.querySelectorAll(".note-item-btn")].find((b) => b.dataset.id === prevListId);
      if (again) focusNoteListButton(again);
      else applyListTabIndices();
    } else {
      applyListTabIndices();
    }

    const qTrim = query.trim();
    const multi = searchTokens(query).length > 1;
    let hint = "";
    if (notes.length === 0) {
      els.noteCount.textContent = "暂无笔记";
      return;
    }
    if (qTrim) {
      hint = `，显示 ${filtered.length} 条`;
      hint += multi ? "（多词须同时命中）" : "（标题命中优先）";
    }
    els.noteCount.textContent = `共 ${notes.length} 条${hint}`;
  }

  function scheduleSearchListRender() {
    clearTimeout(searchListTimer);
    searchListTimer = setTimeout(() => {
      searchListTimer = null;
      renderList();
    }, SEARCH_LIST_DEBOUNCE_MS);
  }

  function showEditor(show) {
    els.emptyState.classList.toggle("hidden", show);
    els.editorWrap.classList.toggle("hidden", !show);
  }

  function escapeHtml(s) {
    return String(s)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;");
  }

  function escapeAttr(s) {
    return String(s)
      .replace(/&/g, "&amp;")
      .replace(/"/g, "&quot;")
      .replace(/</g, "&lt;")
      .replace(/\n/g, " ");
  }

  function safeHref(url) {
    const u = String(url).trim();
    if (/^javascript:/i.test(u) || /^data:/i.test(u)) return null;
    if (/^https?:\/\//i.test(u)) return u;
    if (u.startsWith("/") && !u.startsWith("//")) return u;
    return null;
  }

  function safeImgSrc(url) {
    const u = String(url).trim();
    if (/^javascript:/i.test(u) || /^data:/i.test(u)) return null;
    if (/^https?:\/\//i.test(u)) return u;
    if (u.startsWith("/api/vault/") || u.startsWith("/api/media/")) return u;
    if (u.startsWith("/") && !u.startsWith("//")) return u;
    if (activeNoteDir && !u.includes("://") && !u.startsWith("//")) {
      const rel = u.replace(/^\.\//, "");
      if (rel.startsWith("/") || rel.includes("..")) return null;
      const segs = activeNoteDir.split("/").filter(Boolean).map(encodeURIComponent);
      const fileSegs = rel.split("/").filter(Boolean).map(encodeURIComponent);
      if (!fileSegs.length) return null;
      return "/api/vault/" + segs.join("/") + "/" + fileSegs.join("/");
    }
    return null;
  }

  function inlineFormat(raw) {
    const ph = [];
    function push(tag) {
      ph.push(tag);
      return "\uE000" + (ph.length - 1) + "\uE001";
    }
    let s = raw;
    s = s.replace(/!\[([^\]]*)\]\(([^)]+)\)/g, (_, alt, url) => {
      const src = safeImgSrc(url);
      if (!src) return escapeHtml("![" + alt + "](" + url + ")");
      return push('<img src="' + escapeAttr(src) + '" alt="' + escapeAttr(alt) + '" loading="lazy" />');
    });
    s = s.replace(/\[([^\]]+)\]\(([^)]+)\)/g, (_, text, url) => {
      const href = safeHref(url);
      if (!href) return escapeHtml("[" + text + "](" + url + ")");
      return push(
        '<a href="' +
          escapeAttr(href) +
          '" target="_blank" rel="noopener noreferrer">' +
          escapeHtml(text) +
          "</a>"
      );
    });
    s = s.replace(/`([^`]+)`/g, (_, code) => {
      return push("<code>" + escapeHtml(code) + "</code>");
    });
    s = s.replace(/\*\*([^*]+)\*\*/g, (_, t) => {
      return push("<strong>" + escapeHtml(t) + "</strong>");
    });
    s = s.replace(/\*([^*]+)\*/g, (_, t) => {
      return push("<em>" + escapeHtml(t) + "</em>");
    });
    s = escapeHtml(s);
    for (let k = 0; k < ph.length; k++) {
      s = s.replace("\uE000" + k + "\uE001", ph[k]);
    }
    return s;
  }

  function renderMarkdown(text) {
    if (!String(text).trim()) {
      return '<p class="md-empty">（无内容）</p>';
    }
    const parts = String(text).split(/(```[\s\S]*?```)/g);
    let html = "";
    for (const part of parts) {
      if (part.startsWith("```")) {
        const m = part.match(/^```(\w*)\n?([\s\S]*?)```$/);
        const code = m ? m[2] : part.replace(/^```/, "").replace(/```$/, "");
        html += "<pre><code>" + escapeHtml(code) + "</code></pre>";
        continue;
      }
      const lines = part.split("\n");
      const para = [];
      function flushPara() {
        if (!para.length) return;
        const body = inlineFormat(para.join("\n"));
        html += "<p>" + body.replace(/\n/g, "<br>") + "</p>";
        para.length = 0;
      }
      for (const line of lines) {
        const h = line.match(/^(#{1,6})\s+(.*)$/);
        if (h) {
          flushPara();
          const level = h[1].length;
          html += "<h" + level + ">" + inlineFormat(h[2]) + "</h" + level + ">";
          continue;
        }
        if (line.trim() === "") {
          flushPara();
          continue;
        }
        para.push(line);
      }
      flushPara();
    }
    return html;
  }

  function updatePreview() {
    els.preview.innerHTML = renderMarkdown(els.body.value);
  }

  function setViewMode(mode) {
    viewMode = mode;
    const edit = mode === "edit";
    els.tabEdit.classList.toggle("active", edit);
    els.tabPreview.classList.toggle("active", !edit);
    els.tabEdit.setAttribute("aria-selected", edit ? "true" : "false");
    els.tabPreview.setAttribute("aria-selected", edit ? "false" : "true");
    els.body.classList.toggle("hidden", !edit);
    els.preview.classList.toggle("hidden", edit);
    if (!edit) updatePreview();
  }

  function insertAtCursor(ta, text) {
    const start = ta.selectionStart;
    const end = ta.selectionEnd;
    const v = ta.value;
    ta.value = v.slice(0, start) + text + v.slice(end);
    const pos = start + text.length;
    ta.selectionStart = ta.selectionEnd = pos;
    ta.focus();
  }

  async function uploadImageFile(file) {
    if (!activeId) throw new Error("无活动笔记");
    const fd = new FormData();
    fd.append("note", activeId);
    fd.append("file", file, file.name || "image.png");
    const r = await fetch("/api/media", { method: "POST", body: fd });
    if (!r.ok) {
      let msg = r.statusText;
      try {
        const j = await r.json();
        if (j.error) msg = j.error;
      } catch {
        /* ignore */
      }
      throw new Error(msg);
    }
    const j = await r.json();
    if (!j.name) throw new Error("响应无效");
    return j.name;
  }

  async function insertImagesFromFiles(files) {
    if (!getActiveNote() || !files.length) return;
    for (const file of files) {
      if (!file.type.startsWith("image/")) continue;
      try {
        setSavedHint("上传图片…");
        const url = await uploadImageFile(file);
        if (viewMode === "preview") setViewMode("edit");
        insertAtCursor(els.body, "\n\n![](" + url + ")\n\n");
        scheduleSave();
      } catch {
        setSavedHint("图片上传失败");
        return;
      }
    }
    setSavedHint("");
  }

  function openNote(id, startInEdit) {
    const note = notes.find((n) => n.id === id);
    if (!note) return;
    activeId = id;
    activeNoteDir = note.dir || "";
    els.title.value = note.title;
    els.body.value = note.body;
    setViewMode(startInEdit ? "edit" : "preview");
    showEditor(true);
    els.title.focus();
    renderList();
    setSavedHint("");
  }

  /** @returns {Promise<boolean>} */
  async function flushEditorToStore() {
    const note = getActiveNote();
    if (!note) return true;
    const title = els.title.value;
    const body = els.body.value;
    try {
      const r = await fetch("/api/notes/" + encodeURIComponent(note.id), {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ title, body }),
      });
      if (!r.ok) {
        setSavedHint("保存失败");
        return false;
      }
      const updated = await r.json();
      const idx = notes.findIndex((n) => n.id === updated.id);
      if (idx >= 0) notes[idx] = updated;
      if (updated.dir) activeNoteDir = updated.dir;
      renderList();
      return true;
    } catch {
      setSavedHint("保存失败");
      return false;
    }
  }

  function flushEditorKeepalive() {
    const note = getActiveNote();
    if (!note) return;
    const title = els.title.value;
    const body = els.body.value;
    fetch("/api/notes/" + encodeURIComponent(note.id), {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ title, body }),
      keepalive: true,
    }).catch(() => {});
  }

  function scheduleSave() {
    if (saveTimer) clearTimeout(saveTimer);
    setSavingHint(true);
    saveTimer = setTimeout(async () => {
      const ok = await flushEditorToStore();
      setSavingHint(false);
      if (ok) {
        setSavedHint("已保存");
        if (hintTimer) clearTimeout(hintTimer);
        hintTimer = setTimeout(() => setSavedHint(""), 2000);
        if (viewMode === "preview") updatePreview();
      }
    }, 400);
  }

  function setSavingHint(on) {
    els.savedHint.classList.toggle("is-saving", on);
    if (on) els.savedHint.textContent = "保存中…";
  }

  function setSavedHint(text) {
    if (!els.savedHint.classList.contains("is-saving")) {
      els.savedHint.textContent = text;
    }
  }

  async function createNote() {
    const beforeId = activeId || "";
    const r = await fetch("/api/notes", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ title: "", body: "", beforeId }),
    });
    if (!r.ok) {
      setSavedHint("创建失败");
      return;
    }
    const note = await r.json();
    try {
      await refreshNotes();
    } catch {
      notes.push(note);
    }
    openNote(note.id, true);
  }

  async function deleteActiveNote() {
    const note = getActiveNote();
    if (!note) return;
    const title = listTitle(note);
    if (!confirm(`确定删除「${title}」？`)) return;
    const id = activeId;
    const r = await fetch("/api/notes/" + encodeURIComponent(id), { method: "DELETE" });
    if (!r.ok && r.status !== 204) {
      setSavedHint("删除失败");
      return;
    }
    notes = notes.filter((n) => n.id !== id);
    activeId = null;
    activeNoteDir = "";
    els.title.value = "";
    els.body.value = "";
    els.preview.innerHTML = "";
    showEditor(false);
    renderList();
  }

  function clearPendingSave() {
    if (saveTimer) {
      clearTimeout(saveTimer);
      saveTimer = null;
    }
  }

  els.tabEdit.addEventListener("click", () => setViewMode("edit"));
  els.tabPreview.addEventListener("click", () => setViewMode("preview"));

  els.body.addEventListener("paste", async (e) => {
    const items = e.clipboardData?.items;
    if (!items || !getActiveNote()) return;
    for (const item of items) {
      if (item.kind === "file" && item.type.startsWith("image/")) {
        e.preventDefault();
        const file = item.getAsFile();
        if (!file) continue;
        try {
          setSavedHint("上传图片…");
          const url = await uploadImageFile(file);
          if (viewMode === "preview") setViewMode("edit");
          insertAtCursor(els.body, "\n\n![](" + url + ")\n\n");
          scheduleSave();
          setSavedHint("");
        } catch {
          setSavedHint("图片上传失败");
        }
        break;
      }
    }
  });

  const main = els.editorMain;
  if (main) {
    main.addEventListener("dragover", (e) => {
      if (!getActiveNote()) return;
      e.preventDefault();
      main.classList.add("drop-target");
    });
    main.addEventListener("dragleave", () => {
      main.classList.remove("drop-target");
    });
    document.addEventListener("dragend", () => {
      main.classList.remove("drop-target");
    });
    main.addEventListener("drop", async (e) => {
      if (!getActiveNote()) return;
      e.preventDefault();
      main.classList.remove("drop-target");
      const files = Array.from(e.dataTransfer?.files || []).filter((f) => f.type.startsWith("image/"));
      await insertImagesFromFiles(files);
    });
  }

  els.noteList.addEventListener("click", async (e) => {
    const btn = e.target.closest(".note-item-btn");
    if (!btn || !btn.dataset.id) return;
    if (btn.dataset.id === activeId) return;
    clearPendingSave();
    setSavingHint(false);
    await flushEditorToStore();
    openNote(btn.dataset.id);
  });

  els.btnNew.addEventListener("click", async () => {
    clearPendingSave();
    setSavingHint(false);
    await flushEditorToStore();
    await createNote();
  });

  els.btnDelete.addEventListener("click", deleteActiveNote);

  els.btnTheme.addEventListener("click", toggleTheme);

  els.btnSidebarCollapse?.addEventListener("click", collapseSidebar);
  els.btnSidebarExpand?.addEventListener("click", expandSidebar);

  els.search.addEventListener("input", scheduleSearchListRender);
  els.search.addEventListener("keydown", (e) => {
    if (e.key === "ArrowDown") {
      const first = els.noteList.querySelector(".note-item-btn");
      if (!first) return;
      e.preventDefault();
      focusNoteListButton(first);
      return;
    }
    if (e.key === "Escape" && els.search.value) {
      e.preventDefault();
      els.search.value = "";
      renderList();
    }
  });

  els.noteList.addEventListener("keydown", (e) => {
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
    const items = [...els.noteList.querySelectorAll(".note-item-btn")];
    if (!items.length) return;
    const i = items.indexOf(document.activeElement);
    if (i < 0) return;
    e.preventDefault();
    if (e.key === "ArrowDown") {
      if (i < items.length - 1) focusNoteListButton(items[i + 1]);
    } else if (i === 0) {
      els.search.focus();
    } else {
      focusNoteListButton(items[i - 1]);
    }
  });

  document.addEventListener("keydown", (e) => {
    if (e.key !== "/" || e.ctrlKey || e.metaKey || e.altKey) return;
    const t = e.target;
    const tag = t && t.tagName;
    if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" || (t && t.isContentEditable)) return;
    e.preventDefault();
    if (els.app?.classList.contains("sidebar-collapsed")) expandSidebar();
    els.search.focus();
  });

  ["input", "change"].forEach((ev) => {
    els.title.addEventListener(ev, scheduleSave);
    els.body.addEventListener(ev, () => {
      scheduleSave();
    });
  });

  window.addEventListener("beforeunload", () => {
    clearPendingSave();
    flushEditorKeepalive();
  });

  loadTheme();
  loadSidebarState();
  refreshNotes()
    .then(() => {
      renderList();
      showEditor(false);
    })
    .catch(() => {
      notes = [];
      renderList();
      els.noteCount.textContent = "无法连接服务器，请先运行笔记程序";
      showEditor(false);
    });
})();
