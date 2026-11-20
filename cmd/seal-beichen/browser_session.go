package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/schoolseal"
)

const maxCDPMessage = 4 << 20

type capturedBrowserSession struct {
	session schoolseal.Session
	browser *captureBrowser
}

func (captured *capturedBrowserSession) show(text string, success bool) {
	if captured == nil || captured.browser == nil || captured.browser.cdp == nil {
		return
	}
	color := "#b42318"
	if success {
		color = "#067647"
	}
	content := fmt.Sprintf(`<main style="font:18px/1.6 system-ui;max-width:720px;margin:12vh auto;padding:32px"><h1 style="color:%s">%s</h1><p>本窗口只用于本机取得北辰登录状态，不会显示或保存密码。</p></main>`, color, html.EscapeString(text))
	raw, _ := json.Marshal(content)
	expression := `(() => { document.title='北辰网页会话'; document.body.innerHTML=` + string(raw) + `; return true })()`
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var value json.RawMessage
	_ = captured.browser.cdp.call(ctx, "Runtime.evaluate", map[string]any{"expression": expression}, &value)
}

func (captured *capturedBrowserSession) close() {
	if captured != nil && captured.browser != nil {
		captured.browser.close()
	}
}

func runSession(origin, certPath, out, loginURL, edgePath string, timeout time.Duration) error {
	cert, err := os.ReadFile(certPath)
	if err != nil {
		return errors.New("无法读取已确认的学校证书文件")
	}
	recipient, err := readRecipient(context.Background(), origin, cert)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	captured, err := captureBeichenSession(ctx, loginURL, edgePath)
	if err != nil {
		return err
	}
	defer captured.close()
	sealed, err := schoolseal.SealSession(recipient, captured.session)
	if err != nil {
		captured.show("加密失败，未写入任何文件", false)
		return err
	}
	if err := writeSealed(out, append(sealed, '\n')); err != nil {
		captured.show("写入密文失败", false)
		return err
	}
	captured.show("登录会话已加密", true)
	time.Sleep(2 * time.Second)
	return nil
}

type captureBrowser struct {
	cmd        *exec.Cmd
	profileDir string
	port       int
	cdp        *cdpClient
	emailMu    sync.Mutex
	email      string
}

func captureBeichenSession(ctx context.Context, loginURL, edgePath string) (*capturedBrowserSession, error) {
	if !strings.HasPrefix(loginURL, "https://beichenapi.com/") {
		return nil, errors.New("北辰登录地址必须使用固定 HTTPS 官网")
	}
	browser, err := launchCaptureBrowser(edgePath)
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			browser.close()
		}
	}()

	cdp, err := dialPageCDP(ctx, browser.port)
	if err != nil {
		return nil, err
	}
	browser.cdp = cdp
	cdp.onRequest = func(url, postData string) {
		if !strings.Contains(url, "/api/v1/auth/login") {
			return
		}
		if email := loginEmail(postData); email != "" {
			browser.emailMu.Lock()
			browser.email = email
			browser.emailMu.Unlock()
		}
	}
	for _, method := range []string{"Page.enable", "Network.enable", "Runtime.enable"} {
		if err := cdp.call(ctx, method, map[string]any{}, nil); err != nil {
			return nil, fmt.Errorf("初始化本机登录窗口失败: %w", err)
		}
	}
	if err := cdp.call(ctx, "Page.navigate", map[string]any{"url": loginURL}, nil); err != nil {
		return nil, errors.New("打开北辰登录页失败")
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	var access, refresh, expiry string
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, errors.New("等待登录超时；请重新运行并完成人机验证")
			}
			return nil, ctx.Err()
		case <-ticker.C:
		}
		var err error
		access, refresh, expiry, err = cdp.readSessionStorage(ctx)
		if err != nil {
			return nil, err
		}
		if access != "" && refresh != "" {
			break
		}
	}

	browser.emailMu.Lock()
	email := browser.email
	browser.emailMu.Unlock()
	if email == "" {
		email, err = fetchSessionEmail(ctx, access)
		if err != nil {
			return nil, err
		}
	}
	if err := probeSession(ctx, access); err != nil {
		return nil, err
	}
	expiresAt, err := parseTokenExpiry(expiry, time.Now())
	if err != nil {
		return nil, err
	}
	session := schoolseal.Session{WebEmail: email, WebAccess: access, WebRefresh: refresh, WebExpiresAt: expiresAt.UTC()}
	if err := session.Validate(); err != nil {
		return nil, err
	}
	failed = false
	return &capturedBrowserSession{session: session, browser: browser}, nil
}

