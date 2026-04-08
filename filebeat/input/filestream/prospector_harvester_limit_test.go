//go:build integration

// Licensed to Elasticsearch B.V. under one or more contributor
// license agreements. See the NOTICE file distributed with
// this work for additional information regarding copyright
// ownership. Elasticsearch B.V. licenses this file to you under
// the Apache License, Version 2.0 (the "License"); you may
// not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

package filestream

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loginp "github.com/elastic/beats/v7/filebeat/input/filestream/internal/input-logfile"
	v2 "github.com/elastic/beats/v7/filebeat/input/v2"
	"github.com/elastic/beats/v7/libbeat/common/file"
	conf "github.com/elastic/elastic-agent-libs/config"
	"github.com/elastic/elastic-agent-libs/logp"
	"github.com/elastic/elastic-agent-libs/logp/logptest"
	"github.com/elastic/elastic-agent-libs/monitoring"
	"github.com/elastic/go-concert/unison"
)

// controlledFileWatcher is a mock FSWatcher that reads events from a channel,
// giving the test full control over what the prospector sees and when.
type controlledFileWatcher struct {
	events     chan loginp.FSEvent
	notifyChan chan loginp.HarvesterStatus
}

func (w *controlledFileWatcher) NotifyChan() chan loginp.HarvesterStatus {
	return w.notifyChan
}

func newControlledFileWatcher() *controlledFileWatcher {
	return &controlledFileWatcher{
		events: make(chan loginp.FSEvent),
	}
}

func (w *controlledFileWatcher) Run(_ unison.Canceler) {}

func (w *controlledFileWatcher) Event() loginp.FSEvent {
	evt, ok := <-w.events
	if !ok {
		return loginp.FSEvent{Op: loginp.OpDone}
	}
	return evt
}

func (w *controlledFileWatcher) GetFiles() map[string]loginp.FileDescriptor {
	return nil
}

// send pushes an event and blocks until the prospector consumes it.
func (w *controlledFileWatcher) send(t *testing.T, evt loginp.FSEvent) {
	t.Helper()
	select {
	case w.events <- evt:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout sending event to controlled watcher")
	}
}

func (w *controlledFileWatcher) close() {
	close(w.events)
}

// fileDescriptorFromPath stats a file and computes its fingerprint, returning
// a FileDescriptor suitable for an FSEvent.
func fileDescriptorFromPath(t *testing.T, path string, fpLen int) loginp.FileDescriptor {
	t.Helper()

	fi, err := os.Stat(path)
	require.NoError(t, err, "cannot stat %s", path)

	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()

	buf := make([]byte, fpLen)
	n, err := f.Read(buf)
	require.NoError(t, err, "cannot read fingerprint bytes from %s", path)
	require.Equal(t, fpLen, n, "file %s too small for fingerprint", path)

	h := sha256.Sum256(buf[:n])
	return loginp.FileDescriptor{
		Filename:    path,
		Info:        file.ExtendFileInfo(fi),
		Fingerprint: hex.EncodeToString(h[:]),
	}
}

