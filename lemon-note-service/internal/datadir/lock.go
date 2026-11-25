package datadir

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofrs/flock"
)

// Owner 是 locks/owner.json 的内容。
type Owner struct {
	HostID    string `json:"host_id"`
	Hostname  string `json:"hostname"`
	PID       int    `json:"pid"`
	StartedAt int64  `json:"started_at"`
	Heartbeat int64  `json:"heartbeat"`
	Version   string `json:"version"`
}

// OwnerStale 超过这个时间没有心跳的占用者视为已离线。
const OwnerStale = 5 * time.Minute

// ErrOwnedElsewhere 表示另一台机器正在使用该数据目录。
type ErrOwnedElsewhere struct{ Owner Owner }

func (e *ErrOwnedElsewhere) Error() string {
	return fmt.Sprintf("数据目录正在被另一台机器使用（%s，最后心跳 %s）。同一数据目录同一时间只能在一台机器上运行；确认对方已停止后可用 --take-over 接管",
		e.Owner.Hostname, time.UnixMilli(e.Owner.Heartbeat).Format("2006-01-02 15:04:05"))
}

// Lock 同时持有本机文件锁与跨机心跳。
type Lock struct {
	dir     *Dir
	local   *flock.Flock
	owner   Owner
	stop    chan struct{}
	wg      sync.WaitGroup
	onError func(error)
}

func (d *Dir) ownerPath() string { return filepath.Join(d.LockDir(), "owner.json") }

func (d *Dir) ReadOwner() (*Owner, error) {
	b, err := os.ReadFile(d.ownerPath())
	if err != nil {
		return nil, err
	}
	var o Owner
	if err := json.Unmarshal(b, &o); err != nil {
		return nil, err
	}
	return &o, nil
}

// Acquire 抢占数据目录。localLockPath 放在缓存目录（本机互斥），hostID 是本机稳定标识。
func (d *Dir) Acquire(localLockPath, hostID, version string, takeOver bool) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(localLockPath), 0o755); err != nil {
		return nil, err
	}
	fl := flock.New(localLockPath)
	ok, err := fl.TryLock()
	if err != nil {
		return nil, fmt.Errorf("无法获取本机锁: %w", err)
	}
	if !ok {
		return nil, errors.New("本机已有一个 Lemon Note 实例在使用这个数据目录")
	}
	if !takeOver {
		if o, err := d.ReadOwner(); err == nil && o.HostID != hostID && time.Since(time.UnixMilli(o.Heartbeat)) < OwnerStale {
			fl.Unlock()
			return nil, &ErrOwnedElsewhere{Owner: *o}
		}
	}
	host, _ := os.Hostname()
	now := time.Now().UnixMilli()
	l := &Lock{dir: d, local: fl, stop: make(chan struct{}), owner: Owner{
		HostID: hostID, Hostname: host, PID: os.Getpid(), StartedAt: now, Heartbeat: now, Version: version,
	}}
	if err := l.beat(); err != nil {
		fl.Unlock()
		return nil, fmt.Errorf("数据目录不可写: %w", err)
	}
	l.wg.Add(1)
	go l.loop()
	return l, nil
}

func (l *Lock) beat() error {
	l.owner.Heartbeat = time.Now().UnixMilli()
	b, _ := json.MarshalIndent(l.owner, "", "  ")
	return WriteFileAtomic(l.dir.ownerPath(), b, 0o644)
}

func (l *Lock) loop() {
	defer l.wg.Done()
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-t.C:
			// 心跳前先确认没有被别的机器接管
			if o, err := l.dir.ReadOwner(); err == nil && o.HostID != l.owner.HostID && time.Since(time.UnixMilli(o.Heartbeat)) < OwnerStale {
				if l.onError != nil {
					l.onError(&ErrOwnedElsewhere{Owner: *o})
				}
				continue
			}
			if err := l.beat(); err != nil && l.onError != nil {
				l.onError(err)
			}
		}
	}
}

// OnError 设置心跳出错（被接管、不可写）时的回调。
func (l *Lock) OnError(fn func(error)) { l.onError = fn }

// Release 停止心跳、删除 owner.json、释放本机锁。
func (l *Lock) Release() {
	close(l.stop)
	l.wg.Wait()
	if o, err := l.dir.ReadOwner(); err == nil && o.HostID == l.owner.HostID {
		_ = RemoveRetry(l.dir.ownerPath())
	}
	_ = l.local.Unlock()
}
