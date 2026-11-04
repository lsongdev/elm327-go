# elm327-go

面向 Wi-Fi OBD-II 适配器的 Go 工具集：ELM327 客户端与 TCP 服务端、完整的 OBD-II 数据解析、HTTP API，以及一个无需构建步骤的 Web 界面。

```
obd/        纯数据解析：Mode 01 PID 表、DTC、就绪监视器、VIN 等，不依赖传输层
elm327/     Client（连接适配器）、Server（TCP 服务端框架）、响应帧解析
simulator/  模拟一台车和它的适配器，基于 elm327.Server
api/        Monitor（基于 Client 轮询和重连）与 HTTP Handler
web/        浏览器界面（Preact + HTM，原生 ES modules），嵌入到二进制
cmd/        elm327-server（HTTP + Web）、elm327-sim（TCP 模拟器）
examples/   client（直接使用客户端）、proxy（透明代理，抓取手机 App 的通信）
```

## 快速开始

```sh
make build    # npm ci，然后编译 elm327-server 和 elm327-sim

# 没有车：用内置模拟车，打开 http://localhost:8080
./elm327-server -simulate

# 连接真实适配器
./elm327-server -adapter 192.168.0.10:35000
```

| 参数 | 说明 |
|------|------|
| `-adapter host:port` | 适配器地址 |
| `-simulate` / `-profile x.json` | 使用内置模拟车，可选车辆配置 |
| `-listen :8080` | HTTP 监听地址 |
| `-interval 500ms` | 轮询间隔 |
| `-token xxx` | 要求 Bearer token 或 basic auth 密码，也可用 `ELM327_TOKEN` |
| `-allow-clear` | 允许清除故障码，默认关闭 |

默认监听所有网卡。在共享网络上使用时请设置 `-token`。

## HTTP API

| 接口 | 说明 |
|------|------|
| `GET /api/state` | 下面所有内容的完整快照 |
| `GET /api/status` | 连接状态：`connecting`、`searching`、`no_vehicle`、`online` |
| `GET /api/metrics` | 最近一次轮询到的所有值 |
| `GET /api/pids` | 所有已知 PID，以及车辆是否支持 |
| `GET /api/pids/{pid}` | 立即读取一个 PID，返回每个 ECU 的结果，如 `/api/pids/0C` |
| `GET /api/dtc[?refresh=1]` | 故障灯、就绪监视器、stored/pending/permanent 故障码 |
| `POST /api/dtc/clear` | 清除故障码，需要 `-allow-clear` 和 `{"confirm": true}` |
| `GET /api/freeze-frame` | 冻结帧，没有时返回 `null` |
| `GET /api/vehicle[?refresh=1]` | VIN、标定 ID、CVN、ECU 名称和地址 |
| `GET /api/log?after=&limit=` | 与适配器的最近通信记录 |
| `POST /api/command` | 发送一条只读命令 `{"command": "010C"}` 并解析结果 |
| `POST /api/decode` | 解析一段抓取的输出 `{"response": "7E8 04 41 0C 1A F8"}`，不访问车辆 |
| `GET /api/events?log=1` | SSE：状态变化时发 `state`，带 `log=1` 时每条通信发 `log` |

错误响应为 `{"error": "..."}`：503 没有连接适配器，404 车辆没有该数据，502 适配器或 ECU 报错，403 操作被禁止。

### 写操作的边界

连接的是真车，所以默认不能改变车辆状态：

- `POST /api/command` 只放行只读服务（01、02、03、06、07、09、0A）和只读 AT 查询。清码（04）和可能驱动执行器或刷写模块的服务（如 2E、2F、31、34）一律拒绝，`-allow-clear` 也不会放开。
- 清除故障码只能走 `POST /api/dtc/clear`。它同时会清掉冻结帧和就绪监视器，车辆要跑完一个驾驶循环才能通过排放检测。

## 客户端

