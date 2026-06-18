// Package manager implements DingTalk (钉钉) bot integration.
package manager

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-logr/logr"
)

// DingTalkClient sends messages to a DingTalk group via bot webhook.
type DingTalkClient struct {
	webhookURL string
	secret     string
	httpClient *http.Client
	log        logr.Logger
}

// dingTalkMessage represents the DingTalk bot message format.
type dingTalkMessage struct {
	MsgType  string            `json:"msgtype"`
	Markdown *dingTalkMarkdown `json:"markdown,omitempty"`
	Text     *dingTalkText     `json:"text,omitempty"`
}

type dingTalkMarkdown struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

type dingTalkText struct {
	Content string `json:"content"`
}

// NewDingTalkClient creates a new DingTalk client.
func NewDingTalkClient(webhookURL, secret string, log logr.Logger) *DingTalkClient {
	return &DingTalkClient{
		webhookURL: webhookURL,
		secret:     secret,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		log:        log.WithName("dingtalk"),
	}
}

// SendReleaseNotification sends a formatted release notification to DingTalk.
func (c *DingTalkClient) SendReleaseNotification(chartName, chartVersion string, results []ForwardResult) error {
	if c.webhookURL == "" {
		c.log.V(1).Info("DingTalk webhook not configured, skipping")
		return nil
	}

	title := fmt.Sprintf("📦 Helm Chart 发布通知: %s %s", chartName, chartVersion)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# 📦 Helm Chart 发布通知\n\n"))
	sb.WriteString(fmt.Sprintf("**Chart**: %s  \n", chartName))
	sb.WriteString(fmt.Sprintf("**版本**: %s  \n", chartVersion))
	sb.WriteString(fmt.Sprintf("**时间**: %s  \n\n", time.Now().Format("2006-01-02 15:04:05")))

	sb.WriteString("## 更新结果\n\n")
	sb.WriteString("| 客户 | 状态 | 耗时 | 备注 |\n")
	sb.WriteString("|------|------|------|------|\n")

	successCount, failCount := 0, 0
	for _, r := range results {
		status := "✅ 成功"
		if !r.Success {
			status = "❌ 失败"
			failCount++
		} else {
			successCount++
		}
		remark := "-"
		if r.ErrorMessage != "" {
			remark = r.ErrorMessage
		}
		sb.WriteString(fmt.Sprintf("| %s | %s | %s | %s |\n",
			r.CustomerName, status, r.Duration.Truncate(time.Second), remark))
	}

	sb.WriteString(fmt.Sprintf("\n---\n"))
	sb.WriteString(fmt.Sprintf("**总计**: %d 个客户 | 成功: %d | 失败: %d\n",
		len(results), successCount, failCount))

	return c.sendMarkdown(title, sb.String())
}

// SendStatusUpdate sends a status update for a single customer release.
func (c *DingTalkClient) SendStatusUpdate(customerName, chartName, chartVersion, status, errMsg string) error {
	if c.webhookURL == "" {
		return nil
	}

	emoji := "✅"
	if status == "FAILED" || status == "ROLLBACK_FAILED" {
		emoji = "❌"
	} else if status == "ROLLED_BACK" {
		emoji = "⚠️"
	}

	content := fmt.Sprintf(
		"%s **Release 状态更新**\n\n"+
			"**客户**: %s\n"+
			"**Chart**: %s %s\n"+
			"**状态**: %s\n"+
			"**时间**: %s",
		emoji, customerName, chartName, chartVersion, status,
		time.Now().Format("2006-01-02 15:04:05"),
	)

	if errMsg != "" {
		content += fmt.Sprintf("\n**错误**: %s", errMsg)
	}

	return c.sendText(content)
}

// sendMarkdown sends a markdown-formatted message.
func (c *DingTalkClient) sendMarkdown(title, text string) error {
	msg := dingTalkMessage{
		MsgType: "markdown",
		Markdown: &dingTalkMarkdown{
			Title: title,
			Text:  text,
		},
	}
	return c.send(msg)
}

// sendText sends a text message.
func (c *DingTalkClient) sendText(content string) error {
	msg := dingTalkMessage{
		MsgType: "text",
		Text:    &dingTalkText{Content: content},
	}
	return c.send(msg)
}

// send sends a message to the DingTalk webhook with optional HMAC signing.
func (c *DingTalkClient) send(msg dingTalkMessage) error {
	targetURL := c.webhookURL

	// Add HMAC signature if secret is configured
	if c.secret != "" {
		signedURL, err := signURL(c.webhookURL, c.secret)
		if err != nil {
			return fmt.Errorf("sign webhook URL: %w", err)
		}
		targetURL = signedURL
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}

	resp, err := c.httpClient.Post(targetURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("post to DingTalk: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read dingtalk response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("DingTalk returned %d: %s", resp.StatusCode, string(respBody))
	}

	c.log.V(1).Info("DingTalk message sent", "status", resp.StatusCode)
	return nil
}

// signURL signs the DingTalk webhook URL with HMAC-SHA256.
func signURL(webhookURL, secret string) (string, error) {
	timestamp := time.Now().UnixMilli()
	stringToSign := fmt.Sprintf("%d\n%s", timestamp, secret)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(stringToSign))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	u, err := url.Parse(webhookURL)
	if err != nil {
		return "", fmt.Errorf("parse webhook URL: %w", err)
	}

	q := u.Query()
	q.Set("timestamp", fmt.Sprintf("%d", timestamp))
	q.Set("sign", signature)
	u.RawQuery = q.Encode()

	return u.String(), nil
}
