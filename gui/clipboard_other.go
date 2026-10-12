//go:build !windows

package gui

import "fmt"

func copyTextDefault(s string) error {
	_ = s
	return fmt.Errorf("当前平台不支持从托盘复制到系统剪贴板")
}
