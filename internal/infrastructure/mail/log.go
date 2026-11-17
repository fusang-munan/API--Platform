package mail

import (
	"context"
	"log/slog"
)

// LogSender writes verification emails to server logs (development / lab only).
type LogSender struct{}

func NewLogSender() *LogSender {
	return &LogSender{}
}

func (s *LogSender) Send(_ context.Context, to, subject, text string) error {
	slog.Info("mail delivery (log sender)",
		"to", to,
		"subject", subject,
		"body", text,
	)
	return nil
}
