package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"sort"
)

// CompleteResponsesSSEStream fills an empty final output from completed stream items.
func CompleteResponsesSSEStream(r io.Reader, w io.Writer) (bool, error) {
	reader := bufio.NewReader(r)
	items := make(map[int]json.RawMessage)
	var frame bytes.Buffer
	repaired := false
	flush := func() error {
		if frame.Len() == 0 {
			return nil
		}
		original := frame.Bytes()
		dataStart, dataEnd := -1, -1
		var event string
		for pos := 0; pos < len(original); {
			end := bytes.IndexByte(original[pos:], '\n')
			if end < 0 {
				end = len(original)
			} else {
				end += pos + 1
			}
			line := bytes.TrimSpace(original[pos:end])
			if bytes.HasPrefix(line, []byte("event:")) {
				event = string(bytes.TrimSpace(line[len("event:"):]))
			}
			if bytes.HasPrefix(line, []byte("data:")) {
				if dataStart >= 0 {
					dataStart = -1 // Leave multi-line data events unchanged.
					break
				}
				dataStart, dataEnd = pos, end
			}
			pos = end
		}
		if dataStart >= 0 {
			data := bytes.TrimSpace(original[dataStart+len("data:") : dataEnd])
			switch event {
			case "response.output_item.done":
				var item struct {
					OutputIndex *int            `json:"output_index"`
					Item        json.RawMessage `json:"item"`
				}
				if json.Unmarshal(data, &item) == nil && item.OutputIndex != nil && len(item.Item) > 0 {
					items[*item.OutputIndex] = item.Item
				}
			case "response.completed":
				if len(items) > 0 {
					var envelope map[string]json.RawMessage
					if json.Unmarshal(data, &envelope) == nil {
						var response map[string]json.RawMessage
						if json.Unmarshal(envelope["response"], &response) == nil && response != nil {
							var output []json.RawMessage
							if len(response["output"]) == 0 || json.Unmarshal(response["output"], &output) == nil && len(output) == 0 {
								indices := make([]int, 0, len(items))
								for index := range items {
									indices = append(indices, index)
								}
								sort.Ints(indices)
								for _, index := range indices {
									output = append(output, items[index])
								}
								response["output"], _ = json.Marshal(output)
								envelope["response"], _ = json.Marshal(response)
								if patched, err := json.Marshal(envelope); err == nil {
									updated := make([]byte, 0, len(original)+len(patched))
									updated = append(updated, original[:dataStart]...)
									updated = append(updated, "data: "...)
									updated = append(updated, patched...)
									updated = append(updated, '\n')
									updated = append(updated, original[dataEnd:]...)
									original = updated
									repaired = true
								}
							}
						}
					}
				}
			}
		}
		_, err := w.Write(original)
		frame.Reset()
		return err
	}
	for {
		line, err := reader.ReadBytes('\n')
		frame.Write(line)
		if len(bytes.TrimSpace(line)) == 0 || err != nil {
			if writeErr := flush(); writeErr != nil {
				return repaired, writeErr
			}
		}
		if err != nil {
			if err == io.EOF {
				return repaired, nil
			}
			return repaired, err
		}
	}
}
