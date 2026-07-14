// Package notifier implements notification channels (DingTalk / email).
package notifier

import (
	"bytes"
	"context"
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

// ForwardResult summarizes a notification delivery to a single customer.
type ForwardResult struct {
	CustomerID   string
	CustomerName string
	Success      bool
	ErrorMessage string
	Duration     time.Duration
}

// DingTalkClient sends messages to a DingTalk group via bot webhook.
type DingTalkClient struct {
	webhookURL string
	secret     string
	log        logr.Logger
	client     *http.Client
}

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
		log:        log.WithName("dingtalk"),
		client:     &http.Client{Timeout: 10 * time.Second},
	}
}

// SendReleaseNotification sends a formatted release notification to DingTalk.
func (c *DingTalkClient) SendReleaseNotification(chartName, chartVersion string, results []ForwardResult) error {
	if c.webhookURL == "" {
		return nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## 📦 Release: %s %s\n\n", chartName, chartVersion))
	sb.WriteString(fmt.Sprintf("**时间**: %s\n\n", time.Now().Format("2006-01-02 15:04:05")))

	successCount := 0
	failCount := 0
	for _, r := range results {
		if r.Success {
			successCount++
		} else {
			failCount++
		}
	}
	sb.WriteString(fmt.Sprintf("**结果**: %d 成功 / %d 失败\n\n", successCount, failCount))

	if failCount > 0 {
		sb.WriteString("### ❌ 失败详情\n\n")
		for _, r := range results {
			if !r.Success {
				sb.WriteString(fmt.Sprintf("- **%s** (%s): %s\n", r.CustomerName, r.CustomerID, r.ErrorMessage))
			}
		}
	}

	return c.sendMarkdown(fmt.Sprintf("Release: %s %s", chartName, chartVersion), sb.String())
}

// SendStatusUpdate sends a status update for a single customer release.
func (c *DingTalkClient) SendStatusUpdate(customerName, chartName, chartVersion, status, errMsg string) error {
	if c.webhookURL == "" {
		return nil
	}
	title := fmt.Sprintf("Update: %s → %s", chartName, customerName)
	text := fmt.Sprintf("## 📊 Status Update\n\n**Customer**: %s\n**Chart**: %s %s\n**Status**: %s",
		customerName, chartName, chartVersion, status)
	if errMsg != "" {
		text += fmt.Sprintf("\n**Error**: %s", errMsg)
	}
	return c.sendMarkdown(title, text)
}

func (c *DingTalkClient) sendMarkdown(title, text string) error {
	return c.send(dingTalkMessage{
		MsgType:  "markdown",
		Markdown: &dingTalkMarkdown{Title: title, Text: text},
	})
}

func (c *DingTalkClient) send(msg dingTalkMessage) error {
	destURL := c.webhookURL
	if c.secret != "" {
		signed, err := signURL(c.webhookURL, c.secret)
		if err != nil {
			return fmt.Errorf("sign dingtalk url: %w", err)
		}
		destURL = signed
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal dingtalk message: %w", err)
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, destURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("send to dingtalk: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
		if err != nil {
			respBody = []byte("<unreadable>")
		}
		return fmt.Errorf("dingtalk returned %d: %s", resp.StatusCode, string(respBody))
	}
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
		return "", fmt.Errorf("parse webhook url: %w", err)
	}
	q := u.Query()
	q.Set("timestamp", fmt.Sprintf("%d", timestamp))
	q.Set("sign", signature)
	u.RawQuery = q.Encode()
	return u.String(), nil
}
