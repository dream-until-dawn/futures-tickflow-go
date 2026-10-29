package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

var nodeTestSymbols = []string{"KQ.m@SHFE.rb", "KQ.m@GFEX.si"}

// 系统级Windows临时目录可能允许写入却拒绝解析路径；仅本机测试回退到已有C:/Temp。
func nodeTestTempRoot(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		probe, err := os.MkdirTemp(os.TempDir(), "tickflow-node-path-check-")
		if err != nil {
			t.Fatal(err)
		}
		_, resolveErr := filepath.EvalSymlinks(probe)
		if err := os.Remove(probe); err != nil {
			t.Fatal(err)
		}
		if resolveErr == nil {
			return
		}
		const fallback = `C:\Temp`
		if info, err := os.Stat(fallback); err != nil || !info.IsDir() {
			t.Fatal("系统临时目录不可解析，且本机测试备用目录不存在")
		}
		t.Setenv("TMP", fallback)
		t.Setenv("TEMP", fallback)
	}
}

func syntheticNodePatch(symbols []string, missing bool) map[string]any {
	charts, series := map[string]any{}, map[string]any{}
	for i, symbol := range symbols {
		if missing && i == len(symbols)-1 {
			continue
		}
		chart := "t" + string(rune('0'+i))
		charts[chart] = map[string]any{"ready": true, "more_data": false}
		minute, year := 0, 2026
		if symbol == "KQ.m@CFFEX.IF" {
			minute = 30
		}
		if symbol == "SHFE.rb1605" {
			year = 2016
		}
		stamp := time.Date(year, 10, 8, 9, minute, 0, 0, cst).UnixNano()
		series[symbol] = map[string]any{"60000000000": map[string]any{"last_id": 1, "trading_day_start_id": 0, "trading_day_end_id": 224,
			"data": map[string]any{"0": map[string]any{"datetime": stamp}, "1": map[string]any{"datetime": stamp + 60000000000}}}}
	}
	return map[string]any{"charts": charts, "klines": series}
}

func nodeTestServer(t *testing.T, mode string, hits *atomic.Int32) *httptest.Server {
	return nodeTestServerFor(t, mode, hits, nodeTestSymbols)
}

