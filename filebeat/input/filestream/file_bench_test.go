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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/klauspost/compress/gzip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loginp "github.com/elastic/beats/v7/filebeat/input/filestream/internal/input-logfile"
	"github.com/elastic/beats/v7/libbeat/common/file"
	"github.com/elastic/beats/v7/libbeat/reader/readfile/encoding"
	"github.com/elastic/elastic-agent-libs/logp"
)

// func TestBenchmark_10mb(t *testing.T) {
// 	linesPerFile := 42500 // 42500 lines ~= 10MB
// 	plain, gz := generateRandomJSONLogs(t, t.TempDir(), linesPerFile)
//
// 	t.Run("plain",
// 		func(t *testing.T) {
// 			runner(t, plain, linesPerFile)
// 		},
// 	)
// 	t.Run("gzip",
// 		func(t *testing.T) {
// 			runner(t, gz, linesPerFile)
// 		},
// 	)
// }

func TestBenchmark_64gb(t *testing.T) {
	linesPerFile := 42500 * 6400 // 42500 lines ~= 10MB => 64GB total
	// keep the file in case it gets killed, so we have an idea of how far it got
	dir := filepath.Join("testdata", "benchmark", "64gb")
	plain, _ := generateRandomJSONLogs(t, dir, linesPerFile)

	// take heap profile at 90%
	heapProfile := 244800000

	t.Run("plain",
		func(t *testing.T) {
			runner(t, plain, linesPerFile, heapProfile)
		},
	)
	// t.Run("gzip",
	// 	func(t *testing.T) {
	// 		runner(t, gz, linesPerFile, heapProfile)
	// 	},
	// )
}

func runner(t *testing.T, filepath string, totalLines, lineMenHeap int) {
	logger := logp.NewNopLogger()
	inp := filestream{
		gzipExperimental: true,
		encodingFactory:  encoding.Plain,
		readerConfig:     defaultReaderConfig(),
		closerConfig:     defaultCloserConfig(),
	}
	inp.closerConfig.OnStateChange.Inactive = 24 * time.Hour
	inp.closerConfig.Reader.OnEOF = true

	f, err := os.Open(filepath)
	require.NoError(t, err, "could not open file")

	stat, err := f.Stat()
	require.NoError(t, err, "could not stat file")

	info := file.ExtendFileInfo(stat)
	require.NoError(t, f.Close(), "could not close log file after stat")

	r, _, err := inp.open(logger,
		context.Background(),
		fileSource{
			desc:    loginp.FileDescriptor{Info: info},
			newPath: filepath,
			fileID:  uuid.Must(uuid.NewV4()).String(),
		},
		0)
	require.NoError(t, err, "filestream could not open log file")

	// ========================= Setup CPU profile
	cpuf, err := os.Create(filepath + ".cpu.prof")
	require.NoError(t, err, "could not create CPU profile file")
	defer cpuf.Close()

	// ========================== Setup memory heap profile
	heapf, err := os.Create(filepath + ".men.prof")
	require.NoError(t, err, "could not create heap profile file")
	defer heapf.Close()

	// ========================== Setup memory monitoring
	memf, err := os.Create(filepath + ".mem.stats")
	require.NoError(t, err, "could not create memory stats file")

	type memUsage struct {
		Timestamp  time.Time
		Alloc      uint64 // in bytes
		TotalAlloc uint64 // in bytes
	}
	mu := memUsage{}
	menStatsCh := make(chan struct{})
	go func() {
		tick := 500 * time.Millisecond
		ticker := time.NewTicker(tick)
		defer ticker.Stop()
		t.Logf("memory stats collection started every %s", tick)
		for {
			select {
			case <-ticker.C:
				mu.Timestamp = time.Now()
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				mu.Alloc = m.Alloc
				mu.TotalAlloc = m.TotalAlloc
				_, _ = fmt.Fprintf(memf,
					`{"timestamp":"%s","alloc":%d,"total_alloc":%d}\n`,
					mu.Timestamp, mu.Alloc, mu.TotalAlloc)
			case <-menStatsCh:
				t.Log("memory stats collection stopped")
				return
			}
		}
	}()

	i := 1
	err = pprof.StartCPUProfile(cpuf)
	require.NoError(t, err, "could not start CPU profile")
	for {
		_, err := r.Next()
		i++
		if i == lineMenHeap {
			t.Logf("reached lineMenHeap: %d/%d lines, taking heap profile",
				i, totalLines)
			err = pprof.WriteHeapProfile(heapf)
			assert.NoError(t, err, "could not write heap profile")
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("failed to read a log line %d/%d: %v", i, totalLines, err)
		}
	}
	pprof.StopCPUProfile()
	close(menStatsCh)
}

