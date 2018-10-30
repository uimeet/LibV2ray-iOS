package libv2ray

import (
	"crypto/tls"
	"fmt"
	"github.com/jochasinga/requests"
	mobasset "golang.org/x/mobile/asset"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"v2ray.com/core"
	"v2ray.com/core/main/confloader"
	_ "v2ray.com/core/main/distro/all"
	"v2ray.com/ext/sysio"
	"v2ray.com/ext/tools/conf"
)

const (
	HB_STATUS_RUNNING int = iota
	HB_STATUS_STOPPED
)

type V2RayPoint struct {
	status				Status
	Callbacks			V2RayCallbacks
	v2rayOP         	*sync.Mutex
	interuptDeferto		int64

	ConfigureFile		string
	ConfigureContent	string

	EnableHeartbeat		bool // 是否启用心跳
	hbStatus			int	// 心跳状态

	UID					string

	Config				*conf.Config
}

/*
	V2RayCallbacks a Callback set for V2Ray
 */
type V2RayCallbacks interface {
	OnEmitStatus(int, string) int
}

/**
	PrintVersion 打印内核版本号到控制台
 */
func PrintVersion() {
	version := core.VersionStatement()
	for _, s := range version {
		fmt.Sprintln(s)
	}
}

/**
	Version 返回内核的版本号
 */
func Version() string {
	return core.Version()
}

func (v *V2RayPoint) pointloop() {
	var config core.Config
	if v.ConfigureFile == "json" {
		conf, jsonConf, _ := LoadJSONConfig(strings.NewReader(v.ConfigureContent))
		config, v.Config = *conf, jsonConf
	} else {
		configInput, _ := confloader.LoadConfig(v.ConfigureFile)

		conf, e := core.LoadConfig("json", v.ConfigureFile, configInput)

		log.Println("core.LoadConfig found error:", e.Error())
		config = *conf
	}

	var err error
	v.status.Vpoint, err = core.New(&config)
	if err != nil {
		log.Println("VPoint Start Err:" + err.Error())
	}

	v.status.IsRunning = true
	// 默认禁用心跳
	v.hbStatus = HB_STATUS_STOPPED

	v.status.Vpoint.Start()
	// 启用心跳检测
	if (v.EnableHeartbeat) {
		go v.HeartbeatLoop()
	}

	go v.emitStatus(0, "Running")

	//osSignals := make(chan os.Signal, 1)
	//signal.Notify(osSignals, os.Interrupt, os.Kill, syscall.SIGTERM)
	//
	//<-osSignals
	//go v.StopLoop()
}

func (v *V2RayPoint) heartbeatTimeout (r *requests.Request) {
	r.Timeout = time.Duration(5) * time.Second
	// 设置自身为代理
	r.Proxy = func(_ *http.Request) (*url.URL, error) {
		var proxyUri string;
		if v.Config != nil {
			// 从配置从获取代理
			protocol := v.Config.InboundConfig.Protocol
			if protocol == "socks" {
				protocol += "5"
			}
			proxyUri = fmt.Sprintf("%s://%s:%d", protocol, v.Config.InboundConfig.Listen.String(), v.Config.InboundConfig.Port.From)
		} else {
			// 默认使用的代理配置
			proxyUri = "socks5://127.0.0.1:1089"
		}
		return url.Parse(proxyUri)
	}
	// 启用ssl
	r.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	r.Transport.Proxy = r.Proxy
	r.Transport.TLSClientConfig = r.TLSClientConfig
	// 实际起作用的代理配置
	r.Client.Transport = r.Transport
}

func (v *V2RayPoint) HeartbeatOnce() int {

	response, err := requests.Head("http://192.168.168.168/?uid="+v.UID, v.heartbeatTimeout)
	if err != nil || response.StatusCode < 200 || response.StatusCode > 299 {
		if err != nil {
			log.Println("Error:", err.Error())
		}
		if response != nil {
			log.Println("StatusCode:", response.StatusCode)
		}
		return 503
	}

	return 0
}

func (v * V2RayPoint) HeartbeatLoop() {
	// 休眠 5 秒
	time.Sleep(5 * time.Second)

	v.hbStatus = HB_STATUS_RUNNING

	go func() {
		for {
			if v.hbStatus != HB_STATUS_RUNNING {
				return
			}
			// 执行一次心跳
			v.HeartbeatOnce()
			time.Sleep(5 * time.Second)
		}
	}()

}

func (v * V2RayPoint) emitStatus(code int, message string) {
	if v.Callbacks != nil {
		v.Callbacks.OnEmitStatus(code, message)
	}
}

/**
	RunLoop 运行 V2Ray 服务，该操作是线程安全的
 */
func (v * V2RayPoint) RunLoop() {
	v.v2rayOP.Lock()
	if !v.status.IsRunning {
		go v.pointloop()
	}
	v.v2rayOP.Unlock()
}

func (v *V2RayPoint) stopLoopW() {
	v.status.IsRunning = false
	v.status.Vpoint.Close()
	v.hbStatus = HB_STATUS_STOPPED

	go v.emitStatus(0, "Closed")
}

/**
	StopLoop 停止 V2Ray 服务，该操作是线程安全的
 */
func (v *V2RayPoint) StopLoop() {
	v.v2rayOP.Lock()
	if v.status.IsRunning {
		go v.stopLoopW()
	}
	v.v2rayOP.Unlock()
}

/**
	GetIsRunning 获取 V2Ray 服务是否处于运行状态
 */
func (v *V2RayPoint) GetIsRunning() bool {
	return v.status.IsRunning
}

/**
	NewV2RayPoint 新建 V2RayPoint 的实例
 */
func NewV2RayPoint(assertPrefix string) *V2RayPoint {
	//os.Setenv("v2ray.ray.buffer.size", "1")
	debug.SetGCPercent(10)
	//os.Setenv("v2ray.buf.readv", "enable")
	if assertPrefix != "" {
		// 设置环境变量
		os.Setenv("v2ray.location.asset", assertPrefix)

		sysio.NewFileReader = func(path string) (io.ReadCloser, error) {
			if strings.HasPrefix(path, assertPrefix) {
				p := path[len(assertPrefix)+1:]
				//is it overridden?
				by, ok := overridedAssets[p]
				if ok {
					return os.Open(by)
				}
				return mobasset.Open(p)
			}
			return os.Open(path)
		}
	}

	return &V2RayPoint{ status: Status{}, v2rayOP: new(sync.Mutex), hbStatus: HB_STATUS_STOPPED }
}

/*
	NetworkInterrupted 通知我们重启 V2Ray 服务，关闭死连接
*/
func (v *V2RayPoint) NetworkInterrupted() {
	/*
		Behavior Changed in API Ver 23
		From now, we will defer the start for 3 sec,
		any Interruption Message will be surpressed during this period
	*/
	go func() {
		if v.status.IsRunning {
			//Calc sleep time
			defer func() {
				if r := recover(); r != nil {
					fmt.Println("Your device might not support atomic operation", r)
				}
			}()
			succ := atomic.CompareAndSwapInt64(&v.interuptDeferto, 0, 1)
			if succ {
				v.status.Vpoint.Close()
				time.Sleep(2 * time.Second)
				v.status.Vpoint.Start()
				atomic.StoreInt64(&v.interuptDeferto, 0)
			} else {
			}
		}
	}()
}
