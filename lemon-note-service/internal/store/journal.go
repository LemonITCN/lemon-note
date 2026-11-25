package store

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/klauspost/compress/zstd"
)

// Entry 是日志里的一次提交。
type Entry struct {
	Seq   int64           `json:"seq"`
	TS    int64           `json:"ts"`
	Actor string          `json:"actor"`
	Ops   json.RawMessage `json:"ops"`
}

// SegmentHeader 是日志段首行。
type SegmentHeader struct {
	Version int    `json:"lnj"`
	First   int64  `json:"first"`
	Last    int64  `json:"last"`
	Lineage string `json:"lineage"`
	Schema  int    `json:"schema"`
}

type segmentTrailer struct {
	End    bool   `json:"end"`
	Count  int    `json:"count"`
	SHA256 string `json:"sha256"`
}

// EncodeSegment 把若干条提交编码成日志段（zstd 压缩的 NDJSON，带首行与校验尾行）。
func EncodeSegment(w io.Writer, lineage string, entries []Entry) error {
	if len(entries) == 0 {
		return errors.New("journal: 空日志段")
	}
	zw, err := zstd.NewWriter(w, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return err
	}
	h := sha256.New()
	out := io.MultiWriter(zw, h)
	writeLine := func(w io.Writer, v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		b = append(b, '\n')
		_, err = w.Write(b)
		return err
	}
	hdr := SegmentHeader{Version: 1, First: entries[0].Seq, Last: entries[len(entries)-1].Seq, Lineage: lineage, Schema: SchemaVersion}
	if err := writeLine(out, hdr); err != nil {
		return err
	}
	for _, e := range entries {
		if err := writeLine(out, e); err != nil {
			return err
		}
	}
	if err := writeLine(zw, segmentTrailer{End: true, Count: len(entries), SHA256: hex.EncodeToString(h.Sum(nil))}); err != nil {
		return err
	}
	return zw.Close()
}

// ErrCorruptSegment 表示日志段不完整或校验失败。
var ErrCorruptSegment = errors.New("日志段损坏")

// ReadSegment 读日志段并逐条回调；校验首行、尾行与 sha256，任何不一致都返回 ErrCorruptSegment。
// 注意：回调会在校验完成前被调用，调用方需要在出错时放弃整段（重放时整段在一个事务里）。
func ReadSegment(r io.Reader, fn func(Entry) error) (SegmentHeader, error) {
	var hdr SegmentHeader
	zr, err := zstd.NewReader(r)
	if err != nil {
		return hdr, err
	}
	defer zr.Close()
	br := bufio.NewReaderSize(zr, 1<<20)
	h := sha256.New()
	lineNo := 0
	count := 0
	var trailer *segmentTrailer
	for {
		line, err := br.ReadBytes('\n')
		if len(line) == 0 && errors.Is(err, io.EOF) {
			break
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return hdr, fmt.Errorf("%w: %v", ErrCorruptSegment, err)
		}
		if trailer != nil {
			return hdr, fmt.Errorf("%w: 尾行之后还有内容", ErrCorruptSegment)
		}
		if lineNo == 0 {
			if err := json.Unmarshal(line, &hdr); err != nil || hdr.Version != 1 {
				return hdr, fmt.Errorf("%w: 首行无效", ErrCorruptSegment)
			}
			h.Write(line)
			lineNo++
			continue
		}
		if bytes.HasPrefix(line, []byte(`{"end":`)) {
			var t segmentTrailer
			if err := json.Unmarshal(line, &t); err != nil || !t.End {
				return hdr, fmt.Errorf("%w: 尾行无效", ErrCorruptSegment)
			}
			trailer = &t
			continue
		}
		h.Write(line)
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			return hdr, fmt.Errorf("%w: 第 %d 行无效", ErrCorruptSegment, lineNo)
		}
		if e.Seq < hdr.First || e.Seq > hdr.Last {
			return hdr, fmt.Errorf("%w: 序号 %d 超出范围", ErrCorruptSegment, e.Seq)
		}
		if fn != nil {
			if err := fn(e); err != nil {
				return hdr, err
			}
		}
		count++
		lineNo++
	}
	if trailer == nil {
		return hdr, fmt.Errorf("%w: 缺少尾行（文件被截断）", ErrCorruptSegment)
	}
	if trailer.Count != count || trailer.SHA256 != hex.EncodeToString(h.Sum(nil)) {
		return hdr, fmt.Errorf("%w: 校验不一致", ErrCorruptSegment)
	}
	return hdr, nil
}

