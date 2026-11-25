package target

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// baiduMock 按开放平台文档模拟授权、上传、列表、下载、删除接口。
type baiduMock struct {
	t   *testing.T
	app string
	srv *httptest.Server

	mu            sync.Mutex
	files         map[string]*bdFile
	uploads       map[string]*bdUpload
	access        map[string]bool
	refresh       map[string]bool
	next          int
	refreshCalls  int
	rateLimitList int // listall 先返回几次 31034
	evilDlink     bool
	partSizes     []int
	tokensSeen    map[string]int
	dlNoUA        int
}

type bdFile struct {
	data  []byte
	fsid  int64
	mtime int64
}

type bdUpload struct {
	path   string
	size   int64
	blocks []string
	parts  map[int][]byte
}

func newBaiduMock(t *testing.T) *baiduMock {
	m := &baiduMock{t: t, app: "柠檬笔记备份", files: map[string]*bdFile{}, uploads: map[string]*bdUpload{},
		access: map[string]bool{"at-0": true}, refresh: map[string]bool{"rt-0": true}, tokensSeen: map[string]int{}}
	m.srv = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.srv.Close)
	baiduRotateMu.Lock()
	baiduRotated = map[string]*BaiduToken{}
	baiduRotateMu.Unlock()
	oa, pa, pc, rd := BaiduOAuthBase, BaiduPanBase, BaiduPCSBase, baiduRetryDelays
	BaiduOAuthBase, BaiduPanBase, BaiduPCSBase = m.srv.URL, m.srv.URL, m.srv.URL
	baiduRetryDelays = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { BaiduOAuthBase, BaiduPanBase, BaiduPCSBase, baiduRetryDelays = oa, pa, pc, rd })
	return m
}

