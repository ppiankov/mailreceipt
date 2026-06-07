package eml

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
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

	if e, ok, text := extractMIMEForward(msg.Header.Get("Content-Type"), body); ok {
		return ForwardedEmail{Email: e, Attached: true}, nil
	} else if len(text) > 0 {
		e, err := Parse(bytes.NewReader(text))
		return ForwardedEmail{Email: e}, err
	}

	e, err := Parse(bytes.NewReader(body))
	return ForwardedEmail{Email: e}, err
}

func extractMIMEForward(contentType string, body []byte) (Email, bool, []byte) {
	media, params, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.HasPrefix(strings.ToLower(media), "multipart/") {
		return Email{}, false, nil
	}
	boundary := params["boundary"]
	if boundary == "" {
		return Email{}, false, nil
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
		payload, err := io.ReadAll(part)
		if err != nil {
			continue
		}
		partMedia, _, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if err != nil || partMedia == "" {
			partMedia = "text/plain"
		}
		partMedia = strings.ToLower(partMedia)
		if partMedia == "message/rfc822" {
			e, err := Parse(bytes.NewReader(payload))
			if err == nil {
				return e, true, nil
			}
			continue
		}
		if strings.HasPrefix(partMedia, "multipart/") {
			if e, ok, text := extractMIMEForward(part.Header.Get("Content-Type"), payload); ok {
				return e, true, nil
			} else if firstText == nil && len(text) > 0 {
				firstText = text
			}
			continue
		}
		if firstText == nil && partMedia == "text/plain" {
			firstText = payload
		}
	}
	return Email{}, false, firstText
}
