package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/starprince1234/Nebula-api/internal/beichen"
)

//go:embed window/*
var windowAssets embed.FS

type windowSession interface {
	ReadLocalLogin(context.Context) (*beichen.LocalLoginStatus, error)
	LoginLocally(context.Context, beichen.LocalLoginInput) (*beichen.LocalLoginStatus, error)
	RefreshLocalLogin(context.Context) (*beichen.LocalLoginStatus, error)
	CrawlDirectory(context.Context, beichen.CrawlOptions) (beichen.CrawlReport, error)
}

type loginWindow struct {
	origin  string
	host    string
	launch  chan string
	ready   chan struct{}
	done    chan struct{}
	stop    context.CancelFunc
	ctx     context.Context
	session windowSession
}

type windowRequest struct {
	Action string                  `json:"action"`
	CSRF   string                  `json:"csrf"`
	Login  beichen.LocalLoginInput `json:"login"`
}

type windowReply struct {
	CSRF    string                    `json:"csrf,omitempty"`
	Session *beichen.LocalLoginStatus `json:"session,omitempty"`
	Crawl   *beichen.CrawlRun         `json:"crawl,omitempty"`
	Notice  string                    `json:"notice,omitempty"`
	Error   string                    `json:"error,omitempty"`
	Closed  bool                      `json:"closed,omitempty"`
}

func runLoginWindow(ctx context.Context, session windowSession, open func(string) error) error {
	token, err := windowToken()
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return errors.New("无法启动本机北辰登录窗口")
	}
	defer listener.Close()
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	host := listener.Addr().String()
	window := &loginWindow{
		origin: "http://" + host, host: host, launch: make(chan string, 1), ready: make(chan struct{}), done: make(chan struct{}),
		stop: stop, ctx: ctx, session: session,
	}
	window.launch <- token
	server := &http.Server{
		Handler: window, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10,
		ErrorLog: log.New(io.Discard, "", 0),
	}
	defer func() {
		stop()
		_ = server.Close()
		select {
		case <-window.ready:
			<-window.done
		default:
		}
	}()
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	// This once-only value belongs to the local browser window. The URL
	// fragment stays in the browser and is never sent in an HTTP request.
	if err := open(window.origin + "/#" + token); err != nil {
		return errors.New("无法打开默认浏览器，请检查系统的默认浏览器设置")
	}
	startup := time.NewTimer(2 * time.Minute)
	defer startup.Stop()
	select {
	case <-window.ready:
	case <-startup.C:
		return errors.New("默认浏览器未连接到本机登录服务，请重新打开登录窗口")
	case <-ctx.Done():
		return nil
	case <-served:
		return errors.New("本机北辰登录服务意外停止")
	}
	select {
	case <-ctx.Done():
		return nil
	case <-served:
		return errors.New("本机北辰登录服务意外停止")
	}
}

func (window *loginWindow) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src ws://"+window.host+"; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	if r.Host != window.host || r.URL.RawQuery != "" || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		http.Error(w, "仅供本机登录窗口使用", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "请求方法不受支持", http.StatusMethodNotAllowed)
		return
	}
	var asset, contentType string
	switch r.URL.Path {
	case "/":
		asset, contentType = "index.html", "text/html; charset=utf-8"
	case "/window.js":
		asset, contentType = "window.js", "text/javascript; charset=utf-8"
	case "/window.css":
		asset, contentType = "window.css", "text/css; charset=utf-8"
	case "/session":
		window.connect(w, r)
		return
	default:
		http.NotFound(w, r)
		return
	}
	body, err := windowAssets.ReadFile("window/" + asset)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(body)
}

func (window *loginWindow) connect(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{
		HandshakeTimeout: 5 * time.Second,
		CheckOrigin:      func(r *http.Request) bool { return r.Header.Get("Origin") == window.origin },
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(32 << 10)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	first, err := readWindowRequest(conn)
	if err != nil || first.Action != "open" || !window.authorize(first.CSRF) {
		_ = writeWindowReply(conn, windowReply{Error: "登录窗口授权已失效，请重新运行登录命令"})
		return
	}
	defer close(window.done)
	defer window.stop()
	stopSocketClose := context.AfterFunc(window.ctx, func() { _ = conn.Close() })
	defer stopSocketClose()
	_ = conn.SetReadDeadline(time.Time{})
	close(window.ready)
	requests := make(chan windowRequest, 1)
	go func() {
		defer window.stop()
		for {
			request, err := readWindowRequest(conn)
			if err != nil {
				return
			}
			select {
			case requests <- request:
			default:
				return
			}
		}
	}()
	csrf, err := windowToken()
	if err != nil {
		return
	}
	if err := writeWindowReply(conn, window.handle(first, csrf)); err != nil {
		return
	}
	for {
		select {
		case <-window.ctx.Done():
			return
		case request := <-requests:
			if subtle.ConstantTimeCompare([]byte(request.CSRF), []byte(csrf)) != 1 {
				_ = writeWindowReply(conn, windowReply{Error: "窗口请求校验已失效，请重新打开"})
				return
			}
			csrf, err = windowToken()
			if err != nil {
				return
			}
			reply := window.handle(request, csrf)
			if err := writeWindowReply(conn, reply); err != nil || reply.Closed {
				return
			}
		}
	}
}

func (window *loginWindow) authorize(token string) bool {
	select {
	case expected := <-window.launch:
		if subtle.ConstantTimeCompare([]byte(token), []byte(expected)) == 1 {
			return true
		}
		window.launch <- expected
	default:
	}
	return false
}

func (window *loginWindow) handle(request windowRequest, csrf string) windowReply {
	ctx, cancel := context.WithTimeout(window.ctx, 40*time.Second)
	defer cancel()
	reply := windowReply{CSRF: csrf}
	var err error
	switch request.Action {
	case "open":
		reply.Session, err = window.session.ReadLocalLogin(ctx)
	case "login":
		reply.Session, err = window.session.LoginLocally(ctx, request.Login)
		reply.Notice = "已保存，本机爬虫可直接使用"
	case "refresh":
		reply.Session, err = window.session.RefreshLocalLogin(ctx)
		reply.Notice = "会话已刷新"
	case "crawl":
		var report beichen.CrawlReport
		report, err = window.session.CrawlDirectory(ctx, beichen.CrawlOptions{})
		reply.Crawl = &report.CrawlRun
		reply.Session, _ = window.session.ReadLocalLogin(ctx)
	case "close":
		reply.Closed = true
	default:
		err = errors.New("本机登录操作无效")
	}
	if err != nil {
		reply.Error, reply.Notice = err.Error(), ""
	}
	return reply
}

func readWindowRequest(conn *websocket.Conn) (windowRequest, error) {
	var request windowRequest
	messageType, raw, err := conn.ReadMessage()
	if err != nil {
		return request, err
	}
	defer clear(raw)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if messageType != websocket.TextMessage || decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF {
		return windowRequest{}, errors.New("本机登录请求格式无效")
	}
	return request, nil
}

func writeWindowReply(conn *websocket.Conn, reply windowReply) error {
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return conn.WriteJSON(reply)
}

func windowToken() (string, error) {
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", errors.New("无法生成本机登录窗口校验值")
	}
	return hex.EncodeToString(secret[:]), nil
}
