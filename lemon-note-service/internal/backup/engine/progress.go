package engine

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// Progress 记录一次操作的进度与简要日志，可以并发更新、随时读取。
type Progress struct {
	mu         sync.Mutex
	phase      string
	filesDone  int64
	filesTotal int64
	bytesDone  int64
	bytesTotal int64
	lines      []string
	dropped    int
	warnings   []string
	// OnLine 每写一行日志时回调（命令行用来实时打印），可为 nil。
	OnLine func(line string)
}

// ProgressView 是进度的只读快照。
type ProgressView struct {
	Phase      string `json:"phase"`
	FilesDone  int64  `json:"files_done"`
	FilesTotal int64  `json:"files_total"`
	BytesDone  int64  `json:"bytes_done"`
	BytesTotal int64  `json:"bytes_total"`
	Warnings   int    `json:"warnings"`
}

const (
	maxLogLines  = 300
	keepLogHead  = 60
	maxWarnLines = 200
)

// NewProgress 创建进度记录。
func NewProgress() *Progress { return &Progress{} }

func (p *Progress) addLine(line string) {
	line = time.Now().Format("15:04:05") + " " + line
	p.mu.Lock()
	if len(p.lines) >= maxLogLines {
		// 保留开头与最近的日志，中间省略。
		p.lines = append(p.lines[:keepLogHead], p.lines[keepLogHead+1:]...)
		p.dropped++
	}
	p.lines = append(p.lines, line)
	cb := p.OnLine
	p.mu.Unlock()
	if cb != nil {
		cb(line)
	}
}

// Logf 记一行日志。
func (p *Progress) Logf(format string, a ...any) {
	if p == nil {
		return
	}
	p.addLine(fmt.Sprintf(format, a...))
}

// Warnf 记一条警告：操作仍会完成，但结果状态是「有警告」。
func (p *Progress) Warnf(format string, a ...any) {
	if p == nil {
		return
	}
	msg := fmt.Sprintf(format, a...)
	p.mu.Lock()
	if len(p.warnings) < maxWarnLines {
		p.warnings = append(p.warnings, msg)
	}
	p.mu.Unlock()
	p.addLine("警告：" + msg)
}

// SetPhase 设置当前阶段并记一行日志。
func (p *Progress) SetPhase(phase string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.phase = phase
	p.mu.Unlock()
	p.addLine(phase)
}

// SetTotal 设置总量（文件数、字节数），并清零已完成数。
func (p *Progress) SetTotal(files, bytes int64) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.filesTotal, p.bytesTotal = files, bytes
	p.filesDone, p.bytesDone = 0, 0
	p.mu.Unlock()
}

// AddFiles 累加完成的文件数。
func (p *Progress) AddFiles(n int64) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.filesDone += n
	p.mu.Unlock()
}

// AddBytes 累加完成的字节数。
func (p *Progress) AddBytes(n int64) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.bytesDone += n
	p.mu.Unlock()
}

// View 返回进度快照。
func (p *Progress) View() ProgressView {
	if p == nil {
		return ProgressView{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return ProgressView{Phase: p.phase, FilesDone: p.filesDone, FilesTotal: p.filesTotal, BytesDone: p.bytesDone,
		BytesTotal: p.bytesTotal, Warnings: len(p.warnings)}
}

// Warnings 返回全部警告。
func (p *Progress) Warnings() []string {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.warnings...)
}

// Text 返回日志全文（存进执行记录）。
func (p *Progress) Text() string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dropped == 0 {
		return strings.Join(p.lines, "\n")
	}
	head := p.lines[:keepLogHead]
	tail := p.lines[keepLogHead:]
	return strings.Join(head, "\n") + fmt.Sprintf("\n……省略 %d 行……\n", p.dropped) + strings.Join(tail, "\n")
}

// countingReader 在读取时累加进度字节数。
type countingReader struct {
	r io.Reader
	p *Progress
}

func (c countingReader) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	if n > 0 {
		c.p.AddBytes(int64(n))
	}
	return n, err
}

// humanBytes 把字节数写成便于阅读的形式。
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
