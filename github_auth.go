package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	sessionCookieName  = "notes_session"
	oauthStateCookie   = "notes_oauth_state"
	oauthPopupCookie   = "notes_oauth_popup"
	sessionMaxAge      = 7 * 24 * 3600
	oauthStartNotReady = `<!DOCTYPE html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>尚未配置 OAuth</title></head><body style="font-family:system-ui,sans-serif;padding:1.5rem;line-height:1.6;max-width:28rem">
<p>服务器尚未配置 GitHub OAuth。</p>
<p>请在 <code>notes-config.json</code> 中填写 <code>githubOAuth</code>（clientId、clientSecret、callbackUrl、cookieSecret），保存后<strong>重启本程序</strong>。</p>
<p><a href="/">返回笔记</a></p>
</body></html>`
)

type githubAuth struct {
	cfg GitHubOAuthConfig
}

func (a *githubAuth) enabled() bool {
	if a == nil {
		return false
	}
	c := a.cfg
	return strings.TrimSpace(c.ClientID) != "" &&
		strings.TrimSpace(c.ClientSecret) != "" &&
		strings.TrimSpace(c.CallbackURL) != "" &&
		len(strings.TrimSpace(c.CookieSecret)) >= 16
}

func envOr(s, envKey string) string {
	if strings.TrimSpace(s) != "" {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(os.Getenv(envKey))
}

func normalizeGitHubOAuth(c GitHubOAuthConfig) GitHubOAuthConfig {
	c.ClientID = strings.TrimSpace(c.ClientID)
	c.ClientSecret = envOr(c.ClientSecret, "NOTES_GITHUB_CLIENT_SECRET")
	c.CallbackURL = strings.TrimSpace(c.CallbackURL)
	c.CookieSecret = envOr(c.CookieSecret, "NOTES_AUTH_COOKIE_SECRET")
	var allow []string
	for _, x := range c.AllowedLogins {
		x = strings.TrimSpace(strings.ToLower(x))
		if x != "" {
			allow = append(allow, x)
		}
	}
	c.AllowedLogins = allow
	return c
}

func validateGitHubOAuth(c GitHubOAuthConfig) error {
	if c.ClientID == "" || c.ClientSecret == "" || c.CallbackURL == "" {
		return fmt.Errorf("需要 clientId、clientSecret、callbackUrl")
	}
	if u, err := url.Parse(c.CallbackURL); err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("callbackUrl 无效")
	}
	if len(c.CookieSecret) < 16 {
		return fmt.Errorf("cookieSecret 至少 16 字符（建议 32+ 随机串），可用环境变量 NOTES_AUTH_COOKIE_SECRET")
	}
	return nil
}

type sessionPayload struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatarUrl"`
	Exp       int64  `json:"exp"`
}

func (a *githubAuth) signSession(p sessionPayload) (string, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	b64 := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, []byte(a.cfg.CookieSecret))
	mac.Write([]byte(b64))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return b64 + "." + sig, nil
}

func (a *githubAuth) parseSession(val string) (*sessionPayload, bool) {
	parts := strings.SplitN(val, ".", 2)
	if len(parts) != 2 {
		return nil, false
	}
	b64, sigB64 := parts[0], parts[1]
	mac := hmac.New(sha256.New, []byte(a.cfg.CookieSecret))
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

func (a *githubAuth) sessionFromRequest(c *gin.Context) (*sessionPayload, bool) {
	if !a.enabled() {
		return nil, false
	}
	v, err := c.Cookie(sessionCookieName)
	if err != nil || v == "" {
		return nil, false
	}
	return a.parseSession(v)
}

func randomState() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func readOAuthWantsPopup(c *gin.Context) bool {
	pc, err := c.Request.Cookie(oauthPopupCookie)
	return err == nil && pc.Value == "1"
}

func clearOAuthPopupCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: oauthPopupCookie, Value: "", Path: "/", MaxAge: -1})
}

func htmlOAuthPopupResult(ok bool) string {
	if ok {
		return `<!DOCTYPE html><html lang="zh-CN"><head><meta charset="utf-8"><title>登录成功</title></head><body><script>
(function(){
  try {
    if (window.opener) {
      window.opener.postMessage({ type: "notes-github-oauth", ok: true }, location.origin);
    }
  } catch (e) {}
  window.close();
})();
</script><p>登录成功，窗口将自动关闭。</p></body></html>`
	}
	return `<!DOCTYPE html><html lang="zh-CN"><head><meta charset="utf-8"><title>登录失败</title></head><body><script>
(function(){
  try {
    if (window.opener) {
      window.opener.postMessage({ type: "notes-github-oauth", ok: false }, location.origin);
    }
  } catch (e) {}
  window.close();
})();
</script><p>登录未完成，请关闭窗口后重试。</p></body></html>`
}

