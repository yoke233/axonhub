package gc

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zhenzou/executors"

	"github.com/looplj/axonhub/internal/authz"
	"github.com/looplj/axonhub/internal/ent"
	"github.com/looplj/axonhub/internal/ent/channelprobe"
	"github.com/looplj/axonhub/internal/ent/datastorage"
	"github.com/looplj/axonhub/internal/ent/enttest"
	"github.com/looplj/axonhub/internal/ent/schema/schematype"
	"github.com/looplj/axonhub/internal/ent/thread"
	"github.com/looplj/axonhub/internal/ent/trace"
	"github.com/looplj/axonhub/internal/ent/usagelog"
	"github.com/looplj/axonhub/internal/objects"
	"github.com/looplj/axonhub/internal/pkg/xcache"
	"github.com/looplj/axonhub/internal/server/biz"
)

func TestWorker_getBatchSize(t *testing.T) {
	worker := &Worker{
		Ent:    nil,
		Config: Config{CRON: "0 0 * * *"},
	}

	// Test default batch size
	batchSize := worker.getBatchSize()
	if batchSize != defaultBatchSize {
		t.Errorf("Expected batch size %d, got %d", defaultBatchSize, batchSize)
	}

	// Test with overridden batch size
	originalBatchSize := defaultBatchSize
	defaultBatchSize = 20

	defer func() { defaultBatchSize = originalBatchSize }()

	batchSize = worker.getBatchSize()
	if batchSize != 20 {
		t.Errorf("Expected batch size 20, got %d", batchSize)
	}

	worker.Config.BatchSize = 7
	batchSize = worker.getBatchSize()
	if batchSize != 7 {
		t.Errorf("Expected configured batch size 7, got %d", batchSize)
	}
}

func TestWorker_getBatchThrottle(t *testing.T) {
	worker := &Worker{
		Ent:    nil,
		Config: Config{CRON: "0 0 * * *"},
	}

	batchThrottle := worker.getBatchThrottle()
	if batchThrottle != defaultBatchThrottle {
		t.Errorf("Expected batch throttle %s, got %s", defaultBatchThrottle, batchThrottle)
	}

	worker.Config.BatchThrottle = 25 * time.Millisecond
	batchThrottle = worker.getBatchThrottle()
	if batchThrottle != 25*time.Millisecond {
		t.Errorf("Expected configured batch throttle 25ms, got %s", batchThrottle)
	}
}

func TestWorker_cleanupRequestExternalStorageDeletesFsArtifacts(t *testing.T) {
	worker, ctx, dataStorage, baseDir := setupWorkerWithFSStorage(t)

	req := &ent.Request{
		ID:            101,
		ProjectID:     202,
		DataStorageID: dataStorage.ID,
	}

	fileKeys := []string{
		biz.GenerateRequestBodyKey(req.ProjectID, req.ID),
		biz.GenerateResponseBodyKey(req.ProjectID, req.ID),
		biz.GenerateResponseChunksKey(req.ProjectID, req.ID),
	}

	dirKeys := []string{
		biz.GenerateRequestExecutionsDirKey(req.ProjectID, req.ID),
		biz.GenerateRequestDirKey(req.ProjectID, req.ID),
	}

	for _, key := range fileKeys {
		createFileForKey(t, baseDir, key)
	}

	for _, key := range dirKeys {
		createDirForKey(t, baseDir, key)
	}

	worker.cleanupRequestExternalStorage(ctx, req, make(map[int]*ent.DataStorage))

	for _, key := range append(fileKeys, dirKeys...) {
		assertRemoved(t, baseDir, key)
	}
}

func TestWorker_cleanupExecutionExternalStorageDeletesFsArtifacts(t *testing.T) {
	worker, ctx, dataStorage, baseDir := setupWorkerWithFSStorage(t)

	req := &ent.Request{
		ID:            303,
		ProjectID:     404,
		DataStorageID: dataStorage.ID,
	}

	exec := &ent.RequestExecution{
		ID:            505,
		RequestID:     req.ID,
		ProjectID:     req.ProjectID,
		DataStorageID: dataStorage.ID,
	}

	fileKeys := []string{
		biz.GenerateExecutionRequestBodyKey(exec.ProjectID, exec.RequestID, exec.ID),
		biz.GenerateExecutionResponseBodyKey(exec.ProjectID, exec.RequestID, exec.ID),
		biz.GenerateExecutionResponseChunksKey(exec.ProjectID, exec.RequestID, exec.ID),
	}

	dirKeys := []string{
		biz.GenerateExecutionRequestDirKey(exec.ProjectID, exec.RequestID, exec.ID),
	}

	for _, key := range fileKeys {
		createFileForKey(t, baseDir, key)
	}

	for _, key := range dirKeys {
		createDirForKey(t, baseDir, key)
	}

	worker.cleanupExecutionExternalStorage(ctx, exec, make(map[int]*ent.DataStorage))

	for _, key := range append(fileKeys, dirKeys...) {
		assertRemoved(t, baseDir, key)
	}
}

