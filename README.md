# serial-tool

一个小工具，通过虚拟串口和虚拟相机的方式，把服务器上的真机控制信号与画面经 PC/Mac 转接。

```txt
┌────────────────────┐             ┌─────────┐          ┌─────────┐
│ Ubuntu             │             │         │          │         │
│ (RobotSDK / GPU)   │ <══socat══> │   Mac   │───USB───►│  Robot  │
│ /dev/ttyACM0       │             │         │          │         │
│ /dev/video10       │ <═ffmpeg══> │         │◄──USB────│ Camera  │
└────────────────────┘             └─────────┘          └─────────┘
```

机械臂和相机都是设备：点「＋」添加设备，可添加多个，每个独立配置、独立启停并同时运行。相机分两种型号：普通 RGB 和 Orbbec（彩色 + 深度）。设备列表会保存到本机配置目录（密码不落盘）。

## Install

[Releases](https://github.com/homearchbishop/serial-tool/releases)

本机与远程 Ubuntu 都需要 `socat`（机械臂）和 `ffmpeg`（相机）。

```bash
# Mac
brew install socat ffmpeg
# Ubuntu
sudo apt install socat ffmpeg
```

相机推流还需要在 Ubuntu 上安装 v4l2loopback，模块由应用自动加载，最后一个相机停止时会自动卸载：

```bash
sudo apt install v4l2loopback-dkms v4l2loopback-utils
```

推流在 Mac 上编码 H.264，经 SSH `-R` 隧道送到 Ubuntu，由远程 ffmpeg 写入 `/dev/video10`（多相机自动分配 `/dev/video10..19`）。首次加载模块或写入虚拟设备需要 sudo，应用会弹窗询问密码；设备权限会自动设为 `666`，普通账号下 `cv2.VideoCapture(10)` 可直接读。应用会在启动/停止时清理上次异常退出留下的本机端口监听、远程 ffmpeg/socat 进程和虚拟串口链接；崩溃遗留的 v4l2loopback 模块会在下次启动时尝试卸载。

## Orbbec 相机

添加相机时选「Orbbec DaBai（彩色+深度）」：彩色仍走上面的 ffmpeg/v4l2loopback 链路，深度由 `orbbec-depth` 助手读取。助手按彩色内参和 D2C 外参把深度重投影到彩色分辨率（默认 `aligned=color`），应用用一条 SSH 连接同时转发彩色和深度端口。

服务器上不需要额外进程，机器人程序直接连接 `127.0.0.1:47200`（端口可改）。帧格式、内参和 Python 示例见 [docs/depth-protocol.md](docs/depth-protocol.md)。

构建时会自动下载 Orbbec SDK v1.10.16 到 `.sdk/`（不提交）并编译助手，本机需要 CMake 和 C++ 编译器：

```bash
bash scripts/fetch-orbbec-sdk.sh --all     # 可选：预下载全部平台 SDK
bash scripts/build-depth-helper.sh         # 单独构建当前平台助手
```

## Build from source

```bash
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0
wails3 dev
```

`wails3 task <platform>:build` 会自动构建深度助手并把 SDK 运行时文件放进 `bin/`（macOS 打包时会复制进 `.app`）。