func nodeTestServerFor(t *testing.T, mode string, hits *atomic.Int32, symbols []string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits.Add(1)
		if mode == "handshake" {
			http.Error(writer, "unavailable", 503)
			return
		}
		connection, err := websocket.Accept(writer, request, nil)
		if err != nil {
			return
		}
		defer connection.CloseNow()
		if mode == "disconnect" {
			return
		}
		peeks := 0
		for {
			_, payload, err := connection.Read(context.Background())
			if err != nil {
				return
			}
			var command map[string]any
			if json.Unmarshal(payload, &command) != nil {
				return
			}
			if command["aid"] != "peek_message" {
				continue
			}
			peeks++
			if mode == "silent" || ((mode == "missing" || mode == "sentinel") && peeks > 1) {
				continue
			}
			if mode == "malformed" {
				connection.Write(context.Background(), websocket.MessageText, []byte("{broken"))
				continue
			}
			if mode == "secret" {
				connection.Write(context.Background(), websocket.MessageText, []byte(`{"echo":"secret-not-persisted-12345"}`))
				continue
			}
			patch := syntheticNodePatch(symbols, mode == "missing")
			if mode == "late" && peeks == 1 {
				patch = syntheticNodePatch(nodeTestSymbols[:1], false)
			}
			if mode == "fields_late" && peeks == 1 {
				serial := patch["klines"].(map[string]any)[nodeTestSymbols[0]].(map[string]any)["60000000000"].(map[string]any)
				delete(serial, "trading_day_start_id")
				delete(serial, "trading_day_end_id")
			}
			if mode == "sentinel" || mode == "fractional" {
				serial := patch["klines"].(map[string]any)[nodeTestSymbols[0]].(map[string]any)["60000000000"].(map[string]any)
				if mode == "sentinel" {
					serial["trading_day_start_id"] = -1
				} else {
					serial["last_id"] = 1.5
				}
			}
			data, _ := json.Marshal(map[string]any{"aid": "rtn_data", "data": []any{patch}})
			if err := connection.Write(context.Background(), websocket.MessageText, data); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// 可显式保留合成五路产物，供Python独立CLI重放；默认仍用测试临时目录。
func TestDayNodeReplayFixture(t *testing.T) {
	nodeTestTempRoot(t)
	directory := os.Getenv("TICKFLOW_NODE_FIXTURE")
	if directory == "" {
		directory = filepath.Join(t.TempDir(), "evidence")
	}
	symbols := []string{"KQ.m@SHFE.rb", "KQ.m@GFEX.si", "KQ.m@SHFE.ag", "SHFE.rb1605", "KQ.m@CFFEX.IF"}
	var hits atomic.Int32
	server := nodeTestServerFor(t, "valid", &hits, symbols)
	nodes, err := collectDayNodes(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"), "fixture-only-no-credential", symbols, directory, 2*time.Second)
	if err != nil || len(nodes) != 5 {
		t.Fatalf("五路合成产物失败：%v", err)
	}
	t.Log("synthetic local WebSocket evidence only; not live suspended-night acceptance")
}

func TestDayNodeWireEvidenceAndFailures(t *testing.T) {
	nodeTestTempRoot(t)
	for _, mode := range []string{"valid", "late", "fields_late", "silent", "missing", "sentinel", "fractional", "malformed", "disconnect", "handshake", "secret"} {
		t.Run(mode, func(t *testing.T) {
			var hits atomic.Int32
			server := nodeTestServer(t, mode, &hits)
			directory := filepath.Join(t.TempDir(), "evidence")
			started := time.Now()
			nodes, err := collectDayNodes(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"), "secret-not-persisted-12345", nodeTestSymbols, directory, 250*time.Millisecond)
			valid := mode == "valid" || mode == "late" || mode == "fields_late"
			if (err == nil) != valid {
				t.Fatalf("模式%s结果错误：%v", mode, err)
			}
			if time.Since(started) > 5*time.Second {
				t.Fatal("无响应读取未受期限约束")
			}
			if valid && (len(nodes) != 2 || nodes[nodeTestSymbols[0]].startID != 0 || !nodes[nodeTestSymbols[0]].haveSpan) {
				t.Fatal("零id或晚到节点解释错误")
			}
			raw, readErr := os.ReadFile(filepath.Join(directory, "summary.json"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			var summary map[string]any
			if json.Unmarshal(raw, &summary) != nil {
				t.Fatal("终态摘要无效")
			}
			if (summary["state"] == "completed") != valid {
				t.Fatal("失败被写成成功")
			}
			for _, name := range []string{"metadata.json", "requests.jsonl", "frames.jsonl", "summary.json"} {
				content, err := os.ReadFile(filepath.Join(directory, name))
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(content), "secret-not-persisted-12345") || strings.Contains(string(content), "Authorization") {
					t.Fatal("鉴权材料落盘")
				}
			}
		})
	}
}

type failingNodeWriter struct{}

func (failingNodeWriter) Write(context.Context, websocket.MessageType, []byte) error {
	return errors.New("secret-error-detail")
}

func TestDayNodeSendAndExclusiveDirectory(t *testing.T) {
	nodeTestTempRoot(t)
	if err := writeNodeRequest(context.Background(), failingNodeWriter{}, map[string]any{"aid": "peek_message"}); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal("发送失败丢失或泄露底层错误")
	}
	if err := writeNodeRequest(context.Background(), failingNodeWriter{}, make(chan int)); err == nil {
		t.Fatal("编码错误被忽略")
	}
	var hits atomic.Int32
	server := nodeTestServer(t, "valid", &hits)
	directory := t.TempDir()
	original := filepath.Join(directory, "original")
	if err := os.WriteFile(original, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := collectDayNodes(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"), "unused", nodeTestSymbols, directory, time.Second); err == nil {
		t.Fatal("已有目录被复用")
	}
	content, err := os.ReadFile(original)
	if err != nil || string(content) != "original" || hits.Load() != 0 {
		t.Fatal("已有证据改变或拒绝前已连接")
	}
	otherRepo := filepath.Join(t.TempDir(), "other-repository")
	if err := os.MkdirAll(filepath.Join(otherRepo, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := collectDayNodes(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"), "unused", nodeTestSymbols, filepath.Join(otherRepo, "evidence"), time.Second); err == nil {
		t.Fatal("其他Git工作树被当成仓外目录")
	}
	if hits.Load() != 0 {
		t.Fatal("拒绝仓内输出前已联网")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := collectDayNodes(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), "unused", nodeTestSymbols, filepath.Join(t.TempDir(), "cancelled"), time.Second); err == nil {
		t.Fatal("已取消上下文被接受")
	}
}

func TestDayNodeIntegerAndRetiredControl(t *testing.T) {
	for _, value := range []any{true, 1.5, float64(9007199254740992)} {
		if _, ok := nodeInteger(value); ok {
			t.Fatalf("非法id被接受：%v", value)
		}
	}
	patch := syntheticNodePatch([]string{"SHFE.rb1605"}, false)
	serial := patch["klines"].(map[string]any)["SHFE.rb1605"].(map[string]any)["60000000000"].(map[string]any)
	delete(serial, "trading_day_start_id")
	delete(serial, "trading_day_end_id")
	data, _ := json.Marshal(patch)
	var snap map[string]any
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatal(err)
	}
	nodes, complete, err := parseDayNodes(snap, []nodeRequest{{Symbol: "SHFE.rb1605", Chart: "t0", NeedSpan: false}})
	if err != nil || !complete || nodes["SHFE.rb1605"].haveSpan {
		t.Fatal("退市对照的缺跨度被当零或误拒绝")
	}
	if _, complete, _ := parseDayNodes(snap, []nodeRequest{{Symbol: "SHFE.rb1605", Chart: "t0", NeedSpan: true}}); complete {
		t.Fatal("必需跨度缺失被放行")
	}
}
