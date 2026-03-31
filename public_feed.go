package main

import (
	"encoding/base64"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// PublicPostItem 公共主页一条（无需登录）。
type PublicPostItem struct {
	Provider    string `json:"provider"`
	Login       string `json:"login"`
	AuthorLabel string `json:"authorLabel"`
	ID          string `json:"id"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	Dir         string `json:"dir"`
	UpdatedAt   int64  `json:"updatedAt"`
	DetailURL   string `json:"detailUrl"`
}

var imgMdRE = regexp.MustCompile(`!\[([^\]]*)\]\(([^)]+)\)`)

const (
	publicListDefaultLimit = 24
	publicListMaxLimit     = 100
	publicListPreviewRunes = 420
	publicCacheTTL         = 45 * time.Second
)

var (
	publicCacheMu     sync.RWMutex
	publicCacheVault  string
	publicCacheAt     time.Time
	publicCachePosts  []PublicPostItem
	publicCacheErr    error
)

// PublicPostsListResponse 分页列表（JSON）。
type PublicPostsListResponse struct {
	Items      []PublicPostItem `json:"items"`
	NextCursor string           `json:"nextCursor,omitempty"`
	HasMore    bool             `json:"hasMore"`
	Total      int              `json:"total"`
}

func computeAuthorLabel(provider, login string) string {
	login = strings.TrimSpace(login)
	p := strings.TrimSpace(strings.ToLower(provider))
	pLabel := "GitHub"
	if p == "gitee" {
		pLabel = "Gitee"
	}
	return pLabel + " @" + login
}

func publicAssetPathURL(provider, login, rest string) string {
	rest = strings.TrimPrefix(strings.TrimSpace(rest), "/")
	return "/api/public/asset/" + url.PathEscape(provider) + "/" + url.PathEscape(login) + "/" + rest
}

func rewritePublicMarkdownImages(body, provider, login, dirRel string) string {
	return imgMdRE.ReplaceAllStringFunc(body, func(m string) string {
		sm := imgMdRE.FindStringSubmatch(m)
		if len(sm) < 3 {
			return m
		}
		alt, raw := sm[1], strings.TrimSpace(sm[2])
		if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
			return m
		}
		if strings.HasPrefix(raw, "/api/public/") {
			return m
		}
		if strings.HasPrefix(raw, "/api/vault/") {
			rest := strings.TrimPrefix(raw, "/api/vault/")
			rest = strings.TrimPrefix(rest, "/")
			return "![" + alt + "](" + publicAssetPathURL(provider, login, rest) + ")"
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(raw, "./"), "/")
		if rel == "" || strings.Contains(rel, "..") {
			return m
		}
		return "![" + alt + "](" + publicAssetPathURL(provider, login, dirRel+"/"+rel) + ")"
	})
}

// parseUsersDirNotePath 解析 users 目录下相对路径（不含 note.md），得到 provider、磁盘上的 login 目录名、笔记目录。
func parseUsersDirNotePath(dirRel string) (provider, login string, noteParts []string, ok bool) {
	dirRel = filepath.ToSlash(dirRel)
	parts := strings.Split(dirRel, "/")
	if len(parts) >= 6 && (parts[0] == "github" || parts[0] == "gitee") {
		noteParts = parts[2:6]
		if !isNoteLayoutDir(noteParts) {
			return "", "", nil, false
		}
		return parts[0], parts[1], noteParts, true
	}
	if len(parts) == 5 {
		noteParts = parts[1:5]
		if !isNoteLayoutDir(noteParts) {
			return "", "", nil, false
		}
		return "github", parts[0], noteParts, true
	}
	if len(parts) == 4 && (parts[0] == "github" || parts[0] == "gitee") {
		noteParts = parts[2:4]
		if !isNoteLayoutDir(noteParts) {
			return "", "", nil, false
		}
		return parts[0], parts[1], noteParts, true
	}
	if len(parts) == 3 {
		noteParts = parts[1:3]
		if !isNoteLayoutDir(noteParts) {
			return "", "", nil, false
		}
		return "github", parts[0], noteParts, true
	}
	return "", "", nil, false
}

// collectPublicPosts 扫描 vaultBase/users 下所有已勾选公开的笔记。
func collectPublicPosts(vaultBase string) ([]PublicPostItem, error) {
	usersDir := filepath.Join(vaultBase, "users")
	st, err := os.Stat(usersDir)
	if err != nil || !st.IsDir() {
		return []PublicPostItem{}, nil
	}
	out := make([]PublicPostItem, 0)
	err = filepath.WalkDir(usersDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Base(path) != "note.md" {
			return nil
		}
		parent, e := filepath.Rel(usersDir, filepath.Dir(path))
		if e != nil {
			return nil
		}
		provider, login, noteParts, ok := parseUsersDirNotePath(parent)
		if !ok {
			return nil
		}
		dirRel := strings.Join(noteParts, "/")
		raw, e := os.ReadFile(path)
		if e != nil {
			return nil
		}
		info, _ := d.Info()
		mt := time.Now()
		if info != nil {
			mt = info.ModTime()
		}
		note, e := parseNoteMD(raw, noteLayoutLeafID(noteParts), mt)
		if e != nil || !note.Public {
			return nil
		}
		body := rewritePublicMarkdownImages(note.Body, provider, login, dirRel)
		detail := "/public/post/" + provider + "/" + login + "/" + dirRel
		out = append(out, PublicPostItem{
			Provider:    provider,
			Login:       login,
			AuthorLabel: computeAuthorLabel(provider, login),
			ID:          note.ID,
			Title:       note.Title,
			Body:        body,
			Dir:         dirRel,
			UpdatedAt:   note.UpdatedAt,
			DetailURL:   detail,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt != out[j].UpdatedAt {
			return out[i].UpdatedAt > out[j].UpdatedAt
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

func loadPublicPostsCached(vaultBase string) ([]PublicPostItem, error) {
	publicCacheMu.Lock()
	defer publicCacheMu.Unlock()
	if publicCacheVault == vaultBase && publicCachePosts != nil && time.Since(publicCacheAt) < publicCacheTTL {
		out := make([]PublicPostItem, len(publicCachePosts))
		copy(out, publicCachePosts)
		return out, nil
	}
	posts, err := collectPublicPosts(vaultBase)
	if err != nil {
		return nil, err
	}
	publicCacheVault = vaultBase
	publicCacheAt = time.Now()
	publicCachePosts = make([]PublicPostItem, len(posts))
	copy(publicCachePosts, posts)
	return posts, nil
}

// invalidatePublicPostCache 在本地笔记变更后调用，使公开列表缓存立即失效。
func invalidatePublicPostCache() {
	publicCacheMu.Lock()
	defer publicCacheMu.Unlock()
	publicCachePosts = nil
	publicCacheAt = time.Time{}
}

func plainTextForSearchGo(body string) string {
	s := body
	s = regexp.MustCompile("(?s)```.*?```").ReplaceAllString(s, " ")
	s = imgMdRE.ReplaceAllString(s, " ")
	s = regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`).ReplaceAllString(s, "$1")
	s = strings.NewReplacer("#", " ", "*", " ", ">", " ", "`", " ", "_", " ").Replace(s)
	s = regexp.MustCompile(`\s+`).ReplaceAllString(s, " ")
	return strings.TrimSpace(strings.ToLower(s))
}

