## 极简istio wasmplugin go语言开发示例

### 前置条件

#### k8s集群，版本1.23.8

安装方法略

#### istio安装完成demo，版本1.17.2

```shell
curl -L https://git.io/getLatestIstio | ISTIO_VERSION=1.17.2 sh
cd istio-1.17.2/
./bin/istioctl install --set profile=demo -y
kubectl label namespace default istio-injection=enabled --overwrite
```

#### 安装httpbin，sleep 示例

httpbin主要为了提供http响应，也可以用已有的能够提供http服务的任意服务。

sleep 为了在pod里使用curl命令，有别的pod中能执行curl命令也可以。

```shell
kubectl apply -f samples/httpbin/httpbin.yaml
kubectl apply -f samples/sleep/sleep.yaml
```

#### 本地docker环境，版本 20.10.21

docker安装略

#### go语言环境，版本1.18.1

go安装略

#### 阿里云或其他镜像仓库

类似部署一个普通应用，wasm插件来源于某个镜像仓库。

为了方便使用了阿里云的免费镜像仓库，也可以本地搭建或使用别的仓库。

注意需要将阿里云的仓库设置为公开！

### 示例功能

- 在请求头增加自定义的返回信息

- 将插件的配置信息放到头信息中返回（测试读取配置的功能）

- 简单的限流功能

### 直接看效果

使用我编译好的镜像直接部署，保存以下内容为 gowasm.yaml

```yaml
apiVersion: extensions.istio.io/v1alpha1
kind: WasmPlugin
metadata:
  name: my-go-wasm-plugin
  namespace: default
spec:
  selector:
    matchLabels:
      app: httpbin
  ## 编译好的镜像    
  url: oci://registry.cn-hangzhou.aliyuncs.com/hallywang/gowasm:20230530181612
  
  #插件的配置信息，在代码中可以获取到json string
  pluginConfig:
    testConfig: abcddeeeee
    listconfig:
     - abc
     - def
```

部署到k8s中

```shell
kubectl apply -f gowasm.yaml
```



### 示例验证方法

- 执行以下命令，从sleep pod中发送http 请求到 httpbin ，打印出返回的header

```shell
SLEEP_POD=$(kubectl get pod -l app=sleep -o jsonpath={.items..metadata.name}

kubectl exec ${SLEEP_POD} -c sleep -- sh -c 'for i in $(seq 1 3); do curl --head -s httpbin:8000/headers; sleep 0.1; done'

```

- 在未部署gowasm.yaml之前，每次请求都会返回200成功，且头信息中没有自定义的内容

- 在部署了gowasm.yaml之后，返回如下结果（两次请求的结果），有自定义的头信息和403的返回说明插件部署成功。

  ```shell
  ## 第一次请求的返回结果：
  HTTP/1.1 200 OK
  server: envoy
  date: Wed, 31 May 2023 03:20:12 GMT
  content-type: application/json
  content-length: 526
  access-control-allow-origin: *
  access-control-allow-credentials: true
  x-envoy-upstream-service-time: 3
  ## 下面是插件增加的头信息
  who-am-i: wasm-extension
  injected-by: istio-api!
  hally: wang
  wang: 1234567
  configdata: {"listconfig":["abc","def"],"testConfig":"abcddeeeee"}
  
  
  ## 第二次请求的返回结果：
  ## 限流起作用，返回403
  HTTP/1.1 403 Forbidden
  powered-by: proxy-wasm-go-sdk!!
  content-length: 29
  content-type: text/plain
  who-am-i: wasm-extension
  injected-by: istio-api!
  hally: wang
  wang: 1234567
  configdata: {"listconfig":["abc","def"],"testConfig":"abcddeeeee"}
  date: Wed, 31 May 2023 03:20:12 GMT
  server: envoy
  x-envoy-upstream-service-time: 0
  
  ```



### 从代码开始

#### 安装tinygo

tinygo可以将go编译成wasm文件

https://tinygo.org/getting-started/install/

#### 创建go工程

```shell
mkdir go-wasm-plugin
cd go-wasm-plugin
go mod init go-wasm
```

#### 新增文件main.go

