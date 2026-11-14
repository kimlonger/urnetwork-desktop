# URnetwork 桌面代理

面向 Windows 10/11 x64 的便携代理客户端，使用 URnetwork SDK 连接出口。支持国家选择、上传和下载速率、网站和地址网段分流。只监听本机，运行时禁用流量共享和贡献中继。

## 使用

从[发布页](https://github.com/kimlonger/urnetwork-desktop/releases/latest)下载 Windows x64 版 EXE，直接双击。已有访客身份和分流设置会继续使用。具体用法见[使用说明](README-Start.md)。

## 构建

需要 Go 1.26 或更新版本，以及包含 `windres` 的 MinGW-w64 工具链。将两者加入 PATH 后，在 Windows x64 上运行：

```powershell
.\build.ps1
```

脚本校验固定版本的 SDK，生成图标资源，运行自动测试，并将单文件 EXE 输出到 `dist`。

## 开源许可证

本项目的原创代码、文档和应用图标采用 [MIT 许可证](LICENSE)，也适用于 v0.6.2 中发布的原创内容。随附的 SDK、依赖和规则数据各自沿用原有许可证。

## 第三方组件

- 本仓库附带 URnetwork 官方 Windows SDK `2026.10.27-1083160450` 的原版 x64 DLL。SDK 及其依赖的许可见 [SDK-Licenses.json](SDK-Licenses.json)，对应源码版本为 [SDK](https://github.com/urnetwork/sdk/tree/v2026.10.27-1083160450) 和 [Connect](https://github.com/urnetwork/connect/tree/v2026.10.27-1083160450)。官方 SDK 下载见[上游发布页](https://github.com/urnetwork/build/releases/tag/v2026.10.27-1083160450)。
- 国内域名和网段从 [MetaCubeX 规则库](https://github.com/MetaCubeX/meta-rules-dat) 提取为可编辑的 `cn-domain.list` 和 `cn-ip.list`，随附[规则数据许可](CN-rule-lists-LICENSE.txt)。
