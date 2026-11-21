package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/starprince1234/Nebula-api/internal/beichen"
)

type testWindowAccount struct {
	t     *testing.T
	saved bool
}

func (account *testWindowAccount) ReadLocalLogin(context.Context) (*beichen.LocalLoginStatus, error) {
	if !account.saved {
		return nil, nil
	}
	return &beichen.LocalLoginStatus{Account: "window@example.test", CanRefresh: true}, nil
}

func (account *testWindowAccount) LoginLocally(ctx context.Context, input beichen.LocalLoginInput) (*beichen.LocalLoginStatus, error) {
	if input.Mode != "password" || input.Email != "window@example.test" || input.Password != "fake-window-password" || input.Code != "739250" || input.Remember {
		account.t.Error("wrong local login input")
		return nil, errors.New("无效的测试输入")
	}
	account.saved = true
	return account.ReadLocalLogin(ctx)
}

func (account *testWindowAccount) RefreshLocalLogin(ctx context.Context) (*beichen.LocalLoginStatus, error) {
	return account.ReadLocalLogin(ctx)
}

func (account *testWindowAccount) CrawlDirectory(context.Context, beichen.CrawlOptions) (beichen.CrawlReport, error) {
	if !account.saved {
		account.t.Error("crawl ran before login")
	}
	return beichen.CrawlReport{CrawlRun: beichen.CrawlRun{PlatformCount: 2, ModelCount: 3,
		Failures: []beichen.CrawlFailure{{Path: "data[0].platforms[2]", Reason: "测试平台缺少模型"}},
	}}, errors.New("北辰目录有部分失败")
}

func TestLoginWindowLoopbackCSRFAndCrawl(t *testing.T) {
	for _, finish := range []string{"button", "tab", "replay"} {
		t.Run(finish, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			opened, stopped := make(chan string, 1), make(chan error, 1)
			go func() {
				stopped <- runLoginWindow(ctx, &testWindowAccount{t: t}, func(address string) error {
					opened <- address
					return nil
				})
			}()
			var page *url.URL
			select {
			case address := <-opened:
				var err error
				page, err = url.Parse(address)
				if err != nil || page.Hostname() != "127.0.0.1" || page.Port() == "" || len(page.Fragment) != 64 {
					t.Fatal("window did not bind to a random loopback port")
				}
			case err := <-stopped:
				t.Fatalf("window stopped before starting: %v", err)
			case <-ctx.Done():
				t.Fatal("window startup timed out")
			}
			origin := page.Scheme + "://" + page.Host
			client := &http.Client{Timeout: 3 * time.Second}
			response, err := client.Get(origin + "/")
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil || response.StatusCode != http.StatusOK || response.Header.Get("Cache-Control") != "no-store" ||
				!strings.Contains(string(body), "记住账号密码") || !strings.Contains(string(body), "测试抓取") ||
				strings.Contains(string(body), page.Fragment) || strings.Contains(string(body), "导出") {
				t.Fatal("unexpected login document")
			}
			badHost, _ := http.NewRequest(http.MethodGet, origin, nil)
			badHost.Host = "rebound.example.test"
			response, err = client.Do(badHost)
			if err != nil || response.StatusCode != http.StatusForbidden {
				t.Fatalf("unexpected response to a foreign host: %v", err)
			}
			response.Body.Close()
			dialer := &websocket.Dialer{HandshakeTimeout: 3 * time.Second}
			socket := "ws://" + page.Host + "/session"
			foreign, response, err := dialer.Dial(socket, http.Header{"Origin": {"https://foreign.example.test"}})
			if foreign != nil {
				foreign.Close()
			}
			if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
				t.Fatal("foreign site could connect to the login service")
			}
			response.Body.Close()
			connect := func() *websocket.Conn {
				t.Helper()
				conn, _, err := dialer.Dial(socket, http.Header{"Origin": {origin}})
				if err != nil {
					t.Fatal(err)
				}
				_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
				t.Cleanup(func() { _ = conn.Close() })
				return conn
			}
			send := func(conn *websocket.Conn, request windowRequest) windowReply {
				t.Helper()
				if err := conn.WriteJSON(request); err != nil {
					t.Fatal(err)
				}
				var reply windowReply
				if err := conn.ReadJSON(&reply); err != nil {
					t.Fatal(err)
				}
				return reply
			}
			if reply := send(connect(), windowRequest{Action: "open", CSRF: "guess"}); reply.Error == "" {
				t.Fatal("guessed bootstrap key was accepted")
			}
			conn := connect()
			initial := send(conn, windowRequest{Action: "open", CSRF: page.Fragment})
			if initial.Error != "" || initial.Session != nil || initial.CSRF == "" || initial.CSRF == page.Fragment {
				t.Fatalf("window handshake failed: %s", initial.Error)
			}
			if reply := send(connect(), windowRequest{Action: "open", CSRF: page.Fragment}); reply.Error == "" {
				t.Fatal("bootstrap key could be reused")
			}
			loggedIn := send(conn, windowRequest{Action: "login", CSRF: initial.CSRF, Login: beichen.LocalLoginInput{
				PanelAccountLogin: beichen.PanelAccountLogin{Mode: "password", Email: "window@example.test", Password: "fake-window-password"}, Code: "739250",
			}})
			if loggedIn.Error != "" || loggedIn.Session == nil || loggedIn.Notice != "已保存，本机爬虫可直接使用" {
				t.Fatalf("login view failed: %s", loggedIn.Error)
			}
			crawl := send(conn, windowRequest{Action: "crawl", CSRF: loggedIn.CSRF})
			if crawl.Crawl == nil || crawl.Crawl.PlatformCount != 2 || crawl.Crawl.ModelCount != 3 || len(crawl.Crawl.Failures) != 1 {
				t.Fatal("window did not show the result of the test crawl")
			}
			switch finish {
			case "button":
				if reply := send(conn, windowRequest{Action: "close", CSRF: crawl.CSRF}); !reply.Closed {
					t.Fatal("window did not acknowledge the close button")
				}
			case "tab":
				_ = conn.Close()
			case "replay":
				if reply := send(conn, windowRequest{Action: "refresh", CSRF: loggedIn.CSRF}); reply.Error == "" {
					t.Fatal("action accepted a consumed CSRF token")
				}
			}
			select {
			case err := <-stopped:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("local service survived closing the window")
			}
			if listener, err := net.DialTimeout("tcp", page.Host, 100*time.Millisecond); err == nil {
				listener.Close()
				t.Fatal("loopback port remained open")
			}
		})
	}
}
