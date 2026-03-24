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

// computeVaultRoot：若 -data 指向已有的 .json 文件，则仓库目录为该文件同级的 notes-vault，并返回需迁移的 json 路径。
func computeVaultRoot(dataFlag string) (vaultRoot string, legacyJSON string) {
	if dataFlag != "" && strings.HasSuffix(strings.ToLower(dataFlag), ".json") {
		if st, err := os.Stat(dataFlag); err == nil && !st.IsDir() {
			return filepath.Join(filepath.Dir(dataFlag), "notes-vault"), dataFlag
		}
	}
	return resolveVaultRootFlag(dataFlag), ""
}

func tryMigrateLegacy(vaultRoot, explicitLegacy string) {
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
		if err := migrateLegacyJSON(vaultRoot, lp); err != nil {
			log.Printf("迁移 %s 失败: %v", lp, err)
			continue
		}
		log.Printf("已从 %s 导入 Markdown 仓库: %s", lp, vaultRoot)
		return
	}
}

func registerVaultAPI(r *gin.Engine, v *Vault) {
	r.POST("/api/media", func(c *gin.Context) {
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

	r.GET("/api/vault/*filepath", func(c *gin.Context) {
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

func browserURL(addr string) string {
	switch {
	case strings.HasPrefix(addr, ":"):
		return "http://127.0.0.1" + addr
	case strings.HasPrefix(addr, "0.0.0.0:"):
		return "http://127.0.0.1:" + strings.TrimPrefix(addr, "0.0.0.0:")
	default:
		return "http://" + addr
	}
}

func checkListenAddr(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return ln.Close()
}

func buildRouter(vault *Vault, webRoot fs.FS) http.Handler {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.MaxMultipartMemory = maxImageUpload
	r.RedirectTrailingSlash = false
	r.RedirectFixedPath = false
	r.HandleMethodNotAllowed = false
	r.Use(gin.Recovery())
	r.Use(gin.LoggerWithConfig(gin.LoggerConfig{
		SkipPaths: []string{"/", "/styles.css", "/app.js"},
	}))

	registerVaultAPI(r, vault)

	r.GET("/api/notes", func(c *gin.Context) {
		notes, err := vault.List()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, notes)
	})

	type writeBody struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}

	r.POST("/api/notes", func(c *gin.Context) {
		var wb writeBody
		_ = c.ShouldBindJSON(&wb)
		n, err := vault.Create(wb.Title, wb.Body)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusCreated, n)
	})

	r.PUT("/api/notes/:id", func(c *gin.Context) {
		id := c.Param("id")
		var wb writeBody
		if err := c.ShouldBindJSON(&wb); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
			return
		}
		n, err := vault.Update(id, wb.Title, wb.Body)
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

	r.DELETE("/api/notes/:id", func(c *gin.Context) {
		id := c.Param("id")
		if err := vault.Delete(id); err != nil {
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

	return r
}

type program struct {
	addr      string
	vaultRoot string
	vault     *Vault
	web       fs.FS
	srv       *http.Server
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
	handler := buildRouter(p.vault, p.web)
	p.srv = &http.Server{
		Addr:    p.addr,
		Handler: handler,
	}
	go func() {
		_ = lg.Infof("笔记服务监听 %s，Markdown 仓库 %s", p.addr, p.vaultRoot)
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

func runHTTPServerForeground(addr string, vault *Vault, webRoot fs.FS) error {
	handler := buildRouter(vault, webRoot)
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
	addr := flag.String("addr", "127.0.0.1:8787", "监听地址；局域网访问可用 0.0.0.0:8787")
	dataPath := flag.String("data", "", "Markdown 仓库根目录，默认可执行文件旁的 notes-vault；也可指向旧版 notes-data.json 以自动迁移")
	svcName := flag.String("svc-name", "LocalNotes", "安装系统服务时使用的内部名称（install/uninstall 需一致）")
	svcFlag := flag.String("service", "", "系统服务：install | uninstall | start | stop | restart；留空则前台运行（Ctrl+C 停止）")
	flag.Parse()

	vaultRoot, legacyJSON := computeVaultRoot(*dataPath)
	if err := os.MkdirAll(vaultRoot, 0o755); err != nil {
		log.Fatalf("创建仓库目录失败 %s: %v", vaultRoot, err)
	}
	tryMigrateLegacy(vaultRoot, legacyJSON)

	vault := NewVault(vaultRoot)

	webRoot, err := fs.Sub(embeddedWeb, "web")
	if err != nil {
		log.Fatal(err)
	}

	svcConfig := &service.Config{
		Name:        *svcName,
		DisplayName: "本地网页笔记",
		Description: "本地 Markdown 笔记 HTTP 服务（Gin）",
		Arguments: []string{
			"-addr=" + *addr,
			"-data=" + vaultRoot,
		},
	}

	prg := &program{
		addr:      *addr,
		vaultRoot: vaultRoot,
		vault:     vault,
		web:       webRoot,
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

	log.Printf("Markdown 仓库: %s （结构 YYYY/MM/DD/<id>/note.md，图片与 note.md 同目录）", vaultRoot)
	log.Printf("在浏览器打开（须带端口）: %s", browserURL(*addr))
	log.Printf("安装为系统服务（管理员）:  %s -service install -svc-name %s", filepath.Base(os.Args[0]), *svcName)

	if err := checkListenAddr(*addr); err != nil {
		log.Printf("无法监听 %s: %v", *addr, err)
		log.Fatalf(
			"端口可能已被其它程序占用。请换端口启动，例如：\n  %s -addr 127.0.0.1:8899",
			filepath.Base(os.Args[0]),
		)
	}

	if service.Interactive() {
		if err := runHTTPServerForeground(*addr, vault, webRoot); err != nil {
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
