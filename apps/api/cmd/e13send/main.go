// Command e13send composes the E13 sink test message and, only with an
// explicit matching digest approval, sends it through the production
// SMTP client to the isolated Mailpit sink. It never contacts an
// employer: recipient and sender are sink-only addresses, and the
// message is marked as a test.
//
// Usage:
//
//	e13send compose   - build the MIME from materials v1, store it, print it with its digest
//	e13send send      - resend nothing new: verify E13_SEND_APPROVED against the stored
//	                  digest plus pack currency, then SMTP-send the stored bytes
package main

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
	"github.com/veighnsche/find-income-dashboard/api/internal/delivery"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

const (
	opportunityID = "b8a80e6102af9857415f6f68bb1bd8d6"
	fromDefault   = "applicant-e13-test@jobseek-sink.local"
	toDefault     = "sink-e13@jobseek-sink.local"
)

func privateDataDir() (string, error) {
	if value := os.Getenv("JOBSEEK_DATA_DIR"); value != "" {
		return value, nil
	}
	config, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find user data directory: %w", err)
	}
	return filepath.Join(config, "jobseek-dashboard", "data"), nil
}

func sendDir(dataDir string) string {
	return filepath.Join(dataDir, "muse-runs", "e13-shopify-send")
}

type sendMeta struct {
	Digest           string `json:"digest"`
	From             string `json:"from"`
	To               string `json:"to"`
	Subject          string `json:"subject"`
	MessageID        string `json:"messageId"`
	PackID           string `json:"packId"`
	PackContentSHA   string `json:"packContentSha256"`
	MaterialVersion  int64  `json:"materialVersion"`
	OpportunityRev   int64  `json:"opportunityRevision"`
	ProfileRev       int64  `json:"profileRevision"`
	AttachmentSHA256 string `json:"attachmentSha256"`
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func compose() error {
	ctx := context.Background()
	dataDir, err := privateDataDir()
	if err != nil {
		return err
	}
	database, err := store.Open(ctx, dataDir)
	if err != nil {
		return fmt.Errorf("open private data: %w", err)
	}
	defer database.Close()

	current, err := database.CurrentOpportunityMaterials(ctx, opportunityID)
	if err != nil {
		return fmt.Errorf("current materials: %w", err)
	}
	if current.Status != store.MaterialStatusPrepared || current.Current == nil {
		return fmt.Errorf("materials are %q, want prepared", current.Status)
	}
	version := current.Current
	pack, err := database.ApplicationPack(ctx, version.PackID)
	if err != nil {
		return fmt.Errorf("application pack: %w", err)
	}
	var manifest struct {
		Draft struct {
			Focus   applicationpacks.Line     `json:"focus"`
			Cover   []applicationpacks.Line   `json:"cover"`
			Answers []applicationpacks.Answer `json:"answers"`
		} `json:"draft"`
		Material struct {
			Answers []struct {
				QuestionID string `json:"questionId"`
				State      string `json:"state"`
				Text       string `json:"text"`
			} `json:"answers"`
		} `json:"material"`
	}
	if err := json.Unmarshal(pack.ManifestJSON, &manifest); err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	opportunity, err := database.Opportunity(ctx, opportunityID)
	if err != nil {
		return err
	}
	company, err := database.Company(ctx, opportunity.CompanyID)
	if err != nil {
		return err
	}

	var body strings.Builder
	body.WriteString("E13 TEST MESSAGE - sink only, not an employer submission.\n\n")
	body.WriteString(manifest.Draft.Focus.Text + "\n")
	for _, line := range manifest.Draft.Cover {
		body.WriteString(line.Text + "\n")
	}
	body.WriteString("\nEmployer questions and answers:\n")
	for _, answer := range manifest.Material.Answers {
		text := answer.Text
		if text == "" {
			text = "(blank)"
		}
		fmt.Fprintf(&body, "- %s [%s]: %s\n", answer.QuestionID, answer.State, text)
	}
	fmt.Fprintf(&body, "\nMaterials pack v%d (pack %s) attached as application.pdf.\n", version.Version, pack.ID)
	body.WriteString("Citations verified against approved career sources; relevance judged by Jev.\n")

	from := getenv("E13_SINK_FROM", fromDefault)
	to := getenv("E13_SINK_TO", toDefault)
	subject := fmt.Sprintf("[E13 TEST - sink only] Application for %s at %s", opportunity.Title, company.Name)
	messageID := fmt.Sprintf("<e13-%s@jobseek-sink.local>", pack.ContentSHA256[:12])
	pdfDigest := sha256.Sum256(pack.PDF)
	material, err := delivery.Prepare(delivery.Input{
		From: from, To: to, MessageID: messageID, Date: time.Now().UTC(),
		Subject: subject, Body: body.String(),
		Attachments: []delivery.Attachment{{Filename: "application.pdf",
			ContentType: "application/pdf", Data: pack.PDF,
			SHA256: hex.EncodeToString(pdfDigest[:])}},
	})
	if err != nil {
		return fmt.Errorf("compose MIME: %w", err)
	}
	dir := sendDir(dataDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "send-mime.bin"), material.Bytes(), 0o600); err != nil {
		return err
	}
	meta := sendMeta{Digest: material.Digest(), From: from, To: to, Subject: subject,
		MessageID: messageID, PackID: pack.ID, PackContentSHA: pack.ContentSHA256,
		MaterialVersion: version.Version, OpportunityRev: version.OpportunityRevision,
		ProfileRev: version.ProfileRevision, AttachmentSHA256: hex.EncodeToString(pdfDigest[:])}
	raw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "send-meta.json"), raw, 0o600); err != nil {
		return err
	}
	fmt.Printf("from: %s\nto: %s\nsubject: %s\nmessage-id: %s\n", from, to, subject, messageID)
	fmt.Printf("pack: %s v%d pdf=%d bytes\n", pack.ID, version.Version, len(pack.PDF))
	fmt.Printf("mime: %d bytes digest: %s\n", len(material.Bytes()), material.Digest())
	fmt.Println("---- body ----")
	fmt.Println(body.String())
	return nil
}