func TestGenJsonLogs(t *testing.T) {
	filesToGen := 1
	linesPerFile := 42500 * 6400 // 42500 lines ~= 10MB

	dir := filepath.Join("testdata", "benchmark", "gen_logs")
	err := os.MkdirAll(dir, 0755)
	require.NoError(t, err, "could not create test data directory")

	t.Logf("generating %d log files with %d lines each in %s",
		-filesToGen, linesPerFile, dir)
	generateRandomJSONLogs(t, dir, linesPerFile)
}

// generateRandomJSONLogs generates random JSON logs in a Apache-like log format.
// It saves the logs to 2 files on 'dir': `logs.ndjson` and `logs.ndjson.gz`,
// the latter being a gzipped version of the former.
// For reference, 42500 lines ~= 10MB.
func generateRandomJSONLogs(t testing.TB, dir string, lines int) (string, string) {
	type apacheLog struct {
		Bytes        int    `json:"bytes"`
		ClientIP     string `json:"client_ip"`
		HTTPVersion  string `json:"http_version"`
		Path         string `json:"path"`
		ResponseCode int    `json:"response_code"`
		Timestamp    string `json:"timestamp"`
		UserID       string `json:"user_id"`
		Method       string `json:"method"`
		Referer      string `json:"referer"`
	}
	t.Helper()

	tempDir := dir
	err := os.MkdirAll(tempDir, 0755)
	require.NoError(t, err, "failed to create temporary directory for logs")

	// ========================= plain file
	plainPath := filepath.Join(tempDir, "logs.ndjson")
	plainf, err := os.Create(plainPath)
	require.NoError(t, err, "failed to create plain file")
	defer func() {
		t.Log("closing plain file")
		err = plainf.Close()
		require.NoError(t, err, "failed to close plain file")
	}()

	// ========================= gz file
	gzPath := filepath.Join(tempDir, "logs.ndjson.gz")
	gzf, err := os.Create(gzPath)
	defer func() {
		t.Log("closing gzip file")
		err = gzf.Close()
		require.NoError(t, err, "failed to close gzip writer")
	}()
	require.NoError(t, err, "failed to create gzip file")
	gzw := gzip.NewWriter(gzf)
	defer func() {
		t.Log("closing gzip writer")
		err = gzw.Close()
		require.NoError(t, err, "failed to close gzip writer")
	}()

	// ========================= setup writers
	mWriter := io.MultiWriter(plainf, gzw)
	w := bufio.NewWriterSize(mWriter, 4096)

	verbs := []string{"GET", "POST", "PUT", "DELETE", "HEAD"}
	paths := []string{"/index.html", "/app/login", "/api/v1/users", "/img/logo.png", "/favicon.ico"}
	httpVersions := []string{"1.0", "1.1", "2.0"}
	responseCodes := []int{200, 201, 400, 404, 500}
	websites := []string{
		"https://www.tardistravel.net",
		"https://www.gallifreyanarchives.gov",
		"https://www.k9skorner.info",
		"https://www.adiposeindustries.com",
		"https://www.badwolfbay.co.uk",
		"https://www.shadowproclamation.org",
		"https://www.magpieelectricals.tv",
		"https://www.thedoctorsdiary.blog",
		"https://www.sonicscrewdriver.repair",
		"https://www.dontblink.com",
	}
	userIdentifiers := []string{
		"TheLastTimeLord",
		"TARDIS_Blue_Box",
		"Companion_Alley",
		"SonicScrewdriver_007",
		"BadWolfBay_Watcher",
		"Gallifreyan_Exile",
		"WeepingAngelDontBlink",
		"RiverSongSpoilers",
		"K9_Affirmative",
		"TheOncomingStorm",
	}

	for i := 0; i < lines; i++ {
		alog := apacheLog{
			Bytes:       rand.Intn(4096),
			ClientIP:    fmt.Sprintf("%d.%d.%d.%d", rand.Intn(256), rand.Intn(256), rand.Intn(256), rand.Intn(256)),
			HTTPVersion: httpVersions[rand.Intn(len(httpVersions))],

			Method:       verbs[rand.Intn(len(verbs))],
			Path:         paths[rand.Intn(len(paths))],
			Referer:      websites[rand.Intn(len(websites))],
			ResponseCode: responseCodes[rand.Intn(len(responseCodes))],
			Timestamp:    time.Now().Add(-time.Duration(1) * time.Hour).Format("02-Jan-2006:15:04:05 -0700"),
			UserID:       userIdentifiers[rand.Intn(len(userIdentifiers))],
		}

		line, err := json.Marshal(alog)
		require.NoError(t, err, "failed to marshal log entry")

		line = append(line, '\n')
		_, err = w.Write(line)
		require.NoError(t, err, "failed to write log entry to file")
	}

	return plainPath, gzPath
}
