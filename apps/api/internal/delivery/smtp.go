package delivery

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"net"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"
)

// SMTPConfig uses implicit TLS submission. Credentials are supplied by the
// caller; this package never reads an account, environment variable, or secret.
type SMTPConfig struct {
	Address    string
	ServerName string
	HelloName  string
	Username   string
	Password   string
	Timeout    time.Duration
	RootCAs    *x509.CertPool
}

type SMTP struct{ config SMTPConfig }

type State string

const (
	PreDataFailure     State = "pre_data_failure"
	RejectedBeforeData State = "rejected_before_data"
	RejectedAfterData  State = "rejected_after_data"
	AcceptedBySMTP     State = "accepted_by_smtp"
	Uncertain          State = "uncertain"
)

type Outcome struct {
	State     State
	Stage     string
	SMTPCode  int
	MessageID string
	Digest    string
	Err       error
}

func NewSMTP(cfg SMTPConfig) (*SMTP, error) {
	if _, _, err := net.SplitHostPort(cfg.Address); err != nil {
		return nil, errors.New("SMTP address must be host:port")
	}
	if cfg.ServerName == "" || !asciiHeader(cfg.ServerName) || strings.ContainsAny(cfg.ServerName, " /\\:@") {
		return nil, errors.New("invalid TLS server name")
	}
	if cfg.HelloName == "" || !asciiHeader(cfg.HelloName) || strings.ContainsAny(cfg.HelloName, " /\\:@") {
		return nil, errors.New("invalid SMTP hello name")
	}
	if cfg.Username == "" || !asciiHeader(cfg.Username) || strings.ContainsAny(cfg.Username, "\r\n\x00") || cfg.Password == "" || strings.ContainsAny(cfg.Password, "\r\n\x00") {
		return nil, errors.New("invalid SMTP credentials")
	}
	if cfg.Timeout <= 0 || cfg.Timeout > 5*time.Minute {
		return nil, errors.New("SMTP timeout must be within five minutes")
	}
	return &SMTP{config: cfg}, nil
}

// Send compares the caller's approved digest with the canonical MIME bytes
// before opening a connection. A 250 after DATA means SMTP submission only;
// no recipient receipt is inferred. Uncertain outcomes must never be retried
// automatically, because the server may already have accepted the message.
func (s *SMTP) Send(ctx context.Context, material Material, expectedDigest string) Outcome {
	out := Outcome{State: PreDataFailure, Stage: "validation", MessageID: material.messageID, Digest: material.digest}
	if s == nil || ctx == nil || len(material.mime) == 0 || material.from == "" || material.to == "" || len(expectedDigest) != 64 {
		out.Err = errors.New("invalid SMTP submission material")
		return out
	}
	actual := sha256.Sum256(material.mime)
	actualHex := hex.EncodeToString(actual[:])
	if subtle.ConstantTimeCompare([]byte(actualHex), []byte(material.digest)) != 1 || subtle.ConstantTimeCompare([]byte(actualHex), []byte(strings.ToLower(expectedDigest))) != 1 {
		out.Err = errors.New("approved MIME digest mismatch")
		return out
	}
	if err := ctx.Err(); err != nil {
		out.Err = err
		return out
	}

	cfg := s.config
	deadline := time.Now().Add(cfg.Timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	dialer := net.Dialer{Deadline: deadline}
	raw, err := dialer.DialContext(ctx, "tcp", cfg.Address)
	if err != nil {
		out.Stage, out.Err = "connect", err
		return out
	}
	conn := tls.Client(raw, &tls.Config{ServerName: cfg.ServerName, RootCAs: cfg.RootCAs, MinVersion: tls.VersionTLS12})
	defer conn.Close()
	if err := conn.SetDeadline(deadline); err != nil {
		out.Stage, out.Err = "deadline", err
		return out
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if err := conn.HandshakeContext(ctx); err != nil {
		out.Stage, out.Err = "tls", err
		return out
	}
	client, err := smtp.NewClient(conn, cfg.ServerName)
	if err != nil {
		return classify(out, "greeting", err, false)
	}
	defer client.Close()
	if err := client.Hello(cfg.HelloName); err != nil {
		return classify(out, "hello", err, false)
	}
	if err := client.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.ServerName)); err != nil {
		return classify(out, "auth", err, false)
	}
	if err := client.Mail(material.from); err != nil {
		return classify(out, "mail", err, false)
	}
	if err := client.Rcpt(material.to); err != nil {
		return classify(out, "recipient", err, false)
	}
	writer, err := client.Data()
	if err != nil {
		return classify(out, "data_command", err, false)
	}
	if _, err := writer.Write(material.mime); err != nil {
		out.State, out.Stage, out.Err = Uncertain, "data_write", err
		return out
	}
	if err := writer.Close(); err != nil {
		return classify(out, "data_reply", err, true)
	}
	out.State, out.Stage, out.SMTPCode = AcceptedBySMTP, "data_reply", 250
	return out
}

func classify(out Outcome, stage string, err error, afterData bool) Outcome {
	out.Stage, out.Err = stage, err
	var reply *textproto.Error
	if errors.As(err, &reply) {
		out.SMTPCode = reply.Code
		if reply.Code >= 400 && reply.Code <= 599 && afterData {
			out.State = RejectedAfterData
		} else if reply.Code >= 400 && reply.Code <= 599 {
			out.State = RejectedBeforeData
		} else if afterData {
			out.State = Uncertain
		} else {
			out.State = PreDataFailure
		}
	} else if afterData {
		out.State = Uncertain
	} else {
		out.State = PreDataFailure
	}
	return out
}
