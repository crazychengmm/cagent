// Package config 定义 agent 与 server 的配置结构、默认值、YAML 加载与启动校验。
package config

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration 包装 time.Duration，使 YAML 中可直接书写 "15s"、"5m" 等时长字符串。
type Duration time.Duration

// Std 返回标准库 time.Duration。
func (d Duration) Std() time.Duration {
	return time.Duration(d)
}

// UnmarshalYAML 实现 yaml.Unmarshaler。
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var text string
	if err := value.Decode(&text); err != nil {
		return err
	}

	parsed, err := time.ParseDuration(text)
	if err != nil {
		return fmt.Errorf("无效时长 %q: %w", text, err)
	}
	*d = Duration(parsed)
	return nil
}