func filterPublicByQuery(all []PublicPostItem, q string) []PublicPostItem {
	q = strings.TrimSpace(q)
	if q == "" {
		out := make([]PublicPostItem, len(all))
		copy(out, all)
		return out
	}
	toks := strings.Fields(strings.ToLower(q))
	if len(toks) == 0 {
		out := make([]PublicPostItem, len(all))
		copy(out, all)
		return out
	}
	var out []PublicPostItem
	for _, p := range all {
		title := strings.ToLower(p.Title)
		body := plainTextForSearchGo(p.Body)
		auth := strings.ToLower(p.AuthorLabel)
		ok := true
		for _, t := range toks {
			if !strings.Contains(title, t) && !strings.Contains(body, t) && !strings.Contains(auth, t) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, p)
		}
	}
	return out
}

func truncateBodyRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// strictlyAfterInDescList：a 在「新→旧」列表中排在 b 之后（更旧）。
func strictlyAfterInDescList(a, b PublicPostItem) bool {
	if a.UpdatedAt < b.UpdatedAt {
		return true
	}
	if a.UpdatedAt > b.UpdatedAt {
		return false
	}
	return a.ID < b.ID
}

func encodePublicCursor(p PublicPostItem) string {
	raw := strconv.FormatInt(p.UpdatedAt, 10) + "\t" + p.ID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodePublicCursor(s string) (PublicPostItem, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return PublicPostItem{}, false
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return PublicPostItem{}, false
	}
	parts := strings.SplitN(string(b), "\t", 2)
	if len(parts) != 2 {
		return PublicPostItem{}, false
	}
	at, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || !safeNoteID(parts[1]) {
		return PublicPostItem{}, false
	}
	return PublicPostItem{UpdatedAt: at, ID: parts[1]}, true
}

