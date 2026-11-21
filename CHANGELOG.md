# Changelog

记录本定制版本相对官方 `v0.3.0` 的功能变化。

## Unreleased

### Added

- 新增可选的 Decision-Only 模式，默认关闭。开启后返回 `Pro` 等路由类型，由客户网关选择并调用具体模型，无需配置后端端点。
  配置、接口和测试说明见 [Decision-Only 功能说明](docs/decision-only-mode.md)。
- 决策响应增加 `debug`，提供选中原因、命中规则与信号、候选类型、提示压缩状态、上下文统计和分阶段耗时。
