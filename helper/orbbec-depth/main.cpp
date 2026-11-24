// orbbec-depth: Orbbec SDK depth stream over a framed TCP protocol.
//
// Usage:
//   orbbec-depth --list [--config FILE]
//   orbbec-depth --config FILE --serial SN --port 9200 --width 640 --height 400 --fps 30
//
// Protocol: see docs/depth-protocol.md
#include <libobsensor/ObSensor.hpp>
#include <libobsensor/hpp/Error.hpp>

#include <atomic>
#include <cerrno>
#include <cmath>
#include <csignal>
#include <cstdint>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <string>
#include <vector>

#if defined(_WIN32)
#define NOMINMAX
#include <winsock2.h>
#include <ws2tcpip.h>
#include <windows.h>
typedef SOCKET socket_t;
static const socket_t kInvalidSocket = INVALID_SOCKET;
#else
#include <fcntl.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <sys/file.h>
#include <sys/socket.h>
#include <sys/time.h>
#include <unistd.h>
typedef int socket_t;
static const socket_t kInvalidSocket = -1;
#endif

namespace {

constexpr uint32_t kMagic = 0x5044424f; // "OBDP" little-endian
constexpr uint8_t kVersion = 1;
constexpr uint8_t kTypeMeta = 1;
constexpr uint8_t kTypeDepth = 2;

std::atomic<bool> gStop(false);

void onSignal(int) { gStop.store(true); }

void closeSocketFd(socket_t fd) {
#if defined(_WIN32)
    if (fd != kInvalidSocket) ::closesocket(fd);
#else
    if (fd >= 0) ::close(fd);
#endif
}

// A device can only be opened by one process; a second helper would fail to
// claim the USB interface and silently stream zero-filled frames.
#if defined(_WIN32)
static HANDLE gDeviceLock = nullptr;

bool acquireDeviceLock(const std::string &key) {
    std::string name = "Global\\serial-tool-orbbec-" + key;
    for (auto &ch : name) {
        if (ch == '/' || ch == '\\' || ch == ':') ch = '_';
    }
    HANDLE h = CreateMutexA(nullptr, TRUE, name.c_str());
    if (!h) return false;
    if (GetLastError() == ERROR_ALREADY_EXISTS) {
        CloseHandle(h);
        return false;
    }
    gDeviceLock = h;
    return true;
}
#else
static int gDeviceLockFd = -1;

bool acquireDeviceLock(const std::string &key) {
    std::string safe;
    for (char ch : key) {
        safe += (ch == '/' || ch == ':' || ch == ' ') ? '_' : ch;
    }
    const char *tmp = std::getenv("TMPDIR");
    std::string path = std::string(tmp && *tmp ? tmp : "/tmp/") + "serial-tool-orbbec-" + safe + ".lock";
    int fd = ::open(path.c_str(), O_CREAT | O_RDWR, 0600);
    if (fd < 0) return true; // cannot create lock file: do not block startup
    if (::flock(fd, LOCK_EX | LOCK_NB) != 0) {
        ::close(fd);
        return false;
    }
    gDeviceLockFd = fd;
    return true;
}
#endif

bool setBlocking(socket_t fd, bool blocking) {
#if defined(_WIN32)
    u_long mode = blocking ? 0 : 1;
    return ::ioctlsocket(fd, FIONBIO, &mode) == 0;
#else
    int flags = ::fcntl(fd, F_GETFL, 0);
    if (flags < 0) return false;
    flags = blocking ? (flags & ~O_NONBLOCK) : (flags | O_NONBLOCK);
    return ::fcntl(fd, F_SETFL, flags) == 0;
#endif
}

std::string socketErrorText() {
#if defined(_WIN32)
    int code = WSAGetLastError();
    char *buf = nullptr;
    FormatMessageA(FORMAT_MESSAGE_ALLOCATE_BUFFER | FORMAT_MESSAGE_FROM_SYSTEM | FORMAT_MESSAGE_IGNORE_INSERTS,
                   nullptr, code, 0, reinterpret_cast<char *>(&buf), 0, nullptr);
    std::string msg = buf ? buf : "";
    if (buf) LocalFree(buf);
    return msg.empty() ? ("WSA error " + std::to_string(code)) : msg;
#else
    return std::strerror(errno);
#endif
}

bool interruptedError() {
#if defined(_WIN32)
    return WSAGetLastError() == WSAEINTR;
#else
    return errno == EINTR;
#endif
}

std::string jsonEscape(const std::string &s) {
    std::string out;
    for (char c : s) {
        switch (c) {
        case '"': out += "\\\""; break;
        case '\\': out += "\\\\"; break;
        case '\n': out += "\\n"; break;
        case '\r': out += "\\r"; break;
        case '\t': out += "\\t"; break;
        default:
            if (static_cast<unsigned char>(c) < 0x20) {
                char buf[8];
                std::snprintf(buf, sizeof(buf), "\\u%04x", c);
                out += buf;
            } else {
                out += c;
            }
        }
    }
    return out;
}

void logMsg(const std::string &s) {
    std::fprintf(stderr, "[orbbec-depth] %s\n", s.c_str());
    std::fflush(stderr);
}

std::string formatName(int format) {
    switch (format) {
    case OB_FORMAT_Y16: return "Y16";
    case OB_FORMAT_Y14: return "Y14";
    case OB_FORMAT_Y12: return "Y12";
    case OB_FORMAT_Y11: return "Y11";
    case OB_FORMAT_Y10: return "Y10";
    case OB_FORMAT_Z16: return "Z16";
    default: return "UNKNOWN";
    }
}

void put32(std::vector<uint8_t> &buf, uint32_t v) {
    for (int i = 0; i < 4; i++) buf.push_back(static_cast<uint8_t>((v >> (8 * i)) & 0xff));
}

void put64(std::vector<uint8_t> &buf, uint64_t v) {
    for (int i = 0; i < 8; i++) buf.push_back(static_cast<uint8_t>((v >> (8 * i)) & 0xff));
}

void putFloat(std::vector<uint8_t> &buf, float v) {
    uint32_t bits = 0;
    std::memcpy(&bits, &v, sizeof(bits));
    put32(buf, bits);
}

std::vector<uint8_t> makePacket(uint8_t type, const std::vector<uint8_t> &payload) {
    std::vector<uint8_t> pkt;
    pkt.reserve(12 + payload.size());
    put32(pkt, kMagic);
    pkt.push_back(kVersion);
    pkt.push_back(type);
    pkt.push_back(0);
    pkt.push_back(0);
    put32(pkt, static_cast<uint32_t>(payload.size()));
    pkt.insert(pkt.end(), payload.begin(), payload.end());
    return pkt;
}

bool writeAll(socket_t fd, const uint8_t *data, size_t size) {
    size_t off = 0;
    while (off < size) {
#if defined(_WIN32)
        int n = ::send(fd, reinterpret_cast<const char *>(data + off), static_cast<int>(size - off), 0);
#else
        ssize_t n = ::send(fd, data + off, size - off, 0);
#endif
        if (n > 0) {
            off += static_cast<size_t>(n);
            continue;
        }
        if (n < 0 && interruptedError()) continue;
        logMsg("send failed: " + socketErrorText());
        return false;
    }
    return true;
}

struct Options {
    bool list = false;
    std::string config;
    std::string serial;
    std::string align = "none";
    int port = 9200;
    int outWidth = 0;
    int outHeight = 0;
    int width = 640;
    int height = 400;
    int fps = 30;
};

bool parseArgs(int argc, char **argv, Options &opt) {
    for (int i = 1; i < argc; i++) {
        std::string a = argv[i];
        auto need = [&](const char *name) -> const char * {
            if (i + 1 >= argc) {
                std::fprintf(stderr, "missing value for %s\n", name);
                std::exit(2);
            }
            return argv[++i];
        };
        if (a == "--list") opt.list = true;
        else if (a == "--config") opt.config = need("--config");
        else if (a == "--serial") opt.serial = need("--serial");
        else if (a == "--align") opt.align = need("--align");
        else if (a == "--port") opt.port = std::atoi(need("--port"));
        else if (a == "--out-width") opt.outWidth = std::atoi(need("--out-width"));
        else if (a == "--out-height") opt.outHeight = std::atoi(need("--out-height"));
        else if (a == "--width") opt.width = std::atoi(need("--width"));
        else if (a == "--height") opt.height = std::atoi(need("--height"));
        else if (a == "--fps") opt.fps = std::atoi(need("--fps"));
        else if (a == "-h" || a == "--help") {
            std::printf("usage: %s --list [--config FILE] | --config FILE --serial SN --port N [--width W --height H --fps F] [--align none|color|depth]\n", argv[0]);
            std::exit(0);
        } else {
            std::fprintf(stderr, "unknown argument: %s\n", a.c_str());
            return false;
        }
    }
    return true;
}

int listDevices(const Options &opt) {
    ob::Context ctx(opt.config.empty() ? nullptr : opt.config.c_str());
    auto list = ctx.queryDeviceList();
    std::string json = "{\"devices\":[";
    for (uint32_t i = 0; i < list->deviceCount(); i++) {
        if (i) json += ",";
        const char *rawSerial = list->serialNumber(i);
        const char *rawUid = list->uid(i);
        const char *rawName = list->name(i);
        const char *rawConn = list->connectionType(i);
        std::string serial = rawSerial ? rawSerial : "";
        std::string uid = rawUid ? rawUid : "";
        std::string id = !uid.empty() ? uid : serial;
        json += "{\"id\":\"" + jsonEscape(id) + "\"";
        json += ",\"name\":\"" + jsonEscape(rawName ? rawName : "") + "\"";
        json += ",\"serial\":\"" + jsonEscape(serial) + "\"";
        json += ",\"uid\":\"" + jsonEscape(uid) + "\"";
        json += ",\"pid\":" + std::to_string(list->pid(i));
        json += ",\"vid\":" + std::to_string(list->vid(i));
        json += ",\"connection\":\"" + jsonEscape(rawConn ? rawConn : "") + "\"}";
    }
    json += "]}";
    std::printf("%s\n", json.c_str());
    return 0;
}

bool setupListener(int port, socket_t &listenFd) {
    listenFd = ::socket(AF_INET, SOCK_STREAM, 0);
    if (listenFd == kInvalidSocket) {
        logMsg("socket() failed");
        return false;
    }
    int one = 1;
    ::setsockopt(listenFd, SOL_SOCKET, SO_REUSEADDR, reinterpret_cast<const char *>(&one), sizeof(one));
    sockaddr_in addr{};
    addr.sin_family = AF_INET;
    addr.sin_port = htons(static_cast<uint16_t>(port));
    addr.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    if (::bind(listenFd, reinterpret_cast<sockaddr *>(&addr), sizeof(addr)) != 0) {
        logMsg("bind 127.0.0.1:" + std::to_string(port) + " failed: " + socketErrorText());
        closeSocketFd(listenFd);
        return false;
    }
    if (::listen(listenFd, 4) != 0) {
        logMsg("listen() failed: " + socketErrorText());
        closeSocketFd(listenFd);
        return false;
    }
    if (!setBlocking(listenFd, false)) {
        logMsg("failed to set listener non-blocking");
        closeSocketFd(listenFd);
        return false;
    }
    return true;
}

void configureClient(socket_t fd) {
    setBlocking(fd, true);
    int one = 1;
    ::setsockopt(fd, IPPROTO_TCP, TCP_NODELAY, reinterpret_cast<const char *>(&one), sizeof(one));
#if defined(SO_NOSIGPIPE)
    ::setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &one, sizeof(one));
#endif
#if defined(_WIN32)
    DWORD timeoutMs = 2000;
    ::setsockopt(fd, SOL_SOCKET, SO_SNDTIMEO, reinterpret_cast<const char *>(&timeoutMs), sizeof(timeoutMs));
#else
    timeval tv{};
    tv.tv_sec = 2;
    tv.tv_usec = 0;
    ::setsockopt(fd, SOL_SOCKET, SO_SNDTIMEO, &tv, sizeof(tv));
#endif
}

