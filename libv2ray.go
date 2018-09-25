package libv2ray

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"v2ray.com/core/main/confloader"
	"v2ray.com/ext/sysio"

	"v2ray.com/core"
	_ "v2ray.com/core/main/distro/all"

	v2rayconf "v2ray.com/ext/tools/conf/serial"
	mobasset "golang.org/x/mobile/asset"
)

type V2RayPoint struct {
	status				Status
	Callbacks			V2RayCallbacks
	v2rayOP         	*sync.Mutex
	interuptDeferto		int64

	ConfigureFile		string
	ConfigureContent	string
}

/*V2RayCallbacks a Callback set for V2Ray
 */
type V2RayCallbacks interface {
	OnEmitStatus(int, string) int
}

func PrintVersion() {
	version := core.VersionStatement()
	for _, s := range version {
		fmt.Sprintln(s)
	}
}

func Version() string {
	return core.Version()
}

func createV2Ray(configFile string) (*core.Instance, error) {
	configInput, err := confloader.LoadConfig(configFile)
	if err != nil {
		return nil, newError("failed to load config: ", configFile).Base(err)
	}
	defer configInput.Close()

	config, err := core.LoadConfig("json", configFile, configInput)
	if err != nil {
		return nil, newError("failed to read config file: ", configFile).Base(err)
	}

	server, err := core.New(config)
	if err != nil {
		return nil, newError("failed to create server").Base(err)
	}

	return server, nil
}

//func Start(configFile string) {
//	var err error
//	if (server == nil) {
//		// create a new server
//		server, err = createV2Ray(configFile)
//		if err != nil {
//			fmt.Sprintln(err.Error())
//			os.Exit(23)
//		}
//	}
//
//	if err := server.Start(); err != nil {
//		fmt.Sprintln("Failed to start", err)
//		os.Exit(-1)
//	}
//
//	osSignals := make(chan os.Signal, 1)
//	signal.Notify(osSignals, os.Interrupt, os.Kill, syscall.SIGTERM)
//
//	<-osSignals
//	server.Close()
//}

func (v *V2RayPoint) pointloop() {
	var config core.Config
	log.Println("ConfigureFile:" + v.ConfigureFile)
	if v.ConfigureFile == "json" {
		conf, _ := v2rayconf.LoadJSONConfig(strings.NewReader(v.ConfigureContent))
		config = *conf
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
	v.status.Vpoint.Start()

	go v.emitStatus(0, "Running")
}

func (v * V2RayPoint) emitStatus(code int, message string) {
	if v.Callbacks != nil {
		v.Callbacks.OnEmitStatus(code, message)
	}
}

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

	v.Callbacks.OnEmitStatus(0, "Closed")
}

func (v *V2RayPoint) StopLoop() {
	v.v2rayOP.Lock()
	if v.status.IsRunning {
		go v.stopLoopW()
	}
	v.v2rayOP.Unlock()
}

func (v *V2RayPoint) GetIsRunning() bool {
	return v.status.IsRunning
}

func NewV2RayPoint(assertPrefix string) *V2RayPoint {
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

	return &V2RayPoint{ status: Status{}, v2rayOP: new(sync.Mutex) }
}

/*NetworkInterrupted inform us to restart the v2ray,
closing dead connections.
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