// ReadSegmentFile 读取一个日志段文件，返回全部条目（已校验）。
func ReadSegmentFile(path string) (SegmentHeader, []Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return SegmentHeader{}, nil, err
	}
	defer f.Close()
	var entries []Entry
	hdr, err := ReadSegment(f, func(e Entry) error {
		entries = append(entries, e)
		return nil
	})
	if err != nil {
		return hdr, nil, err
	}
	return hdr, entries, nil
}

// PendingOutbox 读出外箱里序号大于 after 的条目，最多 limit 条或约 maxBytes 字节。
func (s *Store) PendingOutbox(ctx context.Context, after int64, limit int, maxBytes int) ([]Entry, error) {
	rows, err := s.W.QueryContext(ctx, s.d.rebind("SELECT id, ts, actor, ops FROM _outbox WHERE id > ? ORDER BY id LIMIT ?"), after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	size := 0
	for rows.Next() {
		var e Entry
		var ops string
		if err := rows.Scan(&e.Seq, &e.TS, &e.Actor, &ops); err != nil {
			return nil, err
		}
		e.Ops = json.RawMessage(ops)
		out = append(out, e)
		size += len(ops)
		if size >= maxBytes {
			break
		}
	}
	return out, rows.Err()
}

// TrimOutbox 删除序号不大于 upTo 的外箱条目（已封存）。
func (s *Store) TrimOutbox(ctx context.Context, upTo int64) error {
	_, err := s.W.ExecContext(ctx, s.d.rebind("DELETE FROM _outbox WHERE id <= ?"), upTo)
	return err
}

// MinOutboxSeq 返回外箱里最小的序号（空时为 0）。
func (s *Store) MinOutboxSeq(ctx context.Context) (int64, error) {
	var v sql.NullInt64
	err := s.W.QueryRowContext(ctx, "SELECT min(id) FROM _outbox").Scan(&v)
	return v.Int64, err
}

// ApplyEntries 在一个事务里重放若干条提交（不再写外箱），并把序号推进到最后一条。
// 序号不大于当前已提交序号的条目会被跳过，保证幂等。
func (s *Store) ApplyEntries(ctx context.Context, entries []Entry) error {
	if len(entries) == 0 {
		return nil
	}
	sqltx, err := s.W.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer sqltx.Rollback()
	var cur int64
	if err := sqltx.QueryRowContext(ctx, "SELECT seq FROM _seq WHERE id = 1").Scan(&cur); err != nil {
		return err
	}
	last := cur
	for _, e := range entries {
		if e.Seq <= cur {
			continue
		}
		if e.Seq != last+1 {
			return fmt.Errorf("重放序号不连续：期望 %d，得到 %d", last+1, e.Seq)
		}
		dec := json.NewDecoder(bytes.NewReader(e.Ops))
		dec.UseNumber()
		var ops []Op
		if err := dec.Decode(&ops); err != nil {
			return fmt.Errorf("第 %d 条变更无法解析: %w", e.Seq, err)
		}
		for _, op := range ops {
			t, ok := TableByName(op.T)
			if !ok || !t.Journaled {
				continue // 未来版本删除的表，忽略
			}
			switch op.O {
			case "put":
				op.Row["id"] = op.ID
				cols, vals, _, err := normalizeRow(t, op.Row, false)
				if err != nil {
					return fmt.Errorf("第 %d 条变更写 %s 失败: %w", e.Seq, op.T, err)
				}
				if _, err := sqltx.ExecContext(ctx, s.d.rebind(s.d.upsert(t, cols)), vals...); err != nil {
					return err
				}
			case "del":
				if _, err := sqltx.ExecContext(ctx, s.d.rebind("DELETE FROM "+quoteIdent(s.Engine, op.T)+" WHERE id = ?"), op.ID); err != nil {
					return err
				}
			}
		}
		last = e.Seq
	}
	if last != cur {
		if _, err := sqltx.ExecContext(ctx, s.d.rebind("UPDATE _seq SET seq = ? WHERE id = 1"), last); err != nil {
			return err
		}
	}
	return sqltx.Commit()
}
