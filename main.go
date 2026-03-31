package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/kardianos/service"
)

//go:embed web
var embeddedWeb embed.FS

//go:embed web/favicon.svg
var faviconSVG []byte

func newNoteID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("n_%d", time.Now().UnixNano())
	}
	return "n_" + hex.EncodeToString(b)
}

const maxImageUpload = 32 << 20

func mapImageExt(ct string) (ext string, ok bool) {
	switch strings.Split(ct, ";")[0] {
	case "image/png":
		return ".png", true
	case "image/jpeg":
		return ".jpg", true
	case "image/gif":
		return ".gif", true
	case "image/webp":
		return ".webp", true
	case "image/svg+xml":
		return ".svg", true
	default:
		return "", false
	}
}

func detectImageType(data []byte) (ext string, contentType string, ok bool) {
	t := strings.TrimSpace(string(data))
	if len(t) > 0 && bytes.Contains(data, []byte("<svg")) &&
		(strings.HasPrefix(strings.ToLower(t), "<svg") || strings.HasPrefix(strings.ToLower(t), "<?xml")) {
		return ".svg", "image/svg+xml", true
	}
	ct := http.DetectContentType(data)
	ext, ok = mapImageExt(ct)
	if ok {
		return ext, strings.Split(ct, ";")[0], true
	}
	return "", "", false
}

func defaultVaultRoot() string {
	exe, err := os.Executable()
	if err != nil {
		wd, _ := os.Getwd()
		return filepath.Join(wd, "notes-vault")
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		resolved = exe
	}
	lower := strings.ToLower(resolved)
	if strings.Contains(lower, "go-build") {
		wd, _ := os.Getwd()
		return filepath.Join(wd, "notes-vault")
	}
	return filepath.Join(filepath.Dir(resolved), "notes-vault")
}

func resolveVaultRootFlag(flagValue string) string {
	if flagValue != "" {
		if filepath.IsAbs(flagValue) {
			return filepath.Clean(flagValue)
		}
		wd, _ := os.Getwd()
		return filepath.Clean(filepath.Join(wd, flagValue))
	}
	return defaultVaultRoot()
}

// computeVaultRoot：若 data 路径指向已有的 .json 文件，则仓库目录为该文件同级的 notes-vault，并返回需迁移的 json 路径。
func computeVaultRoot(dataFlag string) (vaultRoot string, legacyJSON string) {
	if dataFlag != "" && strings.HasSuffix(strings.ToLower(dataFlag), ".json") {
		if st, err := os.Stat(dataFlag); err == nil && !st.IsDir() {
			return filepath.Join(filepath.Dir(dataFlag), "notes-vault"), dataFlag
		}
	}
	return resolveVaultRootFlag(dataFlag), ""
}

func tryMigrateLegacy(vaultRoot, explicitLegacy string, vaultPassphrase string) {
	if vaultHasAnyNote(vaultRoot) {
		return
	}
	candidates := make([]string, 0, 2)
	if explicitLegacy != "" {
		candidates = append(candidates, explicitLegacy)
	}
	if filepath.Base(vaultRoot) == "notes-vault" {
		candidates = append(candidates, filepath.Join(filepath.Dir(vaultRoot), "notes-data.json"))
	}
	for _, lp := range candidates {
		if lp == "" {
			continue
		}
		if _, err := os.Stat(lp); err != nil {
			continue
		}
		if err := migrateLegacyJSON(vaultRoot, lp, vaultPassphrase); err != nil {
			log.Printf("迁移 %s 失败: %v", lp, err)
			continue
		}
		log.Printf("已从 %s 导入 Markdown 仓库: %s", lp, vaultRoot)
		return
	}
}

func registerVaultAPI(g *gin.RouterGroup) {
	g.POST("/media", func(c *gin.Context) {
		v := mustCtxVault(c)
		noteID := strings.TrimSpace(c.PostForm("note"))
		if !safeNoteID(noteID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "需要表单字段 note（笔记 id）"})
			return
		}
		fh, err := c.FormFile("file")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "需要表单字段 file"})
			return
		}
		src, err := fh.Open()
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "无法读取文件"})
			return
		}
		defer src.Close()

		limited := io.LimitReader(src, maxImageUpload+1)
		data, err := io.ReadAll(limited)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if len(data) > maxImageUpload {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "图片过大"})
			return
		}
		ext, _, ok := detectImageType(data)
		if !ok || ext == "" {
			c.JSON(http.StatusUnsupportedMediaType, gin.H{"error": "仅支持 png / jpeg / gif / webp / svg"})
			return
		}
		name, err := v.SaveImage(noteID, data, ext)
		if err != nil {
			if os.IsNotExist(err) {
				c.JSON(http.StatusNotFound, gin.H{"error": "笔记不存在"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"name": name})
	})

	g.GET("/vault/*filepath", func(c *gin.Context) {
		v := mustCtxVault(c)
		p := strings.TrimPrefix(c.Param("filepath"), "/")
		p = filepath.ToSlash(filepath.Clean(p))
		if p == "." || p == "" || strings.Contains(p, "..") {
			c.Status(http.StatusNotFound)
			return
		}
		abs, err := v.resolveVaultPath(p)
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		st, err := os.Stat(abs)
		if err != nil || st.IsDir() {
			c.Status(http.StatusNotFound)
			return
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		data, err = unwrapVaultBlob(data, v.passphrase)
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		switch strings.ToLower(filepath.Ext(abs)) {
		case ".md", ".markdown":
			c.Data(http.StatusOK, "text/markdown; charset=utf-8", data)
			return
		case ".css":
			c.Data(http.StatusOK, "text/css; charset=utf-8", data)
			return
		case ".js", ".mjs":
			c.Data(http.StatusOK, "application/javascript; charset=utf-8", data)
			return
		}
		ct, _, ok := detectImageType(data)
		if !ok {
			ct = http.DetectContentType(data)
		}
		c.Data(http.StatusOK, ct, data)
	})
}

