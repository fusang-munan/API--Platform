// Command seal-beichen reads a four-channel configuration from standard input
// and writes only a sealed box for the school. The public key must arrive over
// HTTPS authenticated with the school's independently confirmed certificate.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/starprince1234/Nebula-api/internal/infrastructure/schoolseal"
)

const schoolOrigin = "https://106.55.103.208:25555"

func main() {
	cert := flag.String("certificate", "", "经学校独立确认的 HTTPS 叶证书文件，支持 PEM 或 DER")
	out := flag.String("out", "", "Git 发布中要保存的密文文件；默认按模式选择")
	check := flag.Bool("check", false, "仅确认学校收件公钥就绪")
	sessionMode := flag.Bool("session", false, "打开北辰登录页并捕获网页面板会话")
	login := flag.String("login", "https://beichenapi.com/login", "北辰网页登录地址")
	edge := flag.String("edge", "", "Microsoft Edge 可执行文件；留空自动查找")
	timeout := flag.Duration("timeout", 10*time.Minute, "等待本人完成北辰登录的最长时间")
	flag.Parse()
	if *out == "" {
		if *sessionMode {
			*out = "internal/infrastructure/schoolseal/school-session.sealed.json"
		} else {
			*out = "internal/infrastructure/schoolseal/school-config.sealed.json"
		}
	}
	var err error
	if *check {
		err = run(schoolOrigin, *cert, *out, true, os.Stdin)
	} else if *sessionMode {
		err = runSession(schoolOrigin, *cert, *out, *login, *edge, *timeout)
	} else {
		err = run(schoolOrigin, *cert, *out, *check, os.Stdin)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if !*check {
		if *sessionMode {
			fmt.Println("北辰网页会话已加密，可随原 GitHub 自动部署发布。")
		} else {
			fmt.Println("学校配置已加密，可随原 GitHub 自动部署发布。")
		}
	}
}

func readRecipient(ctx context.Context, origin string, rawCert []byte) (schoolseal.Recipient, error) {
	base, err := url.Parse(origin)
	if err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil || base.Path != "" || base.RawQuery != "" || base.Fragment != "" {
		return schoolseal.Recipient{}, errors.New("学校收件公钥须从已核实的 HTTPS 入口读取")
	}
	if block, rest := pem.Decode(rawCert); block != nil {
		if block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
			return schoolseal.Recipient{}, errors.New("学校证书文件须为单张叶证书")
		}
		rawCert = block.Bytes
	}
	leaf, err := x509.ParseCertificate(rawCert)
	if err != nil || leaf.IsCA {
		return schoolseal.Recipient{}, errors.New("学校叶证书格式无效")
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	transport := &http.Transport{TLSClientConfig: &tls.Config{
		MinVersion: tls.VersionTLS12, RootCAs: pool,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 || !bytes.Equal(cs.PeerCertificates[0].Raw, leaf.Raw) {
				return errors.New("学校 HTTPS 证书指纹改变")
			}
			return nil
		},
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+schoolseal.PublicPath, nil)
	if err != nil {
		return schoolseal.Recipient{}, errors.New("学校地址无效")
	}
	response, err := client.Do(req)
	if err != nil {
		return schoolseal.Recipient{}, fmt.Errorf("读取已核实学校的配置公钥失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return schoolseal.Recipient{}, fmt.Errorf("学校配置公钥入口返回 HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 2049))
	var r schoolseal.Recipient
	if err != nil || len(data) > 2048 || json.Unmarshal(data, &r) != nil || r.Validate() != nil {
		return schoolseal.Recipient{}, errors.New("学校尚未提供有效的收件公钥")
	}
	return r, nil
}

func run(origin, certPath, out string, check bool, input io.Reader) error {
	cert, err := os.ReadFile(certPath)
	if err != nil {
		return errors.New("无法读取已确认的学校证书文件")
	}
	recipient, err := readRecipient(context.Background(), origin, cert)
	if err != nil {
		return err
	}
	if check {
		fmt.Println("已通过学校 HTTPS 证书核实收件公钥。")
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(input, schoolseal.MaxPlaintext+1))
	if err != nil || len(raw) > schoolseal.MaxPlaintext {
		return errors.New("读取保密配置失败或长度超限")
	}
	defer clear(raw)
	var cfg schoolseal.Config
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&cfg) != nil || dec.Decode(new(any)) != io.EOF {
		return errors.New("保密配置 JSON 无效")
	}
	sealed, err := schoolseal.Seal(recipient, cfg)
	if err != nil {
		return err
	}
	return writeSealed(out, append(sealed, '\n'))
}

func writeSealed(out string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(out), ".beichen-sealed-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), out)
}
