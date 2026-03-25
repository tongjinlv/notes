package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

const defaultConfigFileName = "notes-config.json"

// GitHubOAuthConfig 可选：未配置时服务仍可启动，页面会提示填写；配置并重启后需登录，笔记在 users/<登录名>/。
type GitHubOAuthConfig struct {
	ClientID      string   `json:"clientId"`
	ClientSecret  string   `json:"clientSecret"`
	CallbackURL   string   `json:"callbackUrl"`
	CookieSecret  string   `json:"cookieSecret"`
	AllowedLogins []string `json:"allowedLogins"`
}

// appConfig 对应 notes-config.json；缺省 listen 为 :8787。
type appConfig struct {
	Listen      string             `json:"listen"`
	Data        string             `json:"data"`
	GitHubOAuth *GitHubOAuthConfig `json:"githubOAuth"`
}

func defaultAppConfig() appConfig {
	return appConfig{Listen: ":8787"}
}

func executableDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, e := filepath.EvalSymlinks(exe); e == nil {
		exe = resolved
	}
	return filepath.Dir(exe), nil
}

// resolveConfigPath -config 为空时使用 <exeDir>/notes-config.json。
func resolveConfigPath(flagConfig string) (string, error) {
	s := strings.TrimSpace(flagConfig)
	if s != "" {
		if filepath.IsAbs(s) {
			return filepath.Clean(s), nil
		}
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Abs(filepath.Join(wd, s))
	}
	ex, err := os.Executable()
	if err == nil && strings.Contains(strings.ToLower(ex), "go-build") {
		wd, werr := os.Getwd()
		if werr == nil {
			return filepath.Abs(filepath.Join(wd, defaultConfigFileName))
		}
	}
	dir, err := executableDir()
	if err != nil {
		wd, e2 := os.Getwd()
		if e2 != nil {
			return defaultConfigFileName, nil
		}
		return filepath.Abs(filepath.Join(wd, defaultConfigFileName))
	}
	return filepath.Join(dir, defaultConfigFileName), nil
}

// loadAppConfig 读取 JSON；文件不存在则返回默认配置（不报错）。
func loadAppConfig(path string) (appConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return defaultAppConfig(), nil
		}
		return appConfig{}, err
	}
	var c appConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return appConfig{}, err
	}
	if strings.TrimSpace(c.Listen) == "" {
		c.Listen = defaultAppConfig().Listen
	}
	return c, nil
}

// resolveDataPathForConfig 将配置项 data 交给 computeVaultRoot；相对路径相对可执行文件目录（go run 时相对当前工作目录）。
func resolveDataPathForConfig(data string) string {
	data = strings.TrimSpace(data)
	if data == "" {
		return ""
	}
	if filepath.IsAbs(data) {
		return filepath.Clean(data)
	}
	ex, err := os.Executable()
	if err == nil && strings.Contains(strings.ToLower(ex), "go-build") {
		wd, werr := os.Getwd()
		if werr == nil {
			return filepath.Clean(filepath.Join(wd, data))
		}
	}
	dir, err := executableDir()
	if err != nil {
		wd, _ := os.Getwd()
		return filepath.Clean(filepath.Join(wd, data))
	}
	return filepath.Clean(filepath.Join(dir, data))
}

func portSuffix(addr string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[i:]
	}
	return ""
}

// browserOpenURL 用于日志提示在浏览器中打开的地址。
func browserOpenURL(addr string) string {
	switch {
	case strings.HasPrefix(addr, ":"):
		return "http://127.0.0.1" + addr
	case strings.HasPrefix(addr, "0.0.0.0:"):
		return "http://127.0.0.1:" + strings.TrimPrefix(addr, "0.0.0.0:")
	case strings.HasPrefix(addr, "[::]:"):
		return "http://127.0.0.1" + strings.TrimPrefix(addr, "[::]:")
	default:
		return "http://" + addr
	}
}

// bindsBroad 是否在多网卡 / 全零地址上监听。
func bindsBroad(addr string) bool {
	return strings.HasPrefix(addr, ":") || strings.HasPrefix(addr, "0.0.0.0:") || strings.HasPrefix(addr, "[::]:")
}
