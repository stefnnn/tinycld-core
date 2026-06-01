package mailer

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"mime"
	"mime/multipart"
	"net/textproto"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	sesv2types "github.com/aws/aws-sdk-go-v2/service/sesv2/types"
)

// SESConfig holds credentials and region for the AWS SES sender.
type SESConfig struct {
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	// DefaultFrom is the From address used when SendRequest.From is empty.
	DefaultFrom string
}

// SESSender sends email via AWS SES v2.
// It implements both Sender and FullSender.
type SESSender struct {
	client      *sesv2.Client
	defaultFrom string
}

// NewSESSender creates an SES-backed sender. When AccessKeyID/SecretAccessKey
// are empty the AWS default credential chain is used.
func NewSESSender(cfg SESConfig) (*SESSender, error) {
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.Region),
	}
	if cfg.AccessKeyID != "" && cfg.SecretAccessKey != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, fmt.Errorf("ses: load aws config: %w", err)
	}
	from := cfg.DefaultFrom
	if from == "" {
		from = "noreply@tinycld.org"
	}
	return &SESSender{
		client:      sesv2.NewFromConfig(awsCfg),
		defaultFrom: from,
	}, nil
}

// Send sends a simple transactional email.
func (s *SESSender) Send(ctx context.Context, msg *Message) error {
	if !deliver {
		return log.Send(ctx, msg)
	}
	from := msg.From
	if from == "" {
		from = s.defaultFrom
	}
	return s.sendSimple(ctx, from, FormatRecipients(msg.To), "", "", msg.Subject, msg.HTML, msg.Text, msg.ReplyTo)
}