// normalizeListenAddr 允许只写端口（如 8787），等价于 :8787（省略 IP 时监听所有网卡，与 0.0.0.0:端口 同义）。
// 配置为 127.0.0.1:端口 时保持原样，仅本机可访；要用本机其它网卡 IP 访问请改为 :端口 或 0.0.0.0:端口。
func normalizeListenAddr(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	if strings.Contains(s, ":") {
		return s
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return s
		}
	}
	return ":" + s
}

func checkListenAddr(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return ln.Close()
}

func buildRouter(vaultBase string, webRoot fs.FS, auth *authBundle, vaultPassphrase string) http.Handler {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.MaxMultipartMemory = maxImageUpload
	r.RedirectTrailingSlash = false
	r.RedirectFixedPath = false
	r.HandleMethodNotAllowed = false
	r.Use(gin.Recovery())
	r.Use(gin.LoggerWithConfig(gin.LoggerConfig{
		SkipPaths: []string{"/", "/styles.css", "/app.js", "/favicon.svg", "/favicon.ico", "/api/auth/status", "/auth/github/callback", "/auth/gitee/callback", "/vendor/easymde/easymde.min.css", "/vendor/easymde/easymde.min.js", "/public", "/public/", "/public.js", "/api/public/posts"},
	}))

	registerPublicAPI(r, vaultBase, vaultPassphrase)
	registerPublicWeb(r, webRoot)

	r.GET("/api/auth/status", handleAuthStatus(auth))
	registerGitHubOAuthRoutes(r, auth.github)
	registerGiteeOAuthRoutes(r, auth.gitee)
	registerLogoutRoute(r, auth)

	api := r.Group("/api", requireOAuthReady(auth), requireAuthAndUserVault(vaultBase, auth, vaultPassphrase))
	registerVaultAPI(api)

	api.GET("/notes", func(c *gin.Context) {
		v := mustCtxVault(c)
		notes, err := v.List()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, notes)
	})

	type writeBody struct {
		Title    string `json:"title"`
		Body     string `json:"body"`
		BeforeID string `json:"beforeId"`
		Public   bool   `json:"public"`
	}

	api.POST("/notes", func(c *gin.Context) {
		v := mustCtxVault(c)
		var wb writeBody
		_ = c.ShouldBindJSON(&wb)
		n, err := v.Create(wb.Title, wb.Body, wb.BeforeID, wb.Public)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusCreated, n)
	})

	api.PUT("/notes/:id", func(c *gin.Context) {
		v := mustCtxVault(c)
		id := c.Param("id")
		var wb writeBody
		if err := c.ShouldBindJSON(&wb); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
			return
		}
		n, err := v.Update(id, wb.Title, wb.Body, wb.Public)
		if err != nil {
			if err == os.ErrNotExist {
				c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, n)
	})

	api.DELETE("/notes/:id", func(c *gin.Context) {
		v := mustCtxVault(c)
		id := c.Param("id")
		if err := v.Delete(id); err != nil {
			if err == os.ErrNotExist {
				c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.Status(http.StatusNoContent)
	})

	r.GET("/", func(c *gin.Context) {
		b, err := fs.ReadFile(webRoot, "index.html")
		if err != nil {
			c.String(http.StatusInternalServerError, "无法读取页面")
			return
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", b)
	})
	r.GET("/styles.css", func(c *gin.Context) {
		b, err := fs.ReadFile(webRoot, "styles.css")
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		c.Data(http.StatusOK, "text/css; charset=utf-8", b)
	})
	r.GET("/app.js", func(c *gin.Context) {
		b, err := fs.ReadFile(webRoot, "app.js")
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		c.Data(http.StatusOK, "application/javascript; charset=utf-8", b)
	})
	if vendorFS, err := fs.Sub(webRoot, "vendor"); err == nil {
		r.StaticFS("/vendor", http.FS(vendorFS))
	}
	serveFavicon := func(c *gin.Context) {
		if len(faviconSVG) == 0 {
			b, err := fs.ReadFile(webRoot, "favicon.svg")
			if err != nil {
				c.Status(http.StatusNotFound)
				return
			}
			c.Data(http.StatusOK, "image/svg+xml; charset=utf-8", b)
			return
		}
		c.Header("Cache-Control", "public, max-age=86400")
		c.Data(http.StatusOK, "image/svg+xml; charset=utf-8", faviconSVG)
	}
	r.GET("/favicon.svg", serveFavicon)
	// 多数浏览器会默认请求 /favicon.ico；无此路由时易表现为「没有图标」
	r.GET("/favicon.ico", serveFavicon)

	return r
}

type program struct {
	addr              string
	vaultBase         string
	vaultPassphrase   string
	web               fs.FS
	auth              *authBundle
	srv               *http.Server
}

func appLog(s service.Service) service.Logger {
	lg, err := s.Logger(nil)
	if err != nil {
		return consoleLogger{}
	}
	return lg
}

type consoleLogger struct{}

func (consoleLogger) Info(args ...interface{}) error {
	log.Println(args...)
	return nil
}

func (consoleLogger) Infof(format string, args ...interface{}) error {
	log.Printf(format, args...)
	return nil
}

func (consoleLogger) Warning(args ...interface{}) error {
	log.Println(append([]interface{}{"WARN"}, args...)...)
	return nil
}

func (consoleLogger) Warningf(format string, args ...interface{}) error {
	log.Printf("WARN "+format, args...)
	return nil
}

func (consoleLogger) Error(args ...interface{}) error {
	log.Println(append([]interface{}{"ERROR"}, args...)...)
	return nil
}

func (consoleLogger) Errorf(format string, args ...interface{}) error {
	log.Printf("ERROR "+format, args...)
	return nil
}

func (p *program) Start(s service.Service) error {
	lg := appLog(s)
	handler := buildRouter(p.vaultBase, p.web, p.auth, p.vaultPassphrase)
	p.srv = &http.Server{
		Addr:    p.addr,
		Handler: handler,
	}
	go func() {
		_ = lg.Infof("笔记服务监听 %s，仓库根 %s（每用户 users/<登录>/）", p.addr, p.vaultBase)
		if err := p.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			_ = lg.Errorf("HTTP 服务退出: %v", err)
		}
	}()
	return nil
}

func (p *program) Stop(s service.Service) error {
	if p.srv == nil {
		return nil
	}
	lg := appLog(s)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := p.srv.Shutdown(ctx); err != nil {
		_ = lg.Errorf("优雅关闭失败: %v", err)
		return err
	}
	_ = lg.Info("笔记服务已停止")
	return nil
}

func runHTTPServerForeground(addr string, vaultBase string, webRoot fs.FS, auth *authBundle, vaultPassphrase string) error {
	handler := buildRouter(vaultBase, webRoot, auth, vaultPassphrase)
	srv := &http.Server{
		Addr:    addr,
		Handler: handler,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("HTTP 已监听 %s（浏览器请打开控制台里打印的地址，按 Ctrl+C 停止）", addr)
		err := srv.ListenAndServe()
		if err != nil && err != http.ErrServerClosed {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("HTTP 服务异常退出: %w", err)
		}
	case <-quit:
		log.Println("正在关闭…")
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return srv.Shutdown(ctx)
	}
	return nil
}

func main() {
	configPath := flag.String("config", "", "配置文件；空则使用可执行文件同目录下 "+defaultConfigFileName)
	svcName := flag.String("svc-name", "LocalNotes", "安装系统服务时使用的内部名称（install/uninstall 需一致）")
	svcFlag := flag.String("service", "", "系统服务：install | uninstall | start | stop | restart；留空则前台运行（Ctrl+C 停止）")
	flag.Parse()

	cfgFile, err := resolveConfigPath(*configPath)
	if err != nil {
		log.Fatalf("配置文件路径: %v", err)
	}
	fileCfg, err := loadAppConfig(cfgFile)
	if err != nil {
		log.Fatalf("读取配置 %s: %v", cfgFile, err)
	}
	listenAddr := normalizeListenAddr(strings.TrimSpace(fileCfg.Listen))

	var gh *githubAuth
	if fileCfg.GitHubOAuth != nil {
		gc := normalizeGitHubOAuth(*fileCfg.GitHubOAuth)
		if err := validateGitHubOAuth(gc); err != nil {
			log.Printf("提示: githubOAuth 未填全或无效，服务已启动但无法登录（%v）", err)
		} else {
			gh = &githubAuth{cfg: gc}
		}
	} else {
		log.Printf("提示: 未配置 githubOAuth。")
	}

	var gitee *giteeAuth
	if fileCfg.GiteeOAuth != nil {
		gc := normalizeGiteeOAuth(*fileCfg.GiteeOAuth)
		if err := validateGiteeOAuth(gc); err != nil {
			log.Printf("提示: giteeOAuth 未填全或无效，服务已启动但无法登录（%v）", err)
		} else {
			gitee = &giteeAuth{cfg: gc}
		}
	} else {
		log.Printf("提示: 未配置 giteeOAuth。")
	}

	auth := &authBundle{github: gh, gitee: gitee}
	if !auth.oauthReady() {
		log.Printf("提示: 未配置有效的 githubOAuth 或 giteeOAuth，服务已启动；在 notes-config.json 中填写并重启后即可登录。")
	}

	vaultBase, legacyJSON := computeVaultRoot(resolveDataPathForConfig(fileCfg.Data))
	vaultPassphrase := vaultPassphraseFromEnvOrConfig(fileCfg)
	if err := os.MkdirAll(vaultBase, 0o755); err != nil {
		log.Fatalf("创建仓库根目录失败 %s: %v", vaultBase, err)
	}
	usersDir := filepath.Join(vaultBase, "users")
	if err := os.MkdirAll(usersDir, 0o755); err != nil {
		log.Fatalf("创建 users 目录失败 %s: %v", usersDir, err)
	}
	tryMigrateLegacy(vaultBase, legacyJSON, vaultPassphrase)
	if vaultHasAnyNote(vaultBase) {
		entries, rerr := os.ReadDir(usersDir)
		if rerr == nil {
			hasUserNotes := false
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				if vaultHasAnyNote(filepath.Join(usersDir, e.Name())) {
					hasUserNotes = true
					break
				}
			}
			if !hasUserNotes {
				log.Printf("提示: 在 %s 根下检测到旧版笔记数据；多用户模式下笔记应在 users/<provider>/<登录名>/ 下，请自行迁移或备份后移动目录。", vaultBase)
			}
		}
	}

	webRoot, err := fs.Sub(embeddedWeb, "web")
	if err != nil {
		log.Fatal(err)
	}

	svcConfig := &service.Config{
		Name:        *svcName,
		DisplayName: "本地网页笔记",
		Description: "本地 Markdown 笔记 HTTP 服务（Gin）",
		Arguments: []string{"-config=" + cfgFile},
	}

	prg := &program{
		addr:              listenAddr,
		vaultBase:         vaultBase,
		vaultPassphrase:   vaultPassphrase,
		web:               webRoot,
		auth:              auth,
	}

	if *svcFlag != "" {
		s, err := service.New(prg, svcConfig)
		if err != nil {
			log.Fatal(err)
		}
		if err := service.Control(s, *svcFlag); err != nil {
			log.Fatalf("service %s: %v", *svcFlag, err)
		}
		return
	}

	log.Printf("配置: %s", cfgFile)
	log.Printf("Markdown 仓库根: %s/users/<provider>/<登录名>/（其下 YYYY/MM/DD/<id>/note.md）", vaultBase)
	if vaultPassphrase != "" {
		log.Println("笔记加密: 已启用（note.md 在磁盘上为密文；口令勿提交到 Git，可用环境变量 NOTES_VAULT_PASSPHRASE）")
	}
	if auth.github != nil && auth.github.enabled() {
		log.Println("GitHub 登录已就绪（OAuth 应用的 callbackUrl 须与配置完全一致）")
	}
	if auth.gitee != nil && auth.gitee.enabled() {
		log.Println("Gitee 登录已就绪（第三方应用的回调地址须与配置完全一致）")
	}
	log.Printf("监听: %s | 在浏览器打开: %s", listenAddr, browserOpenURL(listenAddr))
	if bindsBroad(listenAddr) {
		log.Printf("多网卡监听：其它机器请用 http://<本机IP>%s；注意防火墙与安全组", portSuffix(listenAddr))
	}
	log.Printf("安装为系统服务（管理员）:  %s -service install -svc-name %s", filepath.Base(os.Args[0]), *svcName)
	if err := checkListenAddr(listenAddr); err != nil {
		log.Printf("无法监听 %s: %v", listenAddr, err)
		log.Fatalf(
			"端口可能已被其它程序占用。请修改 %s 中的 listen",
			cfgFile,
		)
	}
	if service.Interactive() {
		if err := runHTTPServerForeground(listenAddr, vaultBase, webRoot, auth, vaultPassphrase); err != nil {
			log.Fatal(err)
		}
		return
	}

	s, err := service.New(prg, svcConfig)
	if err != nil {
		log.Fatal(err)
	}
	if err := s.Run(); err != nil {
		log.Fatal(err)
	}
}
