# URnetwork 桌面代理 0.6.2

关闭旧版代理，双击 `URnetwork-Desktop-v0.6.2-Windows-x64.exe`。需要 Windows 10 或 11 x64。点击「启动代理」。首次会显示 URnetwork 的服务条款确认框，确认后自动为本机开通访客身份。勾选「使用系统代理」后，程序会在出口检测成功时接入系统代理。也可以点击「打开独立浏览器」。

每台电脑的访客身份相互独立，使用 URnetwork 的访客额度。设备凭证由 Windows 加密保存在 `%LOCALAPPDATA%\URnetwork-OneClick\session.dpapi`。新版本会继续使用该位置保存的访客身份。首次访问外网可能需要等待节点连接，启动窗口请保持打开。

「连接概览」显示通过本地代理的实时下载、上传速度和最近 60 秒的流量走势。程序显示所选国家，以及经 URnetwork 请求 Cloudflare 诊断接口得到的出口地址和地区。不同目标可能使用不同出口。「刷新出口」可以立即重查，检测失败时可看到本次连接上一次验证成功的出口。点「重连当前地区」可以重建当前选择的出口连接。

「运行日志」把最近记录放在顶部。完整日志文件保存在 `%LOCALAPPDATA%\URnetwork-OneClick\logs`，按写入顺序记录启动、授权、SDK 输出和代理连接错误。程序默认省略请求地址，SDK 诊断可能包含 IP 和连接标识，分享前可先查看。

「分流规则」可添加网站或 IP 网段。网站同时匹配其子域。IP 规则匹配以 IP 为目标的连接。这些直连条目优先匹配，保存在 `%LOCALAPPDATA%\URnetwork-OneClick\bypass.json`。「高级规则」支持增删改、排序和测试，配置保存在同目录的 `rules.json`。「内置规则」可搜索、逐条修改、停用或恢复默认，修改单独保存到 `builtin-rules.json`。停用一条规则后，程序继续尝试匹配后面的规则。

「代理端口」显示当前设置。默认端口为 `18790`（自动分流）、`18791`（直接连接）、`18792`（走代理）。地址为 `127.0.0.1`，系统代理使用自动分流端口。停止代理后可修改端口，下次启动时生效。

启动时根据电脑的物理内存，采用官方 Windows 客户端对应的 SDK 设备和进程预算，并记录到运行日志。创建设备时禁用流量共享、设备 RPC 和贡献中继，确认只使用代理后再启动。停止或退出时会恢复程序接入前的 Windows 系统代理。若另一个程序已改动代理设置，会保留新设置。

本包为独立制作的试用版。SDK 来自 URnetwork `2026.10.27-1083160450`。相关源码见 [URnetwork 构建版本](https://github.com/urnetwork/build/tree/v2026.10.27-1083160450) 和 [SDK 仓库](https://github.com/urnetwork/sdk)，SDK 与底层连接库采用 MPL 2.0。软件和数据声明位于 `%LOCALAPPDATA%\URnetwork-OneClick\runtime\0.6.2`。国内规则库来自 [MetaCubeX](https://github.com/MetaCubeX/meta-rules-dat)。
