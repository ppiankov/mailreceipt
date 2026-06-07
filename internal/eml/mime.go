package eml

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
)

// ForwardedEmail is the sent message embedded in a receipt request.
type ForwardedEmail struct {
	Email    Email // WO-13: extracted sent-message headers to correlate against the local log
	Attached bool  // WO-13: true when the selector came from a message/rfc822 attachment
}

// ExtractForwardedEmail finds the sent message inside a trigger email. It
// prefers a message/rfc822 attachment and falls back to parsing the plain-text
// body as a pasted forward.
func ExtractForwardedEmail(raw []byte) (ForwardedEmail, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return ForwardedEmail{}, err
	}
	body, err := io.ReadAll(msg.Body)
	if err != nil {
		return ForwardedEmail{}, err
	}

	extracted := extractMIMEForward(msg.Header.Get("Content-Type"), body)
	if extracted.err != nil {
		return ForwardedEmail{}, extracted.err
	}
	if extracted.attached {
		return ForwardedEmail{Email: extracted.email, Attached: true}, nil
	}
	if len(extracted.text) > 0 {
		e, err := Parse(bytes.NewReader(extracted.text))
		return ForwardedEmail{Email: e}, err
	}

	e, err := Parse(bytes.NewReader(body))
	return ForwardedEmail{Email: e}, err
}

type mimeForwardExtract struct {
	email    Email
	attached bool
	text     []byte
	err      error // WO-18: explicit forwarded-attachment failures must block text fallback.
}

func extractMIMEForward(contentType string, body []byte) mimeForwardExtract {
	media, params, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.HasPrefix(strings.ToLower(media), "multipart/") {
		return mimeForwardExtract{}
	}
	boundary := params["boundary"]
	if boundary == "" {
		return mimeForwardExtract{}
	}

	mr := multipart.NewReader(bytes.NewReader(body), boundary)
	var firstText []byte
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		partMedia, _, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if err != nil || partMedia == "" {
			partMedia = "text/plain"
		}
		partMedia = strings.ToLower(partMedia)
		explicitForward := isForwardAttachment(partMedia, part.FileName())
		payload, err := readPartPayload(part)
		if err != nil {
			if explicitForward {
				return mimeForwardExtract{err: err}
			}
			continue
		}
		if explicitForward {
			e, err := Parse(bytes.NewReader(payload))
			if err == nil {
				return mimeForwardExtract{email: e, attached: true}
			}
			return mimeForwardExtract{err: fmt.Errorf("parsing forwarded attachment: %w", err)}
		}
		if strings.HasPrefix(partMedia, "multipart/") {
			nested := extractMIMEForward(part.Header.Get("Content-Type"), payload)
			if nested.err != nil {
				return nested
			}
			if nested.attached {
				return nested
			}
			if firstText == nil && len(nested.text) > 0 {
				firstText = nested.text
			}
			continue
		}
		if firstText == nil && partMedia == "text/plain" {
			firstText = payload
		}
	}
	return mimeForwardExtract{text: firstText}
}

func isForwardAttachment(media, filename string) bool {
	// WO-18: generic MIME types are sent-message selectors only with .eml metadata.
	media = strings.ToLower(strings.TrimSpace(media))
	if media == "message/rfc822" {
		return true
	}
	filename = strings.ToLower(strings.TrimSpace(filename))
	return strings.HasSuffix(filename, ".eml")
}

func readPartPayload(part *multipart.Part) ([]byte, error) {
	// WO-18: forwarded .eml attachments commonly arrive transfer-encoded.
	raw, err := io.ReadAll(part)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(strings.TrimSpace(part.Header.Get("Content-Transfer-Encoding"))) {
	case "", "7bit", "8bit", "binary":
		return raw, nil
	case "base64":
		decoded, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, bytes.NewReader(raw)))
		if err != nil {
			return nil, fmt.Errorf("decoding base64 part: %w", err)
		}
		return decoded, nil
	case "quoted-printable":
		decoded, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(raw)))
		if err != nil {
			return nil, fmt.Errorf("decoding quoted-printable part: %w", err)
		}
		return decoded, nil
	default:
		return nil, fmt.Errorf("unsupported content-transfer-encoding %q", part.Header.Get("Content-Transfer-Encoding"))
	}
}
