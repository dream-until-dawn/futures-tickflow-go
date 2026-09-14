package shinnysource

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// session 交出缓存的 token 与 mdurl；没有就去取。
func (c *Client) session(ctx context.Context) (tok, md string, err error) {
	c.mu.Lock()
	tok, md = c.tok, c.md
	c.mu.Unlock()
	if tok == "" {
		if tok, err = c.token(ctx); err != nil {
			return "", "", err
		}
	}
	if md == "" {
		if md, err = c.mdURL(ctx, tok); err != nil {
			return "", "", err
		}
	}
	c.mu.Lock()
	c.tok, c.md = tok, md
	c.mu.Unlock()
	return tok, md, nil
}

// forget 作废缓存的 token（握手被拒时用）。mdurl 一并作废：它是拿那个 token 问来的。
func (c *Client) forget() {
	c.mu.Lock()
	c.tok, c.md = "", ""
	c.mu.Unlock()
}

func (c *Client) token(ctx context.Context) (string, error) {
	form := url.Values{
		"grant_type":    {"password"},
		"client_id":     {c.cfg.ClientID},
		"client_secret": {c.cfg.ClientSecret},
		"username":      {c.cfg.User},
		"password":      {c.cfg.Password},
	}
	rq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.AuthURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("%w：造请求失败：%v", ErrAuth, err)
	}
	rq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rq.Header.Set("User-Agent", c.cfg.UserAgent)
	rs, err := c.cfg.HTTPClient.Do(rq)
	if err != nil {
		return "", fmt.Errorf("%w：%w", ErrAuth, err)
	}
	defer rs.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(rs.Body, 1<<20))
	if rs.StatusCode != http.StatusOK {
		// ⚠️ 响应体不进报文：它可能回显表单里的东西。
		return "", fmt.Errorf("%w：HTTP %d —— 账户密码对的前提下，最可能是 client_secret 被轮换了（design.md 约束一）",
			ErrAuth, rs.StatusCode)
	}
	var tr struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(b, &tr); err != nil || tr.AccessToken == "" {
		return "", fmt.Errorf("%w：HTTP 200 而响应里没有 access_token（%d 字节，解析错误 %v）", ErrAuth, len(b), err)
	}
	return tr.AccessToken, nil
}

func (c *Client) mdURL(ctx context.Context, tok string) (string, error) {
	rq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.NSURL, nil)
	if err != nil {
		return "", fmt.Errorf("%w：造请求失败：%v", ErrNoMdURL, err)
	}
	rq.Header.Set("Authorization", "Bearer "+tok)
	rq.Header.Set("Accept", "application/json")
	rq.Header.Set("User-Agent", c.cfg.UserAgent)
	rs, err := c.cfg.HTTPClient.Do(rq)
	if err != nil {
		return "", fmt.Errorf("%w：%w", ErrNoMdURL, err)
	}
	defer rs.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(rs.Body, 1<<20))
	var r struct {
		MdURL string `json:"mdurl"`
	}
	_ = json.Unmarshal(b, &r)
	if rs.StatusCode != http.StatusOK || r.MdURL == "" {
		return "", fmt.Errorf("%w：HTTP %d，响应 %d 字节", ErrNoMdURL, rs.StatusCode, len(b))
	}
	return r.MdURL, nil
}
