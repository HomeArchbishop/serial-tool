# serial-tool

一个小工具，通过虚拟串口的方式，将服务器上的真机控制信号经过 PC/Mac 发往机器人。

```txt
┌────────────────────┐             ┌─────────┐          ┌─────────┐
│ Ubuntu             │             │         │          │         │
│ (RobotSDK / GPU)   │ <══socat══> │   Mac   │───USB───►│  Robot  │
│ /dev/ttyACM0       │             │         │          │         │
└────────────────────┘             └─────────┘          └─────────┘
```

## Install

[Releases](https://github.com/homearchbishop/serial-tool/releases)

请先在 PC/Mac 和远程的 Ubuntu 上安装 socat。

```bash
# Mac
brew install socat
# Ubuntu
sudo apt install socat
```

## Build from source

```bash
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0
wails3 dev
```