// registerAuthRoutes 始终注册 /auth/github/start（未配置时返回说明页）；OAuth 就绪时注册 callback 与 logout。
func registerAuthRoutes(r gin.IRoutes, gh *githubAuth) {
	r.GET("/auth/github/start", func(c *gin.Context) {
		if gh == nil || !gh.enabled() {
			c.Header("Content-Type", "text/html; charset=utf-8")
			c.String(http.StatusOK, oauthStartNotReady)
			return
		}
		popup := strings.TrimSpace(c.Query("popup")) == "1"
		if popup {
			http.SetCookie(c.Writer, &http.Cookie{
				Name:     oauthPopupCookie,
				Value:    "1",
				Path:     "/",
				MaxAge:   600,
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
			})
		} else {
			clearOAuthPopupCookie(c.Writer)
		}

		st := randomState()
		http.SetCookie(c.Writer, &http.Cookie{
			Name:     oauthStateCookie,
			Value:    st,
			Path:     "/",
			MaxAge:   600,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
		q := url.Values{}
		q.Set("client_id", gh.cfg.ClientID)
		q.Set("redirect_uri", gh.cfg.CallbackURL)
		q.Set("scope", "read:user user:email")
		q.Set("state", st)
		c.Redirect(http.StatusFound, "https://github.com/login/oauth/authorize?"+q.Encode())
	})

	if gh == nil || !gh.enabled() {
		return
	}
	a := gh

	r.GET("/auth/github/callback", func(c *gin.Context) {
		wantPopup := readOAuthWantsPopup(c)
		fail := func(code int, plain string) {
			clearOAuthPopupCookie(c.Writer)
			if wantPopup {
				c.Header("Content-Type", "text/html; charset=utf-8")
				c.String(http.StatusOK, htmlOAuthPopupResult(false))
				return
			}
			c.String(code, plain)
		}

		if c.Query("error") != "" {
			fail(http.StatusBadRequest, "GitHub 授权被拒绝或失败")
			return
		}
		code := strings.TrimSpace(c.Query("code"))
		state := strings.TrimSpace(c.Query("state"))
		if code == "" || state == "" {
			fail(http.StatusBadRequest, "缺少 code 或 state")
			return
		}
		sc, err := c.Request.Cookie(oauthStateCookie)
		if err != nil || sc.Value == "" || len(sc.Value) != len(state) || subtle.ConstantTimeCompare([]byte(sc.Value), []byte(state)) != 1 {
			fail(http.StatusBadRequest, "state 无效，请重试登录")
			return
		}
		http.SetCookie(c.Writer, &http.Cookie{Name: oauthStateCookie, Value: "", Path: "/", MaxAge: -1})

		token, err := a.exchangeCode(c.Request.Context(), code)
		if err != nil {
			fail(http.StatusBadGateway, "换取 token 失败")
			return
		}
		u, err := a.fetchGitHubUser(c.Request.Context(), token)
		if err != nil {
			fail(http.StatusBadGateway, "读取 GitHub 用户失败")
			return
		}
		loginLower := strings.ToLower(strings.TrimSpace(u.Login))
		if len(a.cfg.AllowedLogins) > 0 {
			ok := false
			for _, al := range a.cfg.AllowedLogins {
				if loginLower == al {
					ok = true
					break
				}
			}
			if !ok {
				fail(http.StatusForbidden, "当前 GitHub 账号不在允许列表中")
				return
			}
		}

		sess := sessionPayload{
			ID:        u.ID,
			Login:     u.Login,
			Name:      u.Name,
			AvatarURL: u.AvatarURL,
			Exp:       time.Now().Add(time.Duration(sessionMaxAge) * time.Second).Unix(),
		}
		signed, err := a.signSession(sess)
		if err != nil {
			fail(http.StatusInternalServerError, "创建会话失败")
			return
		}
		http.SetCookie(c.Writer, &http.Cookie{
			Name:     sessionCookieName,
			Value:    signed,
			Path:     "/",
			MaxAge:   sessionMaxAge,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
		clearOAuthPopupCookie(c.Writer)
		if wantPopup {
			c.Header("Content-Type", "text/html; charset=utf-8")
			c.String(http.StatusOK, htmlOAuthPopupResult(true))
			return
		}
		c.Redirect(http.StatusFound, "/")
	})

	r.POST("/auth/logout", func(c *gin.Context) {
		http.SetCookie(c.Writer, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1})
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
}

func (a *githubAuth) exchangeCode(ctx context.Context, code string) (string, error) {
	form := url.Values{}
	form.Set("client_id", a.cfg.ClientID)
	form.Set("client_secret", a.cfg.ClientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", a.cfg.CallbackURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://github.com/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	var out struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	if out.Error != "" || out.AccessToken == "" {
		return "", fmt.Errorf("%s", out.Error)
	}
	return out.AccessToken, nil
}

type ghUser struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
}

func (a *githubAuth) fetchGitHubUser(ctx context.Context, token string) (*ghUser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api %d", resp.StatusCode)
	}
	var u ghUser
	if err := json.Unmarshal(body, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

func handleAuthStatus(a *githubAuth) gin.HandlerFunc {
	return func(c *gin.Context) {
		if a == nil || !a.enabled() {
			c.JSON(http.StatusOK, gin.H{
				"configured": false,
				"enabled":    false,
				"user":       nil,
			})
			return
		}
		if p, ok := a.sessionFromRequest(c); ok {
			c.JSON(http.StatusOK, gin.H{
				"configured": true,
				"enabled":    true,
				"user": gin.H{
					"login":     p.Login,
					"name":      p.Name,
					"avatarUrl": p.AvatarURL,
				},
			})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"configured": true,
			"enabled":    true,
			"user":       nil,
		})
	}
}
