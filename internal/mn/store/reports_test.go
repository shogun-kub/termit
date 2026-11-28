package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/mn/store"
)

// A replica keeps one report per node — the newest, because a report is a complete statement of what the
// node serves — and the home it came for comes from the row it describes.
func TestQueuedReportsWaitForTheHome(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home, replica := twoStores(t)

	enrolled(t, home, "uv_one", "one.example.com")
	enrolled(t, replica, "uv_local", "local.example.com")
	mnID, err := home.MeshID(ctx)
	if err != nil {
		t.Fatalf("MeshID: %v", err)
	}
	if _, err := replica.ApplyEvents(ctx, journal(t, home, mnID)); err != nil {
		t.Fatalf("ApplyEvents: %v", err)
	}

	if err := replica.QueueReport(ctx, "uv_one", testNow, []byte(`{"step":1}`)); err != nil {
		t.Fatalf("QueueReport: %v", err)
	}
	if err := replica.QueueReport(ctx, "uv_one", testNow.Add(time.Minute), []byte(`{"step":2}`)); err != nil {
		t.Fatalf("QueueReport: %v", err)
	}
	// A report about a row this server homes is not waiting for anybody: the caller drops it.
	if err := replica.QueueReport(ctx, "uv_local", testNow, []byte(`{"step":3}`)); err != nil {
		t.Fatalf("QueueReport: %v", err)
	}

	pending, err := replica.PendingReports(ctx)
	if err != nil {
		t.Fatalf("PendingReports: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("the queue holds %d reports, want 2", len(pending))
	}
	byUV := map[string]store.PendingReport{}
	for _, report := range pending {
		byUV[report.UV] = report
	}
	foreign := byUV["uv_one"]
	if foreign.Home != mnID || string(foreign.Payload) != `{"step":2}` {
		t.Errorf("the foreign report reads %+v, want uv_one at %s with the newest payload", foreign, mnID)
	}
	// The row this server homes names this server, so the queue says where the report belongs; the puller
	// drops it for that reason instead of sending it to itself.
	localID, err := replica.MeshID(ctx)
	if err != nil {
		t.Fatalf("MeshID: %v", err)
	}
	if local := byUV["uv_local"]; local.Home != localID {
		t.Errorf("the local report reads %+v, want home %s", local, localID)
	}

	if err := replica.DropReport(ctx, "uv_one"); err != nil {
		t.Fatalf("DropReport: %v", err)
	}
	if pending, err = replica.PendingReports(ctx); err != nil || len(pending) != 1 {
		t.Fatalf("after the drop the queue = %+v, %v; want one left", pending, err)
	}
}

// A report whose node row is gone has nobody to go to: it must read as empty rather than as a home.
func TestAReportForAGoneNodeHasNoHome(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, replica := twoStores(t)
	if err := replica.QueueReport(ctx, "uv_forgotten", testNow, []byte(`{}`)); err != nil {
		t.Fatalf("QueueReport: %v", err)
	}
	pending, err := replica.PendingReports(ctx)
	if err != nil {
		t.Fatalf("PendingReports: %v", err)
	}
	if len(pending) != 1 || pending[0].Home != "" || pending[0].UV != "uv_forgotten" {
		t.Fatalf("the queue reads %+v, want one report with no home", pending)
	}
}
