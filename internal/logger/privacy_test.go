package logger

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

func TestProductionLoggerKeepsOnlyOperationalFields(t *testing.T) {
	output, err := os.CreateTemp(t.TempDir(), "production.log")
	if err != nil {
		t.Fatal(err)
	}
	stderr, timeFormat := os.Stderr, zerolog.TimeFieldFormat
	os.Stderr = output
	t.Cleanup(func() {
		os.Stderr = stderr
		zerolog.TimeFieldFormat = timeFormat
		output.Close()
	})
	t.Setenv("ENV", "production")
	logger := New()
	logger.Info().
		Int("status_code", 502).
		Str("authorization_preview", "private-token").
		Str("inbound_body_preview", "private-prompt").
		Str("response_body", "private-output").
		RawJSON("new_upstream_field", []byte(`{"nested":"private-new-field"}`)).
		Err(errors.New("private-error")).
		Msg("Upstream request completed")
	logger.Debug().Msg("debug-only")

	data, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private-") || strings.Contains(string(data), "debug-only") {
		t.Fatalf("production logs contain sensitive or debug data: %s", data)
	}
	var event map[string]json.RawMessage
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatalf("expected one JSON event: %v", err)
	}
	if len(event) != 4 || string(event["level"]) != `"info"` || string(event["status_code"]) != "502" ||
		string(event["message"]) != `"Upstream request completed"` || event["time"] == nil {
		t.Fatalf("operational fields changed: %s", data)
	}
}

func TestPrivacyWriterDropsMalformedRecords(t *testing.T) {
	for _, input := range []string{"invalid private-record", `{"message":"private-incomplete"`, `["private-array"]`} {
		var output bytes.Buffer
		writer := privacyWriter{&output}
		n, err := writer.Write([]byte(input))
		if n != len(input) || err != nil || output.Len() != 0 {
			t.Fatalf("malformed record was not discarded: n=%d, err=%v, output=%q", n, err, output.String())
		}
	}
}

func TestPrivacyWriterPropagatesWriteErrors(t *testing.T) {
	writer := privacyWriter{failingLogWriter{}}
	n, err := writer.Write([]byte(`{"level":"error","message":"Operational failure"}`))
	if n != 0 || !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("write = (%d, %v), want (0, %v)", n, err, io.ErrClosedPipe)
	}
}

type failingLogWriter struct{}

func (failingLogWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
