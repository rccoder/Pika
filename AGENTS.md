# Pika Agent Guide

## 项目定位

Pika 是独立的命令行下载器。当前提供 Bilibili 与 YouTube Music 音频下载：接受视频、音频或短链，借助 ffmpeg 输出本地播放器兼容的文件。

`internal/music/` 包含 CLI 所需的平台解析、下载器和通用类型；项目不得依赖相邻仓库、本地 `replace` 或 `github.com/liuran001/MusicBot-Go` import。迁入的代码源自 GPL-3.0 项目，保留 `LICENSE` 并以 GPL-3.0 发布。

## 文件职责

- `main.go`：命令解析、INI/环境变量配置、自动平台识别、关键词按平台优先级搜索、下载、输出命名与 ffmpeg 转封装。
- `internal/music/`：CLI 自身拥有的 Bilibili、YouTube Music、下载器、平台通用类型和辅助代码。
- `main_test.go`：不访问网络的参数解析、配置读取和文件名安全测试。
- `config.example.ini`：可提交的配置样例，绝不放真实 Cookie。
- `README.md`：用户安装、配置、下载和排错说明；新增选项必须同步更新。

## 开发约定

- 保持 CLI 可在未配置 Telegram、数据库或 Bot Token 的机器上运行。
- Bilibili 与 YouTube 视频 URL 的行为是下载音频，不是下载完整视频；文档和错误提示必须明确这一点。
- 下载后必须通过 ffmpeg 转封装，不能仅按推测的扩展名重命名 DASH 分段。
- 所有网络调用必须保留 `context.Context`，并响应 Ctrl-C / SIGTERM。
- 输出文件不能覆盖已有文件；保留安全文件名清理和冲突后缀机制。
- 总 `download` 命令对显式 URL/ID 必须按来源平台路由；仅关键词搜索使用 `[download] platform_priority`。搜索时先检查实际 `DownloadInfo.Quality`，在可用候选中优先高音质。
- 新平台应放入 `internal/music/<platform>/`，只迁入 CLI 实际使用的代码及其最小依赖；不得重新引入 MusicBot-Go 模块依赖。

## 配置与凭据

- 配置来源优先级：命令行参数 > 环境变量 > INI 文件。
- 支持的敏感变量是 `PIKA_BILIBILI_COOKIE` 和 `PIKA_YOUTUBEMUSIC_COOKIE`；Cookie 不得出现在日志、测试断言、README 示例或提交中。
- 优先推荐 Cookie 文件或环境变量，避免 `--cookie` 进入 shell 历史和进程列表。
- `config.ini`、Cookie 文件、下载目录与二进制生成物都不提交。

## 验证

修改 Go 文件后，在项目根目录执行：

```bash
gofmt -w main.go main_test.go
go test ./...
go vet ./...
go build -o bin/pika .
```

涉及下载或转封装行为的变更，还应使用对应平台的公开链接做一次端到端验证，并用 `ffprobe` 确认最终文件包含可播放的音频流。测试媒体应写到 `/tmp` 并在验证后删除。

## 提交前检查

1. 确认 `README.md` 的选项、环境变量和配置优先级与代码一致。
2. 检查没有加入 Cookie、配置文件或下载出的媒体。
3. 运行上述验证命令；无法运行的检查需在交付时说明。
