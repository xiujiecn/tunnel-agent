package lib

// WantGUI 判定是否进入图形控制台。
//
// ★ 规则（与 docs/计划-tunnel-agent-图形界面.md 一致）：
//   - 显式 -gui ⇒ GUI
//   - -server 与 -token 皆空 ⇒ GUI（双击 / 无参数）
//   - 二者齐全 ⇒ CLI
//   - 只填其中一个 ⇒ CLI（由 flag 校验失败，避免半套参数被 GUI 吞掉）
func WantGUI(forceGUI bool, serverURL, token string) bool {
	if forceGUI {
		return true
	}
	return serverURL == "" && token == ""
}
