package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

const ctxUserVaultKey = "ctxUserVault"

// requireGitHubOAuthReady 在未配置或无效 OAuth 时拒绝 /api/*（503），避免进程启动失败。
func requireGitHubOAuthReady(gh *githubAuth) gin.HandlerFunc {
	return func(c *gin.Context) {
		if gh == nil || !gh.enabled() {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error":      "未配置 GitHub OAuth 或配置无效，请编辑 notes-config.json 中的 githubOAuth 后重启服务",
				"configured": false,
			})
			c.Abort()
			return
		}
		c.Next()
	}
}

// userVaultSegment 将 GitHub login 转为安全的单级目录名（小写，非法字符为 _）。
func userVaultSegment(login string) string {
	s := strings.TrimSpace(strings.ToLower(login))
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

// requireAuthAndUserVault 校验会话并在上下文中放入该用户专属的 *Vault（根目录为 <vaultBase>/users/<segment>/）。
func requireAuthAndUserVault(vaultBase string, gh *githubAuth) gin.HandlerFunc {
	return func(c *gin.Context) {
		if gh == nil || !gh.enabled() {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error":      "服务端未正确配置 GitHub 登录",
				"configured": false,
			})
			c.Abort()
			return
		}
		p, ok := gh.sessionFromRequest(c)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error":    "需要登录",
				"loginUrl": "/auth/github/start",
			})
			c.Abort()
			return
		}
		seg := userVaultSegment(p.Login)
		dir := filepath.Join(vaultBase, "users", seg)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "无法创建用户笔记目录"})
			c.Abort()
			return
		}
		c.Set(ctxUserVaultKey, NewVault(dir))
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
