package main

import (
	"testing"

	"shade/internal/apps/cluster"
)

func TestSplitCombinedTasksPairsTreeAndCrossing(t *testing.T) {
	tasks := []combinedTask{
		{teamName: "A", app: cluster.AppTreeSync},
		{teamName: "B", app: cluster.AppTreeSync},
		{teamName: "A", app: cluster.AppCrossingSync},
	}
	batches := splitCombinedTasks(tasks)
	if len(batches) != 2 {
		t.Fatalf("应配对 1 组 + 1 个单任务，got %d", len(batches))
	}
	if len(batches[0]) != 2 || batches[0][0].app != cluster.AppTreeSync || batches[0][1].app != cluster.AppCrossingSync {
		t.Fatalf("首组应为树障+交跨配对: %+v", batches[0])
	}
	if len(batches[1]) != 1 || batches[1][0].teamName != "B" {
		t.Fatalf("剩余树障任务应单独一批: %+v", batches[1])
	}
}

func TestSplitCombinedTasksCrossingOnly(t *testing.T) {
	tasks := []combinedTask{
		{teamName: "A", app: cluster.AppCrossingSync},
		{teamName: "B", app: cluster.AppCrossingSync},
	}
	batches := splitCombinedTasks(tasks)
	if len(batches) != 2 {
		t.Fatalf("无树障时应各自单批，got %d", len(batches))
	}
}
