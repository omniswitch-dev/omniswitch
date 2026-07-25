package provider

import (
	"encoding/base64"
	"mime"
	"net/url"
	"path"
	"strings"
)

type decodedDataURL struct {
	MediaType string
	Data      string
}

func parseDataURL(raw string) (decodedDataURL, bool) {
	if !strings.HasPrefix(raw, "data:") {
		return decodedDataURL{}, false
	}
	header, data, ok := strings.Cut(strings.TrimPrefix(raw, "data:"), ",")
	if !ok {
		return decodedDataURL{}, false
	}
	mediaType := "application/octet-stream"
	base64Encoded := false
	for index, part := range strings.Split(header, ";") {
		if index == 0 && strings.TrimSpace(part) != "" {
			mediaType = strings.TrimSpace(part)
			continue
		}
		if strings.EqualFold(strings.TrimSpace(part), "base64") {
			base64Encoded = true
		}
	}
	if !base64Encoded {
		return decodedDataURL{}, false
	}
	if _, err := base64.StdEncoding.DecodeString(data); err != nil {
		return decodedDataURL{}, false
	}
	return decodedDataURL{MediaType: mediaType, Data: data}, true
}

func mediaTypeFromURL(raw string) string {
	if decoded, ok := parseDataURL(raw); ok {
		return decoded.MediaType
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "application/octet-stream"
	}
	if ext := path.Ext(parsed.Path); ext != "" {
		if mediaType := mime.TypeByExtension(ext); mediaType != "" {
			return mediaType
		}
	}
	return "application/octet-stream"
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