func jsonOut(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (m *baiduMock) issue() map[string]any {
	m.next++
	at, rt := fmt.Sprintf("at-%d", m.next), fmt.Sprintf("rt-%d", m.next)
	m.access[at], m.refresh[rt] = true, true
	return map[string]any{"access_token": at, "refresh_token": rt, "expires_in": 2592000, "scope": "basic netdisk", "session_key": "", "session_secret": ""}
}

func (m *baiduMock) serve(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	q := r.URL.Query()
	switch {
	case r.URL.Path == "/oauth/2.0/device/code":
		if q.Get("client_id") != "ak" || q.Get("response_type") != "device_code" || q.Get("scope") != "basic,netdisk" {
			jsonOut(w, map[string]any{"error": "invalid_client", "error_description": "unknown client id"})
			return
		}
		jsonOut(w, map[string]any{"device_code": "dev-1", "user_code": "ABCD-EFGH", "verification_url": "https://openapi.baidu.com/device",
			"qrcode_url": "https://openapi.baidu.com/device/qrcode/x", "expires_in": 300, "interval": 5})
		return
	case r.URL.Path == "/oauth/2.0/token":
		if q.Get("client_id") != "ak" || q.Get("client_secret") != "sk" {
			w.WriteHeader(401)
			jsonOut(w, map[string]any{"error": "invalid_client", "error_description": "unknown client id"})
			return
		}
		switch q.Get("grant_type") {
		case "device_token":
			switch q.Get("code") {
			case "pending":
				jsonOut(w, map[string]any{"error": "authorization_pending", "error_description": "User has not yet completed the authorization"})
			case "slow":
				jsonOut(w, map[string]any{"error": "slow_down", "error_description": "too fast"})
			case "good":
				jsonOut(w, m.issue())
			default:
				jsonOut(w, map[string]any{"error": "invalid_grant", "error_description": "Invalid device code"})
			}
		case "refresh_token":
			m.refreshCalls++
			rt := q.Get("refresh_token")
			if !m.refresh[rt] {
				jsonOut(w, map[string]any{"error": "expired_token", "error_description": "refresh token has been used"})
				return
			}
			delete(m.refresh, rt) // 只能用一次
			jsonOut(w, m.issue())
		}
		return
	case strings.HasPrefix(r.URL.Path, "/dl/"):
		if r.Header.Get("User-Agent") != "pan.baidu.com" {
			m.dlNoUA++
			w.WriteHeader(403)
			jsonOut(w, map[string]any{"error_code": 31326, "error_msg": "anti hotlinking"})
			return
		}
		if !m.access[q.Get("access_token")] {
			w.WriteHeader(403)
			jsonOut(w, map[string]any{"error_code": 31045, "error_msg": "user not exists"})
			return
		}
		id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/dl/"), 10, 64)
		for _, f := range m.files {
			if f.fsid == id {
				w.Write(f.data)
				return
			}
		}
		w.WriteHeader(404)
		return
	}
	tok := q.Get("access_token")
	m.tokensSeen[tok]++
	if !m.access[tok] {
		jsonOut(w, map[string]any{"errno": -6, "errmsg": "Invalid Bduss", "request_id": 1})
		return
	}
	root := "/apps/" + m.app + "/"
	method := q.Get("method")
	switch r.URL.Path + "?" + method {
	case "/rest/2.0/xpan/file?precreate":
		r.ParseForm()
		p := r.PostForm.Get("path")
		size, _ := strconv.ParseInt(r.PostForm.Get("size"), 10, 64)
		var blocks []string
		json.Unmarshal([]byte(r.PostForm.Get("block_list")), &blocks)
		if !strings.HasPrefix(p, root) {
			jsonOut(w, map[string]any{"errno": 31064, "errmsg": "path error"})
			return
		}
		if size <= 0 || int64(len(blocks)) != (size+baiduBlockSize-1)/baiduBlockSize || r.PostForm.Get("autoinit") != "1" || r.PostForm.Get("rtype") != "3" || r.PostForm.Get("isdir") != "0" {
			jsonOut(w, map[string]any{"errno": 2, "errmsg": "param error"})
			return
		}
		m.next++
		id := fmt.Sprintf("up-%d", m.next)
		m.uploads[id] = &bdUpload{path: p, size: size, blocks: blocks, parts: map[int][]byte{}}
		need := make([]int, len(blocks))
		for i := range need {
			need[i] = i
		}
		jsonOut(w, map[string]any{"errno": 0, "path": p, "uploadid": id, "return_type": 1, "block_list": need, "request_id": 1})
	case "/rest/2.0/pcs/file?locateupload":
		if q.Get("appid") != "250528" || m.uploads[q.Get("uploadid")] == nil || q.Get("upload_version") != "2.0" {
			jsonOut(w, map[string]any{"error_code": 2, "error_msg": "param"})
			return
		}
		jsonOut(w, map[string]any{"error_code": 0, "servers": []map[string]string{{"server": "https://evil.example.com"}, {"server": m.srv.URL}}, "bak_servers": []any{}, "expire": 60})
	case "/rest/2.0/pcs/superfile2?upload":
		up := m.uploads[q.Get("uploadid")]
		if up == nil || q.Get("type") != "tmpfile" || q.Get("path") != up.path {
			jsonOut(w, map[string]any{"error_code": 31355, "error_msg": "upload id invalid"})
			return
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			jsonOut(w, map[string]any{"error_code": 31023, "error_msg": "no file"})
			return
		}
		data, _ := io.ReadAll(f)
		seq, _ := strconv.Atoi(q.Get("partseq"))
		up.parts[seq] = data
		m.partSizes = append(m.partSizes, len(data))
		sum := md5.Sum(data)
		jsonOut(w, map[string]any{"md5": hex.EncodeToString(sum[:]), "request_id": 7})
	case "/rest/2.0/xpan/file?create":
		r.ParseForm()
		up := m.uploads[r.PostForm.Get("uploadid")]
		if up == nil || r.PostForm.Get("path") != up.path || r.PostForm.Get("rtype") != "3" {
			jsonOut(w, map[string]any{"errno": 31355})
			return
		}
		var blocks []string
		json.Unmarshal([]byte(r.PostForm.Get("block_list")), &blocks)
		var all []byte
		for i, b := range blocks {
			d, ok := up.parts[i]
			sum := md5.Sum(d)
			if !ok || hex.EncodeToString(sum[:]) != b || (i < len(blocks)-1 && len(d) != baiduBlockSize) {
				jsonOut(w, map[string]any{"errno": 31363, "errmsg": "block miss"})
				return
			}
			all = append(all, d...)
		}
		if int64(len(all)) != up.size {
			jsonOut(w, map[string]any{"errno": 31190})
			return
		}
		delete(m.uploads, r.PostForm.Get("uploadid"))
		m.next++
		f := &bdFile{data: all, fsid: int64(1000 + m.next), mtime: time.Now().Unix()}
		m.files[up.path] = f
		jsonOut(w, map[string]any{"errno": 0, "fs_id": f.fsid, "path": up.path, "size": len(all), "mtime": f.mtime, "isdir": 0, "md5": "x"})
	case "/rest/2.0/xpan/file?list":
		dir := strings.TrimSuffix(q.Get("dir"), "/")
		start, _ := strconv.Atoi(q.Get("start"))
		limit, _ := strconv.Atoi(q.Get("limit"))
		ents, ok := m.children(dir)
		if !ok {
			jsonOut(w, map[string]any{"errno": -9, "request_id": 1})
			return
		}
		if start > len(ents) {
			start = len(ents)
		}
		end := start + limit
		if end > len(ents) {
			end = len(ents)
		}
		jsonOut(w, map[string]any{"errno": 0, "list": ents[start:end], "guid": 0})
	case "/rest/2.0/xpan/multimedia?listall":
		if m.rateLimitList > 0 {
			m.rateLimitList--
			jsonOut(w, map[string]any{"errno": 31034, "errmsg": "hit api limit"})
			return
		}
		dir := strings.TrimSuffix(q.Get("path"), "/")
		if q.Get("recursion") != "1" {
			jsonOut(w, map[string]any{"errno": 2})
			return
		}
		ents, ok := m.descendants(dir)
		if !ok {
			jsonOut(w, map[string]any{"errno": 31066, "errmsg": "file does not exist"})
			return
		}
		start, _ := strconv.Atoi(q.Get("start"))
		limit := 2 // 故意很小，覆盖分页
		end := start + limit
		hasMore := 1
		if end >= len(ents) {
			end, hasMore = len(ents), 0
		}
		if start > end {
			start = end
		}
		jsonOut(w, map[string]any{"errno": 0, "has_more": hasMore, "cursor": end, "list": ents[start:end]})
	case "/rest/2.0/xpan/multimedia?filemetas":
		var ids []int64
		json.Unmarshal([]byte(q.Get("fsids")), &ids)
		var list []map[string]any
		for p, f := range m.files {
			for _, id := range ids {
				if f.fsid == id {
					dl := m.srv.URL + "/dl/" + strconv.FormatInt(id, 10) + "?sign=abc&expires=8h"
					if m.evilDlink {
						dl = "https://evil.example.com/steal?x=1"
					}
					list = append(list, map[string]any{"fs_id": id, "path": p, "filename": p[strings.LastIndex(p, "/")+1:], "size": len(f.data),
						"isdir": 0, "server_mtime": f.mtime, "dlink": dl})
				}
			}
		}
		jsonOut(w, map[string]any{"errno": 0, "errmsg": "succ", "list": list, "request_id": "1"})
	case "/rest/2.0/xpan/file?filemanager":
		r.ParseForm()
		if q.Get("opera") != "delete" || r.PostForm.Get("async") != "0" {
			jsonOut(w, map[string]any{"errno": 2})
			return
		}
		var paths []string
		json.Unmarshal([]byte(r.PostForm.Get("filelist")), &paths)
		var info []map[string]any
		failed := false
		for _, p := range paths {
			n := 0
			for fp := range m.files {
				if fp == p || strings.HasPrefix(fp, p+"/") {
					delete(m.files, fp)
					n++
				}
			}
			if n == 0 {
				failed = true
				info = append(info, map[string]any{"errno": -9, "path": p})
			} else {
				info = append(info, map[string]any{"errno": 0, "path": p})
			}
		}
		if failed {
			jsonOut(w, map[string]any{"errno": 12, "info": info, "request_id": 1})
			return
		}
		jsonOut(w, map[string]any{"errno": 0, "info": info, "request_id": 1})
	default:
		jsonOut(w, map[string]any{"errno": 2, "errmsg": "unknown " + r.URL.Path + "?" + method})
	}
}

func (m *baiduMock) entry(p string, isDir bool) map[string]any {
	e := map[string]any{"path": p, "server_filename": p[strings.LastIndex(p, "/")+1:], "isdir": 0, "category": 6}
	if isDir {
		e["isdir"] = 1
		e["size"] = 0
		e["fs_id"] = 1
		return e
	}
	f := m.files[p]
	e["size"], e["fs_id"], e["server_mtime"] = len(f.data), f.fsid, f.mtime
	return e
}

// children 列出 dir 的直接子项；dir 不存在返回 false。
func (m *baiduMock) children(dir string) ([]map[string]any, bool) {
	seen := map[string]bool{}
	var out []map[string]any
	exists := false
	for p := range m.files {
		if !strings.HasPrefix(p, dir+"/") {
			continue
		}
		exists = true
		rest := p[len(dir)+1:]
		if i := strings.Index(rest, "/"); i >= 0 {
			d := dir + "/" + rest[:i]
			if !seen[d] {
				seen[d] = true
				out = append(out, m.entry(d, true))
			}
			continue
		}
		out = append(out, m.entry(p, false))
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["path"].(string) < out[j]["path"].(string) })
	return out, exists
}

