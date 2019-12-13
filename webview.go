package blink

//#include "webview.h"
import "C"
import (
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	"github.com/CHH/eventemitter"
	"github.com/lxn/win"
)

type WebView struct {
	eventemitter.EventEmitter

	window         C.wkeWebView
	handle         win.HWND
	origWndProcPtr uintptr

	autoTitle bool
	jsFunc    map[string]interface{}
	jsData    map[string]string

	//事件channels
	DocumentReady chan interface{} //文档ready
	Destroy       chan interface{} //webview销毁

	isDestroy     bool
	hideOnClosing bool

	dropFiles bool
}

func defaultWndProc(hwnd win.HWND, msg uint32, wParam, lParam uintptr) (result uintptr) {
	view := getWebViewByHandle(hwnd)
	if view == nil {
		return win.DefWindowProc(hwnd, msg, wParam, lParam)
	}
	if view.origWndProcPtr != 0 {
		result = view.wndProc(hwnd, msg, wParam, lParam)
		if result != 0 {
			result = win.CallWindowProc(view.origWndProcPtr, hwnd, msg, wParam, lParam)
		}
		return result
	}
	return win.DefWindowProc(hwnd, msg, wParam, lParam)
}

func NewWebView(isTransparent, hideOnClosing bool, bounds ...int) *WebView {
	view := &WebView{
		autoTitle:     true,
		jsFunc:        make(map[string]interface{}),
		jsData:        make(map[string]string),
		DocumentReady: make(chan interface{}),
		Destroy:       make(chan interface{}),
		isDestroy:     false,
		hideOnClosing: hideOnClosing,
		dropFiles:     true,
	}
	//初始化event emitter
	view.Init()

	width, height, x, y := 800, 600, 200, 200

	if len(bounds) >= 2 {
		width = bounds[0]
		height = bounds[1]
	}

	if len(bounds) >= 4 {
		x = bounds[2]
		y = bounds[3]
	}

	done := make(chan bool)
	queueJob(func() {
		view.window = C.createWebWindow(C.bool(isTransparent), C.int(x), C.int(y), C.int(width), C.int(height))
		view.handle = win.HWND(uintptr(unsafe.Pointer(C.getWindowHandle(view.window))))
		if view.hideOnClosing {
			view.origWndProcPtr = win.SetWindowLongPtr(view.handle, win.GWLP_WNDPROC, defaultWndProcPtr)
		}
		close(done)
	})
	<-done

	//初始化各种事件
	//destroy的时候需要设置标志位
	view.On("destroy", func(v *WebView) {
		//关闭destroy,如果已经关闭了,则无需关闭
		select {
		case <-v.Destroy:
			break
		default:
			close(v.Destroy)
		}
		v.isDestroy = true
	})
	view.On("documentReady", func(v *WebView) {
		select {
		case <-v.DocumentReady:
			break
		default:
			close(v.DocumentReady)
		}
	})
	//同步网页标题到窗口
	view.On("titleChanged", func(view *WebView, title string) {
		if view.autoTitle {
			view.SetWindowTitle(title)
		}
	})

	//注入预置的API给js调用
	view.Inject("MoveToCenter", view.MoveToCenter)
	view.Inject("SetWindowTitle", view.SetWindowTitle)
	view.Inject("EnableAutoTitle", view.EnableAutoTitle)
	view.Inject("DisableAutoTitle", view.DisableAutoTitle)
	view.Inject("ShowDockIcon", view.ShowDockIcon)
	view.Inject("HideDockIcon", view.HideDockIcon)
	view.Inject("ShowWindow", view.ShowWindow)
	view.Inject("HideWindow", view.HideWindow)
	view.Inject("ShowDevTools", view.ShowDevTools)
	view.Inject("ToTop", view.ToTop)
	view.Inject("MostTop", view.MostTop)
	view.Inject("MinimizeWindow", view.MinimizeWindow)
	view.Inject("MaximizeWindow", view.MaximizeWindow)
	view.Inject("RestoreWindow", view.RestoreWindow)
	view.Inject("DestroyWindow", view.DestroyWindow)

	//把webview添加到池中
	addViewToPool(view)
	return view
}

// 0 means process, and has no next
// 1 means process next
func (view *WebView) wndProc(hwnd win.HWND, msg uint32, wParam, lParam uintptr) (result uintptr) {
	if msg == win.WM_CLOSE {
		if !view.isDestroy && view.hideOnClosing {
			view.HideWindow()
		}
		return 0
	}

	return 1
}