// SendFull sends a rich email. For sends that include threading headers or
// attachments a raw RFC 5322 message is built; otherwise SES Simple content
// is used.
func (s *SESSender) SendFull(ctx context.Context, req *SendRequest) (*SendResult, error) {
	if !deliver {
		return log.SendFull(ctx, req)
	}

	from := req.From
	if from == "" {
		from = s.defaultFrom
	}

	needsRaw := req.InReplyTo != "" || req.References != "" ||
		len(req.Headers) > 0 || len(req.Attachments) > 0

	if needsRaw {
		raw, msgID, err := buildSESRawRFC5322(req, from)
		if err != nil {
			return nil, fmt.Errorf("ses: build raw message: %w", err)
		}
		out, err := s.client.SendEmail(ctx, &sesv2.SendEmailInput{
			Content: &sesv2types.EmailContent{
				Raw: &sesv2types.RawMessage{Data: raw},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("ses send failed: %w", err)
		}
		return &SendResult{
			ProviderMessageID: aws.ToString(out.MessageId),
			MessageID:         msgID,
		}, nil
	}

	if err := s.sendSimple(ctx, from,
		FormatRecipients(req.To),
		FormatRecipients(req.Cc),
		FormatRecipients(req.Bcc),
		req.Subject, req.HTMLBody, req.TextBody, req.ReplyTo,
	); err != nil {
		return nil, err
	}
	return &SendResult{}, nil
}

func (s *SESSender) sendSimple(ctx context.Context, from, to, cc, bcc, subject, html, text, replyTo string) error {
	dest := &sesv2types.Destination{}
	if to != "" {
		dest.ToAddresses = strings.Split(to, ", ")
	}
	if cc != "" {
		dest.CcAddresses = strings.Split(cc, ", ")
	}
	if bcc != "" {
		dest.BccAddresses = strings.Split(bcc, ", ")
	}

	body := &sesv2types.Body{}
	if html != "" {
		body.Html = &sesv2types.Content{Data: aws.String(html), Charset: aws.String("UTF-8")}
	}
	if text != "" {
		body.Text = &sesv2types.Content{Data: aws.String(text), Charset: aws.String("UTF-8")}
	}

	input := &sesv2.SendEmailInput{
		FromEmailAddress: aws.String(from),
		Destination:      dest,
		Content: &sesv2types.EmailContent{
			Simple: &sesv2types.Message{
				Subject: &sesv2types.Content{Data: aws.String(subject), Charset: aws.String("UTF-8")},
				Body:    body,
			},
		},
	}
	if replyTo != "" {
		input.ReplyToAddresses = []string{replyTo}
	}

	_, err := s.client.SendEmail(ctx, input)
	if err != nil {
		return fmt.Errorf("ses send failed: %w", err)
	}
	return nil
}

// buildSESRawRFC5322 constructs a minimal RFC 5322 message for SES Raw
// sending. Handles threading headers, custom headers, and attachments using
// only the standard library.
func buildSESRawRFC5322(req *SendRequest, from string) (raw []byte, messageID string, err error) {
	var buf bytes.Buffer
	messageID = generateSESMessageID()

	// Standard headers
	buf.WriteString("MIME-Version: 1.0\r\n")
	buf.WriteString(fmt.Sprintf("Date: %s\r\n", time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 -0700")))
	buf.WriteString(fmt.Sprintf("Message-ID: %s\r\n", messageID))
	buf.WriteString(fmt.Sprintf("From: %s\r\n", from))
	buf.WriteString(fmt.Sprintf("To: %s\r\n", FormatRecipients(req.To)))
	if len(req.Cc) > 0 {
		buf.WriteString(fmt.Sprintf("Cc: %s\r\n", FormatRecipients(req.Cc)))
	}
	buf.WriteString(fmt.Sprintf("Subject: %s\r\n", mime.QEncoding.Encode("utf-8", req.Subject)))
	if req.ReplyTo != "" {
		buf.WriteString(fmt.Sprintf("Reply-To: %s\r\n", req.ReplyTo))
	}
	if req.InReplyTo != "" {
		buf.WriteString(fmt.Sprintf("In-Reply-To: %s\r\n", req.InReplyTo))
	}
	if req.References != "" {
		buf.WriteString(fmt.Sprintf("References: %s\r\n", req.References))
	}
	for _, h := range req.Headers {
		if h.Name != "" {
			buf.WriteString(fmt.Sprintf("%s: %s\r\n", h.Name, h.Value))
		}
	}

	if len(req.Attachments) > 0 {
		// multipart/mixed: body alternative + attachments
		mw := multipart.NewWriter(&buf)
		buf.WriteString(fmt.Sprintf("Content-Type: multipart/mixed; boundary=%q\r\n\r\n", mw.Boundary()))

		// Body part (multipart/alternative: text + html)
		altBound := "alt-" + mw.Boundary()
		bh := make(textproto.MIMEHeader)
		bh.Set("Content-Type", fmt.Sprintf("multipart/alternative; boundary=%q", altBound))
		pw, _ := mw.CreatePart(bh)
		writeAlternativePartsRaw(pw, altBound, req.TextBody, req.HTMLBody)

		for _, att := range req.Attachments {
			ah := make(textproto.MIMEHeader)
			ah.Set("Content-Type", att.ContentType)
			ah.Set("Content-Transfer-Encoding", "base64")
			ah.Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, att.Name))
			if att.ContentID != "" {
				ah.Set("Content-ID", att.ContentID)
			}
			aw, _ := mw.CreatePart(ah)
			aw.Write([]byte(att.Content)) //nolint:errcheck
		}
		mw.Close()
	} else {
		// multipart/alternative: text + html
		mw := multipart.NewWriter(&buf)
		buf.WriteString(fmt.Sprintf("Content-Type: multipart/alternative; boundary=%q\r\n\r\n", mw.Boundary()))
		writeAlternativePartsRaw(&buf, mw.Boundary(), req.TextBody, req.HTMLBody)
	}

	return buf.Bytes(), messageID, nil
}

func writeAlternativePartsRaw(w interface{ Write([]byte) (int, error) }, boundary, text, html string) {
	if text != "" {
		fmt.Fprintf(w, "--%s\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n%s\r\n",
			boundary, base64.StdEncoding.EncodeToString([]byte(text)))
	}
	if html != "" {
		fmt.Fprintf(w, "--%s\r\nContent-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n%s\r\n",
			boundary, base64.StdEncoding.EncodeToString([]byte(html)))
	}
	fmt.Fprintf(w, "--%s--\r\n", boundary)
}

func generateSESMessageID() string {
	b := make([]byte, 8)
	rand.Read(b) //nolint:errcheck
	return fmt.Sprintf("<%d.%s@tinycld.ses>", time.Now().UTC().UnixNano(), hex.EncodeToString(b))
}

// Compile-time assertions.
var _ Sender = (*SESSender)(nil)
var _ FullSender = (*SESSender)(nil)
