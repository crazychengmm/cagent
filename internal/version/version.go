// Package version 保存构建期通过 -ldflags -X 注入的版本信息。
// 未注入时取零值 "dev"/"unknown"，仅用于本地开发。
package version

// Version 是语义化版本号，由发布构建注入。
var Version = "dev"

// Commit 是构建对应的 git 短 SHA，由发布构建注入。
var Commit = "unknown"

// String 返回面向命令行展示的完整版本串。
func String() string {
	return "cagent version " + Version + " (commit " + Commit + ")"
}
