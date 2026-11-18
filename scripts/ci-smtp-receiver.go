//go:build ignore

// Standalone SMTP sink built explicitly by the lab backend integration test.
package main

import (
	"encoding/json"
	"io"
	"net"
	"net/textproto"
	"os"
	"strings"
	"time"
)

func main() {
	listener, err := net.Listen("tcp", ":2525")
	if err != nil {
		os.Exit(1)
	}
	for {
		conn, err := listener.Accept()
		if err != nil {
			os.Exit(1)
		}
		receive(conn)
	}
}

func receive(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	smtp := textproto.NewConn(conn)
	_ = smtp.PrintfLine("220 CI SMTP sink")
	var recipients []string
	for {
		line, err := smtp.ReadLine()
		if err != nil {
			return
		}
		switch command := strings.ToUpper(line); {
		case strings.HasPrefix(command, "EHLO "), strings.HasPrefix(command, "HELO "):
			_ = smtp.PrintfLine("250 CI SMTP sink")
		case strings.HasPrefix(command, "MAIL FROM:"):
			recipients = nil
			_ = smtp.PrintfLine("250 OK")
		case strings.HasPrefix(command, "RCPT TO:"):
			recipients = append(recipients, strings.Trim(line[len("RCPT TO:"):], " <>"))
			_ = smtp.PrintfLine("250 OK")
		case command == "DATA" && len(recipients) > 0:
			_ = smtp.PrintfLine("354 End with a dot")
			message, err := io.ReadAll(io.LimitReader(smtp.DotReader(), 64<<10))
			if err != nil || len(message) == 64<<10 || save(recipients, string(message)) != nil {
				_ = smtp.PrintfLine("451 Capture failed")
				return
			}
			recipients = nil
			_ = smtp.PrintfLine("250 Captured")
		case command == "QUIT":
			_ = smtp.PrintfLine("221 Bye")
			return
		default:
			_ = smtp.PrintfLine("502 Unsupported command")
		}
	}
}

func save(recipients []string, message string) error {
	file, err := os.CreateTemp("", "smtp-pending-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	err = json.NewEncoder(file).Encode(struct {
		Recipients []string `json:"recipients"`
		Message    string   `json:"message"`
	}{recipients, message})
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), file.Name()+".json")
}