func (view *WebView) processMessage(msg *win.MSG) bool {
	//TODO:临时监听一波键盘事件,并直接处理了,以后要分发到标准的事件中去的
	if msg.Message == win.WM_KEYDOWN {
		switch msg.WParam {
		case 0x74: //F5
			go view.Reload()
			break
		case 0x7b: //F12
			if isDebug {
				go view.ShowDevTools()
				break
			}
		}
	} else if msg.Message == win.WM_DROPFILES {
		if view.dropFiles {
			return view.processDropFiles(win.HDROP(msg.WParam))
		} else {
			return false
		}
	} else if msg.Message == win.WM_CLOSE {
		if !view.isDestroy && view.hideOnClosing {
			view.HideWindow()
			return false
		}
	}

	return true
}

func (view *WebView) processDropFiles(hDrop win.HDROP) bool {
	var files []string
	n := win.DragQueryFile(hDrop, 0xFFFFFFFF, nil, 0)
	for i := 0; i < int(n); i++ {
		bufSize := uint(512)
		buf := make([]uint16, bufSize)
		if win.DragQueryFile(hDrop, uint(i), &buf[0], bufSize) > 0 {
			files = append(files, syscall.UTF16ToString(buf))
		}
	}
	win.DragFinish(hDrop)

	if len(files) > 0 {
		//如果事件不存在，并且dropFiles为true，则交给mb处理
		return view.Emit("dropFiles", view, files) == nil
	} else {
		return true
	}
}

func (view *WebView) MoveToCenter() {
	var width int32 = 0
	var height int32 = 0
	{
		rect := &win.RECT{}
		win.GetWindowRect(view.handle, rect)
		width = rect.Right - rect.Left
		height = rect.Bottom - rect.Top
	}

	var parentWidth int32 = 0
	var parentHeight int32 = 0
	if win.GetWindowLong(view.handle, win.GWL_STYLE) == win.WS_CHILD {
		parent := win.GetParent(view.handle)
		rect := &win.RECT{}
		win.GetClientRect(parent, rect)
		parentWidth = rect.Right - rect.Left
		parentHeight = rect.Bottom - rect.Top
	} else {
		parentWidth = win.GetSystemMetrics(win.SM_CXSCREEN)
		parentHeight = win.GetSystemMetrics(win.SM_CYSCREEN)
	}

	x := (parentWidth - width) / 2
	y := (parentHeight - height) / 2

	win.MoveWindow(view.handle, x, y, width, height, false)
}

func (view *WebView) SetWindowTitle(title string) {
	done := make(chan bool)
	queueJob(func() {
		C.setWindowTitle(view.window, C.CString(title))
		close(done)
	})
	<-done
}

func (view *WebView) EnableAutoTitle() {
	view.autoTitle = true
	view.SetWindowTitle(view.GetWebTitle())
}

func (view *WebView) DisableAutoTitle() {
	view.autoTitle = false
}

func (view *WebView) GetWebTitle() string {
	//等待document ready,文档没有ready,网页的标题获取不到
	<-view.DocumentReady

	done := make(chan string)
	queueJob(func() {
		done <- C.GoString(C.getWebTitle(view.window))
		close(done)
	})
	return <-done
}

func (view *WebView) LoadURL(url string) {
	done := make(chan bool)
	queueJob(func() {
		C.loadURL(view.window, C.CString(url))
		close(done)
	})
	<-done
}

func (view *WebView) ShowCaption() {
	style := win.GetWindowLongPtr(view.handle, win.GWL_STYLE)
	win.SetWindowLongPtr(view.handle, win.GWL_STYLE, style|win.WS_CAPTION|win.WS_SYSMENU|win.WS_SIZEBOX)
}

func (view *WebView) HideCaption() {
	style := win.GetWindowLongPtr(view.handle, win.GWL_STYLE)
	win.SetWindowLongPtr(view.handle, win.GWL_STYLE, style&^win.WS_CAPTION&^win.WS_SYSMENU&^win.WS_SIZEBOX)
}

func (view *WebView) ShowDockIcon() {
	style := win.GetWindowLong(view.handle, win.GWL_EXSTYLE)
	win.SetWindowLong(view.handle, win.GWL_EXSTYLE, style&^win.WS_EX_TOOLWINDOW)
}

func (view *WebView) HideDockIcon() {
	style := win.GetWindowLong(view.handle, win.GWL_EXSTYLE)
	win.SetWindowLong(view.handle, win.GWL_EXSTYLE, style|win.WS_EX_TOOLWINDOW)

}

