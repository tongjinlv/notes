package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type authBundle struct {
	github *githubAuth
	gitee  *giteeAuth
}

func (b *authBundle) oauthReady() bool {
	if b == nil {
		return false
	}
	return (b.github != nil && b.github.enabled()) || (b.gitee != nil && b.gitee.enabled())
}

func signOAuthSession(p sessionPayload, cookieSecret string) (string, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	b64 := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, []byte(cookieSecret))
	mac.Write([]byte(b64))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return b64 + "." + sig, nil
}

func parseSessionWithSecret(val, cookieSecret string) (*sessionPayload, bool) {
	parts := strings.SplitN(val, ".", 2)
	if len(parts) != 2 {
		return nil, false
	}
	b64, sigB64 := parts[0], parts[1]
	mac := hmac.New(sha256.New, []byte(cookieSecret))
	mac.Write([]byte(b64))
	want := mac.Sum(nil)
	got, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil || len(got) != len(want) || subtle.ConstantTimeCompare(got, want) != 1 {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(b64)
	if err != nil {
		return nil, false
	}
	var p sessionPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, false
	}
	if p.Exp < time.Now().Unix() {
		return nil, false
	}
	return &p, true
}

func (b *authBundle) sessionFromRequest(c *gin.Context) (*sessionPayload, bool) {
	if b == nil || !b.oauthReady() {
		return nil, false
	}
	v, err := c.Cookie(sessionCookieName)
	if err != nil || v == "" {
		return nil, false
	}
	if b.github != nil && b.github.enabled() {
		if p, ok := parseSessionWithSecret(v, b.github.cfg.CookieSecret); ok {
			if strings.TrimSpace(p.Provider) == "" {
				p.Provider = "github"
			}
			return p, true
		}
	}
	if b.gitee != nil && b.gitee.enabled() {
		if p, ok := parseSessionWithSecret(v, b.gitee.cfg.CookieSecret); ok {
			if strings.TrimSpace(p.Provider) == "" {
				p.Provider = "gitee"
			}
			return p, true
		}
	}
	return nil, false
}

func handleAuthStatus(b *authBundle) gin.HandlerFunc {
	return func(c *gin.Context) {
		ghOn := b != nil && b.github != nil && b.github.enabled()
		giteeOn := b != nil && b.gitee != nil && b.gitee.enabled()
		if !ghOn && !giteeOn {
			c.JSON(http.StatusOK, gin.H{
				"configured": false,
				"enabled":    false,
				"githubOAuth": false,
				"giteeOAuth":  false,
				"user":        nil,
			})
			return
		}
		if p, ok := b.sessionFromRequest(c); ok {
			c.JSON(http.StatusOK, gin.H{
				"configured":  true,
				"enabled":     true,
				"githubOAuth": ghOn,
				"giteeOAuth":  giteeOn,
				"user": gin.H{
					"provider":  p.Provider,
					"login":     p.Login,
					"name":      p.Name,
					"avatarUrl": p.AvatarURL,
				},
			})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"configured":  true,
			"enabled":     true,
			"githubOAuth": ghOn,
			"giteeOAuth":  giteeOn,
			"user":        nil,
		})
	}
}

func registerLogoutRoute(r gin.IRoutes, b *authBundle) {
	if b == nil || !b.oauthReady() {
		return
	}
	r.POST("/auth/logout", func(c *gin.Context) {
		http.SetCookie(c.Writer, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1})
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
}
