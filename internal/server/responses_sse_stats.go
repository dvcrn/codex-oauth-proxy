package server

import (
	"bytes"
	"encoding/json"
	"strings"
)

// responsesSSEStats tracks stream framing without retaining response content.
type responsesSSEStats struct {
	bytes           int64
	dataLines       int
	textDeltas      int
	reasoningDeltas int
	outputItems     int
	functionCalls   int
	completed       int
	failed          int
	streamErrors    int
	deltaChars      int
	finalTextChars  int
	finalMessages   int
	finalReasoning  int
	finalFunctions  int
	finalOther      int
	finalCompleted  bool
	finalParsed     bool
	lineTruncated   bool
	event           string
	line            []byte
}

func (s *responsesSSEStats) Write(p []byte) (int, error) {
	n := len(p)
	s.bytes += int64(n)
	for len(p) > 0 {
		end := bytes.IndexByte(p, '\n')
		if end < 0 {
			s.appendLine(p)
			break
		}
		s.appendLine(p[:end])
		s.recordLine()
		s.line = s.line[:0]
		s.lineTruncated = false
		p = p[end+1:]
	}
	return n, nil
}

func (s *responsesSSEStats) appendLine(part []byte) {
	maxFieldLength := 128
	if s.event == "response.completed" {
		maxFieldLength = 512 * 1024
	} else if s.event == "response.output_text.delta" {
		maxFieldLength = 64 * 1024
	}
	remaining := max(0, maxFieldLength-len(s.line))
	if len(part) > remaining {
		s.lineTruncated = true
	}
	s.line = append(s.line, part[:min(len(part), remaining)]...)
}

func (s *responsesSSEStats) recordLine() {
	line := bytes.TrimSuffix(s.line, []byte("\r"))
	if bytes.HasPrefix(line, []byte("data:")) {
		s.dataLines++
		if !s.lineTruncated {
			s.recordData(bytes.TrimSpace(line[len("data:"):]))
		}
		return
	}
	if !bytes.HasPrefix(line, []byte("event:")) {
		return
	}
	s.event = strings.TrimSpace(string(line[len("event:"):]))
	switch s.event {
	case "response.output_text.delta":
		s.textDeltas++
	case "response.reasoning_summary_text.delta":
		s.reasoningDeltas++
	case "response.output_item.done":
		s.outputItems++
	case "response.function_call_arguments.done":
		s.functionCalls++
	case "response.completed":
		s.completed++
	case "response.failed":
		s.failed++
	case "error":
		s.streamErrors++
	}
}

func (s *responsesSSEStats) recordData(data []byte) {
	if s.event == "response.output_text.delta" {
		var delta struct {
			Delta string `json:"delta"`
		}
		if json.Unmarshal(data, &delta) == nil {
			s.deltaChars += len(delta.Delta)
		}
	}
	if s.event != "response.completed" {
		return
	}
	var result struct {
		Response struct {
			Status string `json:"status"`
			Output []struct {
				Type    string `json:"type"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"output"`
		} `json:"response"`
	}
	if json.Unmarshal(data, &result) != nil {
		return
	}
	s.finalParsed = true
	s.finalCompleted = result.Response.Status == "completed"
	for _, item := range result.Response.Output {
		switch item.Type {
		case "message":
			s.finalMessages++
			for _, content := range item.Content {
				if content.Type == "output_text" {
					s.finalTextChars += len(content.Text)
				}
			}
		case "reasoning":
			s.finalReasoning++
		case "function_call":
			s.finalFunctions++
		default:
			s.finalOther++
		}
	}
}
