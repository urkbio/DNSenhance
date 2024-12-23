package tray

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sync"

	"github.com/getlantern/systray"
)

var (
	exitChan chan struct{}
	exitOnce sync.Once
)

func init() {
	exitChan = make(chan struct{})
}

func InitSysTray() {
	systray.Run(onReady, onExit)
}

func onReady() {
	// 设置图标
	systray.SetIcon(getIcon())
	systray.SetTitle("DNSenhance")
	systray.SetTooltip("DNS服务器增强版")

	// 添加菜单项
	mStatus := systray.AddMenuItem("运行中", "服务状态")
	mStatus.Disable()

	systray.AddSeparator()

	mOpenWeb := systray.AddMenuItem("打开管理页面", "打开Web管理界面")
	mRestart := systray.AddMenuItem("重启服务", "重启DNS服务")

	systray.AddSeparator()

	mQuit := systray.AddMenuItem("退出", "退出程序")

	// 处理菜单点击事件
	go func() {
		for {
			select {
			case <-mOpenWeb.ClickedCh:
				openBrowser("http://localhost:8080")
			case <-mRestart.ClickedCh:
				// TODO: 实现重启功能
			case <-mQuit.ClickedCh:
				fmt.Println("正在退出...")
				exitOnce.Do(func() {
					close(exitChan)
				})
				systray.Quit()
				return
			}
		}
	}()
}

func onExit() {
	exitOnce.Do(func() {
		close(exitChan)
	})
}

func openBrowser(url string) {
	var err error
	switch runtime.GOOS {
	case "windows":
		err = exec.Command("cmd", "/c", "start", url).Start()
	case "darwin":
		err = exec.Command("open", url).Start()
	default: // linux
		err = exec.Command("xdg-open", url).Start()
	}
	if err != nil {
		fmt.Printf("Failed to open browser: %v\n", err)
	}
}

func getIcon() []byte {
	// 从文件读取图标数据
	b, err := os.ReadFile("web/static/images/icon.ico")
	if err != nil {
		fmt.Printf("Failed to load icon: %v\n", err)
		return nil
	}
	return b
}

func GetExitChan() chan struct{} {
	return exitChan
}
