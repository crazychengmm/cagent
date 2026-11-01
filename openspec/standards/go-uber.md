# Go 代码规范 checklist — Uber Go Style Guide（索引式精简）

> **来源**：Uber Go Style Guide（英文原文，MIT），https://github.com/uber-go/guide/blob/master/style.md
> 并叠加 Go 官方 [Effective Go](https://go.dev/doc/effective_go) 与 [CodeReviewComments](https://github.com/golang/go/wiki/CodeReviewComments)。
> 本文件是索引式 checklist：每条一句可检查标准，详细理由与正/反例请看原文对应章节（条目标题即原文小节名）。级别为本 skill 标注：【强制】= correctness/明确社区共识，【推荐】= guide 默认主张，【参考】= 视场景。ID 前缀 `G-`。
> 项目必须过 `gofmt`（或 goimports）+ `go vet`，这是所有规则的前提。

## Guidelines（设计准则）

- **G-IFACE-01【强制】 Verify Interface Compliance**：有意实现接口处编译期断言：`var _ Interface = (*Type)(nil)`，防止签名漂移。
- **G-IFACE-02【推荐】 Pointers to Interfaces**：接口通常持值/指针本身；不要把 `*Interface` 当参数或字段（几乎总是错误）。
- **G-IFACE-03【推荐】 Receivers and Interfaces**：方法集要与值/指针接收者一致规划；可复制的小类型用值接收者，含锁/大对象用指针接收者，同一类型不混用。
- **G-MAP-01【强制】 Copy Slices and Maps at Boundaries**：把 slice/map 传过包边界（尤其返回内部持有的）时复制，避免调用方篡改内部状态。
- **G-DEFER-01【推荐】 Defer to Clean up**：资源获取后紧跟 defer 释放，函数再短也不例外。
- **G-CHAN-01【推荐】 Channel Size is One or None**：channel 优先无缓冲；确需缓冲默认 size=1 并注释理由。
- **G-ENUM-01【强制】 Start Enums at One**：枚举从 1 开始；零值保留给「未设置」并让它无效，防漏赋值。
- **G-ENUM-02【推荐】 Use "enums" in switch**：枚举类型配合穷尽 switch；字符串常量用自定义类型防任意字符串传入。
- **G-ERR-01【强制】 Error Types**：定义哨兵错误用 `errors.Is`，自定义错误类型用 `errors.As`；调用方按类型/哨兵判断。
- **G-ERR-02【强制】 Error Wrapping**：跨层返回错误用 `fmt.Errorf("...: %w", err)`（1.13+ 包裹），只在错误将被该层处理时才用 %v 断开链。
- **G-ERR-03【推荐】 Inline Error String 命名**：内联错误字符串小写开头、不带标点（会被再包装）；导出错误类型/哨兵可大写。
- **G-ERR-04【强制】 Handle Errors Once**：错误只处理一次——要么记日志（终止调用链），要么添加上下文返回；禁止「记日志后又返回」造成重复噪音。
- **G-ERR-05【推荐】 Error Patterns**：按仓库既有模式（sentinel/type/wrap）保持一致，不一个项目三种风格。
- **G-CTX-01【强制】 Contexts**：context.Context 作为函数第一个参数且命名 `ctx`；不放进 struct（长期生命周期对象除外，且需注释）。
- **G-CTX-02【推荐】**：派生 ctx 的 CancelFunc 必须被调用（否则 vet 报 lostcancel），即使 ctx 提前结束也要 defer cancel。
- **G-PANIC-01【强制】 Don't Panic / Don't Panic in Libraries**：库代码对可预期错误返回 error，不用 panic；进程最外层(main/init/中间件 recover)才允许兜底 panic。
- **G-CONC-01【推荐】**：goroutine 生命周期可控——知道它何时结束；避免 goroutine 泄漏（无接收方的发送、无退出路径的 for）。
- **G-CONC-02【强制】**：map 不允许并发读写（用 sync.Map 或加锁/RWMutex）；data race 必须用 `-race` 在 CI 拦截。

## Performance（性能）

- **G-PERF-01【推荐】 Prefer strconv over fmt**：非热路径可读性优先；布尔/数字转字符串用 strconv 比 fmt 快。
- **G-PERF-02【推荐】 Avoid string-to-byte conversion**：避免在热路径反复 `string(bytes)` 转换，同一数据保持一种类型。
- **G-PERF-03【推荐】 Prefer Specifying Container Capacity**：容量可预估时给 make 传 cap（slice/map），减少扩容；预估不到不要硬编。
- **G-PERF-04【参考】**：不为假想性能牺牲可读性；先 benchmark/profile 再优化（measure before optimize）。

## Style（风格）

- **G-FMT-01【强制】**：gofmt/goimports 零差异；imports 分两组以上：标准库一组，第三方一组（可再分本地组），组间空行。
- **G-PKG-01【强制】 Package Names**：包名全小写、短、不含下划线/驼峰、不与常用变量名冲突（不叫 base/util/common 大杂烩，按职责命名）。
- **G-PKG-02【强制】**：导入包名与标识符冲突时才用 import alias，且全文件保持一致。
- **G-FUNC-01【推荐】 Function Names**：Go 用 MixedCaps，不用下划线（测试函数为 `Test_*` 表驱动分组、外部 CGo 例外）。
- **G-FUNC-02【推荐】 Function Grouping and Ordering**：函数按调用粗略顺序排列，调用者在被调用者之上；公有方法在前。
- **G-FUNC-03【推荐】 Reduce Nesting**：先处理错误/边界并提前返回，主路径保持最小缩进；嵌套超过 3 层重构成问题信号。
- **G-VAR-01【推荐】 Unnecessary Else**：if 分支 return 后不写 else。
- **G-VAR-02【推荐】 Top-level Package-level Variables**：顶层可变状态最少化；能用 const 就 const，包级 var 尽量保持不可变。
- **G-VAR-03【推荐】 Local Variable Declarations**：显式初始化用 `:=`；零值变量用 `var x T` 表达「等待赋值」语义；不要为声明而声明。
- **G-VAR-04【推荐】 nil is a valid slice**：返回 nil slice 与空 slice 对 len/range 等价，保持一种风格；JSON 输出空数组 `[]` 的 API 场景才显式 make。
- **G-VAR-05【推荐】 Reduce Scope of Variables**：变量作用域尽可能小（与 C-风格「声明在最外」相反），但不牺牲可读性。
- **G-CALL-01【推荐】 Avoid Naked Parameters**：调用处裸字面量含义不清时用具名参数/变量（`// true 表示...` 不够，用 `var enableCache = true`）。
- **G-MAP-02【推荐】 Initialize Maps**：会写入的 map 在创建处初始化（make），避免 nil map panic；struct 持有时在构造函数中初始化。
- **G-STR-01【推荐】 String Formatting outside print**：非打印场景用 fmt.Sprintf；错误用 errors.New/fmt.Errorf。
- **G-STRUCT-01【参考】 Field Alignment / struct 布局**：热点结构可按字段大小排序优化对齐，先正确性再考虑。
- **G-EMBED-01【参考】 Embedding**：优先组合而非继承式嵌入；不要为了省几行方法转发而嵌入让类型语义变混；同步原语（sync.Mutex）不要嵌入导出结构。

## Patterns（模式）

- **G-TBL-01【推荐】 Test Tables**：多输入/期望用表驱动 + 子测试 `t.Run(name, ...)`，失败能定位条目。
- **G-TST-01【强制】**：测试断言用明确信息（输入/期望/实际）；`t.Parallel()` 用于可并行用例；辅助函数首行 `t.Helper()`。
- **G-OPT-01【推荐】 Functional Options**：构造函数选项 ≥3 个且未来会增长时用 functional options 模式；参数少且稳定时普通参数即可，不过度设计。
- **G-GEN-01【参考】 Generics**：同类样板代码确实重复且类型无关时才用泛型；不为用而用。

## Linting（工具门禁）

- **G-LINT-01【强制】**：本地与 CI 至少跑 `gofmt -l .`、`go vet ./...`、`go build ./...`、`go test ./... -race`。
- **G-LINT-02【推荐】**：采用 golangci-lint，配置可参考 Uber 官方配置：https://github.com/uber-go/guide/blob/master/style.md#linting （链接内含仓库地址）；新增 lint 规则的违规在本 change 内清零，不带着存量违规提交新代码。
