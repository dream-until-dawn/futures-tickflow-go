package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestTailSymbolSelection 保留旧主连入口，同时拒绝合约列表和不可解释的代码。
func TestTailSymbolSelection(t *testing.T) {
	for _, symbol := range []string{tailSym, "SHFE.rb2701", "SHFE.rb2612"} {
		if err := validateTailSymbol(symbol); err != nil {
			t.Fatal(symbol, err)
		}
	}
	for _, symbol := range []string{"", "SHFE.rb2700", "SHFE.rb2713", "SHFE.rb2701,SHFE.rb2705", "KQ.m@SHFE.cu", "SHFE.rb2701 "} {
		if err := validateTailSymbol(symbol); err == nil {
			t.Fatal("非法合约被接受", symbol)
		}
	}
}

// TestConcreteTailWireAndExclusiveOutput 核对真实录制入口发出的合约键与旧文件保护。
func TestConcreteTailWireAndExclusiveOutput(t *testing.T) {
	var lock sync.Mutex
	var requested []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionNoContextTakeover})
		if err != nil {
			return
		}
		defer connection.CloseNow()
		for {
			_, data, err := connection.Read(context.Background())
			if err != nil {
				return
			}
			var request map[string]any
			if json.Unmarshal(data, &request) != nil {
				return
			}
			if symbol, ok := request["ins_list"].(string); ok {
				lock.Lock()
				requested = append(requested, symbol)
				lock.Unlock()
			}
			if request["aid"] == "peek_message" {
				time.Sleep(time.Millisecond)
				if connection.Write(context.Background(), websocket.MessageText, []byte(`{"aid":"rtn_data","data":[]}`)) != nil {
					return
				}
			}
		}
	}))
	defer server.Close()
	oldOnly, oldSymbol, oldEnd, oldOut, oldFailed := only, *tailSymbol, *tailEnd, *tailOut, failed
	defer func() { only, *tailSymbol, *tailEnd, *tailOut, failed = oldOnly, oldSymbol, oldEnd, oldOut, oldFailed }()
	only, *tailSymbol, *tailOut, failed = "shinny-live-tail", "SHFE.rb2701", filepath.Join(t.TempDir(), "raw.jsonl"), 0
	*tailEnd = time.Now().Add(150 * time.Millisecond).Format(time.RFC3339Nano)
	probeLiveTail("ws"+strings.TrimPrefix(server.URL, "http"), "local-test-only")
	lock.Lock()
	if len(requested) != 2 || requested[0] != "SHFE.rb2701" || requested[1] != "SHFE.rb2701" {
		t.Errorf("实际订阅不是指定合约：%v", requested)
	}
	lock.Unlock()
	data, err := os.ReadFile(*tailOut)
	if err != nil || len(data) == 0 || failed != 0 {
		t.Fatalf("录制基线失败：%v / %d", err, failed)
	}
	*tailEnd = time.Now().Add(time.Second).Format(time.RFC3339Nano)
	probeLiveTail("ws"+strings.TrimPrefix(server.URL, "http"), "local-test-only")
	after, err := os.ReadFile(*tailOut)
	if failed != 1 || err != nil || string(after) != string(data) {
		t.Fatal("旧原始文件未被拒绝或发生改变")
	}
}