func findPageStart(filtered []PublicPostItem, cursorStr string) int {
	if cursorStr == "" {
		return 0
	}
	cur, ok := decodePublicCursor(cursorStr)
	if !ok {
		return 0
	}
	for i := range filtered {
		if strictlyAfterInDescList(filtered[i], cur) {
			return i
		}
	}
	return len(filtered)
}

func isPublicImageExt(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg":
		return true
	default:
		return false
	}
}

func loadPublicPostDetail(vaultBase, provider, login, dirRel string) (PublicPostItem, error) {
	provider = strings.TrimSpace(strings.ToLower(provider))
	if provider != "github" && provider != "gitee" {
		return PublicPostItem{}, os.ErrNotExist
	}
	login = strings.TrimSpace(login)
	if login == "" || strings.Contains(login, "..") {
		return PublicPostItem{}, os.ErrNotExist
	}
	dirRel = filepath.ToSlash(filepath.Clean(dirRel))
	if dirRel == "." || strings.Contains(dirRel, "..") {
		return PublicPostItem{}, os.ErrNotExist
	}
	parts := strings.Split(dirRel, "/")
	if !isNoteLayoutDir(parts) {
		return PublicPostItem{}, os.ErrNotExist
	}
	notePath := filepath.Join(vaultBase, "users", provider, login, filepath.FromSlash(dirRel), "note.md")
	raw, err := os.ReadFile(notePath)
	if err != nil {
		return PublicPostItem{}, err
	}
	info, err := os.Stat(notePath)
	if err != nil {
		return PublicPostItem{}, err
	}
	note, err := parseNoteMD(raw, noteLayoutLeafID(parts), info.ModTime())
	if err != nil || !note.Public {
		return PublicPostItem{}, os.ErrNotExist
	}
	body := rewritePublicMarkdownImages(note.Body, provider, login, dirRel)
	return PublicPostItem{
		Provider:    provider,
		Login:       login,
		AuthorLabel: computeAuthorLabel(provider, login),
		ID:          note.ID,
		Title:       note.Title,
		Body:        body,
		Dir:         dirRel,
		UpdatedAt:   note.UpdatedAt,
		DetailURL:   "/public/post/" + provider + "/" + login + "/" + dirRel,
	}, nil
}