func (view *WebView) ShowWindow() {
	win.ShowWindow(view.handle, win.SW_SHOW)
}

func (view *WebView) HideWindow() {
	win.ShowWindow(view.handle, win.SW_HIDE)
}

func (view *WebView) ShowDevTools() {
	done := make(chan bool)
	queueJob(func() {
		C.showDevTools(view.window)
		close(done)
	})
	<-done
}

func (view *WebView) Reload() {
	done := make(chan bool)
	queueJob(func() {
		C.reloadURL(view.window)
		close(done)
	})
	<-done
}

func (view *WebView) ToTop() {
	rect := &win.RECT{}
	win.GetWindowRect(view.handle, rect)
	win.SetWindowPos(view.handle, win.HWND_TOP, rect.Left, rect.Top, rect.Right-rect.Left, rect.Bottom-rect.Top, 0)
}

func (view *WebView) MostTop(isTop bool) {
	rect := &win.RECT{}
	win.GetWindowRect(view.handle, rect)
	if isTop {
		win.SetWindowPos(view.handle, win.HWND_TOPMOST, rect.Left, rect.Top, rect.Right-rect.Left, rect.Bottom-rect.Top, 0)
	} else {
		win.SetWindowPos(view.handle, win.HWND_NOTOPMOST, rect.Left, rect.Top, rect.Right-rect.Left, rect.Bottom-rect.Top, 0)
	}
}

func (view *WebView) MaximizeWindow() {
	win.ShowWindow(view.handle, win.SW_MAXIMIZE)
}

func (view *WebView) MinimizeWindow() {
	win.ShowWindow(view.handle, win.SW_MINIMIZE)
}

func (view *WebView) RestoreWindow() {
	win.ShowWindow(view.handle, win.SW_RESTORE)
}

func (view *WebView) DestroyWindow() {
	if !view.isDestroy {
		done := make(chan bool)
		queueJob(func() {
			//关闭destroy,如果已经关闭了,则无需关闭
			select {
			case <-view.Destroy:
				break
			default:
				close(view.Destroy)
			}
			view.isDestroy = true
			view.hideOnClosing = false
			C.destroyWindow(view.window)
			close(done)
		})
		<-done
	}
}

func (view *WebView) GetHandle() win.HWND {
	return view.handle
}

func (view *WebView) SetWindowIcon(s string) {
	done := make(chan bool)
	queueJob(func() {
		defer close(done)

		buff, err := GetNetFSData(s)
		if err != nil {
			logger.Println(err)
			return
		}
		icoPath := filepath.Join(TempPath, "rc.ico")
		fd, err := os.OpenFile(icoPath, os.O_RDWR|os.O_CREATE, 0755)
		if err != nil {
			logger.Printf("读取资源(%s)失败：%s\n", s, err.Error())
			return
		}
		fd.Write(buff)
		fd.Close()

		hIcon := win.HICON(win.LoadImage(
			0,
			syscall.StringToUTF16Ptr(icoPath),
			win.IMAGE_ICON,
			0,
			0,
			win.LR_DEFAULTSIZE|win.LR_LOADFROMFILE))
		if hIcon != 0 {
			win.SendMessage(view.handle, win.WM_SETICON, 0, uintptr(hIcon))
			win.SendMessage(view.handle, win.WM_SETICON, 1, uintptr(hIcon))
		} else {
			logger.Printf("装载资源(%s)失败！\n", s)
		}
	})
	<-done
}

func (view *WebView) SetWindowIconFromPath(icoPath string) {
	done := make(chan struct{})
	queueJob(func() {
		defer close(done)
		hIcon := win.HICON(win.LoadImage(
			0,
			syscall.StringToUTF16Ptr(icoPath),
			win.IMAGE_ICON,
			0,
			0,
			win.LR_DEFAULTSIZE|win.LR_LOADFROMFILE))
		if hIcon != 0 {
			win.SendMessage(view.handle, win.WM_SETICON, 0, uintptr(hIcon))
			win.SendMessage(view.handle, win.WM_SETICON, 1, uintptr(hIcon))
		} else {
			logger.Printf("装载资源(%s)失败！\n", icoPath)
		}
	})
	<-done
}

func (view *WebView) SetEnabledDropFiles(value bool) {
	view.dropFiles = value
	win.DragAcceptFiles(view.GetHandle(), value)
}
