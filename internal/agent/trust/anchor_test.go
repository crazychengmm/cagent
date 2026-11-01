package trust

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crazychengmm/cagent/internal/server/ca"
)

func startTLSServer(t *testing.T) (addr string, certDER []byte, caPEM []byte) {
	t.Helper()
	caDir := filepath.Join(t.TempDir(), "ca")
	rootCA, err := ca.LoadOrCreate(caDir)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := rootCA.EnsureServerCertificate(caDir,
		[]string{"localhost", "127.0.0.1"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	certDER = cert.Certificate[0]
	caPEM = rootCA.CACertPEM()

	lis, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{*cert}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lis.Close() })
	go func() {
		for {
			conn, err := lis.Accept()
			if err != nil {
				return
			}
			go func(c *tls.Conn) {
				_ = c.Handshake()
				_ = c.Close()
			}(conn.(*tls.Conn))
		}
	}()
	return lis.Addr().String(), certDER, caPEM
}

func handshake(t *testing.T, addr string, cfg *tls.Config) error {
	t.Helper()
	conn, err := tls.Dial("tcp", addr, cfg)
	if err == nil {
		defer conn.Close()
		return conn.Handshake()
	}
	return err
}

func TestEnrollmentTLS_NoAnchorRejected(t *testing.T) {
	t.Parallel()

	if _, err := EnrollmentTLS("", ""); err != ErrNoAnchor {
		t.Errorf("无锚点应返回 ErrNoAnchor，实际 %v", err)
	}
}

func TestEnrollmentTLS_PEMAnchor(t *testing.T) {
	t.Parallel()

	addr, _, caPEM := startTLSServer(t)
	caFile := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(caFile, caPEM, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := EnrollmentTLS(caFile, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := handshake(t, addr, cfg); err != nil {
		t.Errorf("正确 CA 锚点握手应成功: %v", err)
	}

	// 外来 CA：另一套根。
	otherCA, err := ca.LoadOrCreate(filepath.Join(t.TempDir(), "ca2"))
	if err != nil {
		t.Fatal(err)
	}
	otherFile := filepath.Join(t.TempDir(), "other-ca.crt")
	if err := os.WriteFile(otherFile, otherCA.CACertPEM(), 0o644); err != nil {
		t.Fatal(err)
	}
	badCfg, err := EnrollmentTLS(otherFile, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := handshake(t, addr, badCfg); err == nil {
		t.Error("外来 CA 锚点握手必须失败")
	}
}

func TestEnrollmentTLS_FingerprintAnchor(t *testing.T) {
	t.Parallel()

	addr, certDER, _ := startTLSServer(t)
	sum := sha256.Sum256(certDER)
	fp := hex.EncodeToString(sum[:])

	t.Run("正确指纹", func(t *testing.T) {
		cfg, err := EnrollmentTLS("", fp)
		if err != nil {
			t.Fatal(err)
		}
		if err := handshake(t, addr, cfg); err != nil {
			t.Errorf("指纹匹配应握手成功: %v", err)
		}
	})
	t.Run("冒号分隔指纹", func(t *testing.T) {
		colon := fp[:2] + ":" + fp[2:]
		cfg, err := EnrollmentTLS("", colon)
		if err != nil {
			t.Fatal(err)
		}
		if err := handshake(t, addr, cfg); err != nil {
			t.Errorf("冒号指纹应被规范化后匹配: %v", err)
		}
	})
	t.Run("错误指纹", func(t *testing.T) {
		cfg, err := EnrollmentTLS("", strings.Repeat("00", 32))
		if err != nil {
			t.Fatal(err)
		}
		if err := handshake(t, addr, cfg); err == nil {
			t.Error("指纹不匹配必须握手失败")
		}
	})
	t.Run("非法长度", func(t *testing.T) {
		if _, err := EnrollmentTLS("", "abc"); err == nil {
			t.Error("非 32 字节十六进制应报错")
		}
	})
}

func TestMTLSConfig_ClonesBase(t *testing.T) {
	t.Parallel()

	base := &tls.Config{MinVersion: tls.VersionTLS12}
	got := MTLSConfig(base, tls.Certificate{})
	if len(got.Certificates) != 1 {
		t.Error("叠加配置应包含客户端证书")
	}
	if len(base.Certificates) != 0 {
		t.Error("不应修改基础配置")
	}
}