// TestHarvesterLimitStaleGoroutineCausesReingestion proves that goroutines
// waiting on the harvester_limit semaphore can cause a file to be harvested
// multiple times.
//
// The test uses:
//   - A controlled file watcher (mock) injected via InputManager.Configure
//   - The real defaultHarvesterGroup with harvester_limit=1
//   - The real filestream harvester reading real files
//   - A blocking pipeline to hold the first harvester on the semaphore slot
//
// Scenario:
//  1. file-0.log is discovered and its harvester blocks on publish (holds the slot)
//  2. fileA.log is discovered (OpCreate) — goroutine blocks on semaphore
//  3. fileA.log is written to (OpWrite) — second goroutine blocks on semaphore
//  4. fileA.log is renamed to fileA.log.1, a new smaller fileA.log is created
//  5. file-0.log is unblocked — semaphore freed
//  6. Both fileA goroutines run sequentially — file harvested twice
func TestHarvesterLimitStaleGoroutineCausesReingestion(t *testing.T) {
	log, _ := logptest.NewTestingLoggerWithObserver(t, "")
	inputID := "test-harvester-limit"

	dir := t.TempDir()
	watcher := newControlledFileWatcher()

	// --- Write real files ---
	fingerprintLen := 1024 // default fingerprint length

	file0Content := strings.Repeat("file-0 log line\n", 100) // > 1024 bytes
	file0Path := filepath.Join(dir, "file-0.log")
	require.NoError(t, os.WriteFile(file0Path, []byte(file0Content), 0o644))

	fileAContent := strings.Repeat("fileA original content line\n", 100)
	fileAPath := filepath.Join(dir, "fileA.log")
	require.NoError(t, os.WriteFile(fileAPath, []byte(fileAContent), 0o644))

	// --- Create input with mock watcher injected via Configure ---
	stateStore := openTestStatestore()
	plugin := Plugin(log, stateStore)
	manager := plugin.Manager.(*loginp.InputManager)

	originalConfigure := manager.Configure
	manager.Configure = func(cfg *conf.C, log *logp.Logger, src *loginp.SourceIdentifier) (loginp.Prospector, loginp.Harvester, error) {
		prospector, harvester, err := originalConfigure(cfg, log, src)
		if err != nil {
			return nil, nil, err
		}

		// Replace the watcher inside the prospector with our controlled one.
		fp := prospector.(*fileProspector)
		fp.filewatcher = watcher

		return fp, harvester, nil
	}

	var grp unison.TaskGroup
	require.NoError(t, manager.Init(&grp))

	cfg := conf.MustNewConfigFrom(map[string]interface{}{
		"id":                                     inputID,
		"paths":                                  []string{filepath.Join(dir, "*.log")},
		"prospector.scanner.check_interval":      "1h", // we drive events manually
		"prospector.scanner.fingerprint.enabled": true,
		"prospector.scanner.fingerprint.length":  fingerprintLen,
		"file_identity.fingerprint":              map[string]any{},
		"close.reader.on_eof":                    true,
		"close.on_state_change.check_interval":   "1ms",
		"close.on_state_change.inactive":         "100ms",
		"harvester_limit":                        1,
	})

	inp, err := manager.Create(cfg)
	require.NoError(t, err, "failed to create input")

	// --- Start the input with a blocking pipeline ---
	pipeline := &mockPipelineConnector{blocking: true}

	ctx, cancelInput := context.WithCancel(context.Background())
	defer cancelInput()

	inputDone := make(chan struct{})
	go func() {
		defer close(inputDone)
		defer func() { _ = grp.Stop() }()
		_ = inp.Run(v2.Context{
			Logger:          log,
			Cancelation:     ctx,
			ID:              inputID,
			MetricsRegistry: monitoring.NewRegistry()},
			pipeline)
	}()

	// --- Step 1: Push OpCreate for file-0.log — blocks on publish, holds slot ---
	file0Desc := fileDescriptorFromPath(t, file0Path, fingerprintLen)
	watcher.send(t, loginp.FSEvent{
		Op:         loginp.OpCreate,
		NewPath:    file0Path,
		Descriptor: file0Desc,
	})

	// Wait until the file-0.log harvester has connected and is blocked on publish.
	require.Eventually(t, func() bool {
		return pipeline.clientsCount() == 1
	}, 5*time.Second, 10*time.Millisecond,
		"file-0.log harvester should have connected to the pipeline")

	// The harvester is now blocked inside PublishAll (holding the semaphore slot).

	// --- Step 2: Push OpCreate for fileA.log — goroutine blocks on semaphore ---
	fileADesc := fileDescriptorFromPath(t, fileAPath, fingerprintLen)
	watcher.send(t, loginp.FSEvent{
		Op:         loginp.OpCreate,
		NewPath:    fileAPath,
		Descriptor: fileADesc,
	})

	// --- Step 3: Push OpWrite for fileA.log — second goroutine blocks on semaphore ---
	watcher.send(t, loginp.FSEvent{
		Op:         loginp.OpWrite,
		NewPath:    fileAPath,
		Descriptor: fileADesc,
	})

	// Give goroutines time to queue on the semaphore.
	time.Sleep(200 * time.Millisecond)

	// --- Step 4: Simulate copy-truncate rotation ---
	// a. Rename fileA.log → fileA.log.1 on disk
	fileARotatedPath := filepath.Join(dir, "fileA.log.1")
	require.NoError(t, os.Rename(fileAPath, fileARotatedPath))

	// b. Create a new, smaller fileA.log with different content
	fileANewContent := strings.Repeat("new fileA line\n", 100) // smaller but > fingerprint length
	require.NoError(t, os.WriteFile(fileAPath, []byte(fileANewContent), 0o644))

	// c. Push OpRename to the prospector
	watcher.send(t, loginp.FSEvent{
		Op:         loginp.OpRename,
		OldPath:    fileAPath,
		NewPath:    fileARotatedPath,
		Descriptor: fileADesc, // same fingerprint (the rotated copy)
	})

	// d. Push OpTruncate for the new fileA.log (different fingerprint)
	fileANewDesc := fileDescriptorFromPath(t, fileAPath, fingerprintLen)
	require.NotEqual(t, fileADesc.Fingerprint, fileANewDesc.Fingerprint,
		"new fileA.log should have a different fingerprint than the original")
	watcher.send(t, loginp.FSEvent{
		Op:         loginp.OpTruncate,
		NewPath:    fileAPath,
		Descriptor: fileANewDesc,
	})

	// --- Step 5: Unblock file-0.log — switch pipeline to non-blocking, cancel blocker client ---
	pipeline.invertBlocking() // new clients won't block
	pipeline.mtx.Lock()
	if len(pipeline.clients) > 0 {
		pipeline.clients[0].canceler() // unblock the first (file-0) client
	}
	pipeline.mtx.Unlock()

	// --- Step 6: Wait for all goroutines to drain ---
	// Give the queued goroutines time to acquire the semaphore and run.
	time.Sleep(2 * time.Second)

	// --- Step 7: Collect and verify events ---
	allEvents := pipeline.GetAllEvents()
	t.Logf("Total events published: %d", len(allEvents))
	t.Logf("Pipeline clients created: %d", pipeline.clientsCount())

	// Log per-client breakdown
	pipeline.mtx.Lock()
	for i, c := range pipeline.clients {
		events := c.GetEvents()
		path := ""
		if len(events) > 0 {
			flat := events[0].Fields.Flatten()
			p, _ := flat.GetValue("log.file.path")
			path, _ = p.(string)
			t.Logf("  client[%d]: %d events, closed=%v, first_path=%s. 1st event: %s", i, len(events), c.closed, path, events[0].String())
		} else {
			t.Logf("  client[%d]: %d events, closed=%v, first_path=%s.", i, len(events), c.closed, path)
		}
	}
	pipeline.mtx.Unlock()

	// Count events that came from fileA (either the original or the new content).
	fileAEventCount := 0
	for _, evt := range allEvents {
		flat := evt.Fields.Flatten()
		pathVal, _ := flat.GetValue("log.file.path")
		if path, ok := pathVal.(string); ok && strings.Contains(path, "fileA") {
			fileAEventCount++
		}
	}

	fileANewLineCount := 100 // lines in the new fileA.log after rotation

	t.Logf("Events from fileA path: %d", fileAEventCount)
	t.Logf("New fileA.log lines: %d", fileANewLineCount)

	// The new fileA.log has 100 lines. It should be harvested exactly once
	// (by the new fingerprint's OpCreate harvester).
	//
	// On UNFIXED code: the stale goroutine from the OpWrite (for the OLD
	// fingerprint) also runs, opens fileA.log (now the new 100-line file),
	// and reads it. Then the OpCreate for the new fingerprint ALSO reads it.
	// Total > 100.
	//
	// On FIXED code (#48445/#49260): the stale OpWrite goroutine is never
	// spawned (reserve prevents it). Only the new fingerprint's harvester
	// reads the new fileA.log. Total == 100.
	assert.Greater(t, fileAEventCount, fileANewLineCount,
		"BUG: stale goroutine should cause fileA to be harvested more than once. "+
			"If this fails, the fix from PR #48445/#49260 is working correctly.")

	// Clean up
	watcher.close()
	cancelInput()
	<-inputDone
}
