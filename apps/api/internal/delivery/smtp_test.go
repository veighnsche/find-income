package delivery

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"io"
	"math/big"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"
	"time"
)

func syntheticMaterial(t *testing.T) Material {
	t.Helper()
	file := []byte("synthetic application PDF bytes\n")
	digest := sha256.Sum256(file)
	m, err := Prepare(Input{
		From: "applicant@example.test", To: "applications@example.test",
		MessageID: "<attempt-17@example.test>", Date: time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC),
		Subject: "Candidature ingénieur logiciel — équipe européenne", Body: "Bonjour,\nVoici ma candidature à Bruxelles.\n",
		Attachments: []Attachment{{Filename: "application.pdf", ContentType: "application/pdf", Data: file, SHA256: hex.EncodeToString(digest[:])}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

type smtpScenario int

const (
	accept smtpScenario = iota
	rejectRecipient
	rejectAfterData
	unexpectedPositiveAfterData
	loseDataReply
)

type smtpFixture struct {
	address string
	roots   *x509.CertPool
	data    <-chan []byte
	done    <-chan struct{}
}

func newSMTPFixture(t *testing.T, scenario smtpScenario) smtpFixture {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "smtp.test"},
		DNSNames: []string{"smtp.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	roots := x509.NewCertPool()
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots.AddCert(parsed)
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	data := make(chan []byte, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		r := bufio.NewReader(conn)
		w := bufio.NewWriter(conn)
		reply := func(s string) bool {
			_, err := w.WriteString(s)
			if err != nil {
				return false
			}
			return w.Flush() == nil
		}
		if !reply("220 smtp.test ESMTP\r\n") {
			return
		}
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			switch {
			case strings.HasPrefix(line, "EHLO "):
				if !reply("250-smtp.test\r\n250 AUTH PLAIN\r\n") {
					return
				}
			case strings.HasPrefix(line, "AUTH PLAIN "):
				if !reply("235 2.7.0 authenticated\r\n") {
					return
				}
			case strings.HasPrefix(line, "MAIL FROM:"):
				if !reply("250 sender accepted\r\n") {
					return
				}
			case strings.HasPrefix(line, "RCPT TO:"):
				if scenario == rejectRecipient {
					_ = reply("550 mailbox rejected\r\n")
					return
				}
				if !reply("250 recipient accepted\r\n") {
					return
				}
			case line == "DATA\r\n":
				if !reply("354 send data\r\n") {
					return
				}
				var message bytes.Buffer
				for {
					part, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if part == ".\r\n" {
						break
					}
					if strings.HasPrefix(part, "..") {
						part = part[1:]
					}
					message.WriteString(part)
				}
				data <- message.Bytes()
				switch scenario {
				case accept:
					_ = reply("250 2.0.0 queued\r\n")
				case rejectAfterData:
					_ = reply("554 5.6.0 content rejected\r\n")
				case unexpectedPositiveAfterData:
					_ = reply("251 unexpected final reply\r\n")
				case loseDataReply:
					// DATA has reached the server; the client times out without a reply.
					_, _ = io.Copy(io.Discard, r)
				}
				return
			default:
				_ = reply("500 unexpected command\r\n")
				return
			}
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	return smtpFixture{address: listener.Addr().String(), roots: roots, data: data, done: done}
}

func smtpForFixture(t *testing.T, f smtpFixture) *SMTP {
	t.Helper()
	s, err := NewSMTP(SMTPConfig{Address: f.address, ServerName: "smtp.test", HelloName: "client.test", Username: "synthetic-user", Password: "synthetic-password", Timeout: time.Second, RootCAs: f.roots})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSMTPProtocolOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		scenario smtpScenario
		want     State
		code     int
		sent     bool
	}{
		{"accepted", accept, AcceptedBySMTP, 250, true},
		{"recipient-rejected-before-data", rejectRecipient, RejectedBeforeData, 550, false},
		{"content-rejected-after-data", rejectAfterData, RejectedAfterData, 554, true},
		{"unexpected-positive-after-data", unexpectedPositiveAfterData, Uncertain, 251, true},
		{"reply-lost-after-data", loseDataReply, Uncertain, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSMTPFixture(t, tc.scenario)
			m := syntheticMaterial(t)
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			out := smtpForFixture(t, f).Send(ctx, m, m.Digest())
			if out.State != tc.want || out.SMTPCode != tc.code {
				t.Fatalf("state=%s code=%d stage=%s err=%v, want %s/%d", out.State, out.SMTPCode, out.Stage, out.Err, tc.want, tc.code)
			}
			if tc.sent {
				select {
				case data := <-f.data:
					if !bytes.Equal(data, m.Bytes()) {
						t.Fatal("SMTP DATA changed canonical MIME bytes")
					}
					if tc.scenario == accept {
						inspectMIME(t, data)
					}
				default:
					t.Fatal("server did not receive DATA")
				}
			} else {
				select {
				case <-f.data:
					t.Fatal("sent DATA after recipient rejection")
				default:
				}
			}
		})
	}
}

func inspectMIME(t *testing.T, data []byte) {
	t.Helper()
	msg, err := mail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if msg.Header.Get("Message-ID") != "<attempt-17@example.test>" {
		t.Fatal("wrong message identity")
	}
	subject, err := (&mime.WordDecoder{}).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil || subject != "Candidature ingénieur logiciel — équipe européenne" {
		t.Fatalf("wrong decoded subject: %q %v", subject, err)
	}
	mediaType := msg.Header.Get("Content-Type")
	if !strings.HasPrefix(mediaType, "multipart/mixed;") {
		t.Fatalf("wrong MIME type: %s", mediaType)
	}
	boundary := strings.TrimSuffix(strings.TrimPrefix(strings.Split(mediaType, "boundary=")[1], `"`), `"`)
	parts := multipart.NewReader(msg.Body, boundary)
	first, err := parts.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := io.ReadAll(first)
	if err != nil {
		t.Fatal(err)
	}
	body, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil || string(body) != "Bonjour,\nVoici ma candidature à Bruxelles.\n" {
		t.Fatalf("wrong body: %q %v", body, err)
	}
	second, err := parts.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	if second.FileName() != "application.pdf" {
		t.Fatalf("wrong filename: %s", second.FileName())
	}
	encoded, err = io.ReadAll(second)
	if err != nil {
		t.Fatal(err)
	}
	attachment, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil || string(attachment) != "synthetic application PDF bytes\n" {
		t.Fatalf("wrong attachment: %q %v", attachment, err)
	}
}

func TestSMTPRejectsChangedMaterialBeforeConnecting(t *testing.T) {
	m := syntheticMaterial(t)
	m.mime[0] ^= 1
	s, err := NewSMTP(SMTPConfig{Address: "127.0.0.1:1", ServerName: "smtp.test", HelloName: "client.test", Username: "user", Password: "password", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	out := s.Send(context.Background(), m, m.Digest())
	if out.State != PreDataFailure || out.Stage != "validation" || out.Err == nil {
		t.Fatalf("unexpected outcome: %+v", out)
	}
	out = s.Send(context.Background(), syntheticMaterial(t), strings.Repeat("0", 64))
	if out.State != PreDataFailure || out.Stage != "validation" || out.Err == nil {
		t.Fatalf("accepted a changed approval digest: %+v", out)
	}
	copy := syntheticMaterial(t)
	publicBytes := copy.Bytes()
	publicBytes[0] ^= 1
	if bytes.Equal(publicBytes, copy.Bytes()) {
		t.Fatal("public MIME bytes alias internal material")
	}
}

func TestPrepareRejectsUntrustedHeadersAndAttachmentDrift(t *testing.T) {
	base := Input{From: "applicant@example.test", To: "applications@example.test", MessageID: "<attempt@example.test>", Date: time.Now(), Subject: "Application", Body: "Hello"}
	for _, mutate := range []func(*Input){
		func(in *Input) { in.To = "applications@example.test\r\nBcc: other@example.test" },
		func(in *Input) { in.Subject = "Application\r\nBcc: other@example.test" },
		func(in *Input) { in.MessageID = "<attempt@example.test>\r\nBcc: other@example.test" },
		func(in *Input) {
			in.Attachments = []Attachment{{Filename: "../application.pdf", ContentType: "application/pdf", Data: []byte("x"), SHA256: strings.Repeat("0", 64)}}
		},
		func(in *Input) {
			in.Attachments = []Attachment{{Filename: "application.pdf", ContentType: "application/pdf", Data: []byte("changed"), SHA256: strings.Repeat("0", 64)}}
		},
		func(in *Input) { in.Body = strings.Repeat("a", maxBodyBytes+1) },
	} {
		input := base
		mutate(&input)
		if _, err := Prepare(input); err == nil {
			t.Fatalf("accepted unsafe input: %+v", input)
		}
	}
}

func TestSMTPVerifiesTLSNameBeforeCredentials(t *testing.T) {
	f := newSMTPFixture(t, accept)
	s, err := NewSMTP(SMTPConfig{Address: f.address, ServerName: "wrong.test", HelloName: "client.test", Username: "synthetic-user", Password: "synthetic-password", Timeout: time.Second, RootCAs: f.roots})
	if err != nil {
		t.Fatal(err)
	}
	m := syntheticMaterial(t)
	out := s.Send(context.Background(), m, m.Digest())
	if out.State != PreDataFailure || out.Stage != "tls" {
		t.Fatalf("unexpected TLS outcome: %+v", out)
	}
	select {
	case <-f.data:
		t.Fatal("sent DATA over unverified TLS")
	default:
	}
}