func launchCaptureBrowser(edgePath string) (*captureBrowser, error) {
	if edgePath == "" {
		edgePath = findEdge()
	}
	if edgePath == "" {
		return nil, errors.New("找不到 Microsoft Edge；请用 -edge 指定 msedge.exe")
	}
	profileDir, err := os.MkdirTemp("", "beichen-web-login-")
	if err != nil {
		return nil, errors.New("创建本机临时登录目录失败")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = os.RemoveAll(profileDir)
		return nil, errors.New("分配本机调试端口失败")
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	args := []string{
		fmt.Sprintf("--remote-debugging-port=%d", port),
		fmt.Sprintf("--remote-allow-origins=http://127.0.0.1:%d", port),
		"--user-data-dir=" + profileDir,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-networking",
		"about:blank",
	}
	cmd := exec.Command(edgePath, args...)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(profileDir)
		return nil, errors.New("启动 Microsoft Edge 失败")
	}
	return &captureBrowser{cmd: cmd, profileDir: profileDir, port: port}, nil
}

func findEdge() string {
	for _, name := range []string{"msedge.exe", "msedge"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	var candidates []string
	if runtime.GOOS == "windows" {
		for _, root := range []string{os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles"), os.Getenv("LocalAppData")} {
			if root != "" {
				candidates = append(candidates, filepath.Join(root, "Microsoft", "Edge", "Application", "msedge.exe"))
			}
		}
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate
		}
	}
	return ""
}

func (browser *captureBrowser) close() {
	if browser == nil {
		return
	}
	if browser.cdp != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = browser.cdp.call(ctx, "Browser.close", map[string]any{}, nil)
		browser.cdp.close()
		cancel()
	}
	if browser.cmd != nil && browser.cmd.Process != nil {
		_ = browser.cmd.Process.Kill()
		_, _ = browser.cmd.Process.Wait()
	}
	if browser.profileDir != "" {
		temp, _ := filepath.Abs(os.TempDir())
		profile, _ := filepath.Abs(browser.profileDir)
		if strings.HasPrefix(filepath.Base(profile), "beichen-web-login-") &&
			filepath.Dir(profile) == temp {
			for i := 0; i < 5; i++ {
				if os.RemoveAll(profile) == nil {
					break
				}
				time.Sleep(200 * time.Millisecond)
			}
		}
	}
}

type cdpTarget struct {
	Type                 string `json:"type"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

func dialPageCDP(ctx context.Context, port int) (*cdpClient, error) {
	targetURL := fmt.Sprintf("http://127.0.0.1:%d/json/list", port)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
		response, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
		if err == nil {
			body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			response.Body.Close()
			if readErr == nil && response.StatusCode == http.StatusOK {
				var targets []cdpTarget
				if json.Unmarshal(body, &targets) == nil {
					for _, target := range targets {
						if target.Type == "page" && strings.HasPrefix(target.WebSocketDebuggerURL, "ws://127.0.0.1:") {
							return newCDPClient(ctx, target.WebSocketDebuggerURL)
						}
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return nil, errors.New("等待本机登录窗口启动超时")
}

type cdpClient struct {
	conn      *websocket.Conn
	writeMu   sync.Mutex
	pendingMu sync.Mutex
	pending   map[int64]chan cdpReply
	nextID    int64
	onRequest func(url, postData string)
	closed    chan struct{}
}

type cdpReply struct {
	Result json.RawMessage
	Err    error
}

type cdpEnvelope struct {
	ID     int64           `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func newCDPClient(ctx context.Context, wsURL string) (*cdpClient, error) {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return nil, errors.New("连接本机登录窗口失败")
	}
	client := &cdpClient{conn: conn, pending: make(map[int64]chan cdpReply), closed: make(chan struct{})}
	go client.readLoop()
	return client, nil
}

