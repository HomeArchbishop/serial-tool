# 深度流 TCP 协议

「深度信息」设备在本机通过 Orbbec SDK 读取深度帧，深度助手 `orbbec-depth` 监听
`127.0.0.1:<端口>`，应用再通过 SSH `-R` 把这个端口映射到服务器的
`127.0.0.1:<同一端口>`。服务器上不需要运行任何进程，机器人程序直接连该端口。

- 默认端口：`47200`（每个深度设备自动分配，可改）
- 默认分辨率：`640x400@30`，Y16，单位由 `valueScale` 换算成毫米
- 默认 `aligned=color`：深度已按彩色内参 + D2C 外参重投影到彩色像素网格（分辨率与彩色流一致，一般 `640x480`），`intrinsics` 就是彩色内参
- 应用停止后端口和隧道都会关闭，需要重连

## 数据包

所有整数小端序。

```
PacketHeader (12 bytes)
  char     magic[4]      = "OBDP"
  uint8    version       = 1
  uint8    type          = 1 (meta) | 2 (depth)
  uint16   flags         = 0
  uint32   payload_size
```

连接建立后先收到一个 `type=1` 的 meta 包（JSON），之后全是 `type=2` 深度帧。

### meta 包

```json
{
  "type": "meta",
  "width": 640,
  "height": 400,
  "format": 8,
  "formatName": "Y16",
  "valueScale": 1.0,
  "serial": "CC1N16200XL",
  "name": "DaBai DC1",
  "intrinsics": {"fx": 476.06, "fy": 476.06, "cx": 322.47, "cy": 201.35},
  "distortion": {"model": 0, "k1": 0, "k2": 0, "p1": 0, "p2": 0, "k3": 0},
  "aligned": "color",
  "depthIntrinsics": {"fx": 476.06, "fy": 476.06, "cx": 322.47, "cy": 201.35},
  "colorIntrinsics": {"fx": 517.3, "fy": 516.5, "cx": 318.6, "cy": 238.2},
  "transform": {"rot": [0.9998, -0.0021, 0.0007, 0.0021, 0.9999, 0.0002, -0.0007, -0.0002, 1.0],
                "trans": [-25.3, 0.4, 0.6]}
}
```

深度值换算：`depth_mm = raw_value * valueScale`。`0` 表示无效/超出量程。

- `aligned=color` 时，`width/height` 是彩色分辨率，`intrinsics` 是彩色内参，深度可直接和彩色图逐像素对应。
- `aligned=none` 时，`width/height` 是深度原生分辨率，`intrinsics` 是深度内参；要叠到彩色需用 `transform` 自行重投影。
- `transform` 是 D2C 外参：`rot` 3×3（行优先）、`trans` 单位毫米。

### 深度帧

```
FrameHeader (36 bytes)
  uint32 frame_id
  uint32 width
  uint32 height
  uint32 format          // 8 = Y16, 11 = Y11 ...
  uint64 timestamp_us
  float  value_scale
  uint32 data_size
  uint32 reserved
```

随后是 `data_size` 字节像素数据。Y16 时大小为 `width * height * 2`。

## 快速查看

应用启动深度设备后，在服务器（或本机）运行：

```bash
python3 scripts/depth_view.py --port 47200 --frames 5   # 终端看中心距离和帧信息
python3 scripts/depth_view.py --port 47200 --save depth.pgm --frames 1
python3 scripts/depth_view.py --port 47200 --gui        # 需要 numpy + opencv-python
```

## Python 示例

```python
import json
import socket
import struct

HOST, PORT = "127.0.0.1", 47200

def readn(sock, n):
    buf = bytearray()
    while len(buf) < n:
        chunk = sock.recv(n - len(buf))
        if not chunk:
            raise EOFError
        buf += chunk
    return bytes(buf)

with socket.create_connection((HOST, PORT), timeout=5) as sock:
    for _ in range(100000):
        magic, version, kind, flags, size = struct.unpack("<4sBBHI", readn(sock, 12))
        if magic != b"OBDP":
            raise RuntimeError("bad magic")
        payload = readn(sock, size)
        if kind == 1:
            meta = json.loads(payload)
            width, height = meta["width"], meta["height"]
            scale = meta["valueScale"]
            print("meta:", meta)
        elif kind == 2:
            frame_id, w, h, fmt, ts, frame_scale, data_size, _ = struct.unpack("<IIIIQfII", payload[:36])
            data = payload[36:]
            # 深度原始值，转毫米：value * frame_scale
            print("frame", frame_id, w, h, fmt, data_size, len(data))
```

## 注意事项

- 先启动应用里的深度设备，再在服务器上连接；否则 SSH 隧道未建立。
- 深度助手一次只服务一个客户端。客户端断开后助手继续监听，可重连。
- 写入超时 2 秒；慢客户端会被断开，避免阻塞采集。
- 分辨率/帧率在「高级」里改；相机不支持时会自动回退到第一个可用配置，日志会提示。