void closeClient(socket_t &fd) {
    if (fd != kInvalidSocket) {
        closeSocketFd(fd);
        fd = kInvalidSocket;
    }
}

std::string metaJson(int width, int height, int format, float valueScale, const OBCameraParam &param,
                     const OBCameraIntrinsic &intr, const std::string &serial, const std::string &name,
                     const std::string &align) {
    const bool aligned = align == "color";
    const OBCameraDistortion &dist = aligned ? param.rgbDistortion : param.depthDistortion;
    char buf[2048];
    std::snprintf(buf, sizeof(buf),
                  "{\"type\":\"meta\",\"width\":%u,\"height\":%u,\"format\":%d,\"formatName\":\"%s\","
                  "\"valueScale\":%.9g,\"serial\":\"%s\",\"name\":\"%s\",\"aligned\":\"%s\","
                  "\"intrinsics\":{\"fx\":%.9g,\"fy\":%.9g,\"cx\":%.9g,\"cy\":%.9g},"
                  "\"distortion\":{\"model\":%d,\"k1\":%.9g,\"k2\":%.9g,\"p1\":%.9g,\"p2\":%.9g,\"k3\":%.9g},"
                  "\"depthIntrinsics\":{\"fx\":%.9g,\"fy\":%.9g,\"cx\":%.9g,\"cy\":%.9g},"
                  "\"colorIntrinsics\":{\"fx\":%.9g,\"fy\":%.9g,\"cx\":%.9g,\"cy\":%.9g},"
                  "\"transform\":{\"rot\":[%.9g,%.9g,%.9g,%.9g,%.9g,%.9g,%.9g,%.9g,%.9g],"
                  "\"trans\":[%.9g,%.9g,%.9g]}}",
                  width, height, format, formatName(format).c_str(), valueScale,
                  jsonEscape(serial).c_str(), jsonEscape(name).c_str(), align.c_str(),
                  intr.fx, intr.fy, intr.cx, intr.cy,
                  0, dist.k1, dist.k2, dist.p1, dist.p2, dist.k3,
                  param.depthIntrinsic.fx, param.depthIntrinsic.fy,
                  param.depthIntrinsic.cx, param.depthIntrinsic.cy,
                  param.rgbIntrinsic.fx, param.rgbIntrinsic.fy,
                  param.rgbIntrinsic.cx, param.rgbIntrinsic.cy,
                  param.transform.rot[0], param.transform.rot[1], param.transform.rot[2],
                  param.transform.rot[3], param.transform.rot[4], param.transform.rot[5],
                  param.transform.rot[6], param.transform.rot[7], param.transform.rot[8],
                  param.transform.trans[0], param.transform.trans[1], param.transform.trans[2]);
    return std::string(buf);
}

