// Command sinahttp 准备或执行单URL获批原生HTTP取证；不执行任何取得的脚本。
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

const target = "http://finance.sina.com.cn/iframe/futures_info_cff.js"

type executionPlan struct {
	Root      string       `json:"execution_root"`
	BinarySHA string       `json:"binary_sha256"`
	Job       batch.Job    `json:"job"`
	Source    evidence.Ref `json:"source"`
	Limits    batch.Limits `json:"limits"`
	Covered   bool         `json:"historical_roster_certified"`
	Financial bool         `json:"financial_eligible"`
}

// executionRoot 固定实际仓外路径；只在本仓目录内调用，避免把源树当作取证输出。
func executionRoot(root string) (string, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return "", err
	}
	absolute = filepath.Join(parent, filepath.Base(absolute))
	if info, err := os.Lstat(absolute); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("执行目录不可是符号链接")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for directory := cwd; ; directory = filepath.Dir(directory) {
		body, readErr := os.ReadFile(filepath.Join(directory, "go.mod"))
		fields := strings.Fields(string(body))
		if readErr == nil && len(fields) >= 2 && fields[0] == "module" && fields[1] == "github.com/dream-until-dawn/futures-tickflow-go" {
			real, err := filepath.EvalSymlinks(directory)
			if err != nil {
				return "", err
			}
			relative, err := filepath.Rel(real, absolute)
			if err != nil {
				return "", err
			}
			if relative == "." || (!strings.HasPrefix(relative, ".."+string(filepath.Separator)) && relative != "..") {
				return "", fmt.Errorf("取证输出必须在仓外")
			}
			return absolute, nil
		}
		if filepath.Dir(directory) == directory {
			break
		}
	}
	return "", fmt.Errorf("必须从本仓目录内调用以核验仓外输出")
}

func digest(body []byte) string { sum := sha256.Sum256(body); return hex.EncodeToString(sum[:]) }

// makePlan 构造和验证不会联网；完整原收据与源码构造器分别守住出处和单URL。
func makePlan(sourceDir, binarySHA string) (executionPlan, batch.SinaStaticHTTPGrant, error) {
	var zero executionPlan
	body, err := os.ReadFile(filepath.Join(sourceDir, "history.jsonp"))
	if err != nil {
		return zero, batch.SinaStaticHTTPGrant{}, err
	}
	raw, err := os.ReadFile(filepath.Join(sourceDir, "receipt.json"))
	if err != nil {
		return zero, batch.SinaStaticHTTPGrant{}, err
	}
	var r batch.Receipt
	if err = json.Unmarshal(raw, &r); err != nil {
		return zero, batch.SinaStaticHTTPGrant{}, err
	}
	if r.State != "received" || r.BodyState != "complete" || r.Bytes != int64(len(body)) || r.HTTP == nil || r.HTTP.Status != 200 || !r.HTTP.Complete || !r.HTTP.ResponseReceived || r.HTTP.FinalURL != r.Evidence.URL || r.Evidence.Verify(body) != nil {
		return zero, batch.SinaStaticHTTPGrant{}, fmt.Errorf("原出处收据/完整性不符")
	}
	decoded, err := hex.DecodeString(binarySHA)
	if err != nil || len(decoded) != 32 {
		return zero, batch.SinaStaticHTTPGrant{}, fmt.Errorf("二进制SHA无效")
	}
	grant := batch.SinaStaticHTTPGrant{URL: target, Method: "GET", Source: r.Evidence, SourceBody: body}
	plan := executionPlan{BinarySHA: binarySHA, Job: batch.Job{ID: "sina-static-http", URL: target, Format: batch.SinaStaticHTTPVersion, Version: batch.SinaStaticHTTPVersion}, Source: r.Evidence, Limits: batch.Limits{Requests: 1, Bytes: (1 << 20) - 1, Interval: time.Second, Timeout: 15 * time.Second}}
	if _, err = batch.NewSinaStaticHTTPDownloader(plan.Limits, grant); err != nil {
		return zero, batch.SinaStaticHTTPGrant{}, err
	}
	return plan, grant, nil
}