func registerPublicAPI(r *gin.Engine, vaultBase string) {
	r.GET("/api/public/posts", func(c *gin.Context) {
		limit := publicListDefaultLimit
		if ls := strings.TrimSpace(c.Query("limit")); ls != "" {
			if v, err := strconv.Atoi(ls); err == nil && v > 0 {
				limit = v
				if limit > publicListMaxLimit {
					limit = publicListMaxLimit
				}
			}
		}
		q := strings.TrimSpace(c.Query("q"))
		cursor := strings.TrimSpace(c.Query("cursor"))

		all, err := loadPublicPostsCached(vaultBase)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		filtered := filterPublicByQuery(all, q)
		total := len(filtered)
		start := findPageStart(filtered, cursor)
		if start > len(filtered) {
			start = len(filtered)
		}
		end := start + limit
		if end > len(filtered) {
			end = len(filtered)
		}
		slice := filtered[start:end]
		resp := PublicPostsListResponse{Total: total, Items: make([]PublicPostItem, 0, len(slice))}
		for _, p := range slice {
			p2 := p
			p2.Body = truncateBodyRunes(p2.Body, publicListPreviewRunes)
			resp.Items = append(resp.Items, p2)
		}
		if end < len(filtered) && len(slice) > 0 {
			resp.NextCursor = encodePublicCursor(slice[len(slice)-1])
			resp.HasMore = true
		}
		c.JSON(http.StatusOK, resp)
	})

	r.GET("/api/public/post", func(c *gin.Context) {
		provider := strings.TrimSpace(c.Query("provider"))
		login := strings.TrimSpace(c.Query("login"))
		dir := strings.TrimSpace(c.Query("dir"))
		if provider == "" || login == "" || dir == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "需要 query 参数: provider, login, dir"})
			return
		}
		item, err := loadPublicPostDetail(vaultBase, provider, login, dir)
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		c.JSON(http.StatusOK, item)
	})

	r.GET("/api/public/asset/:provider/:login/*filepath", func(c *gin.Context) {
		provider := strings.TrimSpace(strings.ToLower(c.Param("provider")))
		if provider != "github" && provider != "gitee" {
			c.Status(http.StatusNotFound)
			return
		}
		login := strings.TrimSpace(c.Param("login"))
		if login == "" || strings.Contains(login, "..") {
			c.Status(http.StatusNotFound)
			return
		}
		fp := strings.TrimPrefix(c.Param("filepath"), "/")
		fp = filepath.ToSlash(filepath.Clean(fp))
		if fp == "." || fp == "" || strings.HasPrefix(fp, "..") || strings.Contains(fp, "..") {
			c.Status(http.StatusNotFound)
			return
		}
		if !isPublicImageExt(fp) {
			c.Status(http.StatusNotFound)
			return
		}
		parts := strings.Split(fp, "/")
		var noteDir string
		var noteLeafID string
		switch {
		case len(parts) >= 5 && isNoteLayoutDir(parts[:4]):
			noteDir = strings.Join(parts[:4], "/")
			noteLeafID = noteLayoutLeafID(parts[:4])
		case len(parts) >= 4 && isNoteLayoutDir(parts[:3]):
			noteDir = strings.Join(parts[:3], "/")
			noteLeafID = noteLayoutLeafID(parts[:3])
		case len(parts) >= 3 && isNoteLayoutDir(parts[:2]):
			noteDir = strings.Join(parts[:2], "/")
			noteLeafID = noteLayoutLeafID(parts[:2])
		default:
			c.Status(http.StatusNotFound)
			return
		}
		notePath := filepath.Join(vaultBase, "users", provider, login, filepath.FromSlash(noteDir), "note.md")
		raw, err := os.ReadFile(notePath)
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		info, err := os.Stat(notePath)
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		note, err := parseNoteMD(raw, noteLeafID, info.ModTime())
		if err != nil || !note.Public {
			c.Status(http.StatusNotFound)
			return
		}
		abs := filepath.Join(vaultBase, "users", provider, login, filepath.FromSlash(fp))
		absClean, err := filepath.Abs(abs)
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		baseDir := filepath.Join(vaultBase, "users", provider, login)
		baseAbs, err := filepath.Abs(baseDir)
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		sep := string(os.PathSeparator)
		if absClean != baseAbs && !strings.HasPrefix(absClean+sep, baseAbs+sep) {
			c.Status(http.StatusNotFound)
			return
		}
		st, err := os.Stat(absClean)
		if err != nil || st.IsDir() {
			c.Status(http.StatusNotFound)
			return
		}
		data, err := os.ReadFile(absClean)
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		ct, _, ok := detectImageType(data)
		if !ok {
			ct = http.DetectContentType(data)
		}
		c.Header("Cache-Control", "public, max-age=3600")
		c.Data(http.StatusOK, ct, data)
	})
}

func servePublicPage(webRoot fs.FS) gin.HandlerFunc {
	return func(c *gin.Context) {
		b, err := fs.ReadFile(webRoot, "public.html")
		if err != nil {
			c.String(http.StatusInternalServerError, "无法读取页面")
			return
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", b)
	}
}

func registerPublicWeb(r *gin.Engine, webRoot fs.FS) {
	h := servePublicPage(webRoot)
	r.GET("/public", h)
	r.GET("/public/", h)
	r.GET("/public/post/*rest", h)
	r.GET("/public.js", func(c *gin.Context) {
		b, err := fs.ReadFile(webRoot, "public.js")
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		c.Data(http.StatusOK, "application/javascript; charset=utf-8", b)
	})
}