func setupWorkerWithFSStorage(t *testing.T) (*Worker, context.Context, *ent.DataStorage, string) {
	t.Helper()

	cacheConfig := xcache.Config{
		Mode: xcache.ModeMemory,
		Memory: xcache.MemoryConfig{
			Expiration:      5 * time.Minute,
			CleanupInterval: 10 * time.Minute,
		},
	}

	client := enttest.NewEntClient(t, "sqlite3", "file:ent?mode=memory&_fk=1")

	executor := executors.NewPoolScheduleExecutor(executors.WithMaxConcurrent(1))

	t.Cleanup(func() {
		_ = executor.Shutdown(context.Background())

		client.Close()
	})

	systemService := biz.NewSystemService(biz.SystemServiceParams{CacheConfig: cacheConfig})
	dataStorageService := biz.NewDataStorageService(biz.DataStorageServiceParams{
		SystemService: systemService,
		CacheConfig:   cacheConfig,
		Client:        client,
	})

	ctx := context.Background()
	ctx = ent.NewContext(ctx, client)
	ctx = authz.WithTestBypass(ctx)

	dir := t.TempDir()
	dirCopy := dir
	settings := &objects.DataStorageSettings{Directory: &dirCopy}

	dataStorage, err := client.DataStorage.Create().
		SetName("fs-storage").
		SetDescription("test fs storage").
		SetPrimary(false).
		SetType(datastorage.TypeFs).
		SetSettings(settings).
		SetStatus(datastorage.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	worker := &Worker{
		DataStorageService: dataStorageService,
		Ent:                client,
	}

	return worker, ctx, dataStorage, dir
}

func createFileForKey(t *testing.T, baseDir, key string) {
	t.Helper()

	path := pathForKey(baseDir, key)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("test"), 0o644))
}

func createDirForKey(t *testing.T, baseDir, key string) {
	t.Helper()

	path := pathForKey(baseDir, key)
	require.NoError(t, os.MkdirAll(path, 0o755))
}

func assertRemoved(t *testing.T, baseDir, key string) {
	t.Helper()

	path := pathForKey(baseDir, key)
	_, err := os.Stat(path)
	require.ErrorIs(t, err, fs.ErrNotExist, "expected %s to be removed", key)
}

func pathForKey(baseDir, key string) string {
	rel := strings.TrimPrefix(key, "/")
	return filepath.Join(baseDir, filepath.FromSlash(rel))
}

func TestWorker_deleteIDsInBatches(t *testing.T) {
	worker := &Worker{
		Ent:    nil,
		Config: Config{CRON: "0 0 * * *", BatchThrottle: time.Nanosecond},
	}

	queryCount := 0
	deleteCount := 0
	queryIDs := func(ctx context.Context, batchSize int) ([]int, error) {
		queryCount++
		if batchSize != defaultBatchSize {
			t.Fatalf("Expected batch size %d, got %d", defaultBatchSize, batchSize)
		}

		switch queryCount {
		case 1:
			return []int{1, 2, 3}, nil
		case 2:
			return []int{4, 5}, nil
		default:
			return nil, nil
		}
	}
	deleteIDs := func(ctx context.Context, ids []int) (int, error) {
		deleteCount++
		return len(ids), nil
	}

	deleted, err := worker.deleteIDsInBatches(context.Background(), "test_records", queryIDs, deleteIDs)
	if err != nil {
		t.Fatalf("deleteIDsInBatches failed: %v", err)
	}

	if deleted != 5 {
		t.Errorf("Expected to delete 5 records total, got %d", deleted)
	}

	if queryCount != 3 {
		t.Errorf("Expected 3 query calls, got %d", queryCount)
	}

	if deleteCount != 2 {
		t.Errorf("Expected 2 delete calls, got %d", deleteCount)
	}
}

func TestWorker_deleteIDsInBatchesStopsOnContextCancel(t *testing.T) {
	worker := &Worker{
		Ent:    nil,
		Config: Config{CRON: "0 0 * * *", BatchThrottle: time.Nanosecond},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	queryCount := 0
	queryIDs := func(ctx context.Context, batchSize int) ([]int, error) {
		queryCount++
		return []int{1}, nil
	}
	deleteIDs := func(ctx context.Context, ids []int) (int, error) {
		cancel()
		return len(ids), nil
	}

	deleted, err := worker.deleteIDsInBatches(ctx, "test_records", queryIDs, deleteIDs)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Expected context.Canceled, got %v", err)
	}

	if deleted != 1 {
		t.Errorf("Expected to delete 1 record before cancellation, got %d", deleted)
	}

	if queryCount != 1 {
		t.Errorf("Expected 1 query call, got %d", queryCount)
	}
}

