package engine

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/backup/target"
)

// baiduPending 是发起设备码授权后、等待用户确认的状态。设备码只留在服务端，不发给浏览器。
type baiduPending struct {
	deviceCode string
	expiresAt  time.Time
	interval   time.Duration
	lastPoll   time.Time
}

// BaiduDeviceView 是发起授权后给界面展示的内容。
type BaiduDeviceView struct {
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	QRCodeURL       string `json:"qrcode_url"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// BaiduPollView 是轮询授权的结果。
type BaiduPollView struct {
	Status   string `json:"status"` // pending | authorized
	SlowDown bool   `json:"slow_down,omitempty"`
}

type baiduAppConfig struct {
	AppKey    string `json:"app_key"`
	SecretKey string `json:"secret_key"`
}

// BaiduDevice 为一个百度网盘目标发起设备码授权（目标需要先保存好 AppKey、SecretKey、应用名称）。
func (e *Engine) BaiduDevice(ctx context.Context, targetID string) (*BaiduDeviceView, error) {
	spec, err := e.loadSpec(ctx, targetID)
	if err != nil {
		return nil, err
	}
	if spec.row.Kind != target.KindBaidu {
		return nil, uerr("这个目标不是百度网盘")
	}
	var c baiduAppConfig
	_ = json.Unmarshal(spec.config, &c)
	d, err := target.BaiduDeviceCode(ctx, e.o.HTTPClient, c.AppKey)
	if err != nil {
		return nil, wrapUser(err)
	}
	e.mu.Lock()
	e.baidu[targetID] = &baiduPending{deviceCode: d.DeviceCode, expiresAt: e.now().Add(time.Duration(d.ExpiresIn) * time.Second),
		interval: time.Duration(d.Interval) * time.Second}
	e.mu.Unlock()
	return &BaiduDeviceView{UserCode: d.UserCode, VerificationURL: d.VerificationURL, QRCodeURL: d.QRCodeURL, ExpiresIn: d.ExpiresIn, Interval: d.Interval}, nil
}

// BaiduPoll 查询授权是否完成；完成后把令牌加密写进目标配置。
func (e *Engine) BaiduPoll(ctx context.Context, targetID string) (*BaiduPollView, error) {
	spec, err := e.loadSpec(ctx, targetID)
	if err != nil {
		return nil, err
	}
	now := e.now()
	e.mu.Lock()
	pend := e.baidu[targetID]
	if pend == nil {
		e.mu.Unlock()
		return nil, uerr("还没有发起百度网盘授权，或授权已结束，请重新发起")
	}
	if pend.expiresAt.Before(now) {
		delete(e.baidu, targetID)
		e.mu.Unlock()
		return nil, uerr("授权码已过期，请重新发起授权")
	}
	if !pend.lastPoll.IsZero() && now.Sub(pend.lastPoll) < pend.interval {
		e.mu.Unlock()
		return &BaiduPollView{Status: "pending"}, nil
	}
	pend.lastPoll = now
	deviceCode := pend.deviceCode
	e.mu.Unlock()
	var c baiduAppConfig
	_ = json.Unmarshal(spec.config, &c)
	tok, err := target.BaiduPollToken(ctx, e.o.HTTPClient, c.AppKey, c.SecretKey, deviceCode)
	if errors.Is(err, target.ErrBaiduSlowDown) {
		e.mu.Lock()
		pend.interval += 5 * time.Second
		e.mu.Unlock()
		return &BaiduPollView{Status: "pending", SlowDown: true}, nil
	}
	if errors.Is(err, target.ErrBaiduAuthPending) {
		return &BaiduPollView{Status: "pending"}, nil
	}
	if err != nil {
		e.mu.Lock()
		delete(e.baidu, targetID)
		e.mu.Unlock()
		return nil, wrapUser(err)
	}
	cfg, err := target.BaiduApplyToken(spec.config, tok)
	if err != nil {
		return nil, wrapUser(err)
	}
	if err := e.saveDriverConfig(targetID, cfg); err != nil {
		return nil, err
	}
	e.mu.Lock()
	delete(e.baidu, targetID)
	e.mu.Unlock()
	return &BaiduPollView{Status: "authorized"}, nil
}