```go
client := &elm327.Client{}
if err := client.Connect("192.168.0.10:35000"); err != nil {
    log.Fatal(err)
}
defer client.Close()
if err := client.Initialize(); err != nil {
    log.Fatal(err)
}

pids, err := client.SupportedPIDs()         // 首次查询会触发协议搜索，最多等 30 秒
reading, err := client.ReadPID(0x0C)        // reading.Name, *reading.Value, reading.Unit
all, err := client.ReadPIDAll(0x0C)         // 每个 ECU 一条结果
stored, err := client.ReadDTCs(obd.DTCStored)
status, err := client.MonitorStatus()       // 故障灯、DTC 数量、就绪监视器
frame, err := client.FreezeFrame()
info, err := client.VehicleInfo()           // VIN 等
adapter, err := client.AdapterInfo()        // 版本、协议、电压
messages, err := client.Query("0902")       // 任意服务，返回重组后的每 ECU 消息
```

`Client` 可并发使用，同一连接上的命令会串行执行。发生超时、短写入或协议流损坏时连接会被关闭（`Connected()` 变为 false），调用方应重新连接并初始化。`ErrNoData`、`*AdapterError` 和 `*NegativeResponseError` 不影响连接，可用 `elm327.IsNotAvailable` 判断车辆是否只是没有这项数据。

## 数据解析

`elm327.ParseMessages` 把适配器输出解析成每个 ECU 一条的完整消息，支持：

- 回显、`SEARCHING...`、有无空格、有无 linefeed
- 11 位、29 位 CAN header，ISO 9141 / KWP / J1850 的 header 和校验和
- ISO-TP 多帧重组（按 ECU 分开，多个 ECU 交错也能处理）和 `0:`/`1:` 编号多行
- CAN 填充字节、`ATCAF0` 的 PCI 字节、NRC 78（响应未完成）
- `<RX ERROR` 等损坏帧标记、`NO DATA`、`UNABLE TO CONNECT` 等状态

`obd` 包的内容：

- `obd.Decode(pid, data)`：解码 107 个 Mode 01 PID，同一张表也用于 Mode 02。表外的 PID 返回原始字节
- `ParseMonitorStatus`：故障灯、DTC 数量、就绪监视器，区分火花点火和压燃
- `ParseDTCs` / `DecodeDTC` / `NewDTC`：CAN 和传统格式的故障码；内置常见 SAE 故障码的英文描述，厂商自定义码只给出系统分类
- `ParseVIN`、`ParseCalibrationIDs`、`ParseCVNs`、`ParseECUName`：Mode 09

尚未实现：Mode 06 测试结果，以及厂商私有 PID（Mode 22）。

## 模拟器

```sh
go run ./cmd/elm327-sim -port 35000
go run ./cmd/elm327-sim -port 35000 -profile simulator/profiles/ford.json
```

内置模拟车按循环工况行驶，带有几个故障码，支持 AT 常用命令和服务 01、02、03、04、07、09、0A，长响应按 ISO-TP 分帧。每个 TCP 连接有独立的 echo、header、spaces 等状态，车辆状态（故障码）则共享。

车辆 profile 支持 `fixed`、`random`、`unsupported` PID，以及可选的 `vin`、`stored`、`pending`、`permanent`。模拟器用于应用层调试，不模拟 CAN 时序、电气故障或非 CAN 协议的分帧。

## 透明代理

代理原样转发手机客户端与真实适配器之间的字节，同时记录可读日志：

```sh
go run ./examples/proxy \
  -listen 0.0.0.0:35000 \
  -upstream 192.168.0.10:35000 \
  -out elm327.log
```

## 开发

```sh
make build        # npm ci，然后编译
make test         # go test ./...
make run          # 模拟车 + Web 界面（允许清码）
make deps         # 按 lockfile 重装 web/ui/node_modules
go test -race ./...
go vet ./...
```

`web/ui` 的代码没有构建步骤，但依赖 `web/ui/node_modules`，它不提交到仓库，由 `npm ci` 按 lockfile 安装后通过 `go:embed` 打进二进制。所以编译前要先运行 `make build` 或 `make deps`：缺少依赖时 `go build` 仍会成功，但界面无法加载，`go test ./web` 会报错。
