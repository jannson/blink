package blink

//#include "blink.h"
import "C"

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	assetfs "github.com/elazarl/go-bindata-assetfs"
	"github.com/lxn/win"
	"github.com/raintean/blink/internal/devtools"
	"github.com/raintean/blink/internal/dll"
)

//任务队列,保证所有的API调用都在痛一个线程
var jobQueue = make(chan func())
var jobQueueFn func(f func())

func saveDll() (string, error) {
	//定义dll的路径
	dllPath := filepath.Join(TempPath, "blink_"+runtime.GOARCH+".dll")

	//准备释放dll到临时目录
	err := os.MkdirAll(TempPath, 0644)
	if err != nil {
		return "", fmt.Errorf("无法创建临时目录：%s, err: %s", TempPath, err)
	}
	data, err := dll.Asset("blink.dll")
	if err != nil {
		return "", fmt.Errorf("找不到内嵌dll,err: %s", err)
	}
	err = func() error {
		file, err := os.Create(dllPath)
		defer file.Close()
		if err != nil {
			return fmt.Errorf("无法创建dll文件,err: %s", err)
		}
		n, err := file.Write(data)
		if err != nil {
			return fmt.Errorf("无法写入dll文件,err: %s", err)
		}
		if len(data) != n {
			return fmt.Errorf("写入校验失败")
		}
		return nil
	}()
	return dllPath, err
}

//初始化blink,释放并加载dll,启动调用队列
func InitBlink() error {
	dllPath, err := saveDll()
	if err != nil {
		return err
	}

	//启动一个新的协程来处理blink的API调用
	go func() {
		//将这个协程锁在当前的线程上
		runtime.LockOSThread()

		//初始化
		C.initBlink(
			C.CString(dllPath),
			C.CString(TempPath),
			C.CString(filepath.Join(TempPath, "cookie.dat")),
		)

		//注册DevTools工具到虚拟文件系统
		RegisterFileSystem("__devtools__", &assetfs.AssetFS{
			Asset:     devtools.Asset,
			AssetDir:  devtools.AssetDir,
			AssetInfo: devtools.AssetInfo,
		})

		//消费API调用,同时处理好windows消息
		for {
			select {
			case job := <-jobQueue:
				job()
			default:
				//消息循环
				msg := &win.MSG{}
				if win.GetMessage(msg, 0, 0, 0) != 0 {
					win.TranslateMessage(msg)
					//是否传递下去
					next := true
					//拿到对应的webview
					view := getWebViewByHandle(msg.HWnd)
					if view != nil {
						next = view.processMessage(msg)
					}
					if next {
						win.DispatchMessage(msg)
					}
				}
			}
		}
	}()

	logger.Println("blink初始化完毕")

	return nil
}

func PreInitBlink(fn func(f func())) error {
	jobQueueFn = fn
	dllPath, err := saveDll()
	if err != nil {
		return err
	}

	C.initBlink(
		C.CString(dllPath),
		C.CString(TempPath),
		C.CString(filepath.Join(TempPath, "cookie.dat")),
	)

	RegisterFileSystem("__devtools__", &assetfs.AssetFS{
		Asset:     devtools.Asset,
		AssetDir:  devtools.AssetDir,
		AssetInfo: devtools.AssetInfo,
	})

	return nil
}

func DispatchBlinkMessage(msg *win.MSG) bool {
	next := true
	//拿到对应的webview
	view := getWebViewByHandle(msg.HWnd)
	if view != nil {
		next = view.processMessage(msg)
	}

	return next
}

func queueJob(f func()) {
	if jobQueueFn != nil {
		jobQueueFn(f)
	} else {
		jobQueue <- f
	}
}
