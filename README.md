# elm327-go

一个面向 Wi-Fi OBD 适配器的 Go ELM327 客户端，以及用于调试手机客户端的 TCP 模拟器和透明代理。

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
rpm, err := client.EngineRPM()
```

`Client` 会以 `>` 为响应边界，并处理连接时已经存在的 prompt、命令回显、CAN 11/29 位 header、DLC/PCI、关闭空格和多个 ECU 响应。一次连接上的命令会被串行化；发生超时、短写入或协议流损坏时，连接会被关闭，调用方应重新连接并初始化。

`ReadPIDResponses` 返回每个 ECU 的 Mode 01 数据；`ReadPIDBytes` 返回第一个 ECU 的数据；`Repeat` 通过单独的 CR 重复上一条 ELM327 命令。

```sh
go run ./examples/client -host 192.168.0.10 -port 35000
```

## 模拟器

```sh
go run ./examples/server -port 35000
go run ./examples/server -port 35000 -profile examples/server/profiles/ford.json
```

每个 TCP 连接拥有独立的 echo、header、spaces、linefeed、protocol、CAN formatting 和 transmit header 状态，因此可同时调试多个客户端。车辆 profile 支持 `fixed`、`random` 和 `unsupported` PID。

模拟器实现常见 ELM327 初始化命令、Mode 01 指标和简单 Mode 22 DID 响应；它用于应用层调试，不模拟真实 CAN 总线时序、电气故障或所有 SAE J1979 服务。

## 透明代理

代理原样转发手机客户端与真实适配器之间的字节，同时记录可读日志：

```sh
go run ./examples/proxy \
  -listen 0.0.0.0:35000 \
  -upstream 192.168.0.10:35000 \
  -out elm327.log
```

## 验证

```sh
go test ./...
go test -race ./...
go vet ./...
go build ./...
```
