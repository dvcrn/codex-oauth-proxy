package logger

import (
	"encoding/json"
	"io"
)

// Allow only operational fields so new payload fields cannot leak through production logs.
type privacyWriter struct{ output io.Writer }

func (w privacyWriter) Write(p []byte) (int, error) {
	var event map[string]json.RawMessage
	if err := json.Unmarshal(p, &event); err != nil {
		return len(p), nil
	}
	for key := range event {
		switch key {
		case "time", "level", "message", "status_code", "duration", "method", "normalized_model", "port", "creds_store",
			"input_count", "minutes_until_expiry", "new_expiry_minutes":
		default:
			delete(event, key)
		}
	}
	data, err := json.Marshal(event)
	if err != nil {
		return len(p), err
	}
	_, err = w.output.Write(append(data, '\n'))
	if err != nil {
		return 0, err
	}
	return len(p), nil
}
