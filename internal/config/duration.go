// Package config 定义 agent 与 server 的配置结构、默认值、YAML 加载与启动校验。
package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration 包装 time.Duration，使 YAML 中可直接书写 "15s"、"5m"、"24h"、"90d"。
type Duration time.Duration

// Std 返回标准库 time.Duration。
func (d Duration) Std() time.Duration {
	return time.Duration(d)
}

// UnmarshalYAML 实现 yaml.Unmarshaler，支持标准单位外加按天（d）。
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var text string
	if err := value.Decode(&text); err != nil {
		return err
	}
	parsed, err := parseDuration(text)
	if err != nil {
		return fmt.Errorf("无效时长 %q: %w", text, err)
	}
	*d = Duration(parsed)
	return nil
}

// parseDuration 在 time.ParseDuration 基础上支持 d（天，按 24h）。
func parseDuration(text string) (time.Duration, error) {
	if strings.HasSuffix(text, "d") {
		days, err := strconv.ParseFloat(strings.TrimSuffix(text, "d"), 64)
		if err != nil || days < 0 {
			return 0, fmt.Errorf("天数非法: %s", text)
		}
		return time.Duration(days * float64(24*time.Hour)), nil
	}
	return time.ParseDuration(text)
}
