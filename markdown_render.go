package main

import (
	"bytes"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
)

const (
	maxMarkdownRenderAuth   = 2 << 20 // 登录后预览
	maxMarkdownRenderPublic = 512 << 10
)

// goldmark + GFM（表格、删除线、任务列表、自动链接等），输出经 bluemonday 消毒。
var mdEngine = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithRendererOptions(
		html.WithHardWraps(),
		html.WithXHTML(),
		html.WithUnsafe(),
	),
)

var mdSanitize = bluemonday.UGCPolicy()

func renderMarkdownHTML(src []byte) (string, error) {
	var buf bytes.Buffer
	if err := mdEngine.Convert(src, &buf); err != nil {
		return "", err
	}
	return mdSanitize.Sanitize(buf.String()), nil
}

func registerMarkdownAPI(api *gin.RouterGroup) {
	api.POST("/md/render", markdownRenderHandler(maxMarkdownRenderAuth))
}

func registerPublicMarkdownRender(r *gin.Engine) {
	r.POST("/api/public/render-md", markdownRenderHandler(maxMarkdownRenderPublic))
}

func markdownRenderHandler(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes+1)
		var req struct {
			Text string `json:"text"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json or body too large"})
			return
		}
		html, err := renderMarkdownHTML([]byte(req.Text))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"html": html})
	}
}
