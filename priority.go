package assetcrawler

// orderTasks 在每一层内做稳定分区：静态资源与常规链接在前，
// 「可以晚一点抓」的页面类任务在后。
//
// 背景：一个 Nuxt 首页的 __NUXT_DATA__ 能给出 200 条站内文档路径。若它们
// 与同层 JS/CSS 一起抢 MaxURLs 配额，实测 JS 提取结果从 62 条掉到 16 条——
// 新增能力反而挤掉了原有召回。sitemap 当年是同类问题，当时的解法是
// 「引导任务延后并入」（bootstrap.go）；这里用「同层内稳定分区」解决。
//
// 只分区、不排序：workers 并发汇总出的顺序本身不具语义，稳定分区足够，
// 也不会改变既有任务的相对次序。
func orderTasks(in []task) []task {
	if len(in) < 2 {
		return in
	}
	out := make([]task, 0, len(in))
	deferred := make([]task, 0, len(in)/2)
	for _, t := range in {
		if t.deferPage {
			deferred = append(deferred, t)
			continue
		}
		out = append(out, t)
	}
	return append(out, deferred...)
}