func (m *baiduMock) descendants(dir string) ([]map[string]any, bool) {
	seen := map[string]bool{}
	var out []map[string]any
	exists := false
	for p := range m.files {
		if !strings.HasPrefix(p, dir+"/") {
			continue
		}
		exists = true
		out = append(out, m.entry(p, false))
		for d := p[:strings.LastIndex(p, "/")]; d != dir && len(d) > len(dir); d = d[:strings.LastIndex(d, "/")] {
			if !seen[d] {
				seen[d] = true
				out = append(out, m.entry(d, true))
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["path"].(string) < out[j]["path"].(string) })
	return out, exists
}

type saved struct {
	mu   sync.Mutex
	list [][]byte
}

func (s *saved) save(b []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.list = append(s.list, append([]byte(nil), b...))
	return nil
}

func (s *saved) last() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.list) == 0 {
		return nil
	}
	var m map[string]any
	json.Unmarshal(s.list[len(s.list)-1], &m)
	return m
}

func baiduCfg(m *baiduMock, access string, expiresAt int64) []byte {
	b, _ := json.Marshal(map[string]any{"app_key": "ak", "secret_key": "sk", "app_name": m.app, "access_token": access,
		"refresh_token": "rt-0", "expires_at": expiresAt, "prefix": "notes/机器 1", "name": "我的百度网盘"})
	return b
}

