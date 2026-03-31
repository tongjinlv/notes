package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

const ctxUserVaultKey = "ctxUserVault"

func oauthLoginURLs(auth *authBundle) []string {
	if auth == nil {
		return []string{"/auth/github/start"}
	}
	var urls []string
	if auth.github != nil && auth.github.enabled() {
		urls = append(urls, "/auth/github/start")
	}
	if auth.gitee != nil && auth.gitee.enabled() {
		urls = append(urls, "/auth/gitee/start")
	}
	if len(urls) == 0 {
		return []string{"/auth/github/start"}
	}
	return urls
}

// requireOAuthReady 在未配置或无效 OAuth 时拒绝 /api/*（503），避免进程启动失败。
func requireOAuthReady(auth *authBundle) gin.HandlerFunc {
	return func(c *gin.Context) {
		if auth == nil || !auth.oauthReady() {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error":      "未配置 OAuth 或配置无效，请在 notes-config.json 中配置 githubOAuth 和/或 giteeOAuth 后重启服务",
				"configured": false,
			})
			c.Abort()
			return
		}
		c.Next()
	}
}

// userVaultSegment 将 provider/login 转为安全的单级目录名（小写，非法字符为 _）。
func userVaultSegment(raw string) string {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" || s == "." || s == ".." {
		return "_invalid"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r - 'A' + 'a')
		default:
			b.WriteRune('_')
		}
	}
	out := strings.Trim(b.String(), "_-")
	if out == "" {
		return "_invalid"
	}
	if len(out) > 200 {
		out = out[:200]
	}
	return out
}

func userVaultProvider(provider string) string {
	p := strings.TrimSpace(strings.ToLower(provider))
	if p == "github" || p == "gitee" {
		return p
	}
	return "github"
}

// requireAuthAndUserVault 校验会话并在上下文中放入该用户专属的 *Vault（根目录为 <vaultBase>/users/<segment>/）。
func requireAuthAndUserVault(vaultBase string, auth *authBundle, vaultPassphrase string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if auth == nil || !auth.oauthReady() {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error":      "服务端未正确配置 OAuth 登录",
				"configured": false,
			})
			c.Abort()
			return
		}
		p, ok := auth.sessionFromRequest(c)
		if !ok {
			loginUrls := oauthLoginURLs(auth)
			c.JSON(http.StatusUnauthorized, gin.H{
				"error":     "需要登录",
				"loginUrl":  loginUrls[0],
				"loginUrls": loginUrls,
			})
			c.Abort()
			return
		}
		prov := userVaultProvider(p.Provider)
		seg := userVaultSegment(p.Login)
		dir := filepath.Join(vaultBase, "users", prov, seg)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "无法创建用户笔记目录"})
			c.Abort()
			return
		}
		c.Set(ctxUserVaultKey, NewVault(dir, vaultPassphrase))
		c.Next()
	}
}

func mustCtxVault(c *gin.Context) *Vault {
	v, ok := c.Get(ctxUserVaultKey)
	if !ok {
		panic("ctxUserVault: middleware missing")
	}
	return v.(*Vault)
}
