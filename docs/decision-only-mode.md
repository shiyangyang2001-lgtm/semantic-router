# Decision-Only 功能说明

本次改动在官方 `v0.3.0` 上新增可选的 Decision-Only 模式。开启后，SR 执行原有规则判断和类型选择，直接返回决策 JSON；客户网关根据返回的类型决定调用哪个具体模型。

`Fast`、`Pro`、`Ultra` 在这里表示网关约定的路由类型，不要求它们对应 SR 可以直接调用的模型服务。为兼容 SR 的字段命名，结果仍使用 `selected_model` 字段承载类型。

## 功能范围

Decision-Only 模式提供配置开关、请求流程接入、决策结果整理和 JSON 响应能力，并在 `debug` 中返回决策依据及分阶段耗时；启用 Router Replay 后，可保存请求和决策结果供后续查询。

## 请求处理流程

```text
客户网关发送请求
  → SR 提取请求内容并执行原有规则判断
  → 得到规则选出的类型，或使用默认类型
  → 按 Router Replay 配置记录请求和决策结果
  → 返回决策 JSON
  → 客户网关将类型映射到具体模型并发起调用
```

开启 Decision-Only 后，正常决策路径跳过快速响应插件、限流、语义缓存、RAG、后续请求改写、记忆注入和后端模型转发。信号提取和决策评估仍执行，所需的分类器或 Embedding 依赖仍由原配置决定。

两处提前返回分别承担不同职责：

| 位置 | 返回内容 | 作用 |
| --- | --- | --- |
| `runRequestPreRoutingStages` | `requestDecisionState` | 选模后结束预处理，将结果交回请求入口 |
| `handleRequestBody` | Envoy `ImmediateResponse` | 将结果转换成 HTTP JSON 响应，结束正常转发流程 |

## 配置和调用示例

开关为 `global.router.decision_only_mode`，默认值为 `false`。设为 `true` 后启用；设为 `false` 或省略该字段，保留原有处理流程。配置加载和导出均保留该字段。

以下示例声明两个路由类型：命中 `coding` 关键词时返回 `Pro`，未命中时返回默认的 `Fast`。示例不配置 `providers.models[].backend_refs` 或 `endpoint`。

```yaml
version: v0.3
listeners:
  - name: http
    address: 0.0.0.0
    port: 8801
    timeout: 300s
providers:
  defaults:
    default_model: Fast
routing:
  modelCards:
    - name: Fast
    - name: Pro
  signals:
    keywords:
      - name: code_keyword
        operator: OR
        keywords: [coding]
  decisions:
    - name: code
      priority: 1
      rules:
        operator: AND
        conditions:
          - type: keyword
            name: code_keyword
      modelRefs:
        - model: Pro
global:
  router:
    decision_only_mode: true
```

在使用本定制版本构建的服务上，按实际访问地址调用：

```bash
curl http://127.0.0.1:8801/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"auto","messages":[{"role":"user","content":"help with coding"}]}'
```

成功响应为 HTTP 200，核心字段示例如下；实际响应还包含 `debug`，并可包含 `category` 和 `confidence`：

```json
{
  "schema_version": "v1",
  "selection_method": "routing",
  "selected_model": "Pro",
  "decision": "code"
}
```

该响应是决策 JSON，不是模型生成的 Chat Completion。即使请求包含 `stream: true`，正常 Decision-Only 路径也直接返回 JSON，不生成 SSE 文本流。

## 返回字段和选择来源

| 字段 | 含义 |
| --- | --- |
| `schema_version` | 决策 JSON 的结构版本，当前为 `v1` |
| `selection_method` | 类型的选择来源，取值见下表 |
| `selected_model` | 最终路由类型，由客户网关映射到具体模型 |
| `decision` | 命中的决策名称；未命中时省略 |
| `category` | 原有决策流程得到的分类；为空时省略 |
| `confidence` | 命中决策的非负置信度，四舍五入到两位小数；无相应结果时省略，不代表后端模型回答质量 |
| `debug` | 选中原因、决策依据、压缩情况、上下文统计和耗时明细 |

| `selection_method` | 场景 | 结果示例 |
| --- | --- | --- |
| `routing` | 请求 `auto` 或配置的自动选模名，规则选出了类型 | 命中代码规则，返回 `Pro` |
| `fallback` | 自动选模没有得到规则选择的类型，使用 `default_model` | 未命中规则，返回 `Fast` |
| `passthrough` | 请求直接指定类型，保留该值 | 请求 `model: Pro`，返回 `Pro` |

规则选出的类型即使与默认类型相同，来源仍为 `routing`。未命中规则，或命中决策但最终使用默认类型时，来源为 `fallback`。

结果整理函数不检查端点是否配置、地址是否有效或后端是否在线，也不额外校验返回类型是否在端点配置中注册。类型到具体模型的映射由客户网关负责。

响应头包含 `content-type: application/json`、`x-selected-model` 和 `x-vsr-selected-model`；有决策及有效置信度时附带相应响应头。

## 调试信息

