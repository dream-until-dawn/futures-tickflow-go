package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/coder/websocket"
)

var dayNodeOut = flag.String("day-node-out", "", "L9原始取证目录；必须为仓外不存在的目录")

type nodeRequest struct {
	Symbol   string `json:"symbol"`
	Chart    string `json:"chart"`
	NeedSpan bool   `json:"need_span"`
}

type nodeFrame struct {
	RecvMS    int64  `json:"recv_ms"`
	ElapsedNS int64  `json:"elapsed_ns"`
	Type      int    `json:"message_type"`
	Payload   []byte `json:"payload_base64"`
}

type nodeReadout struct {
	Symbol   string `json:"symbol"`
	LastID   int64  `json:"last_id"`
	StartID  *int64 `json:"start_id"`
	EndID    *int64 `json:"end_id"`
	FirstNS  *int64 `json:"first_ns"`
	NewestNS int64  `json:"newest_ns"`
	HaveSpan bool   `json:"have_span"`
}

type nodeBundle struct {
	root          string
	file          *os.File
	writer        *json.Encoder
	digest        hash.Hash
	frames        int
	requestFile   *os.File
	requestWriter *json.Encoder
	requestDigest hash.Hash
	requests      int
}

// nodeInteger 拒绝布尔、小数、非有限值及float64不能精确表达的id；零id合法。
func nodeInteger(value any) (int64, bool) {
	number, ok := value.(float64)
	if !ok || math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number || math.Abs(number) > 9007199254740991 {
		return 0, false
	}
	return int64(number), true
}

func nodeTimestamp(row map[string]any) (time.Time, bool) {
	n, ok := row["datetime"].(float64)
	if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 || n >= 9223372036854775808 || math.Trunc(n) != n {
		return time.Time{}, false
	}
	stamp := int64(n)
	if stamp%60000000000 != 0 {
		return time.Time{}, false
	}
	return time.Unix(0, stamp).In(cst), true
}

// parseDayNodes 每次都从当前合并快照重算，避免缓存早期节点而漏掉晚到的修正。
func parseDayNodes(snap map[string]any, requests []nodeRequest) (map[string]dayNode, bool, error) {
	nodes := map[string]dayNode{}
	complete := true
	for _, request := range requests {
		chart := obj(snap, "charts", request.Chart)
		ready, _ := chart["ready"].(bool)
		more, _ := chart["more_data"].(bool)
		if value, exists := chart["more_data"]; exists {
			if _, ok := value.(bool); !ok {
				return nodes, false, errors.New("invalid_chart_state")
			}
		}
		if !ready || more {
			complete = false
			continue
		}
		serial := obj(snap, "klines", request.Symbol, "60000000000")
		last, ok := nodeInteger(serial["last_id"])
		if serial["last_id"] != nil && !ok {
			return nodes, false, errors.New("invalid_node_id")
		}
		if !ok || last < 0 {
			complete = false
			continue
		}
		n := dayNode{lastID: last}
		start, haveStart := nodeInteger(serial["trading_day_start_id"])
		end, haveEnd := nodeInteger(serial["trading_day_end_id"])
		if (serial["trading_day_start_id"] != nil && !haveStart) || (serial["trading_day_end_id"] != nil && !haveEnd) {
			return nodes, false, errors.New("invalid_node_id")
		}
		n.startID, n.endID, n.haveStart, n.haveEnd = start, end, haveStart, haveEnd
		if haveStart && haveEnd && start >= 0 && end >= 0 {
			if start > last || end < last {
				return nodes, false, errors.New("inconsistent_node_ids")
			}
			n.haveSpan = true
			var valid bool
			n.first, valid = nodeTimestamp(obj(serial, "data", strconv.FormatInt(start, 10)))
			if !valid {
				complete = false
				continue
			}
		}
		if request.NeedSpan && !n.haveSpan {
			complete = false
			continue
		}
		var valid bool
		n.newest, valid = nodeTimestamp(obj(serial, "data", strconv.FormatInt(last, 10)))
		if !valid {
			complete = false
			continue
		}
		if n.haveSpan && (n.first.After(n.newest) || (last > start && !n.first.Before(n.newest))) {
			return nodes, false, errors.New("inconsistent_node_labels")
		}
		nodes[request.Symbol] = n
	}
	return nodes, complete && len(nodes) == len(requests), nil
}

func nodeJSON(path string, value any) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	err = json.NewEncoder(file).Encode(value)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

