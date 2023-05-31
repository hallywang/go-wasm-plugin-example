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