func TestBaiduSuite(t *testing.T) {
	m := newBaiduMock(t)
	var sv saved
	tg, err := Open(KindBaidu, baiduCfg(m, "at-0", time.Now().Add(20*24*time.Hour).Unix()), Deps{SaveConfig: sv.save})
	if err != nil {
		t.Fatal(err)
	}
	runSuite(t, tg, suiteOpts{noEmptyFile: true, bigSize: 2*baiduBlockSize + 12345})
	if len(sv.list) != 0 || m.refreshCalls != 0 {
		t.Fatalf("令牌未快过期，不应刷新（刷新 %d 次，回写 %d 次）", m.refreshCalls, len(sv.list))
	}
	// 大文件按 4 MB 分片：4M、4M、余数。
	found := false
	for i := 0; i+2 < len(m.partSizes); i++ {
		if m.partSizes[i] == baiduBlockSize && m.partSizes[i+1] == baiduBlockSize && m.partSizes[i+2] == 12345 {
			found = true
		}
	}
	if !found {
		t.Fatalf("分片大小不对：%v", m.partSizes)
	}
	for p := range m.files {
		if !strings.HasPrefix(p, "/apps/"+m.app+"/notes/机器 1/") {
			t.Errorf("文件 %q 不在 /apps/<应用名>/<prefix>/ 下", p)
		}
	}
	if m.dlNoUA != 0 {
		t.Fatal("下载没有带 User-Agent: pan.baidu.com")
	}
}