func planBytes(plan executionPlan) ([]byte, error) {
	body, err := json.MarshalIndent(plan, "", "  ")
	return append(body, '\n'), err
}
func writeNew(path string, body []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(body); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// prepare 只接受全新目录；不把重复或未知准备恢复为新的取证许可。
func prepare(root string, expected []byte) error {
	if err := os.Mkdir(root, 0700); err != nil {
		return fmt.Errorf("准备目录已存在或未知，禁止重建: %w", err)
	}
	return writeNew(filepath.Join(root, "plan.json"), expected)
}

// begin 的持久标记先于所有下载；崩溃、未知启动或部分结果都不能重发。
func begin(root string, expected []byte, action func() error) error {
	actual, err := os.ReadFile(filepath.Join(root, "plan.json"))
	if err != nil {
		return err
	}
	if !bytes.Equal(actual, expected) {
		return fmt.Errorf("冻结执行计划不符；拒绝请求")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != "plan.json" || !entry.Type().IsRegular() {
			return fmt.Errorf("已有或未知启动/产物；拒绝请求")
		}
	}
	marker, _ := json.Marshal(map[string]any{"plan_sha256": digest(expected), "started_at": time.Now().UTC(), "requests_limit": 1, "identity": batch.SinaStaticHTTPVersion})
	if err = writeNew(filepath.Join(root, "started.json"), append(marker, '\n')); err != nil {
		return fmt.Errorf("已启动或标记持久化失败；拒绝请求: %w", err)
	}
	return action()
}

func execute(root string, plan executionPlan, grant batch.SinaStaticHTTPGrant) error {
	if root != plan.Root {
		return fmt.Errorf("执行目录与冻结计划不符")
	}
	expected, err := planBytes(plan)
	if err != nil {
		return err
	}
	return begin(root, expected, func() error {
		downloader, err := batch.NewSinaStaticHTTPDownloader(plan.Limits, grant)
		if err != nil {
			return err
		}
		results, runErr := batch.Run(context.Background(), filepath.Join(root, "archive"), []batch.Job{plan.Job}, downloader, 1<<20, func(_ batch.Job, body []byte, ref evidence.Ref) error { return ref.Verify(body) })
		requests, size := downloader.Usage()
		report := map[string]any{"results": results, "actual_requests": requests, "actual_bytes": size, "identity": batch.SinaStaticHTTPVersion, "http_untrusted": true, "script_executed": false, "historical_roster_certified": false, "financial_eligible": false}
		if runErr != nil {
			report["error"] = runErr.Error()
		}
		raw, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		if err = writeNew(filepath.Join(root, "result.json"), append(raw, '\n')); err != nil {
			return err
		}
		if runErr != nil {
			return runErr
		}
		if len(results) != 1 || results[0].Receipt.State != "received" {
			return fmt.Errorf("唯一请求失败，诊断保留，禁止重试")
		}
		return nil
	})
}

func run() error {
	source := flag.String("source-dir", "", "冻结HTTPS历史HTML及原收据目录")
	root := flag.String("root", "", "唯一仓外执行目录")
	preparing := flag.Bool("prepare", false, "仅生成冻结计划，零网络")
	executing := flag.Bool("execute", false, "仅在独立审核通过后执行唯一GET")
	flag.Parse()
	if *source == "" || *root == "" || *preparing == *executing {
		return fmt.Errorf("需要source-dir/root，且prepare或execute仅选其一")
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	body, err := os.ReadFile(binary)
	if err != nil {
		return err
	}
	plan, grant, err := makePlan(*source, digest(body))
	if err != nil {
		return err
	}
	actualRoot, err := executionRoot(*root)
	if err != nil {
		return err
	}
	plan.Root = actualRoot
	if *preparing {
		raw, err := planBytes(plan)
		if err != nil {
			return err
		}
		return prepare(actualRoot, raw)
	}
	return execute(actualRoot, plan, grant)
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