func (client *cdpClient) readLoop() {
	defer close(client.closed)
	for {
		_, raw, err := client.conn.ReadMessage()
		if err != nil {
			client.failPending(err)
			return
		}
		if len(raw) > maxCDPMessage {
			client.failPending(errors.New("登录窗口消息过大"))
			return
		}
		var envelope cdpEnvelope
		if json.Unmarshal(raw, &envelope) != nil {
			continue
		}
		if envelope.ID != 0 {
			client.pendingMu.Lock()
			deliver := client.pending[envelope.ID]
			delete(client.pending, envelope.ID)
			client.pendingMu.Unlock()
			if deliver != nil {
				if envelope.Error != nil {
					deliver <- cdpReply{Err: errors.New(envelope.Error.Message)}
				} else {
					deliver <- cdpReply{Result: envelope.Result}
				}
			}
			continue
		}
		if envelope.Method == "Network.requestWillBeSent" && client.onRequest != nil {
			var event struct {
				Request struct {
					URL      string `json:"url"`
					PostData string `json:"postData"`
				} `json:"request"`
			}
			if json.Unmarshal(envelope.Params, &event) == nil {
				client.onRequest(event.Request.URL, event.Request.PostData)
			}
		}
	}
}

func (client *cdpClient) failPending(err error) {
	client.pendingMu.Lock()
	defer client.pendingMu.Unlock()
	for id, deliver := range client.pending {
		delete(client.pending, id)
		deliver <- cdpReply{Err: err}
	}
}

func (client *cdpClient) call(ctx context.Context, method string, params any, result any) error {
	client.pendingMu.Lock()
	client.nextID++
	id := client.nextID
	deliver := make(chan cdpReply, 1)
	client.pending[id] = deliver
	client.pendingMu.Unlock()
	defer func() {
		client.pendingMu.Lock()
		delete(client.pending, id)
		client.pendingMu.Unlock()
	}()
	message, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err != nil {
		return err
	}
	client.writeMu.Lock()
	err = client.conn.WriteMessage(websocket.TextMessage, message)
	client.writeMu.Unlock()
	if err != nil {
		return err
	}
	select {
	case reply := <-deliver:
		if reply.Err != nil {
			return reply.Err
		}
		if result != nil && len(reply.Result) > 0 {
			return json.Unmarshal(reply.Result, result)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-client.closed:
		return errors.New("本机登录窗口已关闭")
	}
}

func (client *cdpClient) close() {
	if client != nil && client.conn != nil {
		_ = client.conn.Close()
	}
}

func (client *cdpClient) readSessionStorage(ctx context.Context) (access, refresh, expiry string, err error) {
	const expression = `(() => {
  const local = window.localStorage || {}, session = window.sessionStorage || {};
  const read = (keys) => { for (const key of keys) { const value = local.getItem(key) || session.getItem(key); if (value) return value; } return ''; };
  return JSON.stringify({
    access: read(['auth_token','access_token']),
    refresh: read(['refresh_token','refreshtoken']),
    expires: read(['token_expires_at','expires_at','expiresAt'])
  });
})()`
	var reply struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
		ExceptionDetails json.RawMessage `json:"exceptionDetails"`
	}
	if err := client.call(ctx, "Runtime.evaluate", map[string]any{"expression": expression, "returnByValue": true}, &reply); err != nil {
		return "", "", "", errors.New("读取本机登录状态失败")
	}
	if len(reply.ExceptionDetails) > 0 {
		return "", "", "", errors.New("北辰登录页尚未准备完成")
	}
	var values struct {
		Access  string `json:"access"`
		Refresh string `json:"refresh"`
		Expires string `json:"expires"`
	}
	if json.Unmarshal([]byte(reply.Result.Value), &values) != nil {
		return "", "", "", errors.New("北辰登录状态格式无效")
	}
	return strings.TrimSpace(strings.TrimPrefix(values.Access, "Bearer ")), strings.TrimSpace(values.Refresh), values.Expires, nil
}

