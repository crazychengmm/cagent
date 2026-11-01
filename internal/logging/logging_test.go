package logging

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestNew_LevelFiltering(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger, err := New("info", &buf)
	if err != nil {
		t.Fatalf("New(info) 失败: %v", err)
	}

	logger.Debug("debug 不应出现")
	logger.Info("info 应出现", "key", "v")

	out := buf.String()
	if strings.Contains(out, "debug 不应出现") {
		t.Errorf("info 级别不应输出 debug 日志，实际: %s", out)
	}
	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &entry); err != nil {
		t.Fatalf("输出不是合法 JSON: %v，原文: %s", err, out)
	}
	if entry["msg"] != "info 应出现" || entry["level"] != "INFO" {
		t.Errorf("JSON 字段不符: %v", entry)
	}
	if entry["key"] != "v" {
		t.Errorf("结构化字段丢失: %v", entry)
	}
}

func TestNew_InvalidLevel(t *testing.T) {
	t.Parallel()

	if _, err := New("verbose", &bytes.Buffer{}); err == nil {
		t.Fatal("未知日志级别应返回错误")
	}
}
