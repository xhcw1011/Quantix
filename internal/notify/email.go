package notify

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"time"
)

// smtpDialTimeout is the maximum time allowed for SMTP connection + send.
const smtpDialTimeout = 15 * time.Second

// emailSender sends plain-text emails via SMTP.
// Supports STARTTLS (port 587, default) and implicit TLS (port 465).
type emailSender struct {
	host     string
	port     int
	user     string
	password string
	from     string
	to       string
}

func newEmailSender(host string, port int, user, password, from, to string) *emailSender {
	if host == "" || to == "" {
		return nil
	}
	if port == 0 {
		port = 587
	}
	return &emailSender{host: host, port: port, user: user, password: password, from: from, to: to}
}

// send delivers a plain-text email. Subject is prefixed with "[Quantix]".
func (e *emailSender) send(subject, body string) error {
	addr := net.JoinHostPort(e.host, strconv.Itoa(e.port))
	msg := []byte(
		"From: " + e.from + "\r\n" +
			"To: " + e.to + "\r\n" +
			"Subject: [Quantix] " + subject + "\r\n" +
			"Content-Type: text/plain; charset=UTF-8\r\n" +
			"\r\n" +
			body,
	)
	auth := smtp.PlainAuth("", e.user, e.password, e.host)
	if e.port == 465 {
		return e.sendTLS(addr, auth, msg)
	}
	return smtp.SendMail(addr, auth, e.from, []string{e.to}, msg)
}

// sendTLS uses an implicit TLS connection (port 465 / SMTPS).
func (e *emailSender) sendTLS(addr string, auth smtp.Auth, msg []byte) error {
	dialer := &net.Dialer{Timeout: smtpDialTimeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: e.host})
	if err != nil {
		return fmt.Errorf("tls dial: %w", err)
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, e.host)
	if err != nil {
		return fmt.Errorf("smtp client: %w", err)
	}
	defer client.Close() //nolint:errcheck

	if err := client.Auth(auth); err != nil {
		return fmt.Errorf("smtp auth: %w", err)
	}
	if err := client.Mail(e.from); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}
	if err := client.Rcpt(e.to); err != nil {
		return fmt.Errorf("smtp rcpt: %w", err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	return w.Close()
}