```go
package main

import (
	"time"

	"github.com/tetratelabs/proxy-wasm-go-sdk/proxywasm"
	"github.com/tetratelabs/proxy-wasm-go-sdk/proxywasm/types"
)

func main() {
	proxywasm.SetVMContext(&vmContext{})
}

type vmContext struct {
	// Embed the default VM context here,
	// so that we don't need to reimplement all the methods.
	types.DefaultVMContext
}

// Override types.DefaultVMContext.
func (*vmContext) NewPluginContext(contextID uint32) types.PluginContext {
	return &pluginContext{}
}

type pluginContext struct {
	// Embed the default plugin context here,
	// so that we don't need to reimplement all the methods.
	types.DefaultPluginContext
	// the remaining token for rate limiting, refreshed periodically.
	remainToken int
	// // the preconfigured request per second for rate limiting.
	// requestPerSecond int
	// NOTE(jianfeih): any concerns about the threading and mutex usage for tinygo wasm?
	// the last time the token is refilled with `requestPerSecond`.
	lastRefillNanoSec int64
}

// Override types.DefaultPluginContext.
func (p *pluginContext) NewHttpContext(contextID uint32) types.HttpContext {
	return &httpHeaders{contextID: contextID, pluginContext: p}
}

type httpHeaders struct {
	// Embed the default http context here,
	// so that we don't need to reimplement all the methods.
	types.DefaultHttpContext
	contextID     uint32
	pluginContext *pluginContext
}

// Additional headers supposed to be injected to response headers.
var additionalHeaders = map[string]string{
	"who-am-i":    "go-wasm-extension",
	"injected-by": "istio-api!",
	"hally":       "wang",
	"wang":        "1234567",
	// 定义自定义的header，每个返回中都添加以上header
}

// 读取部署yaml中的 pluginConfig 内容，用于插件的一些配置信息
var configData string

func (p *pluginContext) OnPluginStart(pluginConfigurationSize int) types.OnPluginStartStatus {
	proxywasm.LogDebug("loading plugin config")
	data, err := proxywasm.GetPluginConfiguration()
	if data == nil {
		return types.OnPluginStartStatusOK
	}

	if err != nil {
		proxywasm.LogCriticalf("error reading plugin configuration: %v", err)
		return types.OnPluginStartStatusFailed
	}

	// 插件启动的时候读取配置
	configData = string(data)

	return types.OnPluginStartStatusOK
}

func (ctx *httpHeaders) OnHttpResponseHeaders(numHeaders int, endOfStream bool) types.Action {
	//添加headr
	for key, value := range additionalHeaders {
		proxywasm.AddHttpResponseHeader(key, value)
	}

	//为了便于演示观察，将配置信息也加到返回头里
	proxywasm.AddHttpResponseHeader("configData", configData)
	return types.ActionContinue
}

// 实现限流
func (ctx *httpHeaders) OnHttpRequestHeaders(int, bool) types.Action {
	current := time.Now().UnixNano()
	// We use nanoseconds() rather than time.Second() because the proxy-wasm has the known limitation.
	// TODO(incfly): change to time.Second() once https://github.com/proxy-wasm/proxy-wasm-cpp-host/issues/199
	// is resolved and released.
	if current > ctx.pluginContext.lastRefillNanoSec+1e9 {
		ctx.pluginContext.remainToken = 2
		ctx.pluginContext.lastRefillNanoSec = current
	}
	proxywasm.LogCriticalf("Current time %v, last refill time %v, the remain token %v",
		current, ctx.pluginContext.lastRefillNanoSec, ctx.pluginContext.remainToken)
	if ctx.pluginContext.remainToken == 0 {
		if err := proxywasm.SendHttpResponse(403, [][2]string{
			{"powered-by", "proxy-wasm-go-sdk!!"},
		}, []byte("rate limited, wait and retry."), -1); err != nil {
			proxywasm.LogErrorf("failed to send local response: %v", err)
			proxywasm.ResumeHttpRequest()
		}
		return types.ActionPause
	}
	ctx.pluginContext.remainToken -= 1
	return types.ActionContinue
}

```

#### 新增文件 Dockerfile

```dockerfile
# Dockerfile for building "compat" variant of Wasm Image Specification.
# https://github.com/solo-io/wasm/blob/master/spec/spec-compat.md

FROM scratch

COPY main.wasm ./plugin.wasm

```

#### 编译go代码为wasm

```shell
go get

tinygo build -o main.wasm -scheduler=none -target=wasi main.go

```

编译成功后，工程目录中将出现 main.wasm 文件

#### build一个Docker镜像,推送到镜像仓库

```shell
docker build . -t registy.cn-hangzhou.aliyuncs.com/USER_NAME/gowasm:0.1
docker push registy.cn-hangzhou.aliyuncs.com/USER_NAME/gowasm:0.1
```

#### 新增部署yaml

gowasm.yaml

```yaml
apiVersion: extensions.istio.io/v1alpha1
kind: WasmPlugin
metadata:
  name: my-go-wasm-plugin
  namespace: default
spec:
  selector:
    matchLabels:
      app: httpbin
  url: oci://registry.cn-hangzhou.aliyuncs.com/USER_NAME/gowasm:0.1
  pluginConfig:
    testConfig: abcddeeeee
    listconfig:
     - abc
     - def
```

#### 部署到k8s,执行测试脚本

```shell
kubectl apply -f gowasm.yaml

SLEEP_POD=$(kubectl get pod -l app=sleep -o jsonpath={.items..metadata.name}

kubectl exec ${SLEEP_POD} -c sleep -- sh -c 'for i in $(seq 1 3); do curl --head -s httpbin:8000/headers; sleep 0.1; done'

## 观察返回内容
```

#### 删除插件

```shell
kubectl delete wasmplugins my-go-wasm-plugin
```

#### 遇到的问题

- 修改代码重新发布部署后，如果镜像的tag没变化，可能出现不生效，这是因为wasmplugin有自己的缓存机制,tag版本发生变化，不会出现该问题

### 本文代码

https://github.com/hallywang/go-wasm-plugin-example.git

### 其他玩法

- istio官方提供的使用webassemblyhub来打包发布，内容参考

https://istio.io/latest/zh/blog/2020/deploy-wasm-declarative/

但是发现webassemblyhub提供的工具不支持最新版本的istio。

- c++ 开发方式

  https://github.com/istio-ecosystem/wasm-extensions/tree/master/doc

### 参考资料

https://tetrate.io/blog/istio-wasm-extensions-and-ecosystem/