struct StreamSink {
    socket_t fd = kInvalidSocket;
    bool metaSent = false;
};

bool acceptInto(socket_t listenFd, StreamSink &sink) {
    sockaddr_in peer{};
#if defined(_WIN32)
    int peerLen = sizeof(peer);
#else
    socklen_t peerLen = sizeof(peer);
#endif
    socket_t fd = ::accept(listenFd, reinterpret_cast<sockaddr *>(&peer), &peerLen);
    if (fd == kInvalidSocket) return false;
    configureClient(fd);
    sink.fd = fd;
    sink.metaSent = false;
    return true;
}

struct DepthAlignment {
    bool enabled = false;
    int width = 0;
    int height = 0;
    float fx = 0, fy = 0, cx = 0, cy = 0;
    float rot[9] = {1, 0, 0, 0, 1, 0, 0, 0, 1};
    float trans[3] = {0, 0, 0};
    float depthFx = 0, depthFy = 0, depthCx = 0, depthCy = 0;
    std::vector<uint16_t> buffer;

    // Reprojects the depth frame onto the color grid using the depth->color
    // extrinsic, so consumers get pixel-aligned depth without needing an SDK
    // color stream (which macOS UVC drivers refuse to release).
    const uint16_t *apply(const std::shared_ptr<ob::DepthFrame> &depth) {
        if (!enabled || depthFx <= 0 || depthFy <= 0) return nullptr;
        const int dw = static_cast<int>(depth->width());
        const int dh = static_cast<int>(depth->height());
        const uint16_t *src = static_cast<const uint16_t *>(depth->data());
        buffer.assign(static_cast<size_t>(width) * height, 0);
        const float invFx = 1.0f / depthFx, invFy = 1.0f / depthFy;
        for (int y = 0; y < dh; y++) {
            for (int x = 0; x < dw; x++) {
                uint16_t z = src[y * dw + x];
                if (z == 0) continue;
                float X = (x - depthCx) * z * invFx;
                float Y = (y - depthCy) * z * invFy;
                float Z = static_cast<float>(z);
                float cx3 = rot[0] * X + rot[1] * Y + rot[2] * Z + trans[0];
                float cy3 = rot[3] * X + rot[4] * Y + rot[5] * Z + trans[1];
                float cz3 = rot[6] * X + rot[7] * Y + rot[8] * Z + trans[2];
                if (cz3 <= 0) continue;
                int u = static_cast<int>(fx * cx3 / cz3 + cx + 0.5f);
                int v = static_cast<int>(fy * cy3 / cz3 + cy + 0.5f);
                if (u < 0 || v < 0 || u >= width || v >= height) continue;
                buffer[static_cast<size_t>(v) * width + u] = z;
            }
        }
        return buffer.data();
    }
};

