# Decision-Only 功能说明

本次改动在官方 `v0.3.0` 上新增可选的 Decision-Only 模式。开启后，SR 执行原有规则判断和类型选择，直接返回决策 JSON；客户网关根据返回的类型决定调用哪个具体模型。

`Fast`、`Pro`、`Ultra` 在这里表示网关约定的路由类型，不要求它们对应 SR 可以直接调用的模型服务。为兼容 SR 的字段命名，结果仍使用 `selected_model` 字段承载类型。

## 功能范围

Decision-Only 模式提供配置开关、请求流程接入、决策结果整理和 JSON 响应能力。

## 请求处理流程

```text
客户网关发送请求
  → SR 提取请求内容并执行原有规则判断
  → 得到规则选出的类型，或使用默认类型
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

成功响应为 HTTP 200，核心字段示例如下；实际响应可额外包含 `category` 和 `confidence`：

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

| `selection_method` | 场景 | 结果示例 |
| --- | --- | --- |
| `routing` | 请求 `auto` 或配置的自动选模名，规则选出了类型 | 命中代码规则，返回 `Pro` |
| `fallback` | 自动选模没有得到规则选择的类型，使用 `default_model` | 未命中规则，返回 `Fast` |
| `passthrough` | 请求直接指定类型，保留该值 | 请求 `model: Pro`，返回 `Pro` |

规则选出的类型即使与默认类型相同，来源仍为 `routing`。未命中规则，或命中决策但最终使用默认类型时，来源为 `fallback`。

结果整理函数不检查端点是否配置、地址是否有效或后端是否在线，也不额外校验返回类型是否在端点配置中注册。类型到具体模型的映射由客户网关负责。

响应头包含 `content-type: application/json`、`x-selected-model` 和 `x-vsr-selected-model`；有决策及有效置信度时附带相应响应头。

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
| `processor_req_body_decision_only.go` | 108 | 0 | 新增模式判断、最终结果整理、JSON 响应构建 |
| `processor_req_body.go` | 3 | 0 | 决策完成后直接返回 JSON |
| `processor_req_body_prepare.go` | 10 | 5 | 在后续插件处理前返回选模结果 |
| 合计 | 121 | 5 | 净增加 116 行 |

以上三个文件均位于 `src/semantic-router/pkg/extproc/`。

配置配套改动位于 `src/semantic-router/pkg/config/` 的 `config.go`、`canonical_global.go`、`canonical_defaults.go` 和 `canonical_export.go`，分别负责运行时字段、加载、默认值及导出；`config/config.yaml` 增加关闭状态的参考配置。

## 测试结果和复现

新增 13 组测试，分布于以下文件：

| 测试文件 | 覆盖范围 |
| --- | --- |
| `pkg/config/decision_only_test.go` | 默认关闭、显式开启或关闭、配置加载与导出、无端点配置 |
| `pkg/extproc/processor_req_body_decision_only_test.go` | 模式判断、规则选择、默认兜底、直接指定、无端点返回、JSON 和原有响应头、错误响应 |
| `pkg/extproc/processor_req_body_decision_only_flow_test.go` | 实际关键词规则与完整请求体处理、关闭模式、跳过后续插件、非法请求、SkipProcessing |
| `pkg/extproc/processor_req_body_decision_only_grpc_test.go` | gRPC 请求头和请求体的完整交互、规则选择与兜底、无端点返回、`stream: true`、Looper 路径 |

测试文件路径相对于 `src/semantic-router/`。gRPC 测试使用内存连接和真实请求处理器，不依赖运行中的后端模型。

验证结果：配置包全量测试通过；Decision-Only 单元与 gRPC 集成测试通过，竞态检查通过；仓库 manifest 校验、Go 结构检查和参考配置检查通过。Docker 镜像构建及实际部署验证尚未执行。

准备 Go 1.24.1、C 编译器和本仓库所需的 Candle、ML、NLP 原生绑定库后，可在仓库根目录执行：

```bash
export LD_LIBRARY_PATH="$PWD/candle-binding/target/release:$PWD/ml-binding/target/release:$PWD/nlp-binding/target/release${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
export CGO_LDFLAGS="-L$PWD/candle-binding/target/release -L$PWD/ml-binding/target/release -L$PWD/nlp-binding/target/release"
export CGO_ENABLED=1
cd src/semantic-router
go test ./pkg/config -count=1
go test -race ./pkg/config ./pkg/extproc -run '^TestDecisionOnly' -count=1
```