正常的 Decision-Only 成功响应附带 `debug`，无需额外配置开关。

| 字段 | 内容 |
| --- | --- |
| `debug.routing` | 请求类型、是否自动选择、默认类型、最终类型、选择来源、选模算法和原因 |
| `debug.decision` | 命中决策的名称、置信度、规则、关键词、优先级、算法和候选类型 |
| `debug.matched_signals` | 各类信号命中的规则名称；`keyword_terms` 为实际命中的关键词 |
| `debug.signal_confidences` | 信号置信度，保留两位小数 |
| `debug.signal_values` | 信号原始数值，保留两位小数 |
| `debug.projection_scores` | 投影评分，保留两位小数 |
| `debug.prompt_compression` | 是否开启、是否执行压缩、状态、Token 数量、压缩比例及使用原文的信号 |
| `debug.context` | 上下文 Token 数、文本字节数、各角色消息数、工具数量和最后一条消息的角色 |
| `debug.timings` | 请求处理及各阶段耗时，单位为毫秒，最多保留三位小数 |

`debug.routing.response_selection_method` 与顶层 `selection_method` 一致；`model_selection_method` 表示内部候选选择方式，例如 `single`、`static` 或 `default`。`reason` 的取值如下：

| `reason` | 含义 |
| --- | --- |
| `decision_routing_selected_model` | 规则选出了类型 |
| `auto_model_without_selected_model_used_default` | 自动选择未得到规则选出的类型，使用默认类型 |
| `explicit_model_passthrough` | 保留请求直接指定的类型 |

`debug.prompt_compression.status` 区分 `disabled`（关闭）、`max_tokens_unset`（开启但未设置有效 Token 预算）、`below_min_length`（未达到长度门槛）、`within_token_budget`（无需压缩）、`applied`（已执行压缩）和 `not_evaluated`（未进入压缩评估）。`enabled` 表示配置状态，`applied` 表示本次是否实际执行压缩。

| 耗时字段 | 测量范围 |
| --- | --- |
| `total_ms` | 从请求体处理开始，到构建决策响应时 |
| `request_parse_ms` | 请求内容快速提取 |
| `pre_routing_ms` | 预路由处理，包含信号输入准备、信号评估、决策评估和候选选择 |
| `signal_input_ms`、`prompt_compression_ms` | 信号输入准备及其中的提示压缩 |
| `signal_evaluation_ms`、`signals` | 信号评估总耗时，以及各类信号的耗时明细 |
| `decision_evaluation_ms` | 决策规则评估 |
| `model_selection_ms` | 命中决策后的候选选择 |

这些耗时存在包含关系，信号也可能并行执行，因此不能直接相加得到总耗时。零值耗时字段会省略，耗时低于计时精度时也可能省略。未命中信号的列表返回空数组；没有相应结果时，评分字段省略。

消息和工具统计来自已解析的请求，即使没有配置决策规则或消息内容为空，也会记录实际数量。可选的信号评分、耗时、压缩比例和候选权重出现 `NaN` 或无穷大时，对应数值会省略，避免这些附加信息影响有效决策结果的返回。

## 请求记录与查询

Decision-Only 成功构建响应后，通过原有 Router Replay 链路保存记录，供 Insights 页面或查询接口使用。记录包括请求 ID、请求中的类型、最终类型、命中决策、信号、HTTP 200 状态，以及按采集配置保存的请求体和决策响应 JSON；响应 JSON 中保留 `debug`。

沿用 `global.services.router_replay` 的启用及存储配置，以及决策中的 `router_replay` 插件覆盖配置，无需增加专用开关。例如，启用 PostgreSQL 存储时，记录写入既有 Replay 表。未命中规则而使用默认类型、或请求直接指定类型时，也按全局 Replay 配置记录；未启用或没有可用 recorder 时不记录。

插件配置中的 `capture_request_body`、`capture_response_body` 分别控制两种正文的采集，`max_body_bytes` 控制单个正文的长度上限。超限记录会标记截断，实际返回给网关的 JSON 保持完整。上下文 Token 统计保存在决策响应的 `debug.context.context_tokens` 中。

创建记录并取得 ID 后，响应头附带 `x-vsr-replay-id`，可用于定位对应记录。SR 未调用后端模型，因此模型调用的输入、缓存、输出和总 Token 用量，以及实际费用、基准费用和节省费用均记为 0。即使请求包含 `stream: true`，记录也标记为非流式响应。

存储创建或更新返回错误时，仍返回正常决策结果；创建失败时不附带记录 ID。记录操作复用既有同步或异步写入设置，存储变慢或队列阻塞仍可能增加返回耗时。`debug.timings.total_ms` 在组装决策响应时计算，不包含后续 Replay 写入耗时。请求校验、选模或响应序列化失败时，不创建成功记录。

## 错误和主动跳过行为

以下情况仍会返回错误：

- 请求缺少 `model`：HTTP 400。
- 自动选模没有结果，也没有配置默认类型：HTTP 400。
- JSON 无法解析，或 Chat Completion 请求缺少 `messages`、`messages` 不是数组：HTTP 400。
- 原有预路由鉴权失败：HTTP 403。