func TestBaiduEmptyAndTooLarge(t *testing.T) {
	m := newBaiduMock(t)
	tg, _ := Open(KindBaidu, baiduCfg(m, "at-0", time.Now().Add(20*24*time.Hour).Unix()), Deps{})
	if err := tg.Put(context.Background(), "e.txt", bytes.NewReader(nil), 0); err == nil || !strings.Contains(err.Error(), "空文件") {
		t.Fatalf("空文件应报错，实际 %v", err)
	}
	if err := tg.Put(context.Background(), "big.bin", strings.NewReader(""), baiduMaxFileSize+1); err == nil || !strings.Contains(err.Error(), "4 GB") {
		t.Fatalf("超过 4 GB 应报错，实际 %v", err)
	}
}

func TestBaiduProactiveRefresh(t *testing.T) {
	m := newBaiduMock(t)
	var sv saved
	tg, _ := Open(KindBaidu, baiduCfg(m, "at-0", time.Now().Add(time.Hour).Unix()), Deps{SaveConfig: sv.save})
	mustPut(t, tg, "a.txt", []byte("hello"))
	if m.refreshCalls != 1 || len(sv.list) != 1 {
		t.Fatalf("快过期时应刷新一次并回写（刷新 %d 次，回写 %d 次）", m.refreshCalls, len(sv.list))
	}
	cfg := sv.last()
	if cfg["access_token"] != "at-1" || cfg["refresh_token"] != "rt-1" || cfg["name"] != "我的百度网盘" || cfg["app_key"] != "ak" {
		t.Fatalf("回写的配置不对：%v", cfg)
	}
	exp := int64(cfg["expires_at"].(float64))
	if d := exp - time.Now().Unix(); d < 2592000-60 || d > 2592000+60 {
		t.Fatalf("expires_at 不对：%d", exp)
	}
	m.mu.Lock()
	usedNew := m.tokensSeen["at-1"]
	m.mu.Unlock()
	if usedNew == 0 {
		t.Fatal("刷新后没有使用新令牌")
	}
	// 再次操作不应重复刷新。
	mustGet(t, tg, "a.txt")
	if m.refreshCalls != 1 {
		t.Fatalf("重复刷新了：%d", m.refreshCalls)
	}
	// 用回写的配置重新打开也能用。
	tg2, err := Open(KindBaidu, sv.list[0], Deps{SaveConfig: sv.save})
	if err != nil {
		t.Fatal(err)
	}
	if got := mustGet(t, tg2, "a.txt"); string(got) != "hello" {
		t.Fatalf("用新配置读取失败：%q", got)
	}
}

func TestBaiduTwoInstancesShareRotation(t *testing.T) {
	m := newBaiduMock(t)
	var sv saved
	cfg := baiduCfg(m, "at-0", time.Now().Add(time.Hour).Unix())
	a, _ := Open(KindBaidu, cfg, Deps{SaveConfig: sv.save})
	b, _ := Open(KindBaidu, cfg, Deps{SaveConfig: sv.save})
	mustPut(t, a, "a.txt", []byte("a"))
	// b 拿着同一个（已经用掉的）refresh_token，应沿用 a 换来的新令牌，而不是再刷新一次失败。
	if got := mustGet(t, b, "a.txt"); string(got) != "a" {
		t.Fatalf("第二个实例读取失败：%q", got)
	}
	if m.refreshCalls != 1 {
		t.Fatalf("同一 refresh_token 只能刷新一次，实际刷新 %d 次", m.refreshCalls)
	}
	if len(sv.list) != 2 || sv.last()["refresh_token"] != "rt-1" {
		t.Fatalf("两个实例都应回写同一份新令牌：%d 次", len(sv.list))
	}
}

func TestBaiduRefreshFailureThrottled(t *testing.T) {
	m := newBaiduMock(t)
	m.refresh = map[string]bool{} // refresh_token 已失效，但 access_token 还有 1 小时
	var sv saved
	tg, _ := Open(KindBaidu, baiduCfg(m, "at-0", time.Now().Add(time.Hour).Unix()), Deps{SaveConfig: sv.save})
	for i := 0; i < 3; i++ {
		if _, err := tg.List(context.Background(), ""); err != nil {
			t.Fatalf("旧令牌还能用时操作不应失败：%v", err)
		}
	}
	if m.refreshCalls != 1 {
		t.Fatalf("刷新失败后短时间内不应反复刷新，实际 %d 次", m.refreshCalls)
	}
}