func send() error {
	approved := os.Getenv("E13_SEND_APPROVED")
	if len(approved) != 64 {
		return errors.New("E13_SEND_APPROVED must be the 64-hex digest the owner approved")
	}
	ctx := context.Background()
	dataDir, err := privateDataDir()
	if err != nil {
		return err
	}
	dir := sendDir(dataDir)
	mimeBytes, err := os.ReadFile(filepath.Join(dir, "send-mime.bin"))
	if err != nil {
		return fmt.Errorf("stored MIME (run compose first): %w", err)
	}
	metaRaw, err := os.ReadFile(filepath.Join(dir, "send-meta.json"))
	if err != nil {
		return err
	}
	var meta sendMeta
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		return err
	}
	sum := sha256.Sum256(mimeBytes)
	if stored := hex.EncodeToString(sum[:]); stored != meta.Digest || !strings.EqualFold(stored, approved) {
		return errors.New("stored MIME does not match the approved digest; refusing the send")
	}
	database, err := store.Open(ctx, dataDir)
	if err != nil {
		return fmt.Errorf("open private data: %w", err)
	}
	defer database.Close()
	current, err := database.CurrentOpportunityMaterials(ctx, opportunityID)
	if err != nil {
		return err
	}
	if current.Current == nil || current.Current.Version != meta.MaterialVersion ||
		current.Current.PackID != meta.PackID {
		return errors.New("materials changed since compose; recompose before sending")
	}
	pack, err := database.ApplicationPack(ctx, meta.PackID)
	if err != nil {
		return err
	}
	if pack.ContentSHA256 != meta.PackContentSHA {
		return errors.New("pack changed since compose; recompose before sending")
	}
	material, err := delivery.RestoreMaterial(meta.From, meta.To, meta.MessageID, meta.Digest, mimeBytes)
	if err != nil {
		return fmt.Errorf("restore material: %w", err)
	}
	certPath := os.Getenv("E13_SINK_CERT")
	if certPath == "" {
		return errors.New("E13_SINK_CERT must point at the sink TLS certificate")
	}
	pemBytes, err := os.ReadFile(certPath)
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pemBytes) {
		return errors.New("cannot parse sink certificate")
	}
	sender, err := delivery.NewSMTP(delivery.SMTPConfig{
		Address:    getenv("E13_SINK_SMTP_ADDRESS", "127.0.0.1:4465"),
		ServerName: getenv("E13_SINK_SMTP_SERVER_NAME", "jobseek-sink.local"),
		HelloName:  getenv("E13_SINK_SMTP_HELLO_NAME", "jobseek-e13.test"),
		Username:   getenv("E13_SINK_SMTP_USERNAME", "sink-user"),
		Password:   getenv("E13_SINK_SMTP_PASSWORD", "sink-pass"),
		Timeout:    30 * time.Second, RootCAs: roots,
	})
	if err != nil {
		return err
	}
	sendCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	outcome := sender.Send(sendCtx, material, approved)
	fmt.Printf("send: state=%s stage=%s code=%d message-id=%s\n",
		outcome.State, outcome.Stage, outcome.SMTPCode, outcome.MessageID)
	if outcome.Err != nil {
		return fmt.Errorf("smtp: %w", outcome.Err)
	}
	if outcome.State != delivery.AcceptedBySMTP {
		return fmt.Errorf("send ended %s, want accepted_by_smtp", outcome.State)
	}
	return nil
}

func main() {
	if len(os.Args) != 2 || (os.Args[1] != "compose" && os.Args[1] != "send") {
		fmt.Fprintln(os.Stderr, "usage: e13send compose|send")
		os.Exit(2)
	}
	var err error
	if os.Args[1] == "compose" {
		err = compose()
	} else {
		err = send()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "e13send: %v\n", err)
		os.Exit(1)
	}
}
