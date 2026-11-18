package mail

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestSMTPDeadlineBeforeServerResponds(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want string
	}{
		{mode: "none", want: "create SMTP client:"},
		{mode: "starttls", want: "create SMTP client:"},
		{mode: "tls", want: "handshake SMTP TLS:"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			if err := listener.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
				t.Fatal(err)
			}
			address := listener.Addr().(*net.TCPAddr)
			sender, err := NewSMTP(address.IP.String(), address.Port, "", "", "sender@example.test", "", tc.mode, 150*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() {
				result <- sender.Send(context.Background(), "recipient@example.test", "Test", "Test")
			}()
			conn, err := listener.AcceptTCP()
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			// Keep the connection open without sending any SMTP or TLS bytes.
			select {
			case err := <-result:
				if err == nil || !strings.HasPrefix(err.Error(), tc.want) {
					t.Fatalf("Send returned %v, want an error starting with %q", err, tc.want)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("Send remained blocked on the silent SMTP server")
			}
		})
	}
}