Decision-Only 不改变两条原有的主动跳过路径：

| 路径 | 触发条件 | 行为 |
| --- | --- | --- |
| SkipProcessing | 配置开启 `global.router.skip_processing.enabled`，且请求携带 `x-vsr-skip-processing: true` | 跳过 SR 处理，继续原有转发路径 |
| 内部 Looper | 请求携带 `x-vsr-looper-request: true`，值为小写 `true` | 进入原有内部处理路径，不执行后面的 Decision-Only 分支 |

正常的外部决策请求不携带这些跳过标记。主动退出决策路径、恢复后端调用时，仍需满足原有转发路径的后端配置和鉴权要求。

## 非测试代码改动

以下行数以本次实现对比官方 `v0.3.0` 统计，包含空行和注释，排除测试及配置改动。

| 文件 | 新增行数 | 删除行数 | 改动内容 |
| --- | ---: | ---: | --- |
| `processor_req_body_decision_only.go` | 155 | 0 | 模式判断、最终结果整理、JSON 响应构建及 Replay 记录接入 |
| `processor_req_body_decision_debug.go` | 304 | 0 | 记录消息统计、计算耗时、处理异常调试数值并组装调试信息 |
| `processor_req_body_decision_debug_types.go` | 136 | 0 | 定义调试 JSON 字段和耗时数据结构 |
| `processor_req_body.go` | 7 | 0 | 请求提取和预路由计时、直接返回决策 JSON |
| `processor_req_body_prepare.go` | 10 | 5 | 在后续插件处理前返回选模结果 |
| `req_filter_classification.go` | 8 | 0 | 记录信号输入、压缩信息及提前返回场景的消息统计 |
| `req_filter_classification_runtime.go` | 10 | 0 | 记录信号、决策和候选选择耗时及命中依据 |
| `req_filter_classification_signal.go` | 66 | 6 | 记录压缩状态、耗时和实际命中关键词 |
| `request_context.go` | 8 | 0 | 保存本次请求的调试信息 |
| `recorder.go` | 1 | 1 | 按请求体采集开关决定是否保存原始请求体 |
| 合计 | 705 | 12 | 净增加 693 行 |

以上文件均位于 `src/semantic-router/pkg/extproc/`。

配置配套改动位于 `src/semantic-router/pkg/config/` 的 `config.go`、`canonical_global.go`、`canonical_defaults.go` 和 `canonical_export.go`，分别负责运行时字段、加载、默认值及导出；`config/config.yaml` 增加关闭状态的参考配置。

## 测试结果和复现

新增 26 组测试，分布于以下文件：

| 测试文件 | 覆盖范围 |
| --- | --- |
| `pkg/config/decision_only_test.go` | 默认关闭、显式开启或关闭、配置加载与导出、无端点配置 |
| `pkg/extproc/processor_req_body_decision_only_test.go` | 模式判断、规则选择、默认兜底、直接指定、无端点返回、JSON 和原有响应头、错误响应 |
| `pkg/extproc/processor_req_body_decision_only_flow_test.go` | 实际关键词规则与完整请求体处理、关闭模式、跳过后续插件、非法请求、SkipProcessing |
| `pkg/extproc/processor_req_body_decision_only_grpc_test.go` | gRPC 请求头和请求体的完整交互、规则选择与兜底、无端点返回、`stream: true`、Looper 路径 |
| `pkg/extproc/processor_req_body_decision_debug_test.go` | 调试 JSON、命中依据、候选信息、耗时、精度、空值、无规则及空内容的消息统计、异常数值、压缩状态、gRPC 调试信息及正常转发回归 |
| `pkg/extproc/processor_req_body_decision_replay_test.go` | 规则、默认兜底和显式类型的记录，开关覆盖、正文采集与截断、存储报错、失败响应不记录、零用量及费用、gRPC 请求与记录 ID |

测试文件路径相对于 `src/semantic-router/`。gRPC 测试使用内存连接和真实请求处理器，不依赖运行中的后端模型。

验证结果：Go 路由器构建、仓库默认 Go 测试及 `agent-ci-gate` 通过；Decision-Only 单元与 gRPC 集成测试通过，竞态检查通过。Replay 测试使用内存存储及模拟报错存储；实际 PostgreSQL、Docker 镜像构建及部署验证尚未执行。

准备 Go 1.24.1、C 编译器和本仓库所需的 Candle、ML、NLP 原生绑定库后，可在仓库根目录执行：

```bash
export LD_LIBRARY_PATH="$PWD/candle-binding/target/release:$PWD/ml-binding/target/release:$PWD/nlp-binding/target/release${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
export CGO_LDFLAGS="-L$PWD/candle-binding/target/release -L$PWD/ml-binding/target/release -L$PWD/nlp-binding/target/release"
export CGO_ENABLED=1
cd src/semantic-router
go test ./pkg/config -count=1
go test -race ./pkg/config ./pkg/extproc -run '^TestDecisionOnly' -count=1
```