func TestBaiduReactiveRefresh(t *testing.T) {
	m := newBaiduMock(t)
	var sv saved
	// 配置里的令牌没过期，但服务端已经不认（例如用户在别处重新授权过）。
	tg, _ := Open(KindBaidu, baiduCfg(m, "revoked", time.Now().Add(20*24*time.Hour).Unix()), Deps{SaveConfig: sv.save})
	if got := listPaths(t, tg, ""); len(got) != 0 {
		t.Fatalf("List: %v", got)
	}
	if m.refreshCalls != 1 || len(sv.list) != 1 || sv.last()["access_token"] != "at-1" {
		t.Fatalf("令牌失效时应刷新一次并重试（刷新 %d 次，回写 %d 次）", m.refreshCalls, len(sv.list))
	}
}

func TestBaiduExpiredWithoutSaveConfig(t *testing.T) {
	m := newBaiduMock(t)
	tg, _ := Open(KindBaidu, baiduCfg(m, "at-0", time.Now().Add(-time.Hour).Unix()), Deps{})
	_, err := tg.List(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "重新授权") {
		t.Fatalf("没有 SaveConfig 且令牌过期时应提示重新授权，实际 %v", err)
	}
	if m.refreshCalls != 0 {
		t.Fatal("没有 SaveConfig 时不能刷新（新的 refresh_token 会丢失）")
	}
}

func TestBaiduRefreshTokenRejected(t *testing.T) {
	m := newBaiduMock(t)
	m.refresh = map[string]bool{} // rt-0 已失效
	var sv saved
	tg, _ := Open(KindBaidu, baiduCfg(m, "at-0", time.Now().Add(-time.Hour).Unix()), Deps{SaveConfig: sv.save})
	_, err := tg.Stat(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "重新授权") {
		t.Fatalf("刷新被拒时应提示重新授权，实际 %v", err)
	}
	if len(sv.list) != 0 {
		t.Fatal("刷新失败时不应回写")
	}
}

func TestBaiduSaveConfigFails(t *testing.T) {
	m := newBaiduMock(t)
	tg, _ := Open(KindBaidu, baiduCfg(m, "at-0", time.Now().Add(-time.Hour).Unix()), Deps{SaveConfig: func([]byte) error { return errors.New("磁盘满") }})
	_, err := tg.List(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "保存新令牌失败") {
		t.Fatalf("回写失败应明确报错，实际 %v", err)
	}
	// 新令牌留在内存里，本进程后续操作仍可用。
	if _, err := tg.List(context.Background(), ""); err != nil {
		t.Fatalf("回写失败后内存里的新令牌应可继续使用：%v", err)
	}
}

func TestBaiduRateLimitRetry(t *testing.T) {
	m := newBaiduMock(t)
	tg, _ := Open(KindBaidu, baiduCfg(m, "at-0", time.Now().Add(20*24*time.Hour).Unix()), Deps{})
	mustPut(t, tg, "x/a.txt", []byte("a"))
	m.mu.Lock()
	m.rateLimitList = 2
	m.mu.Unlock()
	if got := listPaths(t, tg, ""); !eqStrings(got, []string{"x/a.txt"}) {
		t.Fatalf("限流重试后 List = %v", got)
	}
	m.mu.Lock()
	m.rateLimitList = 10
	m.mu.Unlock()
	if _, err := tg.List(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "频繁") {
		t.Fatalf("持续限流应报错，实际 %v", err)
	}
}

func TestBaiduUntrustedDlink(t *testing.T) {
	m := newBaiduMock(t)
	tg, _ := Open(KindBaidu, baiduCfg(m, "at-0", time.Now().Add(20*24*time.Hour).Unix()), Deps{})
	mustPut(t, tg, "a.txt", []byte("hello"))
	m.mu.Lock()
	m.evilDlink = true
	m.mu.Unlock()
	if _, err := tg.Get(context.Background(), "a.txt"); err == nil || !strings.Contains(err.Error(), "不可信") {
		t.Fatalf("非百度域名的下载地址应拒绝，实际 %v", err)
	}
}

