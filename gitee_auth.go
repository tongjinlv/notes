package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const oauthGiteeStartNotReady = `<!DOCTYPE html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>尚未配置 OAuth</title></head><body style="font-family:system-ui,sans-serif;padding:1.5rem;line-height:1.6;max-width:28rem">
<p>服务器尚未配置 Gitee OAuth。</p>
<p>请在 <code>notes-config.json</code> 中填写 <code>giteeOAuth</code>（clientId、clientSecret、callbackUrl、cookieSecret），保存后<strong>重启本程序</strong>。</p>
<p><a href="/">返回笔记</a></p>
</body></html>`

type giteeAuth struct {
	cfg GiteeOAuthConfig
}

func (a *giteeAuth) enabled() bool {
	if a == nil {
		return false
	}
	c := a.cfg
	return strings.TrimSpace(c.ClientID) != "" &&
		strings.TrimSpace(c.ClientSecret) != "" &&
		strings.TrimSpace(c.CallbackURL) != "" &&
		len(strings.TrimSpace(c.CookieSecret)) >= 16
}

func normalizeGiteeOAuth(c GiteeOAuthConfig) GiteeOAuthConfig {
	c.ClientID = strings.TrimSpace(c.ClientID)
	c.ClientSecret = envOr(c.ClientSecret, "NOTES_GITEE_CLIENT_SECRET")
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

func validateGiteeOAuth(c GiteeOAuthConfig) error {
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

func (a *giteeAuth) signSession(p sessionPayload) (string, error) {
	return signOAuthSession(p, a.cfg.CookieSecret)
}

// registerGiteeOAuthRoutes 注册 /auth/gitee/start；Gitee 配置就绪时注册 callback。
func registerGiteeOAuthRoutes(r gin.IRoutes, g *giteeAuth) {
	r.GET("/auth/gitee/start", func(c *gin.Context) {
		if g == nil || !g.enabled() {
			c.Header("Content-Type", "text/html; charset=utf-8")
			c.String(http.StatusOK, oauthGiteeStartNotReady)
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
		q.Set("client_id", g.cfg.ClientID)
		q.Set("redirect_uri", g.cfg.CallbackURL)
		q.Set("response_type", "code")
		q.Set("scope", "user_info")
		q.Set("state", st)
		c.Redirect(http.StatusFound, "https://gitee.com/oauth/authorize?"+q.Encode())
	})

	if g == nil || !g.enabled() {
		return
	}
	a := g

	r.GET("/auth/gitee/callback", func(c *gin.Context) {
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
			fail(http.StatusBadRequest, "Gitee 授权被拒绝或失败")
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
		u, err := a.fetchGiteeUser(c.Request.Context(), token)
		if err != nil {
			fail(http.StatusBadGateway, "读取 Gitee 用户失败")
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
				fail(http.StatusForbidden, "当前 Gitee 账号不在允许列表中")
				return
			}
		}

		sess := sessionPayload{
			ID:        u.ID,
			Provider:  "gitee",
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
}

func (a *giteeAuth) exchangeCode(ctx context.Context, code string) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("client_id", a.cfg.ClientID)
	form.Set("redirect_uri", a.cfg.CallbackURL)
	form.Set("client_secret", a.cfg.ClientSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://gitee.com/oauth/token", strings.NewReader(form.Encode()))
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

func (a *giteeAuth) fetchGiteeUser(ctx context.Context, token string) (*ghUser, error) {
	u, err := url.Parse("https://gitee.com/api/v5/user")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("access_token", token)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gitee api %d", resp.StatusCode)
	}
	var user ghUser
	if err := json.Unmarshal(body, &user); err != nil {
		return nil, err
	}
	return &user, nil
}