func loginEmail(postData string) string {
	if postData == "" {
		return ""
	}
	var fields map[string]any
	if json.Unmarshal([]byte(postData), &fields) == nil {
		for _, key := range []string{"email", "username", "identifier", "account"} {
			if value, ok := fields[key].(string); ok {
				return strings.TrimSpace(value)
			}
		}
	}
	values, err := url.ParseQuery(postData)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(values.Get("email"))
}

func parseTokenExpiry(raw string, now time.Time) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, errors.New("北辰登录状态缺少令牌到期时间")
	}
	if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return parsed, nil
	}
	if number, err := strconv.ParseFloat(raw, 64); err == nil && number > 0 {
		integer := int64(number)
		if integer > 1_000_000_000_000 {
			return time.UnixMilli(integer), nil
		}
		if integer > 1_000_000_000 {
			return time.Unix(integer, 0), nil
		}
	}
	return time.Time{}, fmt.Errorf("北辰令牌到期时间无法识别（相对时间 %q 不受支持）", raw)
}

func fetchSessionEmail(ctx context.Context, access string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://beichenapi.com/api/v1/auth/me", nil)
	if err != nil {
		return "", errors.New("构建北辰账号查询失败")
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-User-UI-Request", "1")
	response, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return "", errors.New("读取北辰登录账号失败")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil || response.StatusCode != http.StatusOK {
		return "", errors.New("北辰登录已成功但无法确认账号邮箱")
	}
	var reply struct {
		Code *int `json:"code"`
		Data struct {
			Email    string `json:"email"`
			Username string `json:"username"`
			User     struct {
				Email    string `json:"email"`
				Username string `json:"username"`
			} `json:"user"`
		} `json:"data"`
		Email string `json:"email"`
	}
	if json.Unmarshal(body, &reply) != nil || (reply.Code != nil && *reply.Code != 0) {
		return "", errors.New("北辰登录已成功但账号响应格式无效")
	}
	for _, candidate := range []string{reply.Data.Email, reply.Data.User.Email, reply.Email} {
		if normalized := strings.TrimSpace(candidate); normalized != "" {
			return normalized, nil
		}
	}
	return "", errors.New("北辰登录已成功但账号没有可确认的邮箱")
}

func probeSession(ctx context.Context, access string) error {
	for _, endpoint := range []string{
		"https://beichenapi.com/api/v1/keys?page_size=1&page=1",
		"https://beichenapi.com/api/v1/channels/available",
	} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return errors.New("构建北辰会话校验失败")
		}
		req.Header.Set("Authorization", "Bearer "+access)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-User-UI-Request", "1")
		response, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
		if err != nil {
			return errors.New("北辰登录成功，但会话校验请求失败")
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		response.Body.Close()
		if readErr != nil || response.StatusCode != http.StatusOK {
			return errors.New("北辰登录成功，但会话不能读取价格或 Key 数据")
		}
		var reply struct {
			Code *int `json:"code"`
		}
		if json.Unmarshal(body, &reply) != nil || (reply.Code != nil && *reply.Code != 0) {
			return errors.New("北辰登录成功，但会话校验响应无效")
		}
	}
	return nil
}
