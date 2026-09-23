package delivery

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxAttachmentBytes = 5 << 20
	maxTotalBytes      = 10 << 20
	maxBodyBytes       = 1 << 20
	maxMIMEBytes       = 16 << 20
)

var (
	messageIDPattern = regexp.MustCompile(`^<[A-Za-z0-9._-]+@[A-Za-z0-9.-]+>$`)
	filenamePattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	mailboxPattern   = regexp.MustCompile(`^[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+$`)
)

// Attachment carries an exact content digest so a changed application pack
// cannot silently produce a different approved message.
type Attachment struct {
	Filename    string
	ContentType string
	Data        []byte
	SHA256      string
}

type Input struct {
	From        string
	To          string
	MessageID   string
	Date        time.Time
	Subject     string
	Body        string
	Attachments []Attachment
}

// Material owns the canonical MIME bytes. Its accessors return copies.
// Approval storage can bind to Digest, then pass that digest to SMTP.Send.
type Material struct {
	from      string
	to        string
	messageID string
	mime      []byte
	digest    string
}

func (m Material) From() string      { return m.from }
func (m Material) To() string        { return m.to }
func (m Material) MessageID() string { return m.messageID }
func (m Material) Digest() string    { return m.digest }
func (m Material) Bytes() []byte     { return bytes.Clone(m.mime) }

func Prepare(in Input) (Material, error) {
	if err := validateAddress(in.From); err != nil {
		return Material{}, fmt.Errorf("from: %w", err)
	}
	if err := validateAddress(in.To); err != nil {
		return Material{}, fmt.Errorf("to: %w", err)
	}
	if !messageIDPattern.MatchString(in.MessageID) || strings.Contains(in.MessageID, "..") {
		return Material{}, errors.New("invalid Message-ID")
	}
	if in.Date.IsZero() {
		return Material{}, errors.New("message date is required")
	}
	if len(in.Subject) == 0 || len(in.Subject) > 512 || !utf8.ValidString(in.Subject) {
		return Material{}, errors.New("invalid subject length or UTF-8")
	}
	for _, r := range in.Subject {
		if unicode.IsControl(r) {
			return Material{}, errors.New("subject contains a control character")
		}
	}
	if len(in.Body) > maxBodyBytes || !utf8.ValidString(in.Body) {
		return Material{}, errors.New("body size or UTF-8 invalid")
	}
	if len(in.Attachments) > 10 {
		return Material{}, errors.New("too many attachments")
	}

	// A content-derived boundary is stable for the same draft. The caller owns
	// Message-ID identity; any change to the final bytes changes Digest.
	seed := sha256.New()
	date := in.Date.UTC().Format(time.RFC1123Z)
	fmt.Fprintf(seed, "%s\x00%s\x00%s\x00%s\x00%s\x00%s", in.From, in.To, in.MessageID, date, in.Subject, in.Body)
	total := 0
	for _, a := range in.Attachments {
		if !filenamePattern.MatchString(a.Filename) || a.Filename == "." || a.Filename == ".." {
			return Material{}, errors.New("invalid attachment filename")
		}
		mediaType, params, err := mime.ParseMediaType(a.ContentType)
		if err != nil || !asciiHeader(a.ContentType) || mediaType != a.ContentType || len(params) != 0 || !strings.Contains(mediaType, "/") {
			return Material{}, errors.New("invalid attachment media type")
		}
		if len(a.Data) == 0 || len(a.Data) > maxAttachmentBytes || total+len(a.Data) > maxTotalBytes {
			return Material{}, errors.New("attachment size outside limits")
		}
		actual := sha256.Sum256(a.Data)
		if len(a.SHA256) != 64 || !strings.EqualFold(a.SHA256, hex.EncodeToString(actual[:])) {
			return Material{}, errors.New("attachment digest mismatch")
		}
		total += len(a.Data)
		fmt.Fprintf(seed, "\x00%s\x00%s\x00%x", a.Filename, mediaType, actual)
	}
	boundary := "find-income-" + hex.EncodeToString(seed.Sum(nil)[:16])
	subjectHeader := " " + in.Subject
	if encoded := mime.BEncoding.Encode("UTF-8", in.Subject); encoded != in.Subject {
		subjectHeader = "\r\n " + strings.ReplaceAll(encoded, " ", "\r\n ")
	}

	var b bytes.Buffer
	fmt.Fprintf(&b, "Date: %s\r\nFrom: <%s>\r\nTo: <%s>\r\nMessage-ID: %s\r\nSubject:%s\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"%s\"\r\n\r\n", date, in.From, in.To, in.MessageID, subjectHeader, boundary)
	fmt.Fprintf(&b, "--%s\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: base64\r\n\r\n", boundary)
	writeBase64(&b, []byte(in.Body))
	for _, a := range in.Attachments {
		fmt.Fprintf(&b, "--%s\r\nContent-Type: %s\r\nContent-Disposition: attachment; filename=\"%s\"\r\nContent-Transfer-Encoding: base64\r\n\r\n", boundary, a.ContentType, a.Filename)
		writeBase64(&b, a.Data)
	}
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	if b.Len() > maxMIMEBytes {
		return Material{}, errors.New("MIME message exceeds size limit")
	}
	materialBytes := bytes.Clone(b.Bytes())
	digest := sha256.Sum256(materialBytes)
	return Material{from: in.From, to: in.To, messageID: in.MessageID, mime: materialBytes, digest: hex.EncodeToString(digest[:])}, nil
}

func validateAddress(value string) error {
	if !mailboxPattern.MatchString(value) || !asciiHeader(value) || strings.Contains(value, "..") {
		return errors.New("invalid SMTP mailbox")
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value || parsed.Name != "" || strings.Count(value, "@") != 1 {
		return errors.New("invalid SMTP mailbox")
	}
	return nil
}

func asciiHeader(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] < 32 || value[i] > 126 {
			return false
		}
	}
	return true
}

func writeBase64(b *bytes.Buffer, data []byte) {
	encoded := base64.StdEncoding.EncodeToString(data)
	for len(encoded) > 76 {
		b.WriteString(encoded[:76])
		b.WriteString("\r\n")
		encoded = encoded[76:]
	}
	if encoded != "" {
		b.WriteString(encoded)
		b.WriteString("\r\n")
	}
}
