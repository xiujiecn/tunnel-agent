package gui

import (
	"fmt"
	"os/exec"
	"runtime"
)

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		if err := exec.Command("xdg-open", url).Start(); err != nil {
			return fmt.Errorf("无法打开浏览器，请手动访问 %s", url)
		}
		return nil
	}
}