func openNodeBundle(directory string, requests []nodeRequest, started time.Time, timeout time.Duration) (*nodeBundle, error) {
	if outputInsideRepo(directory) {
		return nil, errors.New("evidence_directory_inside_repository")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(directory))
	if err != nil {
		return nil, fmt.Errorf("evidence_parent_unavailable: %w", err)
	}
	resolved := filepath.Join(parent, filepath.Base(directory))
	if outputInsideRepo(resolved) {
		return nil, errors.New("evidence_directory_inside_repository")
	}
	// 输出目录即使经其他工作目录或链接指定，也不能落入任何Git工作树。
	for ancestor := filepath.Dir(resolved); ; ancestor = filepath.Dir(ancestor) {
		if _, err := os.Stat(filepath.Join(ancestor, ".git")); err == nil {
			return nil, errors.New("evidence_directory_inside_repository")
		}
		if filepath.Dir(ancestor) == ancestor {
			break
		}
	}
	if err := os.Mkdir(resolved, 0700); err != nil {
		return nil, errors.New("evidence_directory_exists_or_unavailable")
	}
	revision, modified := "unavailable", "unavailable"
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, item := range info.Settings {
			if item.Key == "vcs.revision" {
				revision = item.Value
			}
			if item.Key == "vcs.modified" {
				modified = item.Value
			}
		}
	}
	metadata := map[string]any{"schema": "day-nodes-v1", "classification": "node-collection-only", "requests": requests, "started_at": started.Format(time.RFC3339Nano), "timeout_seconds": timeout.Seconds(), "source_revision": revision, "source_modified": modified}
	if err := nodeJSON(filepath.Join(resolved, "metadata.json"), metadata); err != nil {
		return nil, errors.New("evidence_metadata_write_failed")
	}
	file, err := os.OpenFile(filepath.Join(resolved, "frames.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, errors.New("evidence_frames_open_failed")
	}
	digest := sha256.New()
	requestFile, err := os.OpenFile(filepath.Join(resolved, "requests.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		file.Close()
		return nil, errors.New("evidence_requests_open_failed")
	}
	requestDigest := sha256.New()
	return &nodeBundle{root: resolved, file: file, writer: json.NewEncoder(io.MultiWriter(file, digest)), digest: digest,
		requestFile: requestFile, requestWriter: json.NewEncoder(io.MultiWriter(requestFile, requestDigest)), requestDigest: requestDigest}, nil
}

func nodeReadouts(nodes map[string]dayNode, requests []nodeRequest) []nodeReadout {
	result := []nodeReadout{}
	for _, request := range requests {
		n, ok := nodes[request.Symbol]
		if !ok {
			continue
		}
		item := nodeReadout{Symbol: request.Symbol, LastID: n.lastID, NewestNS: n.newest.UnixNano(), HaveSpan: n.haveSpan}
		if n.haveStart {
			value := n.startID
			item.StartID = &value
		}
		if n.haveEnd {
			value := n.endID
			item.EndID = &value
		}
		if !n.first.IsZero() {
			value := n.first.UnixNano()
			item.FirstNS = &value
		}
		result = append(result, item)
	}
	return result
}

// writeNodeRequest 的每次编码和发送均检查错误，不继续在失败连接上等数据。
func writeNodeRequest(ctx context.Context, connection interface {
	Write(context.Context, websocket.MessageType, []byte) error
}, request any) error {
	data, err := json.Marshal(request)
	if err != nil {
		return errors.New("request_encoding_failed")
	}
	if err := connection.Write(ctx, websocket.MessageText, data); err != nil {
		return errors.New("request_write_failed")
	}
	return nil
}

// collectDayNodes 的同一绝对期限覆盖拨号、发送、所有阻塞读取；不隐式重试连接。
func collectDayNodes(parent context.Context, md, token string, symbols []string, directory string, timeout time.Duration) (nodes map[string]dayNode, failure error) {
	nodes = map[string]dayNode{}
	if len(symbols) == 0 || timeout <= 0 || timeout > 90*time.Second {
		return nodes, errors.New("invalid_node_request")
	}
	requests, seen := []nodeRequest{}, map[string]bool{}
	for i, symbol := range symbols {
		if symbol == "" || seen[symbol] {
			return nodes, errors.New("invalid_node_request")
		}
		seen[symbol] = true
		// 固定的已退市负对照只要求最新标签；它可能没有当前交易日跨度字段。
		requests = append(requests, nodeRequest{Symbol: symbol, Chart: fmt.Sprintf("t%d", i), NeedSpan: symbol != "SHFE.rb1605"})
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	var bundle *nodeBundle
	if directory != "" {
		var err error
		bundle, err = openNodeBundle(directory, requests, started, timeout)
		if err != nil {
			return nodes, err
		}
	}
	stage := "dial"
	defer func() {
		if bundle == nil {
			return
		}
		if err := bundle.file.Close(); err != nil && failure == nil {
			failure = errors.New("evidence_close_failed")
		}
		if err := bundle.requestFile.Close(); err != nil && failure == nil {
			failure = errors.New("evidence_close_failed")
		}
		state, reason := "completed", "complete_nodes"
		if failure != nil {
			state, reason = "failed", failure.Error()
		}
		summary := map[string]any{"schema": "day-nodes-v1", "classification": "node-collection-only", "state": state, "stage": stage, "reason": reason, "frames": bundle.frames, "elapsed_ns": time.Since(started).Nanoseconds(), "ended_at": time.Now().Format(time.RFC3339Nano), "frames_sha256": hex.EncodeToString(bundle.digest.Sum(nil)), "nodes": nodeReadouts(nodes, requests),
			"requests_count": bundle.requests, "requests_sha256": hex.EncodeToString(bundle.requestDigest.Sum(nil))}
		if err := nodeJSON(filepath.Join(bundle.root, "summary.json"), summary); err != nil {
			failure = errors.New("evidence_summary_write_failed")
		}
	}()
	connection, _, err := websocket.Dial(ctx, md, &websocket.DialOptions{CompressionMode: websocket.CompressionNoContextTakeover, HTTPHeader: http.Header{"User-Agent": {uaSelf}, "Accept": {"application/json"}, "Authorization": {"Bearer " + token}}})
	if err != nil {
		if ctx.Err() != nil {
			return nodes, errors.New("deadline_or_cancellation")
		}
		return nodes, errors.New("connection_dial_failed")
	}
	defer connection.CloseNow()
	connection.SetReadLimit(256 << 20)
	send := func(request map[string]any) error {
		if err := writeNodeRequest(ctx, connection, request); err != nil {
			return err
		}
		if bundle != nil {
			if err := bundle.requestWriter.Encode(map[string]any{"sent_ms": time.Now().UnixMilli(), "elapsed_ns": time.Since(started).Nanoseconds(), "request": request}); err != nil {
				return errors.New("evidence_request_write_failed")
			}
			bundle.requests++
		}
		return nil
	}
	stage = "send"
	for _, request := range requests {
		if err := send(map[string]any{"aid": "set_chart", "chart_id": request.Chart, "ins_list": request.Symbol, "duration": int64(60000000000), "view_width": 500}); err != nil {
			return nodes, err
		}
	}
	if err := send(map[string]any{"aid": "peek_message"}); err != nil {
		return nodes, err
	}
	snap := map[string]any{}
	for {
		stage = "read"
		kind, payload, err := connection.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nodes, errors.New("deadline_or_cancellation")
			}
			return nodes, errors.New("connection_read_failed")
		}
		if token != "" && bytes.Contains(payload, []byte(token)) {
			return nodes, errors.New("authorization_material_in_response_suppressed")
		}
		if bundle != nil {
			if err := bundle.writer.Encode(nodeFrame{RecvMS: time.Now().UnixMilli(), ElapsedNS: time.Since(started).Nanoseconds(), Type: int(kind), Payload: payload}); err != nil {
				return nodes, errors.New("evidence_frame_write_failed")
			}
			bundle.frames++
		}
		stage = "decode"
		var message struct {
			Aid  string           `json:"aid"`
			Data []map[string]any `json:"data"`
		}
		if kind != websocket.MessageText || json.Unmarshal(payload, &message) != nil {
			return nodes, errors.New("invalid_response")
		}
		if message.Aid == "rtn_data" {
			for _, patch := range message.Data {
				merge(snap, patch)
			}
		}
		stage = "nodes"
		var complete bool
		nodes, complete, err = parseDayNodes(snap, requests)
		if err != nil {
			return nodes, err
		}
		if ctx.Err() != nil {
			return nodes, errors.New("deadline_or_cancellation")
		}
		if complete {
			return nodes, nil
		}
		stage = "send"
		if err := send(map[string]any{"aid": "peek_message"}); err != nil {
			return nodes, err
		}
	}
}
