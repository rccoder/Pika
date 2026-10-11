# Pika

**把喜欢的音乐带回家。**

`Pika` 是独立的命令行下载器。当前支持将哔哩哔哩或 YouTube Music 链接中的**音轨**保存为本地可播放文件；所需的平台解析和下载实现均包含在本仓库的 `internal/music/` 中，不依赖相邻项目或本地 `replace`。

视频链接下载的是音频，不会下载画面。

## 前置条件

- `ffmpeg`（必需）：Bilibili 与 YouTube Music 返回的常是 DASH 音频分段，CLI 会用它重新封装为常见播放器可打开的文件。

### 安装 ffmpeg

macOS：使用 [Homebrew](https://brew.sh/) 安装；尚未安装 Homebrew 时，请先按其官网说明安装。

```bash
brew install ffmpeg
```

Arch Linux：

```bash
sudo pacman -Syu ffmpeg
```

Debian：

```bash
sudo apt update
sudo apt install ffmpeg
```

这些安装方式同时提供 `ffmpeg` 和 `ffprobe`。安装完成后，检查它们是否可从终端调用：

```bash
ffmpeg -version
ffprobe -version
```

Pika 使用 ffmpeg 提取音轨并重新封装，直接复制音频数据，不重新编码；ffprobe 可用于检查下载文件的编码、容器和时长。

## 安装 Pika

macOS 和 Linux 可以直接安装预编译二进制，无需 Go：

```bash
curl -fsSL https://raw.githubusercontent.com/rccoder/Pika/main/install.sh | sh
```

脚本自动识别系统和 CPU 架构，下载最新 Release，校验 SHA256，并安装到 `~/.local/bin/pika`。支持 Intel / Apple Silicon Mac，以及 amd64 / arm64 Linux；再次运行即可升级。

若提示 `pika: command not found`，将下面一行加到 `~/.zshrc` 或 `~/.bashrc`，然后重新打开终端：

```bash
export PATH="$HOME/.local/bin:$PATH"
```

```bash
pika --help
pika download '周传雄 黄昏' -o ./music
```

也可以从 [Releases](https://github.com/rccoder/Pika/releases) 手动下载对应压缩包。指定版本或安装目录时：

```bash
curl -fsSL https://raw.githubusercontent.com/rccoder/Pika/main/install.sh | PIKA_VERSION=v0.1.0 PIKA_INSTALL_DIR="$HOME/.local/bin" sh
```

## 最简单的下载方式

```bash
pika download \
  'https://www.bilibili.com/video/BV1Rc1gBaERq' \
  --output ./downloads \
  --quality high
```

完成后会显示最终保存路径，例如：

```text
Saved: downloads/UP主 - 视频标题.m4a
```

输出目录不存在时会自动创建；同名文件不会覆盖，而会自动追加 ` (2)`、` (3)` 等后缀。

`download` 后的 URL 可以放在选项之前或之后，两种写法都有效：

```bash
# 推荐：URL 在前，阅读更自然
pika download 'https://www.bilibili.com/video/BV1Rc1gBaERq' -o ./music

# 也支持先写选项
pika download -o ./music 'BV1Rc1gBaERq'
```

## 总下载命令：自动识别与关键词搜索

`download` 是总入口。它会自动识别 Bilibili、YouTube 或 YouTube Music 的链接/ID，并使用相应插件：

```bash
pika download 'https://youtu.be/dQw4w9WgXcQ' -o ./music
pika download 'https://www.bilibili.com/video/BV1Rc1gBaERq' -o ./music
```

也可以传入关键词。CLI 会按 `platform_priority` 搜索，检查候选曲目的实际下载流；有可用 `high` 流时优先下载它，所有候选都只能标准音质时则使用最高优先级平台的可用流。

```bash
pika download 'Never Gonna Give You Up' -o ./music
```

链接的来源是确定的，不受平台优先级影响。关键词搜索不接受 `--cookie` 或 `--cookie-file`；请用环境变量或配置文件为各平台提供 Cookie。

在 `[download]` 中配置关键词搜索顺序：

```ini
[download]
platform_priority = bilibili,youtubemusic
```

支持 `bilibili`、`youtubemusic`（也可写 `youtube` 或 `ytm`）；未配置时默认为 `bilibili,youtubemusic`。

## 支持的输入

### Bilibili

- 视频链接：`https://www.bilibili.com/video/BV...`
- AV 号链接或 ID：`av123456` / `https://www.bilibili.com/video/av123456`
- 音频页：`https://www.bilibili.com/audio/au123456`
- 短链：`https://b23.tv/xxxxxx`
- 多 P 视频：在链接中加 `?p=2`，例如 `https://www.bilibili.com/video/BV.../?p=2`
- 纯 ID：`BV...`、`av...`、`au...`

视频 URL 会下载选中分 P 的音轨。短链会先解析到最终的 Bilibili 页面。

### YouTube Music

使用 `youtube download` 子命令：

```bash
pika youtube download \
  'https://music.youtube.com/watch?v=dQw4w9WgXcQ' \
  --output ./music \
  --quality high
```

也支持普通 YouTube 链接、短链、Shorts 和 11 位视频 ID：

```bash
pika youtube download 'https://youtu.be/dQw4w9WgXcQ' -o ./music
pika youtube download 'https://www.youtube.com/shorts/dQw4w9WgXcQ' -o ./music
pika youtube download 'dQw4w9WgXcQ' -o ./music
```

YouTube Music 仅提供标准/高音质的 AAC、Opus 等有损音频，不支持无损或 Hi-Res；因此该命令只接受 `standard` 与 `high`。

## 音质

使用 `--quality` 选择期望音质：

| 值 | 含义 |
| --- | --- |
| `standard` | 标准音质 |
| `high` | 高音质，默认值 |
| `lossless` | 无损优先 |
| `hires` | Hi-Res 优先 |

例子：

```bash
pika download 'BV1Rc1gBaERq' -o ./music --quality hires
```

实际能获取到的音质取决于稿件与账号权限。无法取得所选流时，命令会报错，不会悄悄保存不完整文件。

## Cookie 配置

公开稿件通常不需要 Cookie。若需要访问受限内容、较高音质或遇到访问限制，请提供对应网站已登录会话的 Cookie。

Cookie 的优先级为：`--cookie` > `--cookie-file` > 平台环境变量 > 配置文件。

### 推荐：环境变量

环境变量可避免在下载命令中传入 Cookie；直接执行含 Cookie 的 `export` 仍可能进入 shell 历史，建议从受保护的配置文件读取：

```bash
export PIKA_BILIBILI_COOKIE='SESSDATA=...; bili_jct=...'
pika download 'BV1Rc1gBaERq' -o ./music --quality hires
```

YouTube Music 则使用：

```bash
export PIKA_YOUTUBEMUSIC_COOKIE='SID=...; SAPISID=...'
pika youtube download 'dQw4w9WgXcQ' -o ./music
```

### Cookie 文件

将 Cookie 单独放入权限受限的文件：

```bash
chmod 600 ~/.config/pika/bilibili.cookie
pika download 'BV1Rc1gBaERq' \
  --cookie-file ~/.config/pika/bilibili.cookie \
  -o ./music
```

### 配置文件

复制示例配置：

```bash
curl -fsSL https://raw.githubusercontent.com/rccoder/Pika/main/config.example.ini -o config.ini
chmod 600 config.ini
```

然后编辑 `config.ini`：

```ini
[plugins.bilibili]
cookie = `SESSDATA=...; bili_jct=...`

[plugins.youtubemusic]
# 可选：从已登录 music.youtube.com 会话导出的 Cookie
cookie = `SID=...; SAPISID=...`

[download]
platform_priority = bilibili,youtubemusic
timeout_seconds = 90
proxy = http://127.0.0.1:7890
multipart = true
multipart_concurrency = 4
```

使用它下载：

```bash
pika download 'BV1Rc1gBaERq' --config ./config.ini -o ./music
```

也兼容较短的 `[bilibili]` 配置节。

不要提交 Cookie 文件，也不要把它贴进 Issue、日志或聊天记录。

## 环境变量

| 变量 | 用途 |
| --- | --- |
| `PIKA_CONFIG` | 默认 INI 配置文件路径，可被 `--config` 替代 |
| `PIKA_BILIBILI_COOKIE` | Bilibili Cookie |
| `PIKA_YOUTUBEMUSIC_COOKIE` | YouTube Music Cookie |
| `PIKA_DOWNLOAD_TIMEOUT_SECONDS` | 下载超时秒数，例如 `120` |
| `PIKA_DOWNLOAD_PROXY` | 媒体下载代理，例如 `http://127.0.0.1:7890` |

命令行选项会覆盖对应的环境变量；环境变量会覆盖配置文件。

## 所有选项

```text
pika download <Bilibili/YouTube URL、ID 或关键词> [options]
pika youtube download <YouTube / YouTube Music URL 或视频 ID> [options]

  --output, -o DIR       输出目录，默认当前目录
  --quality QUALITY      standard、high、lossless 或 hires，默认 high
  --config FILE          可选 INI 配置文件
  --cookie VALUE         当前平台 Cookie（不建议直接传，容易进入历史记录）
  --cookie-file FILE     读取当前平台 Cookie 的文件
  --timeout SECONDS      下载超时秒数，默认 90
  --proxy URL            HTTP/SOCKS 下载代理
```

## 常见问题

### 提示找不到 `ffmpeg`

安装 ffmpeg 并确认它在 `PATH` 中。CLI 需要它生成播放器兼容的最终文件，不能简单地把 DASH 分段改扩展名。

### 提示无法取得下载地址或内容不可用

先确认链接在浏览器中可以播放；若是会员、地区限制或登录可见内容，配置包含 `SESSDATA` 的有效 Cookie 后重试。Cookie 过期时请从已登录浏览器重新导出。

### 下载很慢或连接失败

可增加超时，或使用代理：

```bash
pika download 'BV1Rc1gBaERq' \
  --timeout 180 \
  --proxy http://127.0.0.1:7890 \
  -o ./music
```

### 如何确认文件可播放

下载结束后，可用 ffprobe 检查容器和音频编码：

```bash
ffprobe -v error \
  -show_entries format=format_name,duration \
  -show_entries stream=codec_name \
  -of default=noprint_wrappers=1 \
  './music/下载后的文件.m4a'
```

正常的 AAC 输出通常会显示 `codec_name=aac` 和 `format_name=mov,mp4,m4a,...`。

## 开发者：源码构建与发布

需要 Go `1.26+`。克隆后即可独立构建：

```bash
git clone https://github.com/rccoder/Pika.git
cd Pika
go build -o bin/pika .
./bin/pika --help
```

开发时也可直接执行 `go run . download '周传雄 黄昏' -o ./music`。检查代码：

```bash
go test ./...
go vet ./...
go build -o bin/pika .
```

维护者推送版本标签后，Release 工作流自动测试并生成 macOS/Linux 的 amd64、arm64 压缩包和 `SHA256SUMS`，发布到 GitHub Releases：

```bash
git tag v0.1.0
git push origin v0.1.0
```

后续版本使用新的标签，例如 `v0.1.1`；安装脚本默认跟随最新 Release。

## 致谢

感谢 [MusicBot-Go](https://github.com/liuran001/MusicBot-Go) 原项目及其作者、贡献者。本项目的 Bilibili、YouTube Music 平台解析、搜索与音频流获取，以及分片下载、重试和完整性校验等实现，源自 MusicBot-Go，并在此基础上整理为独立的命令行工具。原项目的工作为 Pika 提供了重要基础。

## 许可证

本项目包含并修改了 GPL-3.0 许可的 MusicBot-Go 相关实现，因此以 [GPL-3.0](LICENSE) 发布。
