(function () {
  "use strict";

  try {
    const t = localStorage.getItem("local-notes-theme");
    if (t === "dark" || t === "light") document.documentElement.setAttribute("data-theme", t);
  } catch {
    /* ignore */
  }

  const root = document.getElementById("public-root");
  const LIST_LIMIT = 24;

  let nextCursor = "";
  let hasMore = false;
  let totalAll = 0;
  let loadedCount = 0;
  let searchDebounce = 0;

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
    if (u.startsWith("/api/public/")) return u;
    if (u.startsWith("/") && !u.startsWith("//")) return u;
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

  /** 公共页渲染失败时的简易 HTML（无 GFM） */
  function renderMarkdownFallback(text) {
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

  async function renderMarkdownServer(text) {
    const r = await fetch("/api/public/render-md", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ text: text || "" }),
      credentials: "same-origin",
    });
    if (!r.ok) throw new Error(String(r.status));
    return r.json();
  }

  function formatTime(ms) {
    if (!ms) return "";
    try {
      return new Date(ms).toLocaleString("zh-CN", { dateStyle: "medium", timeStyle: "short" });
    } catch {
      return "";
    }
  }

  function excerptFromBody(body, max) {
    const t = String(body || "")
      .replace(/```[\s\S]*?```/g, " ")
      .replace(/!\[[^\]]*\]\([^)]+\)/g, " ")
      .replace(/\[([^\]]+)\]\([^)]+\)/g, "$1")
      .replace(/[#>*`_]/g, " ")
      .replace(/\s+/g, " ")
      .trim();
    if (t.length <= max) return t;
    return t.slice(0, max) + "…";
  }

  function renderListItemsLisOnly(posts) {
    if (!posts || !posts.length) return "";
    const h = [];
    for (const p of posts) {
      const title = escapeHtml(p.title || "无标题");
      const url = escapeAttr(p.detailUrl || "#");
      const by = escapeHtml(p.authorLabel || "");
      const when = escapeHtml(formatTime(p.updatedAt));
      const ex = escapeHtml(excerptFromBody(p.body, 220));
      h.push('<li class="public-post-list-item">');
      h.push('<article class="public-post-card">');
      h.push('<h2 class="public-post-card-title"><a href="' + url + '">' + title + "</a></h2>");
      h.push('<p class="public-post-byline">' + by + " · " + when + "</p>");
      h.push('<p class="public-post-excerpt">' + ex + "</p>");
      h.push("</article></li>");
    }
    return h.join("");
  }

  function resultMetaHtml(qRaw) {
    const q = String(qRaw || "").trim();
    if (!totalAll) {
      if (q) {
        return (
          '<p class="public-result-meta" id="public-result-meta">没有找到匹配「<strong>' +
          escapeHtml(q) +
          "</strong>」的公开手记。</p>"
        );
      }
      return '<p class="public-result-meta" id="public-result-meta">暂无可展示的公开笔记。</p>';
    }
    let line =
      '<p class="public-result-meta" id="public-result-meta">共 <strong>' +
      totalAll +
      "</strong> 篇";
    if (q) {
      line += ' · 关键词「<strong>' + escapeHtml(q) + "</strong>」";
    }
    line += ' · 已加载 <strong>' + loadedCount + "</strong> 篇";
    if (hasMore) {
      line += " · 可继续加载更多";
    } else {
      line += " · 已全部加载";
    }
    line += "</p>";
    return line;
  }

  function refreshListMeta(q) {
    const el = document.getElementById("public-result-meta");
    if (el) el.outerHTML = resultMetaHtml(q);
  }

  function updateLoadMoreButton() {
    const btn = document.getElementById("public-load-more");
    if (btn) {
      btn.hidden = !hasMore || totalAll === 0;
      btn.disabled = false;
    }
  }

  async function fetchListJson(q, cursor) {
    const params = new URLSearchParams({ limit: String(LIST_LIMIT) });
    const qv = String(q || "").trim();
    if (qv) params.set("q", qv);
    if (cursor) params.set("cursor", cursor);
    const r = await fetch("/api/public/posts?" + params.toString(), { credentials: "same-origin" });
    if (!r.ok) throw new Error(String(r.status));
    return r.json();
  }

  async function fetchDetailJson(provider, login, dir) {
    const params = new URLSearchParams({ provider, login, dir });
    const r = await fetch("/api/public/post?" + params.toString(), { credentials: "same-origin" });
    if (!r.ok) throw new Error(String(r.status));
    return r.json();
  }

  async function loadFirstPage(q) {
    const j = await fetchListJson(q, null);
    const items = Array.isArray(j.items) ? j.items : [];
    nextCursor = j.nextCursor || "";
    hasMore = !!j.hasMore;
    totalAll = typeof j.total === "number" ? j.total : items.length;
    loadedCount = items.length;

    const mount = document.getElementById("public-list-mount");
    if (!mount) return;
    let inner = resultMetaHtml(q);
    if (items.length) {
      inner +=
        '<ul class="public-post-list" id="public-post-list">' + renderListItemsLisOnly(items) + "</ul>";
    } else if (!String(q || "").trim() && totalAll === 0) {
      inner += '<p class="public-empty-hint">尚无作者勾选「在公共主页展示」。</p>';
    } else if (String(q || "").trim() && totalAll === 0) {
      inner += '<p class="public-empty-hint">试试缩短关键词，或<a href="/public">清空搜索</a>。</p>';
    }
    mount.innerHTML = inner;
    document.getElementById("public-load-more-wrap")?.classList.remove("hidden");
    updateLoadMoreButton();
  }

  async function loadMorePage(q) {
    if (!hasMore || !nextCursor) return;
    const btn = document.getElementById("public-load-more");
    if (btn) btn.disabled = true;
    try {
      const j = await fetchListJson(q, nextCursor);
      const items = Array.isArray(j.items) ? j.items : [];
      nextCursor = j.nextCursor || "";
      hasMore = !!j.hasMore;
      loadedCount += items.length;
      const ul = document.getElementById("public-post-list");
      if (ul && items.length) {
        ul.insertAdjacentHTML("beforeend", renderListItemsLisOnly(items));
      }
      refreshListMeta(q);
      updateLoadMoreButton();
    } finally {
      if (btn) btn.disabled = false;
    }
  }

  function syncSearchUrl(q) {
    const params = new URLSearchParams(location.search);
    const t = String(q || "").trim();
    if (t) params.set("q", t);
    else params.delete("q");
    const qs = params.toString();
    const next = "/public" + (qs ? "?" + qs : "");
    if (next !== location.pathname + location.search) {
      history.replaceState(null, "", next);
    }
  }

  function renderListShell(q) {
    const qAttr = escapeAttr(q);
    root.innerHTML =
      '<div class="public-shell-inner">' +
      '<header class="public-hero">' +
      '<div class="public-hero-top">' +
      '<h1 class="public-blog-title">公共手记</h1>' +
      '<nav class="public-blog-nav public-hero-top-nav" aria-label="站点导航"><a href="/">我的笔记</a></nav>' +
      "</div>" +
      '<p class="public-blog-sub">数据由服务端分页与搜索；列表仅返回摘要正文，点标题进入全文。多词用空格连接表示同时包含。</p>' +
      '<div class="public-search-wrap">' +
      '<label class="sr-only" for="public-search">搜索公开手记</label>' +
      '<input type="search" id="public-search" class="public-search-input" placeholder="搜索标题、正文、作者…" autocomplete="off" value="' +
      qAttr +
      '" />' +
      "</div>" +
      "</header>" +
      '<div class="public-list-section">' +
      '<div id="public-list-mount"><p class="public-loading">加载中…</p></div>' +
      '<div class="public-load-more-wrap hidden" id="public-load-more-wrap">' +
      '<button type="button" class="btn btn-primary public-load-more-btn" id="public-load-more" hidden>加载更多</button>' +
      "</div>" +
      "</div>" +
      "</div>";
  }

  async function renderDetail(post) {
    const title = escapeHtml(post.title || "无标题");
    const by = escapeHtml(post.authorLabel || "");
    const when = escapeHtml(formatTime(post.updatedAt));
    const h = [];
    h.push('<div class="public-shell-inner public-shell-inner-detail">');
    h.push('<header class="public-detail-header">');
    h.push('<div class="public-detail-toolbar">');
    h.push('<nav class="public-blog-nav public-detail-nav" aria-label="导航">');
    h.push('<a href="/public">手记列表</a>');
    h.push('<span class="public-nav-sep" aria-hidden="true">·</span>');
    h.push('<a href="/">我的笔记</a>');
    h.push("</nav>");
    h.push(
      '<form class="public-search-form" action="/public" method="get" role="search">' +
        '<label class="sr-only" for="public-detail-search">搜索</label>' +
        '<input type="search" id="public-detail-search" name="q" class="public-search-input public-search-input-compact" placeholder="搜索全部公开手记…" />' +
        '<button type="submit" class="btn btn-primary public-search-submit">搜索</button>' +
        "</form>"
    );
    h.push("</div>");
    h.push('<div class="public-detail-hero-card">');
    h.push('<h1 class="public-post-detail-title">' + title + "</h1>");
    h.push('<p class="public-post-byline">' + by + " · " + when + "</p>");
    h.push("</div>");
    h.push("</header>");
    h.push('<article class="markdown-body public-post-body" id="public-article-body"><p class="public-loading">渲染中…</p></article>');
    h.push("</div>");
    root.innerHTML = h.join("");
    const bodyEl = document.getElementById("public-article-body");
    try {
      const j = await renderMarkdownServer(post.body || "");
      const html = typeof j.html === "string" ? j.html : "";
      if (bodyEl) {
        bodyEl.innerHTML =
          html.trim() === "" && !String(post.body || "").trim()
            ? '<p class="md-empty">（无内容）</p>'
            : html || '<p class="md-empty">（无内容）</p>';
      }
    } catch {
      if (bodyEl) bodyEl.innerHTML = renderMarkdownFallback(post.body || "");
    }
  }

  function onSearchInput(ev) {
    const el = ev.target;
    if (!el || el.id !== "public-search") return;
    clearTimeout(searchDebounce);
    searchDebounce = setTimeout(async () => {
      const q = el.value;
      syncSearchUrl(q);
      try {
        await loadFirstPage(q);
      } catch {
        const mount = document.getElementById("public-list-mount");
        if (mount) mount.innerHTML = '<p class="public-error">加载失败，请稍后重试。</p>';
      }
    }, 220);
  }

  document.addEventListener("input", onSearchInput);

  document.addEventListener("click", (e) => {
    const t = e.target;
    if (t && t.id === "public-load-more") {
      const inp = document.getElementById("public-search");
      const q = inp ? inp.value : "";
      loadMorePage(q);
    }
  });

  window.addEventListener("popstate", async () => {
    const path = location.pathname.replace(/\/$/, "") || "/public";
    if (path !== "/public") return;
    const q = new URLSearchParams(location.search).get("q") || "";
    const inp = document.getElementById("public-search");
    if (inp) inp.value = q;
    try {
      await loadFirstPage(q);
    } catch {
      /* ignore */
    }
  });

  async function main() {
    if (!root) return;
    const path = location.pathname.replace(/\/$/, "") || "/public";

    if (path.startsWith("/public/post/")) {
      const rest = path.slice("/public/post/".length);
      const parts = rest.split("/").filter(Boolean);
      if (parts.length >= 5) {
        const provider = parts[0];
        const login = parts[1];
        const dir = parts.slice(2).join("/");
        try {
          const detail = await fetchDetailJson(provider, login, dir);
          await renderDetail(detail);
          document.title = (detail.title || "手记") + " · 公共手记";
        } catch {
          root.innerHTML =
            '<div class="public-shell-inner"><p class="public-error">未找到该手记或已不再公开。</p><p class="public-blog-nav"><a href="/public">返回列表</a></p></div>';
          document.title = "手记未找到";
        }
        return;
      }
    }

    if (path === "/public") {
      const q = new URLSearchParams(location.search).get("q") || "";
      renderListShell(q);
      try {
        await loadFirstPage(q);
      } catch {
        const mount = document.getElementById("public-list-mount");
        if (mount) mount.innerHTML = '<p class="public-error">无法加载公共手记列表，请稍后再试。</p>';
      }
      document.title = "公共手记";
      return;
    }

    root.innerHTML =
      '<div class="public-shell-inner"><p class="public-error">页面不存在。</p><p class="public-blog-nav"><a href="/public">返回</a></p></div>';
  }

  main();
})();
