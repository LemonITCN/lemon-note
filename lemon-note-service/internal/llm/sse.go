package llm

import (
	"bufio"
	"io"
	"strings"
)

// sseReader 读取 text/event-stream。
type sseReader struct {
	r    *bufio.Reader
	body io.ReadCloser
}

func newSSE(body io.ReadCloser) *sseReader {
	return &sseReader{r: bufio.NewReaderSize(body, 64*1024), body: body}
}

// next 返回下一个事件的 event 名与 data（多行 data 已拼接）。
func (s *sseReader) next() (event string, data string, err error) {
	var dataLines []string
	for {
		line, err := s.r.ReadString('\n')
		if err != nil {
			if len(dataLines) > 0 && err == io.EOF {
				return event, strings.Join(dataLines, "\n"), nil
			}
			return "", "", err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if len(dataLines) == 0 && event == "" {
				continue
			}
			return event, strings.Join(dataLines, "\n"), nil
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		k, v, _ := strings.Cut(line, ":")
		v = strings.TrimPrefix(v, " ")
		switch k {
		case "event":
			event = v
		case "data":
			dataLines = append(dataLines, v)
		}
	}
}

func (s *sseReader) close() error { return s.body.Close() }
