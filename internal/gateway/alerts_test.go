package gateway

import (
	"bytes"
	"log"
	"net"
	"strings"
	"testing"
)

func TestAlertLogsRedactWebhookSecrets(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close() // nothing listens, so the POST fails to connect

	var buf bytes.Buffer
	original := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(original) })

	manager := NewAlertManager(AlertConfig{})
	manager.postJSON("http://"+addr+"/services/T000/B000/SECRETTOKEN?sig=abc", map[string]any{"text": "x"})
	manager.postJSON("http://[::1/hooks/SECRETTOKEN", map[string]any{"text": "x"})
	out := buf.String()
	if strings.Contains(out, "SECRETTOKEN") || strings.Contains(out, "sig=abc") {
		t.Fatalf("alert log leaked webhook secret: %s", out)
	}
	if !strings.Contains(out, "http://"+addr) {
		t.Fatalf("alert log missing redacted host: %s", out)
	}
}
