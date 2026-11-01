package audit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestFileLogger_WriteJSONL(t *testing.T) {
	t.Parallel()

	dataDir := filepath.Join(t.TempDir(), "data")
	l, err := OpenFileLogger(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.JobStarted(JobStart{
		TS: time.Now().UTC(), JobID: "j-1", RequestedBy: "alice",
		Command: "uptime", WorkingDir: "/tmp",
	}); err != nil {
		t.Fatal(err)
	}
	if err := l.JobFinished(JobFinish{
		TS: time.Now().UTC(), JobID: "j-1", Result: "succeeded",
		ExitCode: 0, OutputPreview: "out",
	}); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(Path(dataDir))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != filePerm {
		t.Errorf("audit.log 权限 = %o, 期望 %o", info.Mode().Perm(), filePerm)
	}
	dirInfo, _ := os.Stat(dataDir)
	if dirInfo.Mode().Perm() != dirPerm {
		t.Errorf("目录权限 = %o", dirInfo.Mode().Perm())
	}

	f, err := os.Open(Path(dataDir))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	var lines []map[string]any
	for scanner.Scan() {
		var line map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatalf("非合法 JSON: %v", err)
		}
		lines = append(lines, line)
	}
	if len(lines) != 2 {
		t.Fatalf("行数 = %d", len(lines))
	}
	if lines[0]["event"] != "job.started" || lines[0]["command"] != "uptime" {
		t.Errorf("started 行错误: %v", lines[0])
	}
	if lines[1]["event"] != "job.finished" || lines[1]["result"] != "succeeded" ||
		lines[1]["output_preview"] != "out" {
		t.Errorf("finished 行错误: %v", lines[1])
	}
}

func TestFileLogger_AppendAcrossReopen(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	l1, _ := OpenFileLogger(dataDir)
	_ = l1.JobStarted(JobStart{JobID: "a"})
	l1.Close()

	l2, _ := OpenFileLogger(dataDir)
	_ = l2.JobFinished(JobFinish{JobID: "b"})
	l2.Close()

	content, err := os.ReadFile(Path(dataDir))
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(content))
	n := 0
	for scanner.Scan() {
		n++
	}
	if n != 2 {
		t.Errorf("重开追加后应 2 行，实际 %d", n)
	}
}

func TestFileLogger_ConcurrentSafe(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	l, _ := OpenFileLogger(dataDir)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = l.JobFinished(JobFinish{JobID: "concurrent"})
		}()
	}
	wg.Wait()
	l.Close()

	content, _ := os.ReadFile(Path(dataDir))
	scanner := bufio.NewScanner(bytes.NewReader(content))
	n := 0
	for scanner.Scan() {
		n++
	}
	if n != 20 {
		t.Errorf("并发写应 20 行，实际 %d", n)
	}
}
