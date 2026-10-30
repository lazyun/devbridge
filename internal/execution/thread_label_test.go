package execution

import (
	"github.com/lazyun/devbridge/internal/run"
	"testing"
)

func TestExecutorThreadLabelUsesEngineeringContext(t *testing.T) {
	if got := executorThreadLabel("节点发现", run.PurposeImplementation); got != "Dev Bridge · 实施 · 节点发现" {
		t.Fatal(got)
	}
	if got := executorThreadLabel("", run.PurposeVerification); got != "Dev Bridge · 验证 · 工程任务" {
		t.Fatal(got)
	}
}
