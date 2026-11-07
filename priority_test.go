package assetcrawler

import (
	"net/url"
	"testing"

	"assetcrawler/extractor"
)

func priorityTask(p, source string, deferred bool) task {
	return task{url: &url.URL{Path: p}, source: source, deferPage: deferred}
}

func TestOrderTasksKeepsAssetsFirst(t *testing.T) {
	in := []task{
		priorityTask("/app.js", "script", false),
		priorityTask("/docs/1", "inline-json:nuxt-data", true),
		priorityTask("/style.css", "link", false),
		priorityTask("/docs/2", "inline-json:nuxt-data", true),
	}
	got := orderTasks(in)

	want := []string{"/app.js", "/style.css", "/docs/1", "/docs/2"}
	if len(got) != len(want) {
		t.Fatalf("长度 %d，期望 %d", len(got), len(want))
	}
	for i, p := range want {
		if got[i].url.Path != p {
			t.Errorf("位置 %d = %s，期望 %s（实际 %v）", i, got[i].url.Path, p, got)
		}
	}
}

func TestOrderTasksWithoutDeferredKeepsOrder(t *testing.T) {
	in := []task{
		priorityTask("/a.js", "script", false),
		priorityTask("/b.css", "link", false),
	}
	got := orderTasks(in)
	if got[0].url.Path != "/a.js" || got[1].url.Path != "/b.css" {
		t.Errorf("顺序被改变: %v", got)
	}
}

func TestOrderTasksEmpty(t *testing.T) {
	if got := orderTasks(nil); len(got) != 0 {
		t.Errorf("nil 输入应返回空，实际 %v", got)
	}
}

// TestToTasksDefersInlineJSON 锁定「提取层 → 调度层」的接线，
// 防止新增载体只改了 extractor 却忘了标记优先级。
func TestToTasksDefersInlineJSON(t *testing.T) {
	c, err := New(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse("https://example.com/")
	c.frontier.AddSeed(base)
	tasks := c.toTasks([]extractor.Request{
		{Raw: "/app.js", Source: "script", Method: "GET"},
		{Raw: "/docs/1", Source: "inline-json:nuxt-data", Method: "GET"},
	}, base, 1, "https://example.com/")
	if len(tasks) != 2 {
		t.Fatalf("期望 2 个任务，实际 %d", len(tasks))
	}
	if tasks[0].deferPage || !tasks[1].deferPage {
		t.Errorf("deferPage 标记错误: %+v / %+v", tasks[0], tasks[1])
	}
	if got := orderTasks(tasks); got[0].url.Path != "/app.js" {
		t.Errorf("资源应排在页面之前，实际 %s", got[0].url.Path)
	}
}