int runStream(const Options &opt) {
    ob::Context ctx(opt.config.empty() ? nullptr : opt.config.c_str());
    auto list = ctx.queryDeviceList();
    if (list->deviceCount() == 0) {
        logMsg("no Orbbec device found");
        return 1;
    }

    std::shared_ptr<ob::Device> device;
    for (uint32_t i = 0; i < list->deviceCount(); i++) {
        const char *rawSerial = list->serialNumber(i);
        const char *rawUid = list->uid(i);
        std::string serial = rawSerial ? rawSerial : "";
        std::string uid = rawUid ? rawUid : "";
        if (opt.serial.empty() || opt.serial == serial || opt.serial == uid) {
            device = list->getDevice(i);
            break;
        }
    }
    if (!device) {
        logMsg("requested device not found: " + opt.serial);
        return 1;
    }

    auto info = device->getDeviceInfo();
    std::string serial = info->serialNumber() ? info->serialNumber() : "";
    std::string name = info->name() ? info->name() : "";
    if (serial.empty()) serial = opt.serial;

    if (!acquireDeviceLock(serial)) {
        logMsg("设备已被另一个 orbbec-depth 进程占用（" + serial + "），请先结束它");
        return 3;
    }

    ob::Pipeline pipe(device);
    auto profiles = pipe.getStreamProfileList(OB_SENSOR_DEPTH);
    std::shared_ptr<ob::VideoStreamProfile> profile;
    try {
        profile = profiles->getVideoStreamProfile(opt.width, opt.height, OB_FORMAT_ANY, opt.fps);
    } catch (const ob::Error &) {
        profile = profiles->getProfile(0)->as<ob::VideoStreamProfile>();
        logMsg("requested profile unavailable, using " + std::to_string(profile->width()) + "x" +
               std::to_string(profile->height()) + "@" + std::to_string(profile->fps()));
    }

    auto config = std::make_shared<ob::Config>();
    config->enableStream(profile);
    pipe.start(config);

    auto param = pipe.getCameraParam();

    DepthAlignment alignment;
    if (opt.align == "color") {
        auto &ri = param.rgbIntrinsic;
        int ow = opt.outWidth > 0 ? opt.outWidth : (ri.width > 0 ? ri.width : 640);
        int oh = opt.outHeight > 0 ? opt.outHeight : (ri.height > 0 ? ri.height : 480);
        if (ri.fx <= 0 || ri.fy <= 0) {
            logMsg("彩色内参不可用，回退为原始深度（aligned=none）");
        } else {
            float sx = (ri.width > 0) ? static_cast<float>(ow) / ri.width : 1.0f;
            float sy = (ri.height > 0) ? static_cast<float>(oh) / ri.height : 1.0f;
            alignment.enabled = true;
            alignment.width = ow;
            alignment.height = oh;
            alignment.fx = ri.fx * sx;
            alignment.fy = ri.fy * sy;
            alignment.cx = ri.cx * sx;
            alignment.cy = ri.cy * sy;
            for (int i = 0; i < 9; i++) alignment.rot[i] = param.transform.rot[i];
            for (int i = 0; i < 3; i++) alignment.trans[i] = param.transform.trans[i];
            alignment.depthFx = param.depthIntrinsic.fx;
            alignment.depthFy = param.depthIntrinsic.fy;
            alignment.depthCx = param.depthIntrinsic.cx;
            alignment.depthCy = param.depthIntrinsic.cy;
            logMsg("depth aligned to color " + std::to_string(ow) + "x" + std::to_string(oh));
        }
    }

    socket_t depthListen = kInvalidSocket;
    if (!setupListener(opt.port, depthListen)) return 1;
    logMsg("listening depth=127.0.0.1:" + std::to_string(opt.port) +
           " device=" + name + " serial=" + serial +
           " profile=" + std::to_string(profile->width()) + "x" + std::to_string(profile->height()) + "@" + std::to_string(profile->fps()) +
           " align=" + (alignment.enabled ? "color" : "none"));

    StreamSink depthSink;
    int zeroStreak = 0;
    bool zeroWarned = false;

    while (!gStop.load()) {
        if (depthSink.fd == kInvalidSocket) {
            if (acceptInto(depthListen, depthSink)) logMsg("depth client connected");
        }
        auto frameSet = pipe.waitForFrames(100);
        if (!frameSet || !frameSet->depthFrame()) continue;
        auto depth = frameSet->depthFrame();
        const uint16_t *alignedData = alignment.apply(depth);
        const void *depthData = alignedData ? static_cast<const void *>(alignedData) : depth->data();
        const int outW = alignedData ? alignment.width : static_cast<int>(depth->width());
        const int outH = alignedData ? alignment.height : static_cast<int>(depth->height());
        const size_t outBytes = static_cast<size_t>(outW) * outH * 2;

        if (depth) {
            if (!zeroWarned && depth->dataSize() > 0) {
                const uint8_t *raw = static_cast<const uint8_t *>(depth->data());
                bool allZero = true;
                for (size_t i = 0; i < depth->dataSize(); i++) {
                    if (raw[i] != 0) { allZero = false; break; }
                }
                zeroStreak = allZero ? zeroStreak + 1 : 0;
                if (zeroStreak >= 30) {
                    zeroWarned = true;
                    logMsg("深度帧持续全为 0：可能是设备被其他进程占用、激光关闭或超出量程");
                }
            }

            if (depthSink.fd != kInvalidSocket) {
                if (!depthSink.metaSent) {
                    OBCameraIntrinsic intr = param.depthIntrinsic;
                    if (alignedData) {
                        intr.fx = alignment.fx;
                        intr.fy = alignment.fy;
                        intr.cx = alignment.cx;
                        intr.cy = alignment.cy;
                        intr.width = static_cast<int16_t>(alignment.width);
                        intr.height = static_cast<int16_t>(alignment.height);
                    }
                    std::string meta = metaJson(outW, outH, static_cast<int>(depth->format()), depth->getValueScale(),
                                                param, intr, serial, name, alignedData ? "color" : "none");
                    std::vector<uint8_t> payload(meta.begin(), meta.end());
                    auto pkt = makePacket(kTypeMeta, payload);
                    if (!writeAll(depthSink.fd, pkt.data(), pkt.size())) {
                        closeClient(depthSink.fd);
                    } else {
                        depthSink.metaSent = true;
                    }
                }
                if (depthSink.fd != kInvalidSocket) {
                    std::vector<uint8_t> payload;
                    payload.reserve(32 + outBytes);
                    put32(payload, depth->index());
                    put32(payload, static_cast<uint32_t>(outW));
                    put32(payload, static_cast<uint32_t>(outH));
                    put32(payload, static_cast<uint32_t>(depth->format()));
                    put64(payload, depth->timeStampUs());
                    putFloat(payload, depth->getValueScale());
                    put32(payload, static_cast<uint32_t>(outBytes));
                    put32(payload, 0);
                    const uint8_t *data = static_cast<const uint8_t *>(depthData);
                    payload.insert(payload.end(), data, data + outBytes);

                    auto pkt = makePacket(kTypeDepth, payload);
                    if (!writeAll(depthSink.fd, pkt.data(), pkt.size())) {
                        closeClient(depthSink.fd);
                        logMsg("depth client disconnected or too slow");
                    }
                }
            }
        }
    }

    closeClient(depthSink.fd);
    closeSocketFd(depthListen);
    pipe.stop();
    logMsg("stopped");
    return 0;
}

} // namespace

int main(int argc, char **argv) {
    std::signal(SIGINT, onSignal);
    std::signal(SIGTERM, onSignal);
#if !defined(_WIN32)
    std::signal(SIGPIPE, SIG_IGN);
#else
    WSADATA wsa{};
    if (WSAStartup(MAKEWORD(2, 2), &wsa) != 0) {
        logMsg("WSAStartup failed");
        return 1;
    }
#endif

    Options opt;
    if (!parseArgs(argc, argv, opt)) return 2;

    ob::Context::setLoggerSeverity(OB_LOG_SEVERITY_WARN);
    try {
        if (opt.list) return listDevices(opt);
        return runStream(opt);
    } catch (const ob::Error &e) {
        logMsg(std::string("SDK error: ") + e.getMessage());
        return 1;
    } catch (const std::exception &e) {
        logMsg(std::string("error: ") + e.what());
        return 1;
    }
#if defined(_WIN32)
    WSACleanup();
#endif
}