func TestWorker_cleanupSmallResourcesDeletesByIDBatches(t *testing.T) {
	originalBatchSize := defaultBatchSize
	defaultBatchSize = 2
	defer func() {
		defaultBatchSize = originalBatchSize
	}()

	client := enttest.NewEntClient(t, "sqlite3", "file:gc_batch_resources?mode=memory&_fk=0")
	t.Cleanup(func() { client.Close() })

	worker := &Worker{Ent: client, Config: Config{BatchThrottle: time.Nanosecond}}
	ctx := context.Background()
	ctx = ent.NewContext(ctx, client)
	ctx = authz.WithTestBypass(ctx)
	ctx = schematype.SkipSoftDelete(ctx)

	oldTime := time.Now().AddDate(0, 0, -3)
	recentTime := time.Now()

	for i := 0; i < 5; i++ {
		_, err := client.UsageLog.Create().
			SetRequestID(1000 + i).
			SetModelID("test-model").
			SetCreatedAt(oldTime).
			Save(ctx)
		require.NoError(t, err)

		_, err = client.Thread.Create().
			SetProjectID(1).
			SetThreadID(fmt.Sprintf("old-thread-%d", i)).
			SetCreatedAt(oldTime).
			Save(ctx)
		require.NoError(t, err)

		_, err = client.Trace.Create().
			SetProjectID(1).
			SetTraceID(fmt.Sprintf("old-trace-%d", i)).
			SetCreatedAt(oldTime).
			Save(ctx)
		require.NoError(t, err)

		_, err = client.ChannelProbe.Create().
			SetChannelID(1).
			SetTotalRequestCount(1).
			SetSuccessRequestCount(1).
			SetTimestamp(oldTime.Unix()).
			Save(ctx)
		require.NoError(t, err)
	}

	_, err := client.UsageLog.Create().
		SetRequestID(2000).
		SetModelID("test-model").
		SetCreatedAt(recentTime).
		Save(ctx)
	require.NoError(t, err)

	_, err = client.Thread.Create().
		SetProjectID(1).
		SetThreadID("recent-thread").
		SetCreatedAt(recentTime).
		Save(ctx)
	require.NoError(t, err)

	_, err = client.Trace.Create().
		SetProjectID(1).
		SetTraceID("recent-trace").
		SetCreatedAt(recentTime).
		Save(ctx)
	require.NoError(t, err)

	_, err = client.ChannelProbe.Create().
		SetChannelID(1).
		SetTotalRequestCount(1).
		SetSuccessRequestCount(1).
		SetTimestamp(recentTime.Unix()).
		Save(ctx)
	require.NoError(t, err)

	require.NoError(t, worker.cleanupUsageLogs(ctx, 1, false))
	require.NoError(t, worker.cleanupThreads(ctx, 1, false))
	require.NoError(t, worker.cleanupTraces(ctx, 1, false))
	require.NoError(t, worker.cleanupChannelProbes(ctx, 1, false))

	usageLogCount, err := client.UsageLog.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, usageLogCount)

	threadCount, err := client.Thread.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, threadCount)

	traceCount, err := client.Trace.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, traceCount)

	channelProbeCount, err := client.ChannelProbe.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, channelProbeCount)

	oldUsageLogCount, err := client.UsageLog.Query().Where(usagelog.CreatedAtLT(time.Now().AddDate(0, 0, -1))).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, oldUsageLogCount)

	oldThreadCount, err := client.Thread.Query().Where(thread.CreatedAtLT(time.Now().AddDate(0, 0, -1))).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, oldThreadCount)

	oldTraceCount, err := client.Trace.Query().Where(trace.CreatedAtLT(time.Now().AddDate(0, 0, -1))).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, oldTraceCount)

	oldChannelProbeCount, err := client.ChannelProbe.Query().Where(channelprobe.TimestampLT(time.Now().AddDate(0, 0, -1).Unix())).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, oldChannelProbeCount)
}

func TestWorker_cleanupWithZeroDays(t *testing.T) {
	worker := &Worker{
		Ent:    nil,
		Config: Config{CRON: "0 0 * * *"},
	}

	ctx := context.Background()

	// Test with 0 days - should not error
	err := worker.cleanupRequests(ctx, 0, false)
	if err != nil {
		t.Fatalf("cleanupRequests with 0 days failed: %v", err)
	}

	// Test with negative days - should not error
	err = worker.cleanupUsageLogs(ctx, -1, false)
	if err != nil {
		t.Fatalf("cleanupUsageLogs with negative days failed: %v", err)
	}
}