func TestBaiduNoTokenInErrors(t *testing.T) {
	m := newBaiduMock(t)
	tg, _ := Open(KindBaidu, baiduCfg(m, "at-0", time.Now().Add(20*24*time.Hour).Unix()), Deps{})
	m.srv.Close() // 网络错误
	for _, err := range []error{
		func() error { _, err := tg.List(context.Background(), ""); return err }(),
		tg.Put(context.Background(), "a.txt", strings.NewReader("abc"), 3),
	} {
		if err == nil || strings.Contains(err.Error(), "at-0") || strings.Contains(err.Error(), "access_token") || strings.Contains(err.Error(), "sk") {
			t.Fatalf("错误信息不能带令牌或密钥：%v", err)
		}
	}
}

func TestBaiduDeviceFlow(t *testing.T) {
	m := newBaiduMock(t)
	ctx := context.Background()
	d, err := BaiduDeviceCode(ctx, nil, "ak")
	if err != nil {
		t.Fatal(err)
	}
	if d.DeviceCode != "dev-1" || d.UserCode != "ABCD-EFGH" || d.VerificationURL == "" || d.QRCodeURL == "" || d.Interval != 5 || d.ExpiresIn != 300 {
		t.Fatalf("设备码不对：%+v", d)
	}
	if _, err := BaiduDeviceCode(ctx, nil, "wrong"); err == nil {
		t.Fatal("AppKey 错误应报错")
	}
	if _, err := BaiduPollToken(ctx, nil, "ak", "sk", "pending"); !errors.Is(err, ErrBaiduAuthPending) {
		t.Fatalf("未授权应返回 ErrBaiduAuthPending，实际 %v", err)
	}
	if _, err := BaiduPollToken(ctx, nil, "ak", "sk", "slow"); !errors.Is(err, ErrBaiduSlowDown) || !errors.Is(err, ErrBaiduAuthPending) {
		t.Fatalf("轮询过快应返回 ErrBaiduSlowDown（且属于可重试），实际 %v", err)
	}
	if _, err := BaiduPollToken(ctx, nil, "ak", "sk", "used"); err == nil || errors.Is(err, ErrBaiduAuthPending) {
		t.Fatalf("设备码无效应返回不可重试的错误，实际 %v", err)
	}
	if _, err := BaiduPollToken(ctx, nil, "ak", "bad-secret", "good"); err == nil || strings.Contains(err.Error(), "bad-secret") {
		t.Fatalf("SecretKey 错误应报错且不泄露密钥，实际 %v", err)
	}
	tok, err := BaiduPollToken(ctx, nil, "ak", "sk", "good")
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken == "" || tok.RefreshToken == "" || tok.ExpiresIn != 2592000 || tok.ExpiresAt < time.Now().Unix()+2592000-60 {
		t.Fatalf("令牌不对：%+v", tok)
	}
	cfg, err := BaiduApplyToken([]byte(`{"app_key":"ak","secret_key":"sk","app_name":"`+m.app+`","prefix":"p","extra":{"x":1}}`), tok)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	json.Unmarshal(cfg, &got)
	if got["access_token"] != tok.AccessToken || got["extra"] == nil || got["prefix"] != "p" {
		t.Fatalf("写入令牌后的配置不对：%s", cfg)
	}
	// 授权后的配置可以直接打开使用。
	tg, err := Open(KindBaidu, cfg, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	if err := Probe(ctx, tg); err != nil {
		t.Fatalf("授权后 Probe 失败：%v", err)
	}
}

func TestBaiduConfigErrors(t *testing.T) {
	for _, c := range []string{
		`{"secret_key":"sk","app_name":"a","access_token":"x"}`,
		`{"app_key":"ak","secret_key":"sk","access_token":"x"}`,
		`{"app_key":"ak","secret_key":"sk","app_name":"a/b","access_token":"x"}`,
		`{"app_key":"ak","secret_key":"sk","app_name":"..","access_token":"x"}`,
		`{"app_key":"ak","secret_key":"sk","app_name":"a"}`,
		`{"app_key":"ak","secret_key":"sk","app_name":"a","access_token":"x","prefix":"../x"}`,
	} {
		if _, err := Open(KindBaidu, []byte(c), Deps{}); err == nil {
			t.Errorf("配置 %s 应报错", c)
		}
	}
}
